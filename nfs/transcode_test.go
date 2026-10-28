// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vynulldev/vynull/internal/ffmpeg"
)

// mp3Of is a conversion that writes data at once, its size known ahead.
func mp3Of(data []byte) (int, io.ReadCloser, error) {
	return len(data), io.NopCloser(bytes.NewReader(data)), nil
}

// fakeTranscoder is an mp3Transcoder whose conversions write size bytes of
// the path's first letter, counting each conversion, and which keeps no MP3
// for having been read lately (its clock ticks a second a reading).
func fakeTranscoder(max, size int) (*mp3Transcoder, *atomic.Int32) {
	t := newMP3Transcoder()
	t.max = max
	t.keepFor = 0
	var clock time.Time
	t.now = func() time.Time { clock = clock.Add(time.Second); return clock } // under t.mu
	var n atomic.Int32
	t.start = func(path string) (int, io.ReadCloser, error) {
		n.Add(1)
		return mp3Of(bytes.Repeat([]byte{path[1]}, size))
	}
	return t, &n
}

// converted asks for path's size, as a deck's LOOKUP does, and waits for
// the conversions running to end.
func converted(t *testing.T, tc *mp3Transcoder, path string) int64 {
	t.Helper()
	size, err := tc.Size(path)
	if err != nil {
		t.Fatal(err)
	}
	tc.running.Wait()
	return size
}

// TestTranscodedReadAtEnd checks a read at or past the end of a transcoded
// file gets no data rather than an error.
func TestTranscodedReadAtEnd(t *testing.T) {
	tc := newMP3Transcoder()
	tc.start = func(string) (int, io.ReadCloser, error) { return mp3Of([]byte("mp3 data")) }
	for _, off := range []uint32{8, 20} {
		if got, err := tc.ReadAt("/music/track.flac", off, 2048); err != nil || len(got) != 0 {
			t.Errorf("read at %d of 8 bytes: %q, %v; want none, no error", off, got, err)
		}
	}
	if got, err := tc.ReadAt("/music/track.flac", 4, 2048); err != nil || string(got) != "data" {
		t.Errorf("read crossing the end: %q, %v; want \"data\"", got, err)
	}
}

// TestTranscodedFilesBounded checks the MP3s kept stay within the budget,
// the least recently read dropped first and converted again when read again.
func TestTranscodedFilesBounded(t *testing.T) {
	tc, conversions := fakeTranscoder(100, 40)
	for _, p := range []string{"/a.flac", "/b.flac", "/a.flac", "/c.flac"} {
		converted(t, tc, p)
	}
	// a was read after b, so c's arrival (120 bytes held) drops b.
	if tc.total > 100 || tc.files["/b.flac"] != nil || tc.files["/a.flac"] == nil || tc.files["/c.flac"] == nil {
		t.Fatalf("held %d bytes, files %v; want a and c within 100", tc.total, keys(tc.files))
	}
	if n := conversions.Load(); n != 3 {
		t.Errorf("%d conversions, want 3 (a once)", n)
	}
	if data, err := tc.ReadAt("/b.flac", 0, 10); err != nil || !bytes.Equal(data, bytes.Repeat([]byte("b"), 10)) {
		t.Errorf("dropped file read back as %q, %v", data, err)
	}
	if n := conversions.Load(); n != 4 {
		t.Errorf("%d conversions after reading the dropped file, want 4", n)
	}
}

// TestTranscodedFileOverBudget checks a file larger than the whole budget is
// still kept while it is the last one converted.
func TestTranscodedFileOverBudget(t *testing.T) {
	tc, conversions := fakeTranscoder(100, 40)
	converted(t, tc, "/a.flac")
	tc.start = func(path string) (int, io.ReadCloser, error) { conversions.Add(1); return mp3Of(make([]byte, 300)) }
	if size := converted(t, tc, "/m.flac"); size != 300 {
		t.Fatalf("size %d; want 300", size)
	}
	if tc.files["/m.flac"] == nil || tc.files["/a.flac"] != nil {
		t.Errorf("files %v; want only the long one", keys(tc.files))
	}
	converted(t, tc, "/m.flac")
	if n := conversions.Load(); n != 2 {
		t.Errorf("%d conversions, want 2 (the long one kept)", n)
	}
}

// TestTranscodedFileInUseKept checks an MP3 read lately is kept however far
// over the budget: a long mix playing on one deck while another loads a
// track. Once it hasn't been read for keepFor, it goes.
func TestTranscodedFileInUseKept(t *testing.T) {
	tc := newMP3Transcoder()
	tc.max = 100
	clock := time.Unix(0, 0)
	tc.now = func() time.Time { return clock }
	tc.start = func(path string) (int, io.ReadCloser, error) {
		if path == "/mix.flac" {
			return mp3Of(make([]byte, 300))
		}
		return mp3Of(make([]byte, 40))
	}
	converted(t, tc, "/mix.flac")
	clock = clock.Add(time.Minute)
	converted(t, tc, "/track.flac")
	if tc.files["/mix.flac"] == nil || tc.files["/track.flac"] == nil {
		t.Fatalf("files %v; want the mix read a minute ago kept, with the track", keys(tc.files))
	}

	clock = clock.Add(keepReadFor + time.Second)
	converted(t, tc, "/next.flac")
	if tc.files["/mix.flac"] != nil || tc.files["/track.flac"] == nil || tc.files["/next.flac"] == nil || tc.total != 80 {
		t.Errorf("held %d bytes, files %v; want the mix unread for %v dropped", tc.total, keys(tc.files), keepReadFor)
	}
}

// TestTranscodeOnce checks reads arriving together share one conversion.
func TestTranscodeOnce(t *testing.T) {
	tc, conversions := fakeTranscoder(1<<20, 1000)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tc.ReadAt("/a.flac", 0, 100); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := conversions.Load(); n != 1 {
		t.Errorf("%d conversions, want 1", n)
	}
}

// TestTranscodeFailureRetried checks a failed conversion isn't kept: the
// next read converts again.
func TestTranscodeFailureRetried(t *testing.T) {
	tc := newMP3Transcoder()
	fail := true
	tc.start = func(string) (int, io.ReadCloser, error) {
		if fail {
			return 0, nil, errors.New("file busy")
		}
		return mp3Of([]byte("mp3"))
	}
	if _, err := tc.Size("/a.flac"); err == nil {
		t.Fatal("first conversion should fail")
	}
	tc.running.Wait()
	fail = false
	if size, err := tc.Size("/a.flac"); err != nil || size != 3 {
		t.Errorf("after a failure: size %d, %v; want 3", size, err)
	}
}

// TestTranscodeStreams checks a file's size is given before its conversion
// has written anything, and a read waits only for the bytes it asks for.
func TestTranscodeStreams(t *testing.T) {
	tc := newMP3Transcoder()
	r, w := io.Pipe()
	tc.start = func(string) (int, io.ReadCloser, error) { return 100, r, nil }
	if size, err := tc.Size("/mix.flac"); err != nil || size != 100 {
		t.Fatalf("size %d, %v; want 100 before any of it is converted", size, err)
	}
	w.Write(bytes.Repeat([]byte("a"), 40))
	if got, err := tc.ReadAt("/mix.flac", 0, 20); err != nil || string(got) != strings.Repeat("a", 20) {
		t.Fatalf("read of converted bytes: %q, %v", got, err)
	}

	later := make(chan string, 1)
	go func() {
		got, err := tc.ReadAt("/mix.flac", 50, 10)
		later <- fmt.Sprintf("%s %v", got, err)
	}()
	select {
	case got := <-later:
		t.Fatalf("read of bytes not converted yet answered %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	w.Write(bytes.Repeat([]byte("b"), 60))
	w.Close()
	if got := <-later; got != strings.Repeat("b", 10)+" <nil>" {
		t.Errorf("read once converted: %q", got)
	}
	if got, err := tc.ReadAt("/mix.flac", 100, 10); err != nil || len(got) != 0 {
		t.Errorf("read at the end: %q, %v; want none", got, err)
	}
}

// TestTranscodeKeepsPredictedSize checks an MP3 that comes out shorter or
// longer than predicted is served at the predicted size, padded with zeros
// or cut, since decks were told that size.
func TestTranscodeKeepsPredictedSize(t *testing.T) {
	for _, wrote := range []int{30, 50} {
		tc := newMP3Transcoder()
		tc.start = func(string) (int, io.ReadCloser, error) {
			return 40, io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("m"), wrote))), nil
		}
		got, err := tc.ReadAt("/a.flac", 0, 100)
		want := append(bytes.Repeat([]byte("m"), min(wrote, 40)), make([]byte, 40-min(wrote, 40))...)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("wrote %d of 40: served %q, %v; want %q", wrote, got, err, want)
		}
		if size, _ := tc.Size("/a.flac"); size != 40 {
			t.Errorf("wrote %d of 40: size %d", wrote, size)
		}
	}
}

// TestTranscodeSizeUnknown checks a file that doesn't say how long it is
// gets its size once the conversion ends.
func TestTranscodeSizeUnknown(t *testing.T) {
	tc := newMP3Transcoder()
	r, w := io.Pipe()
	tc.start = func(string) (int, io.ReadCloser, error) { return -1, r, nil }
	sized := make(chan int64, 1)
	go func() {
		size, _ := tc.Size("/a.flac")
		sized <- size
	}()
	w.Write([]byte("mp3 "))
	select {
	case size := <-sized:
		t.Fatalf("size %d given before the conversion ended", size)
	case <-time.After(50 * time.Millisecond):
	}
	w.Write([]byte("data"))
	w.Close()
	if size := <-sized; size != 8 {
		t.Errorf("size %d, want 8", size)
	}
	if got, err := tc.ReadAt("/a.flac", 4, 10); err != nil || string(got) != "data" {
		t.Errorf("read %q, %v", got, err)
	}
}

// TestTranscodeDoesNotHoldUpReads serves NFS over UDP with a conversion
// that doesn't get going: a deck looking up the file being converted waits,
// and another deck's reads of a plain file are still answered meanwhile.
func TestTranscodeDoesNotHoldUpReads(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("mp3 "), 1000)
	if err := os.WriteFile(filepath.Join(root, "playing.mp3"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "long.flac"), []byte("flac"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(root)
	srv.Transcode = true
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv.transcoder.start = func(path string) (int, io.ReadCloser, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return mp3Of(make([]byte, 5000))
	}
	serve := func(handler func(*rpcHeader) []byte, concurrent bool) *net.UDPAddr {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			srv.serveUDP(ctx, conn, handler, false, concurrent)
		}()
		return conn.LocalAddr().(*net.UDPAddr)
	}
	mountAddr, nfsAddr := serve(srv.handleMount, false), serve(srv.handleNFS, true)
	deck := func(timeout time.Duration) (*Client, [fhSize]byte) {
		c := newClient(net.IPv4(127, 0, 0, 1), timeout)
		c.mountAddr, c.nfsAddr = mountAddr, nfsAddr
		dir, err := c.mount("/C/")
		if err != nil {
			t.Fatal(err)
		}
		return c, dir
	}
	playing, playingDir := deck(300 * time.Millisecond)
	loading, loadingDir := deck(5 * time.Second)
	fh, _, err := playing.lookup(playingDir, "playing.mp3")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		size uint32
		err  error
	}
	looked := make(chan result, 1)
	go func() {
		_, attr, err := loading.lookup(loadingDir, "long.flac")
		looked <- result{attr.Size, err}
	}()
	<-started

	for off := uint32(0); off < uint32(len(content)); off += 1000 {
		got, err := playing.read(fh, off, 1000)
		if err != nil {
			t.Fatalf("read at %d during the conversion: %v", off, err)
		}
		if !strings.HasPrefix(string(content[off:]), string(got)) || len(got) != 1000 {
			t.Fatalf("read at %d during the conversion: %d bytes, not the file's", off, len(got))
		}
	}
	select {
	case r := <-looked:
		t.Fatalf("lookup of the file being converted answered before its size was known: %v", r)
	default:
	}

	releaseOnce.Do(func() { close(release) })
	if r := <-looked; r.err != nil || r.size != 5000 {
		t.Errorf("lookup once the conversion started: size %d, %v; want the MP3's 5000", r.size, r.err)
	}
}

// needFFmpeg skips a test without ffmpeg and ffprobe.
func needFFmpeg(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
}

// convertWhole converts path with startMP3 and returns the size it
// predicted and the MP3 as ffmpeg wrote it.
func convertWhole(t *testing.T, path string) (int, []byte) {
	t.Helper()
	size, r, err := startMP3(path)
	if err != nil {
		t.Fatal(err)
	}
	mp3, err := io.ReadAll(r)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return size, mp3
}

// TestMP3StartsWithAudio converts a FLAC with cover art: the MP3 must start
// right with an MPEG frame, with no tag and no picture. Written to a pipe,
// ffmpeg's tag holding a picture claims to be empty, with the picture after.
func TestMP3StartsWithAudio(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	cover, flac := filepath.Join(dir, "cover.jpg"), filepath.Join(dir, "track.flac")
	ctx := context.Background()
	if _, err := ffmpeg.Run(ctx, "-f", "lavfi", "-i", "color=c=red:s=600x600:d=0.04", "-frames:v", "1", cover); err != nil {
		t.Fatal(err)
	}
	if _, err := ffmpeg.Run(ctx, "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:a", "flac", "-c:v", "copy", "-disposition:v", "attached_pic", flac); err != nil {
		t.Fatal(err)
	}

	size, mp3 := convertWhole(t, flac)
	if len(mp3) < 2 || mp3[0] != 0xff || mp3[1]&0xe0 != 0xe0 {
		t.Errorf("MP3 starts %x; want an MPEG frame", mp3[:min(len(mp3), 4)])
	}
	if bytes.Contains(mp3, []byte("APIC")) {
		t.Error("the MP3 carries the cover art")
	}
	if size != len(mp3) {
		t.Errorf("predicted %d bytes, got %d", size, len(mp3))
	}
}

// TestMP3SizePredicted converts files of awkward lengths and rates and
// checks each MP3 is the size predicted before the conversion: at and around
// frame boundaries, mono, 32 to 48 kHz as they are, and 22.05 and 96 kHz
// resampled. A FLAC written without its length gets no prediction.
func TestMP3SizePredicted(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	ctx := context.Background()
	for _, c := range []struct {
		rate, samples, channels int
	}{
		{44100, 1, 2}, {44100, 1151, 2}, {44100, 1152, 2}, {44100, 1153, 2}, {44100, 123457, 2},
		{48000, 99999, 2}, {32000, 50000, 2}, {44100, 44100, 1}, {22050, 30001, 2}, {96000, 200001, 2},
	} {
		path := filepath.Join(dir, fmt.Sprintf("%d-%d-%d.flac", c.rate, c.samples, c.channels))
		if _, err := ffmpeg.Run(ctx, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=%d", c.rate),
			"-af", fmt.Sprintf("atrim=end_sample=%d", c.samples), "-ac", fmt.Sprint(c.channels), path); err != nil {
			t.Fatal(err)
		}
		if size, mp3 := convertWhole(t, path); size != len(mp3) {
			t.Errorf("%d samples at %d Hz, %d channels: predicted %d bytes, got %d", c.samples, c.rate, c.channels, size, len(mp3))
		}
	}

	flac, err := ffmpeg.Run(ctx, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-f", "flac", "-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "piped.flac")
	if err := os.WriteFile(path, flac, 0o644); err != nil {
		t.Fatal(err)
	}
	if size, mp3 := convertWhole(t, path); size != -1 || len(mp3) == 0 {
		t.Errorf("FLAC without its length: predicted %d, converted %d bytes; want -1 and an MP3", size, len(mp3))
	}
}

// TestMP3SizeKnownValues pins mp3Size to sizes ffmpeg wrote.
func TestMP3SizeKnownValues(t *testing.T) {
	for _, c := range []struct {
		samples    int64
		rate, want int
	}{
		{1000, 44100, 2089}, {44100, 44100, 41796}, {5000000, 44100, 4536947},
		{1000, 48000, 1920}, {1234567, 48000, 1030080}, {7624704, 48000, 6355200},
	} {
		if got := mp3Size(c.samples, c.rate); got != c.want {
			t.Errorf("mp3Size(%d, %d) = %d, want %d", c.samples, c.rate, got, c.want)
		}
	}
}

func keys(m map[string]*mp3File) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	return k
}

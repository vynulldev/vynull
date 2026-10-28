// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTranscoder is an mp3Transcoder whose conversions return size bytes of
// the path's first letter, counting each conversion, and which keeps no MP3
// for having been read lately (its clock ticks a second a reading).
func fakeTranscoder(max, size int) (*mp3Transcoder, *atomic.Int32) {
	t := newMP3Transcoder()
	t.max = max
	t.keepFor = 0
	var clock time.Time
	t.now = func() time.Time { clock = clock.Add(time.Second); return clock } // under t.mu
	var n atomic.Int32
	t.convert = func(path string) ([]byte, error) {
		n.Add(1)
		return bytes.Repeat([]byte{path[1]}, size), nil
	}
	return t, &n
}

// TestTranscodedReadAtEnd checks a read at or past the end of a transcoded
// file gets no data rather than an error.
func TestTranscodedReadAtEnd(t *testing.T) {
	tc := newMP3Transcoder()
	tc.convert = func(string) ([]byte, error) { return []byte("mp3 data"), nil }
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
		if _, err := tc.Size(p); err != nil {
			t.Fatal(err)
		}
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
	tc.Size("/a.flac")
	tc.convert = func(path string) ([]byte, error) { conversions.Add(1); return make([]byte, 300), nil }
	if size, err := tc.Size("/m.flac"); err != nil || size != 300 {
		t.Fatalf("size %d, %v; want 300", size, err)
	}
	if tc.files["/m.flac"] == nil || tc.files["/a.flac"] != nil {
		t.Errorf("files %v; want only the long one", keys(tc.files))
	}
	tc.Size("/m.flac")
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
	tc.convert = func(path string) ([]byte, error) {
		if path == "/mix.flac" {
			return make([]byte, 300), nil
		}
		return make([]byte, 40), nil
	}
	tc.Size("/mix.flac")
	clock = clock.Add(time.Minute)
	tc.Size("/track.flac")
	if tc.files["/mix.flac"] == nil || tc.files["/track.flac"] == nil {
		t.Fatalf("files %v; want the mix read a minute ago kept, with the track", keys(tc.files))
	}

	clock = clock.Add(keepReadFor + time.Second)
	tc.Size("/next.flac")
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
	tc.convert = func(string) ([]byte, error) {
		if fail {
			return nil, errors.New("file busy")
		}
		return []byte("mp3"), nil
	}
	if _, err := tc.Size("/a.flac"); err == nil {
		t.Fatal("first conversion should fail")
	}
	fail = false
	if size, err := tc.Size("/a.flac"); err != nil || size != 3 {
		t.Errorf("after a failure: size %d, %v; want 3", size, err)
	}
}

// TestTranscodeDoesNotHoldUpReads serves NFS over UDP with a conversion
// that doesn't finish: a deck looking up the file being converted waits, and
// another deck's reads of a plain file are still answered meanwhile.
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
	srv.transcoder.convert = func(path string) ([]byte, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return make([]byte, 5000), nil
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
		t.Fatalf("lookup of the file being converted answered before the conversion ended: %v", r)
	default:
	}

	releaseOnce.Do(func() { close(release) })
	if r := <-looked; r.err != nil || r.size != 5000 {
		t.Errorf("lookup after the conversion: size %d, %v; want the MP3's 5000", r.size, r.err)
	}
}

func keys(m map[string]*mp3File) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	return k
}

// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/ffmpeg"
	"github.com/vynulldev/vynull/internal/filetype"
)

// transcode.go serves FLAC, WAV and AIFF files as MP3s when the server runs
// with --transcode (Server.Transcode). The CDJ's NFS client has a 4-byte read
// alignment bug at 2048-byte read sizes that corrupts lossless streams; MP3
// tolerates it, since its decoder can resync on frame boundaries.
//
// A file is converted in the background from its first LOOKUP. The MP3's
// size is known before the conversion starts (mp3Size), so the LOOKUP is
// answered at once and each READ waits only for the bytes it asks for. A
// deck gives up on a file it has had no answer about for 25 s, and an hour
// of FLAC takes about 36 s to convert, where its first bytes take 50 ms.
//
// A CDJ-2000NXS2's first read of an MP3 is its last 160 bytes, looking for
// a tag, so the MP3 ends with a silent frame made here (silentFrame): its
// bytes are known from the start, and a read within them is answered at
// once rather than when the whole file is converted.

// maxTranscodedBytes bounds the MP3s kept in memory. A 320 kbps MP3 takes
// 2.4 MB a minute, so this holds well over an hour of music. The least
// recently read go first, but never one read in the last keepReadFor.
const maxTranscodedBytes = 256 << 20

// keepReadFor is how long an MP3 is kept after its last read, whatever the
// budget: the deck that read it may be playing it or holding it cued, and
// would wait for it to be converted again (minutes, for a long mix).
const keepReadFor = 30 * time.Minute

// mp3Transcoder converts files to MP3 in the background, once each, and
// keeps the results within a byte budget.
type mp3Transcoder struct {
	start   func(path string) (conversion, error) // startMP3, or a test's
	now     func() time.Time                      // time.Now, or a test's
	max     int                                   // byte budget (maxTranscodedBytes)
	keepFor time.Duration                         // keepReadFor

	mu      sync.Mutex
	files   map[string]*mp3File
	total   int            // bytes of converted MP3s held
	seq     uint64         // read counter, for least-recently-read eviction
	running sync.WaitGroup // conversions running (tests wait for them)
}

// A conversion is an MP3 being written: its size, if known ahead, then its
// body as ffmpeg writes it, and its tail, known ahead.
type conversion struct {
	size int           // the whole MP3's size, -1 if not known ahead
	body io.ReadCloser // the MP3 up to its tail
	tail []byte        // the MP3's last bytes; none if size is unknown
}

// mp3File is a file's MP3, converted in the background. Once written, a
// byte is never changed, so a reader can keep the bytes it was given while
// the conversion goes on, or after the transcoder has dropped the file.
type mp3File struct {
	// Guarded by the transcoder's mu.
	lastRead uint64    // t.seq at the last read
	readAt   time.Time // t.now() at the last read
	held     int       // bytes counted in t.total, once converted

	mu   sync.Mutex
	cond sync.Cond // on mu: broadcast as the MP3 grows, its size is known, or it ends
	size int       // the MP3's size: -1 until known
	data []byte    // the MP3 so far
	tail []byte    // its last bytes, known ahead
	done bool
	err  error
}

func newMP3Transcoder() *mp3Transcoder {
	return &mp3Transcoder{
		start:   startMP3,
		now:     time.Now,
		max:     maxTranscodedBytes,
		keepFor: keepReadFor,
		files:   make(map[string]*mp3File),
	}
}

// NeedsTranscode returns true if the file is a lossless format (FLAC, WAV,
// AIFF) that needs transcoding to survive the CDJ's NFS read alignment bug.
func NeedsTranscode(path string) bool {
	ft, ok := filetype.ForPath(path)
	return ok && ft.Lossless
}

// Size returns the size of a file's MP3, starting its conversion if need be.
// It waits only until the size is known: at once for a file that says how
// many samples it holds, at the end of the conversion for one that doesn't.
func (t *mp3Transcoder) Size(path string) (int64, error) {
	f := t.file(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.size < 0 && !f.done {
		f.cond.Wait()
	}
	if f.size < 0 {
		return 0, f.err
	}
	return int64(f.size), nil
}

// ReadAt reads from a file's MP3, starting its conversion if need be and
// waiting for it to reach the bytes asked for. A read at or past the end gets
// no data, as for any file.
func (t *mp3Transcoder) ReadAt(path string, offset, count uint32) ([]byte, error) {
	f := t.file(path)
	start, end := int64(offset), int64(offset)+int64(count)
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		if f.size >= 0 {
			size, have := int64(f.size), int64(len(f.data))
			if start >= size {
				return nil, nil
			}
			if have >= min(end, size) {
				return f.data[start:min(end, have)], nil
			}
			if tailAt := size - int64(len(f.tail)); start >= tailAt {
				return f.tail[start-tailAt : min(end, size)-tailAt], nil
			}
		}
		if f.done {
			return nil, f.err
		}
		f.cond.Wait()
	}
}

// file returns path's MP3, starting its conversion if it has none, and
// counts a read of it.
func (t *mp3Transcoder) file(path string) *mp3File {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.files[path]
	if f == nil {
		f = &mp3File{size: -1}
		f.cond.L = &f.mu
		t.files[path] = f
		t.running.Add(1)
		go t.convert(path, f)
	}
	t.seq++
	f.lastRead, f.readAt = t.seq, t.now()
	return f
}

// convert fills f with path's MP3, then counts it against the budget, or
// forgets it if the conversion failed, so the next read tries again.
func (t *mp3Transcoder) convert(path string, f *mp3File) {
	defer t.running.Done()
	log.Printf("nfs: transcoding to MP3: %s", path)
	err := f.fill(t.start, path)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		log.Printf("nfs: transcode error: %s: %v", path, err)
		if t.files[path] == f {
			delete(t.files, path)
		}
		return
	}
	f.held = f.size // fill set it, on this goroutine, and it no longer changes
	log.Printf("nfs: transcode complete: %s -> MP3 (%d bytes)", path, f.held)
	t.total += f.held
	t.evictLocked(f)
}

// fill starts path's conversion and copies the MP3 into f as it comes,
// waking the readers waiting for it. A body that comes out longer or shorter
// than predicted is cut or padded with zeros to fit, so the size a deck was
// given stays true.
func (f *mp3File) fill(start func(string) (conversion, error), path string) error {
	c, err := start(path)
	if err != nil {
		f.finish(err)
		return err
	}
	body := -1 // the body's predicted size
	f.mu.Lock()
	f.size = c.size
	if c.size >= 0 {
		f.data = make([]byte, 0, c.size) // never grown, so bytes handed out stay put
		f.tail = c.tail
		body = c.size - len(c.tail)
	}
	f.cond.Broadcast()
	f.mu.Unlock()

	extra := 0
	buf := make([]byte, 64<<10)
	for {
		n, rerr := c.body.Read(buf)
		if n > 0 {
			f.mu.Lock()
			chunk := buf[:n]
			if body >= 0 {
				keep := min(n, body-len(f.data))
				extra += n - keep
				chunk = chunk[:keep]
			}
			f.data = append(f.data, chunk...)
			f.cond.Broadcast()
			f.mu.Unlock()
		}
		if rerr != nil {
			if rerr != io.EOF {
				err = rerr
			}
			break
		}
	}
	if cerr := c.body.Close(); err == nil {
		err = cerr
	}
	if err == nil && body >= 0 {
		f.mu.Lock()
		if extra > 0 || len(f.data) < body {
			log.Printf("nfs: transcode of %s came out %d bytes, %d predicted; served as %d",
				path, len(f.data)+extra, body, body)
			f.data = f.data[:body] // the zeros make() left past len
		}
		f.data = append(f.data, f.tail...)
		f.mu.Unlock()
	}
	f.finish(err)
	return err
}

// finish marks f converted (or failed) and wakes its readers.
func (f *mp3File) finish(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		f.size = len(f.data)
	}
	f.done, f.err = true, err
	f.cond.Broadcast()
}

// evictLocked drops the least recently read MP3s while the total is over the
// budget, keeping keep (the one just converted) and any read in the last
// t.keepFor. Conversions still running don't count yet. The caller holds t.mu.
func (t *mp3Transcoder) evictLocked(keep *mp3File) {
	readBefore := t.now().Add(-t.keepFor)
	for t.total > t.max {
		var oldPath string
		var old *mp3File
		for path, f := range t.files {
			if f != keep && f.held > 0 && f.readAt.Before(readBefore) && (old == nil || f.lastRead < old.lastRead) {
				oldPath, old = path, f
			}
		}
		if old == nil {
			return
		}
		delete(t.files, oldPath)
		t.total -= old.held
		dlog.Debugf("nfs: dropped the MP3 of %s (%d bytes held)", oldPath, t.total)
	}
}

// startMP3 starts converting path to a stereo 320 kbps MP3 with ffmpeg and
// predicts its size, unless the file doesn't say how many samples it holds.
//
// The MP3 starts right with audio: no ID3 tag, whose size depends on the
// file's tags, and no cover art (-vn), which written to a pipe came out as a
// tag claiming to be empty with megabytes of picture after it. Decks get
// titles and art from the dbserver. It ends with a silent frame, as its
// tail.
func startMP3(path string) (conversion, error) {
	ctx := context.Background()
	rate, samples, err := probeSamples(ctx, path)
	if err != nil {
		return conversion{}, err
	}
	out := mp3Rate(rate)
	c := conversion{size: -1}
	if samples >= 0 {
		c.tail = silentFrame(out)
		c.size = mp3Size((samples*int64(out)+int64(rate)-1)/int64(rate), out) + len(c.tail)
	}
	c.body, err = ffmpeg.Stream(ctx,
		"-i", path,
		"-vn",
		"-ac", "2",
		"-ar", strconv.Itoa(out),
		"-f", "mp3",
		"-id3v2_version", "0",
		"-write_xing", "0",
		"-acodec", "libmp3lame",
		"-b:a", "320k",
		"-",
	)
	return c, err
}

// silentFrame is an MPEG-1 Layer III frame of silence, joint stereo at
// 320 kbps and rate (32, 44.1 or 48 kHz): a header, then side information
// and main data all zero, which code no sound.
func silentFrame(rate int) []byte {
	index := map[int]byte{44100: 0, 48000: 1, 32000: 2}[rate]
	frame := make([]byte, 144*320000/rate)
	copy(frame, []byte{0xff, 0xfb, 14<<4 | index<<2, 0x44})
	return frame
}

// probeSamples returns the sample rate of path's audio and how many samples
// it holds, or -1 if the file doesn't say (a FLAC written without its
// length).
func probeSamples(ctx context.Context, path string) (rate int, samples int64, err error) {
	out, err := ffmpeg.Probe(ctx, path, "stream=sample_rate,time_base,duration_ts", "default=noprint_wrappers=1")
	if err != nil {
		return 0, 0, err
	}
	v := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			v[k] = val
		}
	}
	rate, err = strconv.Atoi(v["sample_rate"])
	if err != nil || rate <= 0 {
		return 0, 0, fmt.Errorf("ffprobe: %s: no sample rate", path)
	}
	samples = -1
	if n, err := strconv.ParseInt(v["duration_ts"], 10, 64); err == nil && v["time_base"] == "1/"+v["sample_rate"] {
		samples = n
	}
	return rate, samples, nil
}

// mp3Rate is the sample rate a file is converted at: its own if a 320 kbps
// MP3 can have it (32, 44.1 or 48 kHz), else 44.1 kHz for rates in its family
// and 48 kHz for the rest, so a resampled file keeps a whole-number ratio
// and a predictable length.
func mp3Rate(rate int) int {
	switch {
	case rate == 32000 || rate == 44100 || rate == 48000:
		return rate
	case rate%11025 == 0:
		return 44100
	default:
		return 48000
	}
}

// mp3Size is the size of the MP3 ffmpeg's LAME encoder writes for samples at
// rate (32, 44.1 or 48 kHz) and 320 kbps, with no tag or Xing frame. It writes
// ceil(samples/1152)+1 frames (its delay and flush add one). A frame takes
// 144*320000/rate bytes, a fraction at 44.1 kHz that LAME makes up with a
// padding byte on each frame where the running remainder wraps, from the
// second frame on. Checked against ffmpeg for 66 lengths and rates (frame
// boundaries, mono, 22.05 to 96 kHz) and 12 FLAC, WAV and AIFF files.
func mp3Size(samples int64, rate int) int {
	frames := (samples+1151)/1152 + 1
	base, frac := int64(144*320000/rate), int64(144*320000%rate)
	return int(frames*base + ((frames-1)*frac+int64(rate)-1)/int64(rate))
}

// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"log"
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

// maxTranscodedBytes bounds the MP3s kept in memory. A 320 kbps MP3 takes
// 2.4 MB a minute, so this holds well over an hour of music. The least
// recently read go first, but never one read in the last keepReadFor.
const maxTranscodedBytes = 256 << 20

// keepReadFor is how long an MP3 is kept after its last read, whatever the
// budget: the deck that read it may be playing it or holding it cued, and
// would wait for it to be converted again (minutes, for a long mix).
const keepReadFor = 30 * time.Minute

// mp3Transcoder converts files to MP3, once each, and keeps the results
// within a byte budget.
type mp3Transcoder struct {
	convert func(path string) ([]byte, error) // ffmpegToMP3, or a test's
	now     func() time.Time                  // time.Now, or a test's
	max     int                               // byte budget (maxTranscodedBytes)
	keepFor time.Duration                     // keepReadFor

	mu    sync.Mutex
	files map[string]*mp3File
	total int    // bytes of converted MP3s held
	seq   uint64 // read counter, for least-recently-read eviction
}

// mp3File is a file's MP3, converted on first use. Once converted, data is
// never changed, so a reader holding the file can keep reading it after the
// transcoder has dropped it.
type mp3File struct {
	once     sync.Once
	data     []byte
	err      error
	lastRead uint64    // t.seq at the last read
	readAt   time.Time // t.now() at the last read
}

func newMP3Transcoder() *mp3Transcoder {
	return &mp3Transcoder{
		convert: ffmpegToMP3,
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

// Size returns the size of a file's MP3, converting it if need be.
func (t *mp3Transcoder) Size(path string) (int64, error) {
	data, err := t.mp3(path)
	return int64(len(data)), err
}

// ReadAt reads from a file's MP3, converting it if need be. A read at or past
// the end gets no data, as for any file.
func (t *mp3Transcoder) ReadAt(path string, offset, count uint32) ([]byte, error) {
	data, err := t.mp3(path)
	if err != nil {
		return nil, err
	}
	if int(offset) >= len(data) {
		return nil, nil
	}
	end := min(int(offset)+int(count), len(data))
	return data[offset:end], nil
}

// mp3 returns a file's MP3, converting it on first use: reads that arrive
// while it converts wait for that one conversion. A failed conversion isn't
// kept, so the next read tries again.
func (t *mp3Transcoder) mp3(path string) ([]byte, error) {
	t.mu.Lock()
	f := t.files[path]
	if f == nil {
		f = &mp3File{}
		t.files[path] = f
	}
	t.seq++
	f.lastRead, f.readAt = t.seq, t.now()
	t.mu.Unlock()

	f.once.Do(func() {
		log.Printf("nfs: transcoding to MP3: %s", path)
		data, err := t.convert(path)
		t.mu.Lock()
		defer t.mu.Unlock()
		f.data, f.err = data, err // under t.mu, which evictLocked reads them under
		if f.err != nil {
			log.Printf("nfs: transcode error: %s: %v", path, f.err)
			if t.files[path] == f {
				delete(t.files, path)
			}
			return
		}
		log.Printf("nfs: transcode complete: %s -> MP3 (%d bytes)", path, len(f.data))
		t.total += len(f.data)
		t.evictLocked(f)
	})
	return f.data, f.err
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
			if f != keep && f.data != nil && f.readAt.Before(readBefore) && (old == nil || f.lastRead < old.lastRead) {
				oldPath, old = path, f
			}
		}
		if old == nil {
			return
		}
		delete(t.files, oldPath)
		t.total -= len(old.data)
		dlog.Debugf("nfs: dropped the MP3 of %s (%d bytes held)", oldPath, t.total)
	}
}

// ffmpegToMP3 converts a file to a 320 kbps MP3.
func ffmpegToMP3(path string) ([]byte, error) {
	return ffmpeg.Run(context.Background(),
		"-i", path,
		"-f", "mp3",
		"-acodec", "libmp3lame",
		"-b:a", "320k",
		"-",
	)
}

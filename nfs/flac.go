// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// flacTranscoder lazily transcodes non-MP3 files to MP3 for NFS serving.
// The CDJ's NFS client has a 4-byte read alignment bug at 2048-byte
// read sizes that corrupts lossless streams (FLAC, WAV, AIFF). MP3
// tolerates this because its decoder can resync on frame boundaries.
type flacTranscoder struct {
	mu    sync.Mutex
	cache map[string]*wavData // path -> cached WAV
}

type wavData struct {
	data []byte
	err  error
	once sync.Once
}

func newFlacTranscoder() *flacTranscoder {
	return &flacTranscoder{
		cache: make(map[string]*wavData),
	}
}

// NeedsTranscode returns true if the file is a non-MP3 format that needs
// transcoding to survive the CDJ's NFS read alignment bug.
func NeedsTranscode(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".flac") ||
		strings.HasSuffix(lower, ".wav") ||
		strings.HasSuffix(lower, ".aiff") ||
		strings.HasSuffix(lower, ".aif")
}

// Size returns the transcoded WAV size for a FLAC file.
// Triggers transcoding if not already cached.
func (t *flacTranscoder) Size(path string) (int64, error) {
	w := t.getOrCreate(path)
	w.transcode(path)
	if w.err != nil {
		return 0, w.err
	}
	return int64(len(w.data)), nil
}

// ReadAt reads from the cached WAV transcoding of a FLAC file.
func (t *flacTranscoder) ReadAt(path string, offset, count uint32) ([]byte, error) {
	w := t.getOrCreate(path)
	w.transcode(path)
	if w.err != nil {
		return nil, w.err
	}

	if int(offset) >= len(w.data) {
		return nil, fmt.Errorf("offset %d beyond WAV size %d", offset, len(w.data))
	}
	end := int(offset) + int(count)
	if end > len(w.data) {
		end = len(w.data)
	}
	return w.data[offset:end], nil
}

func (t *flacTranscoder) getOrCreate(path string) *wavData {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w, ok := t.cache[path]; ok {
		return w
	}
	w := &wavData{}
	t.cache[path] = w
	return w
}

func (w *wavData) transcode(path string) {
	w.once.Do(func() {
		log.Printf("nfs: transcoding to MP3: %s", path)

		// Transcode to high-quality MP3. The CDJ's NFS client has a 4-byte
		// read alignment bug that corrupts lossless formats. MP3's frame
		// sync mechanism tolerates the misalignment.
		cmd := exec.Command("ffmpeg",
			"-i", path,
			"-f", "mp3",
			"-acodec", "libmp3lame",
			"-b:a", "320k",
			"-loglevel", "error",
			"-",
		)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			w.err = fmt.Errorf("ffmpeg transcode: %v: %s", err, stderr.String())
			log.Printf("nfs: FLAC transcode error: %v", w.err)
			return
		}

		w.data = stdout.Bytes()
		log.Printf("nfs: transcode complete: %s -> MP3 (%d bytes)", path, len(w.data))
	})
}

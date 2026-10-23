// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/internal/ffmpeg"
)

// browserAudioTypes maps the library FileType to a Content-Type for formats
// every desktop browser decodes natively and gaplessly. Anything else (AIFF,
// M4A/AAC whose priming offset varies between decoders, …) is transcoded to
// WAV through ffmpeg, the same decoder the analysis uses, so the browser
// position lines up with the beat grid and cue times.
var browserAudioTypes = map[string]string{
	"mp3":  "audio/mpeg",
	"wav":  "audio/wav",
	"flac": "audio/flac",
	"ogg":  "audio/ogg",
}

// transcodeLocks gives one mutex per source file: concurrent requests for the
// same track (the browser issues a probe plus a Range request) must not both
// encode into the cache, but different tracks transcode in parallel.
var transcodeLocks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: make(map[string]*sync.Mutex)}

// transcodeLock returns the per-file mutex for src, creating it on first use.
func transcodeLock(src string) *sync.Mutex {
	transcodeLocks.Lock()
	defer transcodeLocks.Unlock()
	mu, ok := transcodeLocks.m[src]
	if !ok {
		mu = &sync.Mutex{}
		transcodeLocks.m[src] = mu
	}
	return mu
}

// audioCacheMaxBytes caps the transcoded-WAV cache directory. When a new
// encode pushes the total over the cap, oldest-mtime entries are deleted
// (skipping the file just written) until it fits. 2 GiB is generous — a
// 6-minute track at 44.1 kHz stereo 16-bit is ~60 MB of WAV.
const audioCacheMaxBytes = 2 << 30

// handleAudio streams a library track to the web UI player:
// GET|HEAD /api/audio/{trackID}[?transcode=1]. Range requests are honoured so
// the browser can seek. transcode=1 forces the ffmpeg WAV path even for
// natively playable formats, to compare against the analysis decode.
// Only tracks in the library are served, never arbitrary paths.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/audio/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil || t.FilePath == "" {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(t.FilePath); err != nil {
		http.Error(w, "audio file missing", http.StatusNotFound)
		return
	}

	path := t.FilePath
	ctype, native := browserAudioTypes[strings.ToLower(t.FileType)]
	source := "direct"
	if !native || r.URL.Query().Get("transcode") == "1" {
		p, err := s.transcodedAudio(t.FilePath)
		if err != nil {
			log.Printf("api: audio transcode track %d: %v", trackID, err)
			http.Error(w, "transcode failed", http.StatusInternalServerError)
			return
		}
		path, ctype, source = p, "audio/wav", "transcoded"
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "audio file unreadable", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "audio file unreadable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Audio-Source", source)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// transcodedAudio returns a cached 16-bit WAV of src, creating it with ffmpeg
// on first use. The cache key includes size and mtime so an edited file is
// re-encoded; stale entries of the same source (previous size/mtime) are
// removed after a successful encode, and the directory is swept down to
// audioCacheMaxBytes so it cannot grow without bound.
func (s *Server) transcodedAudio(src string) (string, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	dir := s.CacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "audio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	h := fnv.New32a()
	h.Write([]byte(src))
	key := fmt.Sprintf("%08x", h.Sum32())
	dst := filepath.Join(dir, fmt.Sprintf("%s_%d_%d.wav", key, fi.Size(), fi.ModTime().UnixNano()))

	mu := transcodeLock(src)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	tmp := dst + ".part"
	start := time.Now()
	if _, err := ffmpeg.Run(context.Background(), "-y",
		"-i", src, "-vn", "-map", "0:a:0",
		"-c:a", "pcm_s16le", "-f", "wav", tmp,
	); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	log.Printf("api: transcoded %s to WAV in %v", filepath.Base(src), time.Since(start).Round(time.Millisecond))
	s.sweepAudioCache(dir, key, dst)
	return dst, nil
}

// sweepAudioCache drops stale entries of the same source (older size/mtime
// keys left behind by an edited file) and evicts oldest entries until the
// cache directory fits under audioCacheMaxBytes. Best-effort: failures are
// logged and never fail the request being served.
func (s *Server) sweepAudioCache(dir, key, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("api: audio cache sweep: %v", err)
		return
	}
	type cached struct {
		name  string
		size  int64
		mtime time.Time
	}
	var files []cached
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Stale key of the same source: hash matches, size/mtime don't.
		if strings.HasPrefix(e.Name(), key+"_") && e.Name() != filepath.Base(keep) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				log.Printf("api: audio cache: dropped stale entry %s", e.Name())
				continue
			}
		}
		files = append(files, cached{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= audioCacheMaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	base := filepath.Base(keep)
	for _, f := range files {
		if total <= audioCacheMaxBytes {
			break
		}
		if f.name == base {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.name)); err == nil {
			total -= f.size
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// logRing keeps the last N lines of server log output for the web UI's
// diagnostic view. It tees writes through to the underlying writer so
// stderr (or wherever log was pointed before) keeps getting everything.
type logRing struct {
	mu    sync.Mutex
	lines []logEntry
	cap   int
	seq   uint64
	tee   io.Writer
	carry []byte // bytes from the most recent partial write, no newline yet
}

type logEntry struct {
	Seq  uint64 `json:"seq"`
	Time int64  `json:"time"` // unix ms
	Line string `json:"line"`
}

func newLogRing(capacity int, tee io.Writer) *logRing {
	return &logRing{cap: capacity, tee: tee}
}

func (r *logRing) Write(p []byte) (int, error) {
	if r.tee != nil {
		r.tee.Write(p)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Concatenate any leftover from the previous write, split on '\n'.
	buf := append(r.carry, p...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			r.carry = append(r.carry[:0], buf...)
			break
		}
		line := string(buf[:i])
		buf = buf[i+1:]
		r.seq++
		r.lines = append(r.lines, logEntry{
			Seq:  r.seq,
			Time: time.Now().UnixMilli(),
			Line: line,
		})
		if len(r.lines) > r.cap {
			r.lines = r.lines[len(r.lines)-r.cap:]
		}
	}
	return len(p), nil
}

// since returns entries with Seq > since, or all of them if since==0.
func (r *logRing) since(since uint64) []logEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]logEntry, 0, len(r.lines))
	for _, e := range r.lines {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

// installLogTail wires the package-level log output through a ring
// buffer in addition to stderr so /api/diag/logs can serve recent
// lines. Idempotent: subsequent calls reuse the existing ring.
func (s *Server) installLogTail() {
	if s.logs != nil {
		return
	}
	tee := log.Writer()
	s.logs = newLogRing(500, tee)
	log.SetOutput(s.logs)
}

// DiagResponse is the runtime + library snapshot served at /api/diag.
// Numbers update on every poll so the UI can show a "live" view.
type DiagResponse struct {
	StartedUnixMs int64  `json:"started_unix_ms"`
	UptimeSec     int64  `json:"uptime_sec"`
	Linked        bool   `json:"linked"`
	GoVersion     string `json:"go_version"`
	Goroutines    int    `json:"goroutines"`
	HeapMB        uint64 `json:"heap_mb"`
	SysMB         uint64 `json:"sys_mb"`
	GCPauseUs     uint64 `json:"gc_pause_us"`
	GCCount       uint32 `json:"gc_count"`

	LibraryTracks    int   `json:"library_tracks"`
	AnalysisPending  int   `json:"analysis_pending"`
	AnalysisAnalyzed int   `json:"analysis_analyzed"`
	AnalysisCached   int   `json:"analysis_cached"`
	CacheDirBytes    int64 `json:"cache_dir_bytes"`

	DecodeOK        int `json:"decode_ok"`
	DecodeWarn      int `json:"decode_warn"`
	DecodeError     int `json:"decode_error"`
	DecodeUnchecked int `json:"decode_unchecked"`
}

func (s *Server) handleDiag(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	resp := DiagResponse{
		StartedUnixMs: s.started.UnixMilli(),
		UptimeSec:     int64(time.Since(s.started).Seconds()),
		Linked:        s.Device != nil && s.Device.Linked(),
		GoVersion:     runtime.Version(),
		Goroutines:    runtime.NumGoroutine(),
		HeapMB:        ms.HeapAlloc / (1 << 20),
		SysMB:         ms.Sys / (1 << 20),
		GCPauseUs:     ms.PauseNs[(ms.NumGC+255)%256] / 1000,
		GCCount:       ms.NumGC,
	}
	if s.Library != nil {
		resp.LibraryTracks = s.Library.TrackCount()
		// Decode-health histogram, walking the live track slice once.
		for _, t := range s.Library.Tracks() {
			switch t.DecodeStatus {
			case "ok":
				resp.DecodeOK++
			case "warn":
				resp.DecodeWarn++
			case "error":
				resp.DecodeError++
			default:
				resp.DecodeUnchecked++
			}
		}
	}
	if s.Analysis != nil {
		resp.AnalysisPending = int(s.Analysis.Pending())
		resp.AnalysisAnalyzed = s.Analysis.Count()
		resp.AnalysisCached = s.Analysis.CachedCount()
	}
	if s.CacheDir != "" {
		resp.CacheDirBytes = dirBytes(s.CacheDir)
	}
	writeJSON(w, resp)
}

func (s *Server) handleDiagLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, []logEntry{})
		return
	}
	var since uint64
	if q := r.URL.Query().Get("since"); q != "" {
		since, _ = strconv.ParseUint(q, 10, 64)
	}
	writeJSON(w, s.logs.since(since))
}

// handleDiagStatus returns the single-line activity status (export
// takes precedence over analysis), the same payload the CLI TUI puts
// on its status line. The web bottom-bar polls this.
func (s *Server) handleDiagStatus(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Kind string `json:"kind"` // "export", "analysis", or ""
		Text string `json:"text"`
	}{}
	if s.Device != nil && s.Device.Monitor != nil {
		if s.Device.Monitor.ExportStatus != nil {
			if t := s.Device.Monitor.ExportStatus(); t != "" {
				out.Kind = "export"
				out.Text = t
				writeJSON(w, out)
				return
			}
		}
		if s.Device.Monitor.AnalysisStatus != nil {
			if t := s.Device.Monitor.AnalysisStatus(); t != "" {
				out.Kind = "analysis"
				out.Text = t
			}
		}
	}
	writeJSON(w, out)
}

func dirBytes(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

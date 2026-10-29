// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// tryQueueAnalysis requests background analysis for a track that has none
// (the web UI's list thumbnails, a newly added track). The analysis queue
// joins requests for a track already queued or running and runs them behind
// any a deck or the web UI is waiting for. A cached analysis is served
// instead, and a track whose file is missing is skipped. Returns true if it
// requested an analysis. If filePath is empty, looks it up from the
// library/PDB.
func (s *Server) tryQueueAnalysis(trackID uint32, filePath string) bool {
	if s.Analysis == nil {
		return false
	}
	if filePath == "" {
		filePath = s.trackFilePath(trackID)
	}
	if filePath == "" {
		return false
	}
	s.Analysis.SetPath(trackID, filePath)
	// Try the on-disk cache before queuing a fresh AnalyzeTrack. Without
	// this, every page reload that fans out waveform PNG requests will
	// re-analyze every track from scratch (handleWaveformPNG calls Get,
	// which silently misses the cache when pathMap was empty, then queues
	// here). SetPath above lets Get find the .gob now.
	if r := s.Analysis.Get(trackID); r != nil {
		// A cache hit skips the queue, so apply its library-row writeback
		// here — without this, a deleted-and-re-added track (whose cache
		// entry survives, keyed by file path) keeps BPM/duration 0.
		s.applyAnalysisToTrack(trackID, r)
		return false
	}
	// A missing file can't be analyzed. Without this check, every waveform
	// request for the track (the web UI asks again on each render) queued
	// another ffmpeg run that failed the same way.
	if s.audioMissing(trackID, filePath) {
		return false
	}
	// The store writes the result back (OnAnalyzed), once.
	s.Analysis.Request(trackID, filePath, analysis.Background, nil)
	return true
}

// audioMissing reports whether a track's audio file is absent, flagging the
// library track as missing when it wasn't already, so the web UI shows it.
// The file is checked each time, so one that reappears is analyzed then.
func (s *Server) audioMissing(trackID uint32, filePath string) bool {
	if _, err := os.Stat(filePath); err == nil {
		return false
	}
	if s.Library != nil && s.Library.SetFileMissing(trackID, true) {
		log.Printf("api: track %d's file is missing, not analyzing: %s", trackID, filePath)
	}
	return true
}

// applyAnalysisToTrack writes a result back to its library track through
// Library.ApplyAnalysis (BPM, duration, key and bitrate, saving only on a
// change, so callers can invoke it on every analysis retrieval) and adopts
// its artwork.
//
// This is the single writeback point for every api route that lands an
// analysis result: the worker queue, the on-demand path, and a disk-cache
// hit. Before this, only the worker wrote back, so a track deleted and
// re-added (whose cache entry survives, keyed by file path) skipped the
// worker and kept BPM/duration/key zeroed forever — and the web UI scales
// the detail waveform by the row's duration, so the re-added track showed
// no waveform at all.
func (s *Server) applyAnalysisToTrack(trackID uint32, r *analysis.Result) {
	if s.Library == nil || r == nil {
		return
	}
	s.Library.ApplyAnalysis(trackID, library.AnalysisFacts{BPM: r.BPM, Duration: int(r.Duration), KeyCamelot: r.KeyCamelot})
	if r.Artwork != nil {
		s.Library.AdoptArtwork(trackID, "image/jpeg", r.Artwork)
	}
}

// getOrAnalyze returns the analysis result for a track, analyzing it on
// demand if the result is missing or was invalidated (stale cache version).
// The analysis goes through the analysis queue as a Foreground request; ctx
// (the HTTP request's) ends the wait, not the analysis.
func (s *Server) getOrAnalyze(ctx context.Context, trackID uint32) *analysis.Result {
	if s.Analysis == nil {
		return nil
	}
	if r := s.Analysis.Get(trackID); r != nil {
		s.applyAnalysisToTrack(trackID, r)
		return r
	}

	filePath := s.trackFilePath(trackID)
	if filePath == "" {
		return nil
	}

	s.Analysis.SetPath(trackID, filePath)

	// A locked track must never be re-analyzed on demand: serve its cached grid
	// even if the cache version or tempo range is stale. Only when there is no
	// cache at all do we fall through and analyze once (so locking a track that
	// was never analyzed still leaves it with a grid).
	if s.analysisLocked(trackID) {
		if r := s.Analysis.LoadStale(trackID); r != nil {
			s.applyAnalysisToTrack(trackID, r)
			return r
		}
		log.Printf("api: track %d is locked but has no cached analysis — analyzing once", trackID)
	} else if r := s.Analysis.Get(trackID); r != nil {
		s.applyAnalysisToTrack(trackID, r)
		return r // disk cache was valid after SetPath
	}

	// A missing file can't be analyzed; don't run ffmpeg for it on every
	// drawer or waveform request (cached analysis above is still served).
	if s.audioMissing(trackID, filePath) {
		return nil
	}
	log.Printf("api: on-demand analysis for track %d: %s", trackID, filePath)
	return s.Analysis.Analyze(ctx, trackID, filePath) // written back by the store's OnAnalyzed
}

// handleReanalyze invalidates the cached analysis for a track and
// kicks off a fresh analysis run. Synchronous — returns once the new
// result is ready (a few seconds for typical tracks). The fresh
// result replaces both the in-memory entry and the on-disk .gob cache.
// POST /api/analysis/reanalyze/{trackID}
func (s *Server) handleReanalyze(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/reanalyze/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	if s.Analysis == nil {
		http.Error(w, "analysis store not available", http.StatusServiceUnavailable)
		return
	}

	// parts selects what to recompute: "all" (default), "grid", or "waveform".
	// grid and waveform are independent, so either can be redone alone.
	parts := r.URL.Query().Get("parts")
	if parts == "" {
		parts = "all"
	}
	if parts != "all" && parts != "grid" && parts != "waveform" {
		http.Error(w, "parts must be 'all', 'grid', or 'waveform'", http.StatusBadRequest)
		return
	}

	// A locked track refuses anything that rewrites its grid; a waveform-only
	// re-analysis never touches the grid, so it is allowed.
	if parts != "waveform" && s.analysisLocked(trackID) {
		http.Error(w, "track analysis is locked; only a waveform re-analysis is allowed while locked", http.StatusConflict)
		return
	}

	var result *analysis.Result
	if parts == "all" {
		s.Analysis.Invalidate(trackID)
		log.Printf("api: reanalyzing track %d (all, user-requested)", trackID)
		result = s.getOrAnalyze(r.Context(), trackID) // honors the ANLZ importer for served USB tracks
	} else {
		filePath := s.trackFilePath(trackID)
		if filePath == "" {
			http.Error(w, "track not found", http.StatusNotFound)
			return
		}
		s.Analysis.SetPath(trackID, filePath)
		existing := s.Analysis.LoadStale(trackID) // keep the current grid/waveform
		pe := analysis.PartsGrid
		if parts == "waveform" {
			pe = analysis.PartsWaveform
		}
		log.Printf("api: reanalyzing track %d (%s, user-requested)", trackID, parts)
		r2, err := analysis.Reanalyze(filePath, existing, pe)
		if err == nil && r2 != nil {
			s.Analysis.Set(trackID, r2)
			s.applyAnalysisToTrack(trackID, r2)
			result = r2
		}
	}
	if result == nil {
		http.Error(w, "reanalysis failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":         true,
		"track_id":   trackID,
		"parts":      parts,
		"bpm":        result.BPM,
		"key":        result.KeyCamelot,
		"beat_count": len(result.Beats),
	})
}

// trackFilePath resolves a track's audio path from the library or PDB.
func (s *Server) trackFilePath(trackID uint32) string {
	if s.Library != nil {
		if t := s.Library.Track(trackID); t != nil {
			return t.FilePath
		}
	}
	if s.PDB != nil {
		if t := s.PDB.TrackByID(trackID); t != nil {
			return t.FilePath
		}
	}
	return ""
}

// handleAnalysis returns the analysis data for a track.
// GET /api/analysis/{trackID}
func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		return
	}

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	result := s.getOrAnalyze(r.Context(), trackID)
	if result == nil {
		http.Error(w, "analysis not found", http.StatusNotFound)
		return
	}

	type BeatInfo struct {
		TimeMs    float64 `json:"time_ms"`
		BeatInBar int     `json:"beat_in_bar"` // 1-4
	}

	// Effective beats: if the library track has a BPM/phase override
	// (PUT /api/tracks/{id}/beats), rebuild the beat array at the new
	// tempo so the zoom waveform's beat-grid overlay reflects what the
	// user actually set. Without this, /api/analysis/{id} keeps
	// returning the analyzer's original beats forever.
	effectiveBPM := result.BPM
	effectiveBeats := result.Beats
	downbeatIdx := result.DownbeatIndex
	if t := s.Library.Track(trackID); t != nil {
		if t.BPM > 0 && t.DetectedBPM > 0 && t.BPM != result.BPM {
			effectiveBPM = t.BPM
			interval := 60000.0 / effectiveBPM
			phase := 0.0
			if len(result.Beats) > 0 {
				phase = result.Beats[0]
			}
			durationMs := float64(result.Duration) * 1000
			// A response must not reuse the stored result's backing array:
			// decks and concurrent grid edits still hold that immutable grid.
			effectiveBeats = nil
			for tt := phase; tt < durationMs; tt += interval {
				effectiveBeats = append(effectiveBeats, tt)
			}
		}
		if t.BeatPhaseShift != 0 {
			downbeatIdx = ((downbeatIdx+t.BeatPhaseShift)%4 + 4) % 4
		}
	}

	// Build beat list with bar positions.
	var beats []BeatInfo
	for i, b := range effectiveBeats {
		beatInBar := ((i - downbeatIdx) % 4)
		if beatInBar < 0 {
			beatInBar += 4
		}
		beats = append(beats, BeatInfo{
			TimeMs:    b,
			BeatInBar: beatInBar + 1,
		})
	}

	// Get cues if available.
	var cues []CueInfo
	if s.Cues != nil {
		cues = s.Cues.GetCues(trackID)
	}
	if cues == nil {
		cues = []CueInfo{}
	}

	resp := struct {
		TrackID       uint32     `json:"track_id"`
		BPM           float64    `json:"bpm"`
		Key           string     `json:"key"`
		KeyStandard   string     `json:"key_standard"`
		Duration      uint16     `json:"duration_sec"`
		BeatCount     int        `json:"beat_count"`
		DownbeatIndex int        `json:"downbeat_index"`
		Beats         []BeatInfo `json:"beats"`
		Cues          []CueInfo  `json:"cues"`
	}{
		TrackID:       trackID,
		BPM:           effectiveBPM,
		Key:           result.KeyCamelot,
		KeyStandard:   result.KeyStandard,
		Duration:      result.Duration,
		BeatCount:     len(effectiveBeats),
		DownbeatIndex: downbeatIdx,
		Beats:         beats,
		Cues:          cues,
	}
	writeJSON(w, resp)
}

// GET /api/analysis/ext/{player}/{slot}/{trackID} — waveform + cues for a track
// a deck plays from another player's media (ANLZ downloaded over NFS). 404 until
// the download lands or when the track has no analysis.
func (s *Server) handleExtAnalysis(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.ExtAnalysis == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/analysis/ext/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.Error(w, "expected /api/analysis/ext/{player}/{slot}/{trackID}", http.StatusBadRequest)
		return
	}
	player, err1 := strconv.ParseUint(parts[0], 10, 8)
	slot, err2 := strconv.ParseUint(parts[1], 10, 8)
	trackID, err3 := strconv.ParseUint(parts[2], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil || trackID == 0 {
		http.Error(w, "invalid player/slot/trackID", http.StatusBadRequest)
		return
	}
	a := s.ExtAnalysis(uint8(player), uint8(slot), uint32(trackID))
	if a == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, struct {
		TrackID    uint32              `json:"track_id"`
		DurationMs uint32              `json:"duration_ms"`
		BPM        float64             `json:"bpm"`
		SampleRate int                 `json:"sample_rate"`
		Waveform   []WaveEntrySpectral `json:"waveform"`
		Cues       []CueInfo           `json:"cues"`
		Beats      []float64           `json:"beats,omitempty"` // beat grid ms, for the playhead
	}{
		TrackID:    uint32(trackID),
		DurationMs: a.DurationMs,
		BPM:        a.BPM,
		SampleRate: 150,
		Waveform:   decodePWV5(a.WaveDetail),
		Cues:       importedCuesToInfo(a.Cues),
		Beats:      a.Beats,
	})
}

// GET /api/tracks/{id}/phrases — list detected phrases (intro/up/down/chorus/outro)
// with start/end times in milliseconds for visual rendering.
func (s *Server) handleTrackPhrases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	type PhraseInfo struct {
		Kind       uint16  `json:"kind"`     // 1=intro, 2=up, 3=down, 5=chorus, 6=outro
		Name       string  `json:"name"`     // human label
		StartMs    uint32  `json:"start_ms"` // start time in milliseconds
		EndMs      uint32  `json:"end_ms"`   // end time in milliseconds
		Energy     float64 `json:"energy,omitempty"`
		HasVocal   bool    `json:"has_vocal"`   // contains singing at the default threshold
		VocalScore float64 `json:"vocal_score"` // raw score, for the UI's live sensitivity toggle
	}
	names := map[uint16]string{
		1: "INTRO", 2: "UP", 3: "DOWN", 5: "CHORUS", 6: "OUTRO",
	}
	out := []PhraseInfo{}
	if s.Analysis != nil {
		// Set the path first so Get can fall back to the on-disk analysis cache
		// on a cold first open. Without this, pathMap is empty for a track that
		// isn't already in memory, Get returns nil, and phrases stay blank until
		// some other request (e.g. /api/analysis, fetched in parallel) loads the
		// result — which is why they only appeared after re-opening the track.
		if s.Library != nil {
			if t := s.Library.Track(trackID); t != nil && t.FilePath != "" {
				s.Analysis.SetPath(trackID, t.FilePath)
			}
		}
		if res := s.Analysis.Get(trackID); res != nil && len(res.Phrases) > 0 && res.BPM > 0 {
			msPerBeat := 60000.0 / res.BPM
			// Map a 1-based grid beat to its actual time from the beat grid, so
			// phrase boundaries line up with the waveform/beats instead of being
			// anchored at t=0 (which dropped the lead-in before the first beat
			// and left phrases shifted left of the first downbeat). Falls back to
			// a linear estimate (extrapolated past the grid ends, or from 0 when
			// no grid is present).
			beatMs := func(beat int) float64 {
				n := len(res.Beats)
				if n == 0 {
					return float64(beat-1) * msPerBeat
				}
				if beat < 1 {
					return res.Beats[0] + float64(beat-1)*msPerBeat
				}
				if beat <= n {
					return res.Beats[beat-1]
				}
				return res.Beats[n-1] + float64(beat-n)*msPerBeat
			}
			// Prefer each phrase's absolute time (anchored to the audio at analysis
			// time), so it stays on the music when the beat grid is edited. Fall
			// back to mapping beat numbers through the current grid only for older
			// cached phrases that predate StartMs/EndMs.
			np := len(res.Phrases)
			starts := make([]float64, np)
			ends := make([]float64, np)
			for i, p := range res.Phrases {
				if p.EndMs > 0 {
					starts[i], ends[i] = p.StartMs, p.EndMs
				} else {
					starts[i], ends[i] = beatMs(p.StartBeat), beatMs(p.EndBeat)
				}
			}
			for i := range res.Phrases {
				name := names[res.Phrases[i].Kind]
				if name == "" {
					name = fmt.Sprintf("KIND %d", res.Phrases[i].Kind)
				}
				start, end := starts[i], ends[i]
				// Phrases are contiguous sections: each ends exactly where the next
				// begins. Snap the end to the next phrase's start so there's no gap
				// (covers older cached times computed before the EndBeat+1 fix too).
				if i+1 < np && starts[i+1] > start {
					end = starts[i+1]
				}
				if start < 0 {
					start = 0
				}
				if end < start {
					end = start
				}
				out = append(out, PhraseInfo{
					Kind:       res.Phrases[i].Kind,
					Name:       name,
					StartMs:    uint32(start),
					EndMs:      uint32(end),
					Energy:     res.Phrases[i].Energy,
					HasVocal:   res.Phrases[i].HasVocal,
					VocalScore: res.Phrases[i].VocalScore,
				})
			}
		}
	}
	writeJSON(w, out)
}

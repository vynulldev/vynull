// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"log"
	"math"
	"net/http"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/link/prolink"
)

// handleBeatGridAdjust shifts the beat grid by a given offset.
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "offset_ms": -25.5}
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "bpm": 130.00}
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "set_downbeat_ms": 4975.0}
func (s *Server) handleBeatGridAdjust(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TrackID       uint32   `json:"track_id"`
		OffsetMs      *float64 `json:"offset_ms"`       // shift all beats by this amount
		BPM           *float64 `json:"bpm"`             // set new BPM (rebuilds grid)
		SetDownbeatMs *float64 `json:"set_downbeat_ms"` // set beat 1 at this position
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TrackID == 0 {
		http.Error(w, "track_id required", http.StatusBadRequest)
		return
	}
	if s.analysisLocked(req.TrackID) {
		http.Error(w, "track analysis is locked; unlock it to adjust the beat grid", http.StatusConflict)
		return
	}

	stored := s.getOrAnalyze(r.Context(), req.TrackID)
	if stored == nil {
		http.Error(w, "analysis not found", http.StatusNotFound)
		return
	}
	// Edit a copy, stored by Set below: the stored result may be being served
	// to a deck meanwhile, and must not change under it.
	result := stored.CopyForEdit()

	// Anchor phrases to absolute time from the CURRENT (pre-edit) grid for older
	// phrases that lack it, so the grid edits below don't drag the phrase strip
	// off the audio. New analyses already carry StartMs/EndMs; this only fills in
	// pre-existing cached results, and Set() below persists it.
	if len(result.Phrases) > 0 && result.Phrases[0].EndMs <= 0 {
		analysis.SetPhraseTimes(result.Phrases, result.Beats, result.BPM)
	}

	modified := false

	// Shift all beats by offset.
	if req.OffsetMs != nil {
		offset := *req.OffsetMs
		for i := range result.Beats {
			result.Beats[i] += offset
		}
		result.BeatGrid = nil // invalidate cached beat grid blobs
		result.BeatGridPQT2 = nil
		modified = true
		log.Printf("api: beat grid for track %d shifted by %.1fms", req.TrackID, offset)
	}

	// Set new BPM — rebuild grid keeping the same phase.
	if req.BPM != nil && *req.BPM > 0 {
		newBPM := *req.BPM
		newInterval := 60000.0 / newBPM
		phase := 0.0
		if len(result.Beats) > 0 {
			phase = result.Beats[0]
		}
		durationMs := float64(result.Duration) * 1000
		result.Beats = result.Beats[:0]
		for t := phase; t < durationMs; t += newInterval {
			result.Beats = append(result.Beats, t)
		}
		result.BPM = newBPM
		result.BeatGrid = nil
		result.BeatGridPQT2 = nil
		modified = true
		log.Printf("api: beat grid for track %d BPM set to %.2f", req.TrackID, newBPM)
	}

	// Set downbeat position — shift grid so that a beat lands at the given time.
	if req.SetDownbeatMs != nil {
		target := *req.SetDownbeatMs
		interval := 60000.0 / result.BPM
		// Find the current beat nearest to the target.
		if len(result.Beats) > 0 {
			nearestIdx := 0
			nearestDist := math.Abs(result.Beats[0] - target)
			for i, b := range result.Beats {
				d := math.Abs(b - target)
				if d < nearestDist {
					nearestDist = d
					nearestIdx = i
				}
			}
			offset := target - result.Beats[nearestIdx]
			for i := range result.Beats {
				result.Beats[i] += offset
			}
			// Set the downbeat index so this beat becomes beat 1.
			result.DownbeatIndex = nearestIdx
			result.BeatGrid = nil
			result.BeatGridPQT2 = nil
			modified = true
			log.Printf("api: beat grid for track %d downbeat set to %.1fms (shifted %.1fms, beat %d)",
				req.TrackID, target, offset, nearestIdx)

			// Rebuild beats forward/backward from new phase to cover full track.
			newPhase := math.Mod(result.Beats[0], interval)
			if newPhase < 0 {
				newPhase += interval
			}
			durationMs := float64(result.Duration) * 1000
			result.Beats = result.Beats[:0]
			startPhase := newPhase
			for startPhase-interval >= 0 {
				startPhase -= interval
			}
			downbeatSet := false
			for t := startPhase; t < durationMs; t += interval {
				result.Beats = append(result.Beats, t)
				if !downbeatSet && t >= target-interval/2 {
					result.DownbeatIndex = len(result.Beats) - 1
					downbeatSet = true
				}
			}
		}
	}

	if modified {
		// Mark the grid as user-edited so the dbserver serves our regenerated
		// blobs instead of the (now-stale) on-disk ANLZ .EXT/.DAT files.
		result.GridEdited = true
		// Regenerate beat grid blobs from updated beats.
		if len(result.Beats) > 0 {
			beatResult := &analysis.BeatResult{
				BPM:      result.BPM,
				Beats:    result.Beats,
				Downbeat: result.Beats[0],
			}
			if result.DownbeatIndex >= 0 && result.DownbeatIndex < len(result.Beats) {
				beatResult.Downbeat = result.Beats[result.DownbeatIndex]
			}
			result.BeatGrid = analysis.GenerateBeatGridFromBeats(beatResult)
			result.BeatGridPQT2 = prolink.GeneratePQT2(result.BPM, result.Beats, result.DownbeatIndex)
		}
		s.Analysis.Set(req.TrackID, result)
	}

	writeJSON(w, struct {
		OK    bool    `json:"ok"`
		BPM   float64 `json:"bpm"`
		Beats int     `json:"beats"`
		Phase float64 `json:"phase_ms"`
	}{
		OK:    true,
		BPM:   result.BPM,
		Beats: len(result.Beats),
		Phase: func() float64 {
			if len(result.Beats) > 0 {
				return result.Beats[0]
			}
			return 0
		}(),
	})
}

// GET/PUT /api/tracks/{id}/beats — beat-grid overrides
//
//	GET:  returns { bpm, detected_bpm, beat_phase_shift }
//	PUT:  body { bpm?: number, beat_phase_shift?: int, clear?: bool }
//	      - bpm omitted/0 keeps current effective BPM
//	      - beat_phase_shift in 0..3
//	      - clear: true reverts BPM to detected_bpm and phase to 0
func (s *Server) handleTrackBeats(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			BPM            float64 `json:"bpm"`
			DetectedBPM    float64 `json:"detected_bpm"`
			BeatPhaseShift int     `json:"beat_phase_shift"`
		}{t.BPM, t.DetectedBPM, t.BeatPhaseShift})

	case "PUT", "POST":
		if t.AnalysisLocked {
			http.Error(w, "track analysis is locked; unlock it to edit the beat grid", http.StatusConflict)
			return
		}
		var req struct {
			BPM            *float64 `json:"bpm"`
			BeatPhaseShift *int     `json:"beat_phase_shift"`
			Clear          bool     `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, ok := s.updateTrack(w, trackID, func(t *library.Track) error {
			// Checked again under the lock: the track may have been locked
			// since the check above.
			if t.AnalysisLocked {
				return &editError{http.StatusConflict, "track analysis is locked; unlock it to edit the beat grid"}
			}
			if req.Clear {
				if t.DetectedBPM > 0 {
					t.BPM = t.DetectedBPM
				}
				// Also clear DetectedBPM so the UI no longer reports the
				// track as overridden (next BPM edit re-snapshots).
				t.DetectedBPM = 0
				t.BeatPhaseShift = 0
				return nil
			}
			if req.BPM != nil && *req.BPM > 0 {
				// Snapshot the detected BPM the first time we override,
				// so a later "clear" can restore it.
				if t.DetectedBPM == 0 {
					t.DetectedBPM = t.BPM
				}
				t.BPM = *req.BPM
			}
			if req.BeatPhaseShift != nil {
				v := *req.BeatPhaseShift % 4
				if v < 0 {
					v += 4
				}
				t.BeatPhaseShift = v
			}
			return nil
		})
		if !ok {
			return
		}
		if s.Device != nil {
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct {
			OK             bool    `json:"ok"`
			BPM            float64 `json:"bpm"`
			DetectedBPM    float64 `json:"detected_bpm"`
			BeatPhaseShift int     `json:"beat_phase_shift"`
		}{true, updated.BPM, updated.DetectedBPM, updated.BeatPhaseShift})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// analysisLocked reports whether a track's grid is protected from re-analysis
// and grid edits. False when there is no library or no such track.
func (s *Server) analysisLocked(trackID uint32) bool {
	if s.Library == nil {
		return false
	}
	t := s.Library.Track(trackID)
	return t != nil && t.AnalysisLocked
}

// GET/PUT /api/tracks/{id}/lock — the per-track analysis lock. While locked,
// the daemon refuses re-analysis and grid edits (409) and will not auto- or
// re-analyze the track, so a hand-corrected grid cannot be overwritten.
func (s *Server) handleTrackLock(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			Locked bool `json:"locked"`
		}{t.AnalysisLocked})
	case "PUT", "POST":
		var req struct {
			Locked bool `json:"locked"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := s.updateTrack(w, trackID, func(t *library.Track) error { t.AnalysisLocked = req.Locked; return nil }); !ok {
			return
		}
		log.Printf("api: track %d analysis lock = %v", trackID, req.Locked)
		writeJSON(w, map[string]bool{"OK": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

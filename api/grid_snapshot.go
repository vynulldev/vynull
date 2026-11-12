// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/link/prolink"
)

// gridSnapshot is a track's exact grid and BPM settings, which Undo and Redo
// put back: a grid rebuilt from the BPM alone loses a variable tempo.
type gridSnapshot struct {
	Version        int       `json:"version"`
	FilePath       string    `json:"file_path"`
	BPM            float64   `json:"bpm"`
	Duration       uint16    `json:"duration_sec"`
	DownbeatIndex  int       `json:"downbeat_index"`
	Beats          []float64 `json:"beats_ms"`
	LibraryBPM     float64   `json:"library_bpm"`
	DetectedBPM    float64   `json:"detected_bpm"`
	BeatPhaseShift int       `json:"beat_phase_shift"`
}

func snapshotGrid(t *library.Track, r *analysis.Result) *gridSnapshot {
	if t == nil || r == nil {
		return nil
	}
	return &gridSnapshot{1, t.FilePath, r.BPM, r.Duration, r.DownbeatIndex, r.Beats,
		t.BPM, t.DetectedBPM, t.BeatPhaseShift}
}

func (g *gridSnapshot) valid() bool {
	if g == nil || g.Version != 1 || g.FilePath == "" || len(g.Beats) > 1_000_000 ||
		g.DownbeatIndex < 0 || g.DownbeatIndex > 1_000_000 || g.BeatPhaseShift < 0 || g.BeatPhaseShift > 3 {
		return false
	}
	for _, bpm := range []float64{g.BPM, g.LibraryBPM, g.DetectedBPM} {
		if math.IsNaN(bpm) || math.IsInf(bpm, 0) || bpm < 0 || bpm > 1000 {
			return false
		}
	}
	if len(g.Beats) > 0 && (g.BPM <= 0 || g.Duration == 0) {
		return false
	}
	for i, beat := range g.Beats {
		if math.IsNaN(beat) || math.IsInf(beat, 0) || (i > 0 && beat <= g.Beats[i-1]) {
			return false
		}
	}
	return true
}

func (g *gridSnapshot) equal(other *gridSnapshot) bool {
	return g != nil && other != nil && g.Version == other.Version && g.FilePath == other.FilePath &&
		g.BPM == other.BPM && g.Duration == other.Duration && g.DownbeatIndex == other.DownbeatIndex &&
		g.LibraryBPM == other.LibraryBPM && g.DetectedBPM == other.DetectedBPM &&
		g.BeatPhaseShift == other.BeatPhaseShift && slices.Equal(g.Beats, other.Beats)
}

// POST /api/analysis/beatgrid/restore {track_id, expected, restore}: puts a
// saved grid back if the track's grid is still expected. Only the grid and BPM
// settings change.
func (s *Server) handleBeatGridRestore(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if s.Library == nil || s.Analysis == nil {
		jsonError(w, "library or analysis unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("X-Vynull-Library-Session") == "" {
		jsonError(w, "library session required", http.StatusBadRequest)
		return
	}
	var req struct {
		TrackID  uint32        `json:"track_id"`
		Expected *gridSnapshot `json:"expected"`
		Restore  *gridSnapshot `json:"restore"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.TrackID == 0 || !req.Expected.valid() || !req.Restore.valid() {
		jsonError(w, "invalid grid snapshot", http.StatusBadRequest)
		return
	}
	if req.Restore.FilePath != req.Expected.FilePath || req.Restore.Duration != req.Expected.Duration {
		jsonError(w, "snapshot source or duration differs", http.StatusConflict)
		return
	}
	stored := s.Analysis.Get(req.TrackID)
	if stored == nil {
		jsonError(w, "analysis not found", http.StatusNotFound)
		return
	}
	result := stored.CopyForEdit()
	if len(result.Phrases) > 0 && result.Phrases[0].EndMs <= 0 {
		analysis.SetPhraseTimes(result.Phrases, result.Beats, result.BPM)
	}
	target := req.Restore
	result.Beats = slices.Clone(target.Beats)
	result.BPM = target.BPM
	result.DownbeatIndex = target.DownbeatIndex
	result.GridEdited = true
	result.BeatGrid, result.BeatGridPQT2 = nil, nil
	if len(result.Beats) > 0 {
		downbeat := result.Beats[0]
		if result.DownbeatIndex < len(result.Beats) {
			downbeat = result.Beats[result.DownbeatIndex]
		}
		result.BeatGrid = analysis.GenerateBeatGridFromBeats(&analysis.BeatResult{BPM: result.BPM, Beats: result.Beats, Downbeat: downbeat})
		result.BeatGridPQT2 = prolink.GeneratePQT2(result.BPM, result.Beats, result.DownbeatIndex)
	}
	// Check and swap under the library lock, so no edit lands in between. The
	// analysis is read before it: a disk read can call back into the library.
	if _, ok := s.updateTrack(w, req.TrackID, func(t *library.Track) error {
		if t.AnalysisLocked {
			return &editError{http.StatusConflict, "track analysis is locked"}
		}
		if !snapshotGrid(t, stored).equal(req.Expected) {
			return &editError{http.StatusConflict, "grid or source changed; reload before restoring"}
		}
		if !s.Analysis.CompareAndSwap(req.TrackID, stored, result) {
			return &editError{http.StatusConflict, "analysis changed; reload before restoring"}
		}
		t.BPM, t.DetectedBPM, t.BeatPhaseShift = target.LibraryBPM, target.DetectedBPM, target.BeatPhaseShift
		return nil
	}); !ok {
		return
	}
	s.Analysis.Save(req.TrackID)
	if s.Device != nil {
		s.Device.BroadcastTrackRefresh(req.TrackID)
	}
	ok(w)
}

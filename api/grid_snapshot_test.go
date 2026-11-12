// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// gridRestoreFixture is a server with one track whose grid has a leading
// negative beat and uneven intervals.
func gridRestoreFixture() *Server {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Keep title", FilePath: "/music/one.flac", BPM: 120, Rating: 4, DetectedBPM: 119, BeatPhaseShift: 1})
	store := analysis.NewStore()
	store.Set(1, &analysis.Result{BPM: 120, Duration: 3, DownbeatIndex: 2, Beats: []float64{-100, 400, 925, 1460, 2000, 2540}, WaveDetail: []byte{1, 2, 3}, Phrases: []analysis.Phrase{{StartBeat: 1, EndBeat: 4, StartMs: 400, EndMs: 2000}}})
	return &Server{Library: lib, Analysis: store}
}

// readGridSnapshot reads track 1's grid_snapshot from GET /api/analysis.
func readGridSnapshot(t *testing.T, s *Server) *gridSnapshot {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/analysis/1", nil))
	if w.Code != 200 {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	var body struct {
		Snapshot *gridSnapshot `json:"grid_snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Snapshot.valid() {
		t.Fatalf("snapshot: %s / %v", w.Body, err)
	}
	return body.Snapshot
}

// restoreGridRequest posts a restore for track 1.
func restoreGridRequest(s *Server, expected, restore *gridSnapshot, session string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"track_id": 1, "expected": expected, "restore": restore})
	r := httptest.NewRequest("POST", "/api/analysis/beatgrid/restore", bytes.NewReader(body))
	r.Header.Set("X-Vynull-Library-Session", session)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// Undo and Redo put back the exact grid, uneven beats included, and keep
// everything else.
func TestGridRestoreRoundTripPreservesVariableBeatsAndOtherData(t *testing.T) {
	s := gridRestoreFixture()
	original := readGridSnapshot(t, s)
	served := s.Analysis.Get(1)
	steady := *original
	steady.BPM = 60
	steady.LibraryBPM = 60
	steady.DetectedBPM = 120
	steady.BeatPhaseShift = 3
	steady.Beats = []float64{-75, 925, 1925, 2925}
	for _, target := range []*gridSnapshot{&steady, original, &steady, original} {
		before := readGridSnapshot(t, s)
		w := restoreGridRequest(s, before, target, s.Library.SessionID())
		if w.Code != 200 {
			t.Fatalf("restore: %d %s", w.Code, w.Body)
		}
		if got := readGridSnapshot(t, s); !got.equal(target) {
			t.Fatalf("restored %+v, want %+v", got, target)
		}
		result := s.Analysis.Get(1)
		if !result.GridEdited || len(result.BeatGrid) == 0 || len(result.BeatGridPQT2) == 0 {
			t.Fatal("grid blobs not regenerated")
		}
		if !slices.Equal(result.WaveDetail, served.WaveDetail) || !slices.Equal(result.Phrases, served.Phrases) {
			t.Fatal("waveform/phrases changed")
		}
		track := s.Library.Track(1)
		if track.Title != "Keep title" || track.Rating != 4 || track.AnalysisLocked {
			t.Fatal("unrelated track fields changed")
		}
	}
	if !slices.Equal(served.Beats, original.Beats) || served.GridEdited {
		t.Fatal("old readers were mutated")
	}
}

// A restore that doesn't match, or isn't valid, changes nothing.
func TestGridRestoreRejectsInvalidStaleLockedAndWrongSession(t *testing.T) {
	for _, name := range []string{"session", "missing-session", "locked", "source", "bpm", "phase", "beats", "duration", "invalid-version", "unsorted", "invalid-phase", "missing-restore"} {
		t.Run(name, func(t *testing.T) {
			s := gridRestoreFixture()
			expected := readGridSnapshot(t, s)
			target := *expected
			target.Beats = []float64{-100, 900, 1900, 2900}
			target.BPM = 60
			target.LibraryBPM = 60
			session := s.Library.SessionID()
			switch name {
			case "session":
				session = "another-library"
			case "missing-session":
				session = ""
			case "locked":
				s.Library.Update(1, func(t *library.Track) error { t.AnalysisLocked = true; return nil })
			case "source":
				s.Library.Update(1, func(t *library.Track) error { t.FilePath = "/different.flac"; return nil })
			case "bpm":
				s.Library.Update(1, func(t *library.Track) error { t.BPM = 124; return nil })
			case "phase":
				s.Library.Update(1, func(t *library.Track) error { t.BeatPhaseShift = 2; return nil })
			case "beats":
				changed := s.Analysis.Get(1).CopyForEdit()
				changed.Beats[1]++
				s.Analysis.Set(1, changed)
			case "duration":
				target.Duration++
			case "invalid-version":
				target.Version = 2
			case "unsorted":
				target.Beats = []float64{100, 50}
			case "invalid-phase":
				target.BeatPhaseShift = 4
			}
			before := snapshotGrid(s.Library.Track(1), s.Analysis.Get(1))
			restore := &target
			if name == "missing-restore" {
				restore = nil
			}
			w := restoreGridRequest(s, expected, restore, session)
			if w.Code != 400 && w.Code != 409 {
				t.Fatalf("status: %d %s", w.Code, w.Body)
			}
			if !before.equal(snapshotGrid(s.Library.Track(1), s.Analysis.Get(1))) {
				t.Fatal("refused restore changed data")
			}
		})
	}
}

// A restore reaches the disk cache, once the library lock is released.
func TestGridRestoreSavesToCache(t *testing.T) {
	dir := t.TempDir()
	s := gridRestoreFixture()
	stored := analysis.NewStoreWithCache(dir)
	stored.SetPath(1, "/music/one.flac")
	stored.Set(1, s.Analysis.Get(1))
	s.Analysis = stored
	target := readGridSnapshot(t, s)
	target.Beats = []float64{-100, 900, 1900, 2900}
	target.BPM, target.LibraryBPM = 60, 60
	if w := restoreGridRequest(s, readGridSnapshot(t, s), target, s.Library.SessionID()); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	reader := analysis.NewStoreWithCache(dir)
	reader.SetKeep(func(uint32) bool { return true }) // the fixture's result has no cache version
	reader.SetPath(1, "/music/one.flac")
	if got := reader.Get(1); got == nil || !slices.Equal(got.Beats, target.Beats) || got.BPM != 60 {
		t.Fatalf("cache holds %+v, want the restored grid", got)
	}
}

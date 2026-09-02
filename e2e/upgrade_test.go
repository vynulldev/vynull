// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vynulldev/vynull/analysis"
)

// TestUpgradeReanalysis covers the analysis-cache upgrade path:
// a data-dir whose analysis cache predates the current cacheVersion gets a
// re-analysis pass when its tracks are next touched, the stale fractional
// BPM on the library row FOLLOWS the re-analysis (the writeback change),
// and a manual BPM override survives it.
//
// The stale state is manufactured rather than restored from a fixture: the
// suite analyzes fresh, stops the server, rewrites the cache gob with an
// ancient CacheVersion and a pre-snap fractional BPM, edits the row to
// match, and restarts. That reproduces exactly what a v26→v27 upgrade
// looks like on disk, without shipping binary fixtures that rot.
func TestUpgradeReanalysis(t *testing.T) {
	dataDir := t.TempDir()
	media := t.TempDir()
	stale := kickWAV(t, media, "stale.wav", 124)
	overridden := kickWAV(t, media, "overridden.wav", 128)

	// Phase 1: fresh analysis, then set a manual override on one track.
	s := startServer(t, dataDir)
	s.addTracks(stale, overridden)
	s.waitFor("initial analysis", 2*time.Minute, func() bool {
		n := 0
		for _, tr := range s.tracks() {
			if tr.BPM > 0 {
				n++
			}
		}
		return n == 2
	})
	var overriddenID, staleID uint32
	for _, tr := range s.tracks() {
		switch tr.Title {
		case "overridden":
			overriddenID = tr.ID
		case "stale":
			staleID = tr.ID
		}
	}
	// Manual override: user pins 130 over the detected 128.
	req, _ := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/api/tracks/%d/beats", s.baseURL, overriddenID),
		bytes.NewReader([]byte(`{"bpm": 130}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("set override: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	s.stop()

	// Phase 2: age the on-disk state to look like a pre-snap library.
	// Stale cache entry: ancient CacheVersion, fractional BPM.
	h := sha256.Sum256([]byte(stale))
	gobPath := filepath.Join(dataDir, "analysis", fmt.Sprintf("%x.gob", h[:8]))
	if _, err := os.Stat(gobPath); err != nil {
		t.Fatalf("expected cache gob at %s: %v", gobPath, err)
	}
	var gb bytes.Buffer
	if err := gob.NewEncoder(&gb).Encode(&analysis.Result{CacheVersion: 1, BPM: 123.77, Duration: 30}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gobPath, gb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// Matching stale row BPM in library.json.
	libPath := filepath.Join(dataDir, "library.json")
	raw, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["title"] == "stale" {
			row["bpm"] = 123.77
		}
	}
	edited, _ := json.Marshal(rows)
	if err := os.WriteFile(libPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	// Phase 3: restart and touch both tracks — the stale cache must be
	// discarded (version mismatch), re-analysis runs, and the row follows.
	s = startServer(t, dataDir)
	for _, id := range []uint32{staleID, overriddenID} {
		// The waveform request is what a UI view or deck load does; it
		// routes through getOrAnalyze and triggers the re-analysis.
		http.Get(fmt.Sprintf("%s/api/analysis/waveform-png/%d?type=detail&w=64&h=16", s.baseURL, id))
	}
	s.waitFor("stale row re-analyzed to 124", 2*time.Minute, func() bool {
		for _, tr := range s.tracks() {
			if tr.ID == staleID {
				return tr.BPM == 124
			}
		}
		return false
	})
	for _, tr := range s.tracks() {
		switch tr.ID {
		case staleID:
			if tr.BPM != 124 {
				t.Errorf("stale row BPM %v after re-analysis, want 124", tr.BPM)
			}
		case overriddenID:
			if tr.BPM != 130 {
				t.Errorf("override lost through re-analysis: BPM %v, want 130", tr.BPM)
			}
			// detected_bpm lives on the beats endpoint, not the list payload.
			var beats struct {
				DetectedBPM float64 `json:"detected_bpm"`
			}
			s.getJSON(fmt.Sprintf("/api/tracks/%d/beats", overriddenID), &beats)
			if beats.DetectedBPM == 0 {
				t.Errorf("override snapshot (detected_bpm) missing from /beats")
			}
		}
	}
}

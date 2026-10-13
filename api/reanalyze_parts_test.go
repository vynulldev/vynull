// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// Covers the reanalyze `parts` contract and its interaction with the lock: a
// locked track refuses grid/all re-analysis (409) but permits a waveform-only
// re-analysis (which never touches the grid), and an invalid parts value is a
// 400. Verifies the gating and routing, not the DSP output.
func TestReanalyzeParts(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "T", FilePath: "/music/missing.flac", BPM: 128, AnalysisLocked: true})
	srv := &Server{Library: lib, Analysis: analysis.NewStore()}
	h := srv.Handler()

	post := func(path string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		return w.Code
	}

	// Invalid parts → 400, before any lock or analysis work.
	if c := post("/api/analysis/reanalyze/1?parts=bogus"); c != http.StatusBadRequest {
		t.Errorf("parts=bogus: want 400, got %d", c)
	}

	// Locked: grid and all are refused with 409.
	if c := post("/api/analysis/reanalyze/1?parts=grid"); c != http.StatusConflict {
		t.Errorf("locked parts=grid: want 409, got %d", c)
	}
	if c := post("/api/analysis/reanalyze/1"); c != http.StatusConflict { // default = all
		t.Errorf("locked parts=all: want 409, got %d", c)
	}

	// Locked: waveform is allowed past the lock gate. The file does not exist,
	// so analysis then fails with 500 — the point is it is NOT a 409.
	if c := post("/api/analysis/reanalyze/1?parts=waveform"); c == http.StatusConflict {
		t.Errorf("locked parts=waveform: should pass the lock gate, got 409")
	}
}

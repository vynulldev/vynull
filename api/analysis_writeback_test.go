// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// Covers the BPM re-analysis writeback in applyAnalysisToTrack: a row whose
// BPM came from a previous analysis follows the new result (the served beat
// grid always comes from the analysis, so the row must not diverge), while a
// user override (marked by the DetectedBPM snapshot) is never clobbered.
func TestApplyAnalysisBPMWriteback(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Stale", FilePath: "/m/a.mp3", BPM: 120.85})
	lib.AddTrack(&library.Track{ID: 2, Title: "Overridden", FilePath: "/m/b.mp3", BPM: 122, DetectedBPM: 121})
	lib.AddTrack(&library.Track{ID: 3, Title: "Empty", FilePath: "/m/c.mp3"})
	srv := &Server{Library: lib}

	// Re-analysis produced the snapped tempo: a non-overridden row follows it.
	srv.applyAnalysisToTrack(1, &analysis.Result{BPM: 121})
	if got := lib.Track(1).BPM; got != 121 {
		t.Errorf("stale row BPM = %v, want 121 (re-analysis writeback)", got)
	}

	// A user override survives re-analysis untouched.
	srv.applyAnalysisToTrack(2, &analysis.Result{BPM: 121})
	if got := lib.Track(2).BPM; got != 122 {
		t.Errorf("overridden row BPM = %v, want 122 (user override kept)", got)
	}

	// The original fill-when-empty behaviour still works.
	srv.applyAnalysisToTrack(3, &analysis.Result{BPM: 124})
	if got := lib.Track(3).BPM; got != 124 {
		t.Errorf("empty row BPM = %v, want 124 (fill)", got)
	}

	// Identical result is a no-op (no spurious library save).
	srv.applyAnalysisToTrack(1, &analysis.Result{BPM: 121})
	if got := lib.Track(1).BPM; got != 121 {
		t.Errorf("row BPM = %v after identical result, want 121", got)
	}
}

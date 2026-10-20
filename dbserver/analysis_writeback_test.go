// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// An analysis a deck's load triggered is written back like one the web UI
// triggered: BPM follows it (the deck plays its grid) and the library saves.
func TestDeckAnalysisWriteBack(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, BPM: 120})
	h := &Handler{lib: lib}
	rev := lib.Rev()
	h.storeAnalysisResult(1, &analysis.Result{BPM: 121, Duration: 300, KeyCamelot: "11A"})
	if tr := lib.Track(1); tr.BPM != 121 || tr.Key != "11A" || tr.Duration == 0 {
		t.Errorf("track: BPM %v key %q duration %v; want 121, 11A, set", tr.BPM, tr.Key, tr.Duration)
	}
	if lib.Rev() == rev {
		t.Error("the write-back wasn't saved")
	}
}

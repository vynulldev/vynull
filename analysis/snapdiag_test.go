// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

// TestSnapDiag prints the snap-verification coherence profile around the
// stored tempo for chosen library tracks — the curve snapVerifyBPM decides
// on. Clean constant-tempo tracks show one sharp peak; phase-discontinuous
// tracks show a null at the true tempo with symmetric side lobes (the
// segments cancel exactly there), which is what SnapMinCoherence gates on.
//
// Env-gated diagnostic, not a correctness test:
//
//	VYNULL_SNAP_DIAG_LIB=/path/library.json VYNULL_SNAP_DIAG_IDS="49,167" \
//	  go test ./analysis/ -run SnapDiag -v
func TestSnapDiag(t *testing.T) {
	lib := os.Getenv("VYNULL_SNAP_DIAG_LIB")
	if lib == "" {
		t.Skip("set VYNULL_SNAP_DIAG_LIB to a library.json to run")
	}
	var ids []int
	if err := json.Unmarshal([]byte("["+os.Getenv("VYNULL_SNAP_DIAG_IDS")+"]"), &ids); err != nil || len(ids) == 0 {
		t.Fatal("set VYNULL_SNAP_DIAG_IDS to a comma-separated track ID list")
	}
	raw, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	var tracks []struct {
		ID       int     `json:"id"`
		Title    string  `json:"title"`
		BPM      float64 `json:"bpm"`
		FilePath string  `json:"file_path"`
	}
	if err := json.Unmarshal(raw, &tracks); err != nil {
		t.Fatal(err)
	}
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, tr := range tracks {
		if !want[tr.ID] {
			continue
		}
		samples, err := DecodePCM(tr.FilePath, AnalysisRate)
		if err != nil {
			t.Errorf("id %d: %v", tr.ID, err)
			continue
		}
		mb, mbMs := multiBandOnset(samples, AnalysisRate)
		if mb == nil {
			t.Errorf("id %d: track too short for multiband onset", tr.ID)
			continue
		}
		center := math.Round(tr.BPM)
		fmt.Printf("\nid=%d %.35q stored=%.2f  (curve around %.0f)\n", tr.ID, tr.Title, tr.BPM, center)
		for d := -1.5; d <= 1.51; d += 0.1 {
			b := center + d
			c, ok := snapCoherence(mb, mbMs, 60000.0/b)
			mark := " "
			if math.Abs(d) < 0.001 {
				mark = "*"
			}
			fmt.Printf("  %7.2f %s coh=%.3f ok=%v %s\n", b, mark, c, ok, strings.Repeat("#", int(c*60)))
		}
	}
}

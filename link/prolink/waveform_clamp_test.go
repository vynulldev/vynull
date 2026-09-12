// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"math"
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// TestPWV4BandClamp locks the 7-bit invariant on the PWV4 band bytes: real
// rekordbox never emits >127 in d2-d5 (it saturates a linear value there),
// and values above 127 in d3/d4/d5 hard-restart an XDJ-AZ when the track
// loads (issue #37). The golden-hash signal is too quiet to reach the clamp,
// so this drives a full-scale bass-heavy signal that saturates all four
// bands and asserts every band byte stays within 7 bits.
func TestPWV4BandClamp(t *testing.T) {
	n := analysis.AnalysisRate * 3
	s := make([]float32, n)
	for i := 0; i < n; i++ {
		tm := float64(i) / float64(analysis.AnalysisRate)
		// 80Hz bass, 500Hz at the mid band's response peak (the mid path
		// rolls off above ~600Hz), 8kHz treble — each hot enough to push
		// its band past 127 before the clamp.
		s[i] = float32(
			0.9*math.Sin(2*math.Pi*80*tm) +
				0.7*math.Sin(2*math.Pi*500*tm) +
				0.5*math.Sin(2*math.Pi*8000*tm),
		)
	}
	buf := GenerateColorPreview(s, analysis.AnalysisRate)
	if len(buf) == 0 {
		t.Fatal("empty PWV4")
	}
	const entrySize = 6
	saturated := [6]bool{}
	for i := 0; i+entrySize <= len(buf); i += entrySize {
		for b := 2; b < 6; b++ {
			if v := buf[i+b]; v > 127 {
				t.Fatalf("entry %d byte d%d = %d, exceeds the 7-bit PWV4 band range", i/entrySize, b, v)
			} else if v == 127 {
				saturated[b] = true
			}
		}
	}
	// The signal must be hot enough to actually reach the clamp in the three
	// spectral bands, otherwise this test can silently stop testing it.
	for b := 3; b < 6; b++ {
		if !saturated[b] {
			t.Errorf("d%d never reached 127 — test signal no longer exercises the clamp", b)
		}
	}
}

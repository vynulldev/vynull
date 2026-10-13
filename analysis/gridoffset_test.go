// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

func grid(first, period float64, n, downbeat int) *Result {
	r := &Result{BPM: 60000 / period, DownbeatIndex: downbeat, GridDetected: true}
	for i := 0; i < n; i++ {
		r.Beats = append(r.Beats, first+float64(i)*period)
	}
	return r
}

// The downbeat must stay on the same musical beat (same absolute time + delta)
// however the shift adds or drops beats at the start.
func TestShiftGridKeepsDownbeat(t *testing.T) {
	const p = 500.0
	for _, tc := range []struct {
		name  string
		first float64
		db    int
		delta float64
	}{
		{"earlier, stays positive", 100, 1, -25},
		{"later, opens a gap that is refilled", 480, 2, 50},
	} {
		r := grid(tc.first, p, 16, tc.db)
		want := r.Beats[r.DownbeatIndex] + tc.delta
		shiftGrid(r, tc.delta)
		if r.Beats[0] < 0 || r.Beats[0] >= p {
			t.Errorf("%s: first beat %.1f not in [0,%g)", tc.name, r.Beats[0], p)
		}
		got := r.Beats[r.DownbeatIndex]
		if math.Mod(math.Abs(got-want), 4*p) > 0.001 {
			t.Errorf("%s: downbeat at %.1f, want %.1f (mod a bar)", tc.name, got, want)
		}
	}
}

// A beat the detector found at >= 0 must never be dropped by the offset: a
// track cut right on its downbeat (first beat at 21ms, offset -25) keeps that
// beat, clamped to 0, as beat 1 — not beat 1 a whole bar later.
func TestOffsetClampsFirstBeat(t *testing.T) {
	r := grid(21, 468.75, 16, 0)
	shiftGrid(r, -25)
	if r.Beats[0] != 0 {
		t.Fatalf("first beat = %.1f, want clamped to 0", r.Beats[0])
	}
	if r.DownbeatIndex != 0 {
		t.Errorf("downbeat index = %d, want 0 (the beat at the track start)", r.DownbeatIndex)
	}
	if got := r.Beats[1]; math.Abs(got-(21+468.75-25)) > 0.001 {
		t.Errorf("second beat = %.2f, want shifted by -25", got)
	}
	if len(r.Beats) != 16 {
		t.Errorf("beat count changed: %d", len(r.Beats))
	}

	// Downbeat later in the bar: still the same musical beat after the clamp.
	r = grid(21, 468.75, 16, 2)
	shiftGrid(r, -25)
	if r.DownbeatIndex != 2 || math.Abs(r.Beats[2]-(21+2*468.75-25)) > 0.001 {
		t.Errorf("downbeat moved: idx %d at %.2f", r.DownbeatIndex, r.Beats[r.DownbeatIndex])
	}
}

func TestWithGridOffset(t *testing.T) {
	defer SetGridOffset(0)
	SetGridOffset(-25)

	det := grid(100, 500, 8, 0)
	if got := withGridOffset(det); got == det || got.Beats[0] != 75 || got.GridOffsetMs != -25 {
		t.Fatalf("detected grid not shifted: %+v", got)
	}
	if len(withGridOffset(det).BeatGrid) == 0 {
		t.Error("shifted grid must have its beat-grid blob rebuilt")
	}
	if det.Beats[0] != 100 {
		t.Fatal("original result mutated")
	}

	edited := grid(100, 500, 8, 0)
	edited.GridEdited = true
	if withGridOffset(edited) != edited {
		t.Error("hand-edited grid must not shift")
	}
	imported := grid(100, 500, 8, 0)
	imported.GridDetected = false
	if withGridOffset(imported) != imported {
		t.Error("imported grid must not shift")
	}
	current := grid(100, 500, 8, 0)
	current.GridOffsetMs = -25
	if withGridOffset(current) != current {
		t.Error("grid already at the current offset must not shift")
	}
}

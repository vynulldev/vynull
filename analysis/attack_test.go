// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// kickTrack renders a 120 BPM kick (fast attack, slow decay, 55 Hz) whose
// attacks start at onsetMs + k*500ms.
func kickTrack(sr int, onsetMs float64, seconds int) []float32 {
	x := make([]float32, sr*seconds)
	for t0 := onsetMs; t0 < float64(seconds*1000)-500; t0 += 500 {
		s0 := int(t0 * float64(sr) / 1000)
		for i := 0; i < sr/4 && s0+i < len(x); i++ {
			t := float64(i) / float64(sr)
			env := (1 - math.Exp(-t/0.001)) * math.Exp(-t/0.06)
			x[s0+i] += float32(0.8 * env * math.Sin(2*math.Pi*55*t))
		}
	}
	return x
}

func TestAttackShiftFindsOnset(t *testing.T) {
	const sr = 44100
	x := kickTrack(sr, 237, 30)
	// A grid 20ms late against the attacks, as the tempogram phase can be.
	var beats []float64
	for b := 257.0; b < 29000; b += 500 {
		beats = append(beats, b)
	}
	shift, ok := attackShift(x, sr, beats, 0)
	if !ok {
		t.Fatal("no attack measured")
	}
	got := beats[0] + shift - AttackLeadMs
	if math.Abs(got-237) > 2 {
		t.Errorf("attack at %.1fms, want ~237 (shift %.1f)", got, shift)
	}
}

func TestAttackShiftGuard(t *testing.T) {
	const sr = 44100
	x := kickTrack(sr, 237, 30)
	// A grid far from the attacks (more than AttackGuardMs) is left alone.
	var beats []float64
	for b := 237 + 2*AttackGuardMs; b < 29000; b += 500 {
		beats = append(beats, b)
	}
	if shift, ok := attackShift(x, sr, beats, 0); ok {
		t.Errorf("shift %.1f accepted beyond the guard", shift)
	}
	if _, ok := attackShift(x[:sr], sr, []float64{300}, 0); ok {
		t.Error("too few beats must not be measured")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestStartLevel(t *testing.T) {
	const sr = 44100
	quiet := make([]float32, sr)
	if got := startLevel(quiet, sr); got > StartLevelGate {
		t.Errorf("silent start level %.4f above gate", got)
	}
	hot := make([]float32, sr)
	for i := range hot {
		hot[i] = 0.5
		if i%2 == 1 {
			hot[i] = -0.5
		}
	}
	if got := startLevel(hot, sr); got <= StartLevelGate {
		t.Errorf("full-level start %.4f not above gate", got)
	}
	if startLevel(nil, sr) != 0 {
		t.Error("empty input must give 0")
	}
}

// A grid detected on padded audio maps back by the pad length; a beat just
// before 0 (the track starts on it) clamps to 0, beats inside the pad drop.
func TestUnpadBeats(t *testing.T) {
	const p = 500.0
	got := unpadBeats([]float64{10, 30, 530, 1030}, 50, p) // -40 clamps, -20 drops
	want := []float64{0, 480, 980}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	got = unpadBeats([]float64{-200, 300, 800}, 50, p)
	if len(got) != 2 || got[0] != 250 {
		t.Errorf("beat far before 0 must drop: %v", got)
	}
}

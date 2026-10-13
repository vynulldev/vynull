// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

// A track whose grid starts one beat before the music (an empty first beat)
// gets beat 1 on the first kick; one that starts on a kick keeps beat 0; one
// that starts at full level keeps beat 0 whatever the strengths say.
func TestSilentLeadIndex(t *testing.T) {
	const sr = 44100
	beats := func(first float64) []float64 {
		var b []float64
		for t := first; t < 29000; t += 500 {
			b = append(b, t)
		}
		return b
	}

	late := kickTrack(sr, 500, 30) // kicks from 500ms; the grid's first beat at 0 is empty
	if got := SilentLeadIndex(beats(0), late, sr); got != 1 {
		t.Errorf("empty first beat: beat 1 at index %d, want 1", got)
	}

	onTime := kickTrack(sr, 100, 30)
	if got := SilentLeadIndex(beats(100), onTime, sr); got != 0 {
		t.Errorf("kick on the first beat: index %d, want 0", got)
	}

	hot := kickTrack(sr, 500, 30)
	for i := 0; i < sr/100; i++ {
		hot[i] = 0.5 // starts at full level
	}
	if got := SilentLeadIndex(beats(0), hot, sr); got != 0 {
		t.Errorf("hot start: index %d, want 0", got)
	}
}

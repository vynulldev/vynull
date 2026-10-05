// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// withTempoRange sets the package tempo-range config for the duration of fn and
// restores it after, so tests don't leak state into each other.
func withTempoRange(lo, hi float64, fn func()) {
	om, oM := TempoRange()
	SetTempoRange(lo, hi)
	defer SetTempoRange(om, oM)
	fn()
}

// TestTempoWindowAndPriorDefaults: with no range set, the window and prior keep
// their historical values so nothing changes for existing users.
func TestTempoWindowAndPriorDefaults(t *testing.T) {
	withTempoRange(0, 0, func() {
		if lo, hi := tempoWindow(); lo != defaultTempoLo || hi != defaultTempoHi {
			t.Errorf("default window = %g-%g, want %g-%g", lo, hi, defaultTempoLo, defaultTempoHi)
		}
		if c, s := tempoPriorParams(); c != tempoPriorCenter || s != tempoPriorSigma {
			t.Errorf("default prior = center %g sigma %g, want %g/%g", c, s, tempoPriorCenter, tempoPriorSigma)
		}
	})
}

// TestTempoWindowAndPriorConfigured: an explicit range becomes the window and
// re-centres the prior on the range's geometric mean.
func TestTempoWindowAndPriorConfigured(t *testing.T) {
	withTempoRange(160, 185, func() {
		lo, hi := tempoWindow()
		if lo != 160 || hi != 185 {
			t.Errorf("window = %g-%g, want 160-185", lo, hi)
		}
		c, s := tempoPriorParams()
		wantCenter := math.Sqrt(160 * 185)
		if math.Abs(c-wantCenter) > 0.01 {
			t.Errorf("prior center = %g, want %g (geometric mean)", c, wantCenter)
		}
		if s <= 0 || s > 0.5 {
			t.Errorf("prior sigma = %g, want a tight positive value", s)
		}
	})
}

// TestTempoPriorFavoursRange: inside a DnB range the prior must prefer the true
// tempo (174) over its half (87) — the opposite of the default-130 prior.
func TestTempoPriorFavoursRange(t *testing.T) {
	withTempoRange(160, 185, func() {
		if tempoPrior(174) <= tempoPrior(87) {
			t.Errorf("prior(174)=%.4f should exceed prior(87)=%.4f under a 160-185 range",
				tempoPrior(174), tempoPrior(87))
		}
	})
}

// TestBPMRangeForcesTrueTempo is the end-to-end proof. A clean 174 BPM kick
// train is exactly the reporter's case: with the default 80-170 window the real
// 174 is above the ceiling, so detection folds it to the half (~87); set a
// 160-185 range and it comes back at ~174.
func TestBPMRangeForcesTrueTempo(t *testing.T) {
	samples := synthKickTrain(174, 90)

	withTempoRange(0, 0, func() {
		got := DetectBeatsWithEncoderDelay(samples, AnalysisRate, 0).BPM
		if got < 84 || got > 90 {
			t.Errorf("default: 174 track detected %.2f, expected the ~87 half-tempo fold", got)
		}
	})

	withTempoRange(160, 185, func() {
		got := DetectBeatsWithEncoderDelay(samples, AnalysisRate, 0).BPM
		if got < 160 || got > 185 {
			t.Errorf("with range 160-185: 174 track detected %.2f, want it inside the range (~174)", got)
		}
	})
}

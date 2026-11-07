// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// synthKickTrain renders a four-on-the-floor kick pattern at exactly the given
// BPM: a decaying 60Hz burst on every beat over durSec seconds. Deterministic
// (no RNG), long enough that a 0.2 BPM period error accumulates a visible
// phase drift for the tempogram coherence to catch.
func synthKickTrain(bpm, durSec float64) []float32 {
	n := int(float64(AnalysisRate) * durSec)
	out := make([]float32, n)
	periodSamples := 60.0 / bpm * float64(AnalysisRate)
	burstLen := int(0.08 * float64(AnalysisRate)) // 80ms kick
	twoPiF := 2.0 * math.Pi * 60.0 / float64(AnalysisRate)
	for beat := 0; ; beat++ {
		start := int(float64(beat) * periodSamples)
		if start >= n {
			break
		}
		for i := 0; i < burstLen && start+i < n; i++ {
			env := math.Exp(-float64(i) / (0.02 * float64(AnalysisRate))) // 20ms decay
			out[start+i] += float32(0.8 * env * math.Sin(twoPiF*float64(i)))
		}
	}
	return out
}

// TestSnapVerify_IntegerTempo: a track at exactly 121 BPM must come out 121.00
// even when the autocorrelation peak interpolation lands a fraction off — the
// coherence comparison against the integer candidate corrects it.
func TestSnapVerify_IntegerTempo(t *testing.T) {
	for _, bpm := range []float64{121, 124, 140} {
		samples := synthKickTrain(bpm, 150)
		res := DetectBeatsWithEncoderDelay(samples, AnalysisRate, 0)
		if res.BPM != bpm {
			t.Errorf("synth %.0f BPM: detected %.2f, want %.2f exactly", bpm, res.BPM, bpm)
		}
	}
}

// TestSnapVerify_FractionalTempoSurvives: a track genuinely at a fractional
// tempo inside the snap window must NOT be dragged to the integer — its true
// period has the decisive coherence advantage.
func TestSnapVerify_FractionalTempoSurvives(t *testing.T) {
	const want = 122.30 // 0.30 from 122, inside SnapWindowBPM
	samples := synthKickTrain(want, 150)
	res := DetectBeatsWithEncoderDelay(samples, AnalysisRate, 0)
	if math.Abs(res.BPM-want) > 0.1 {
		t.Errorf("synth %.2f BPM: detected %.2f, want within 0.1 (not snapped to %g)",
			want, res.BPM, math.Round(want))
	}
	if res.BPM == math.Round(want) {
		t.Errorf("genuinely fractional tempo was snapped to the integer %.0f", res.BPM)
	}
}

// TestSnapVerify_GridUsesSnappedPeriod: after snapping, the emitted beat grid
// must run at the snapped period (the phase pipeline consumed the corrected
// tempo), not the fractional one patched afterwards.
func TestSnapVerify_GridUsesSnappedPeriod(t *testing.T) {
	samples := synthKickTrain(121, 150)
	res := DetectBeatsWithEncoderDelay(samples, AnalysisRate, 0)
	if res.BPM != 121 {
		t.Fatalf("detected %.2f, want 121", res.BPM)
	}
	if len(res.Beats) < 10 {
		t.Fatalf("too few beats: %d", len(res.Beats))
	}
	wantInterval := 60000.0 / 121
	got := (res.Beats[len(res.Beats)-1] - res.Beats[0]) / float64(len(res.Beats)-1)
	if math.Abs(got-wantInterval) > 0.05 {
		t.Errorf("mean beat interval %.3fms, want %.3fms (grid not on snapped period)", got, wantInterval)
	}
}

// TestWidenSnap: the wide check moves a snapped tempo only on decisive
// coherence beyond the snap's window, and never onto one of a phase jump's
// side lobes.
func TestWidenSnap(t *testing.T) {
	// peaks builds a coherence curve: a floor plus Gaussian peaks (BPM, height).
	peaks := func(floor float64, ps ...[2]float64) func(float64) float64 {
		return func(b float64) float64 {
			c := floor
			for _, p := range ps {
				c = math.Max(c, p[1]*math.Exp(-math.Pow((b-p[0])/0.08, 2)))
			}
			return c
		}
	}
	for _, c := range []struct {
		name         string
		coh          func(float64) float64
		bpm, snapped float64
		lo, hi, want float64
	}{
		{"detection 2.4 off a clean 120", peaks(0.1, [2]float64{120, 0.91}), 122.41, 122, 0, 0, 120},
		{"phase jump: null at 128, lobes at 127 and 129", peaks(0.12, [2]float64{126.98, 0.66}, [2]float64{129.02, 0.59}), 128.2, 128, 0, 0, 128},
		{"weak peak", peaks(0.1, [2]float64{120, 0.4}), 122.41, 122, 0, 0, 122},
		{"peak not twice the snapped tempo's", peaks(0.35, [2]float64{120, 0.6}), 122.41, 122, 0, 0, 122},
		{"near-perfect fractional peak", peaks(0.1, [2]float64{117.30, 0.95}), 118.6, 118, 0, 0, 117.30},
		{"fractional peak below the gate", peaks(0.1, [2]float64{117.30, 0.8}), 118.6, 118, 0, 0, 118},
		{"peak outside an explicit range", peaks(0.1, [2]float64{120, 0.91}), 122.41, 122, 121, 124, 122},
	} {
		withTempoRange(c.lo, c.hi, func() {
			if got := widenSnap(c.coh, c.bpm, c.snapped); math.Abs(got-c.want) > 0.005 {
				t.Errorf("%s: %.2f, want %.2f", c.name, got, c.want)
			}
		})
	}
}

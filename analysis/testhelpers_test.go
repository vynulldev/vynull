// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "math"

// synthSine produces a mono sine wave at the given frequency and amplitude.
// Duration is in seconds; sample rate is AnalysisRate. Kept in the analysis
// test package for bandwaveform_test.go; the encoder tests moved to
// link/prolink carry their own copy.
func synthSine(freqHz, durSec, amplitude float64) []float32 {
	n := int(float64(AnalysisRate) * durSec)
	out := make([]float32, n)
	twoPiF := 2.0 * math.Pi * freqHz / float64(AnalysisRate)
	for i := range out {
		out[i] = float32(amplitude * math.Sin(twoPiF*float64(i)))
	}
	return out
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import "math"

// SplitBandsAndPeaks band-splits samples with 2nd-order Butterworth filters at
// the given crossovers (bassMidHz, midTrebleHz) and returns per-segment peak
// amplitudes for bass, mid, treble, and the unfiltered overall — numPoints
// segments.
//
// Mid is a proper band-pass (HP@bassMid + LP@midTreble) rather than
// complementary subtraction, which had constructive-interference artefacts near
// the bass cutoff that rendered mid-range tones as bass-tinted on the CDJ
// (yellow/orange instead of green).
func SplitBandsAndPeaks(samples []float32, sampleRate, numPoints int, bassMidHz, midTrebleHz float64) (allBass, allMid, allTreble, allTotal []float64) {
	segLen := len(samples) / numPoints
	if segLen < 1 {
		segLen = 1
	}

	sr := float64(sampleRate)
	bassFiltered := ApplyBiquad(samples, ButterworthLow(bassMidHz, sr))
	trebleFiltered := ApplyBiquad(samples, ButterworthHigh(midTrebleHz, sr))
	midFiltered := ApplyBiquad(
		ApplyBiquad(samples, ButterworthHigh(bassMidHz, sr)),
		ButterworthLow(midTrebleHz, sr),
	)

	allBass = make([]float64, numPoints)
	allMid = make([]float64, numPoints)
	allTreble = make([]float64, numPoints)
	allTotal = make([]float64, numPoints)

	for i := 0; i < numPoints; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(samples) {
			end = len(samples)
		}
		if start >= len(samples) {
			break
		}
		var bp, mp, tp, overall float64
		for j := start; j < end; j++ {
			bv := math.Abs(float64(bassFiltered[j]))
			mv := math.Abs(float64(midFiltered[j]))
			tv := math.Abs(float64(trebleFiltered[j]))
			sv := math.Abs(float64(samples[j]))
			if bv > bp {
				bp = bv
			}
			if mv > mp {
				mp = mv
			}
			if tv > tp {
				tp = tv
			}
			if sv > overall {
				overall = sv
			}
		}
		allBass[i] = bp
		allMid[i] = mp
		allTreble[i] = tp
		allTotal[i] = overall
	}
	return
}

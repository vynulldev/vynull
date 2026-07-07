// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"github.com/vynulldev/vynull/core"
	"github.com/vynulldev/vynull/dsp"
)

// Neutral waveform resolutions. Detail matches the PWV5 detail rate; overview
// is a fixed coarse width (like the PWV4 overview) whose points-per-second
// therefore varies with track length.
const overviewPoints = 1200

// BandWaveformDetail extracts the brand-neutral per-band (bass/mid/treble)
// amplitude envelope of a track at the detail resolution (DetailEntriesPerSec).
// This is the shareable DSP output every backend re-encodes into its own colour
// waveform (Pioneer PWV5, Engine high-res, …); it reuses the same band split as
// GenerateDetail so the two agree.
func BandWaveformDetail(samples []float32, sampleRate int) core.BandWaveform {
	durationSec := float64(len(samples)) / float64(sampleRate)
	numPoints := int(durationSec * DetailEntriesPerSec)
	if numPoints < 1 {
		numPoints = 1
	}
	bass, mid, treble, _ := dsp.SplitBandsAndPeaks(samples, sampleRate, numPoints, BandBassMidHz, BandMidTrebleHz)
	return bandsToWaveform(DetailEntriesPerSec, bass, mid, treble)
}

// BandWaveformOverview extracts the neutral per-band envelope at a fixed coarse
// width (overviewPoints), for whole-track overview rendering.
func BandWaveformOverview(samples []float32, sampleRate int) core.BandWaveform {
	durationSec := float64(len(samples)) / float64(sampleRate)
	if durationSec <= 0 {
		return core.BandWaveform{}
	}
	bass, mid, treble, _ := dsp.SplitBandsAndPeaks(samples, sampleRate, overviewPoints, BandBassMidHz, BandMidTrebleHz)
	return bandsToWaveform(float64(overviewPoints)/durationSec, bass, mid, treble)
}

// bandsToWaveform normalises per-point peak amplitudes to a neutral 0..1
// BandWaveform, dividing every band by the single loudest band-point so the
// relative energy between the bands is preserved.
func bandsToWaveform(pointsPerSec float64, bass, mid, treble []float64) core.BandWaveform {
	max := 0.0
	for _, s := range [][]float64{bass, mid, treble} {
		for _, v := range s {
			if v > max {
				max = v
			}
		}
	}
	inv := 0.0
	if max > 0 {
		inv = 1.0 / max
	}
	scale := func(src []float64) []float32 {
		dst := make([]float32, len(src))
		for i, v := range src {
			dst[i] = float32(v * inv)
		}
		return dst
	}
	return core.BandWaveform{
		PointsPerSec: pointsPerSec,
		Bass:         scale(bass),
		Mid:          scale(mid),
		Treble:       scale(treble),
	}
}

// CoreWithBands returns the full neutral analysis: the Result's DSP facts (via
// Core) plus the detail and overview band waveforms extracted from samples.
// The caller supplies samples because they aren't kept in the cached Result —
// keeping the (large) band arrays out of the on-disk cache.
func (r *Result) CoreWithBands(samples []float32, sampleRate int) *core.Analysis {
	a := r.Core()
	a.Detail = BandWaveformDetail(samples, sampleRate)
	a.Overview = BandWaveformOverview(samples, sampleRate)
	return a
}

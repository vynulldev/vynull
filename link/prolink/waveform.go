// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"math"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/dsp"
)

// GeneratePreview computes the waveform preview for the dbserver 0x2004 response.
// Returns 904 bytes: 200×2 (PWAV) + 200×2 (second preview) + 100×1 (PWV2) + 4 (footer).
// Each PWAV entry is 2 bytes: height + whiteness.
// Uses max height 16 and whiteness ~5.
func GeneratePreview(samples []float32, sampleRate int) []byte {
	h200a := computeHeights(samples, 200, analysis.PreviewMaxHeight) // first half
	h200b := computeHeights(samples, 200, analysis.PreviewMaxHeight) // second half (same data, different view)
	h100 := computeHeights(samples, 100, 11)                         // tiny waveform (PWV2, max ~11)

	buf := make([]byte, 904) // 400 + 400 + 100 + 4

	// Section 1: 200 entries × 2 bytes (height + whiteness)
	for i := 0; i < 200; i++ {
		buf[i*2] = h200a[i]
		// Whiteness: 5 normally, drops to 3 at peaks
		w := uint8(5)
		if h200a[i] >= 13 {
			w = 3
		}
		buf[i*2+1] = w
	}

	// Section 2: 200 entries × 2 bytes (height + whiteness)
	for i := 0; i < 200; i++ {
		buf[400+i*2] = h200b[i]
		w := uint8(5)
		if h200b[i] >= 13 {
			w = 3
		}
		buf[400+i*2+1] = w
	}

	// Section 3: 100 entries × 1 byte (PWV2 tiny waveform)
	copy(buf[800:], h100)

	// Footer
	buf[900] = 0x9e
	buf[901] = 0xeb
	buf[902] = 0x78
	buf[903] = 0x10

	return buf
}

// GenerateTinyPreviewANLZ computes the 100-point tiny waveform preview for
// the PWV2 section in ANLZ .DAT files. PWV2 has brightness=0
// for every entry (each byte is just the raw height 0-11), unlike PWAV
// which packs brightness into the high 3 bits.
func GenerateTinyPreviewANLZ(samples []float32) []byte {
	return computeHeights(samples, 100, 11)
}

// GeneratePreviewANLZ computes a 400-point waveform preview for ANLZ .DAT files.
// Each byte is `(brightness << 5) | (height & 0x1f)`. Brightness is encoded
// inversely to height: brighter pixels for quiet sections,
// darker for loud peaks (mode brightness ≈ 3 across a typical track, with
// 5 for low heights and 2 for high). We approximate with `5 - h/3`.
func GeneratePreviewANLZ(samples []float32, sampleRate int) []byte {
	heights := computeHeights(samples, analysis.AnlzPreviewPoints, analysis.PreviewMaxHeight)
	for i, h := range heights {
		h &= 0x1f
		brightness := uint8(5)
		if h > 0 {
			b := 5 - int(h)/3
			if b < 0 {
				b = 0
			}
			brightness = uint8(b)
		}
		heights[i] = (brightness << 5) | h
	}
	return heights
}

// GenerateColorPreview computes a 1200-point color waveform preview (PWV4
// format). Each point is 6 bytes. Returns raw data bytes (no ANLZ header).
// V2 is the only encoder now — it targets the byte ranges of the
// .EXT PWV4 sections (calibrated against the 20Hz-20kHz sine sweep
// reference). V1 was the FFT-based experimental encoder that over-saturated
// band bytes 10-20×; it lived behind --pwv4-v2 until V2 was
// validated, then was removed.
func GenerateColorPreview(samples []float32, sampleRate int) []byte {
	return generateColorPreviewV2(samples, sampleRate)
}

// generateColorPreviewV2 targets the byte ranges of the
// .EXT PWV4 sections (calibrated against a 20Hz-20kHz sine sweep):
//
//	d0: unknown — set to 0 for now.
//	d1: RMS luminance envelope, 0-255, with pow(0.3) compression so even
//	    low-energy sections render bright on the CDJ.
//	d2: 5-bit narrow-low detector, 0-31, low-pass at ~500 Hz (blue mode).
//	d3: 5-bit bass intensity (low-pass ~200 Hz).
//	d4: 5-bit mid intensity (~200-2000 Hz).
//	d5: 5-bit treble intensity (>2000 Hz).
//
// Uses time-domain IIR Butterworth filters rather than FFT — short-window
// FFTs leak ~250 Hz of energy from sub-bass tones into the mid band, which
// rendered as orange/yellow bass on the CDJ instead of pure red. IIR gives
// clean separation matching the expected band response.
//
// d_band = pow(peak_amplitude, 0.5) * 31 (sqrt compression), calibrated so
// A=0.126 (the test sweep) produces d≈11 — the expected value.
func generateColorPreviewV2(samples []float32, sampleRate int) []byte {
	segLen := len(samples) / analysis.ColorPreviewPoints
	if segLen < 1 {
		segLen = 1
	}

	sr := float64(sampleRate)
	// 8th-order Butterworth (4 cascaded biquads, 48 dB/oct rolloff). Lower
	// orders left ~50% of a 1.8 kHz tone in the treble band when cutoff was
	// at 2 kHz; the target shows essentially zero in that transition zone,
	// implying a near-brick-wall response.
	bp8Low := func(s []float32, cutoff float64) []float32 {
		c := dsp.ButterworthLow(cutoff, sr)
		return dsp.ApplyBiquad(dsp.ApplyBiquad(dsp.ApplyBiquad(dsp.ApplyBiquad(s, c), c), c), c)
	}
	bp8High := func(s []float32, cutoff float64) []float32 {
		c := dsp.ButterworthHigh(cutoff, sr)
		return dsp.ApplyBiquad(dsp.ApplyBiquad(dsp.ApplyBiquad(dsp.ApplyBiquad(s, c), c), c), c)
	}
	// Mid uses an 8th-order HP@200 for sharp bass rejection followed by a
	// gentle 2nd-order LP@800 — the mid has a humped response
	// peaking around 400-600 Hz then rolling off at ~3-6 dB/octave above.
	// A flat band-pass 200-2000 Hz over-emits the 1-2 kHz region by ~2×.
	midLP := dsp.ButterworthLow(800, sr)
	// d2 is a 2nd-order LP@400 with gentle 12 dB/octave rolloff. The target is
	// 16→8→2 from 100→500→1000 Hz which an 8th-order would overshoot at the
	// top end (still 8 at 1 kHz when the target is at 2).
	lowLP := dsp.ButterworthLow(400, sr)
	bassSamples := bp8Low(samples, 200)
	lowSamples := dsp.ApplyBiquad(samples, lowLP)
	midSamples := dsp.ApplyBiquad(bp8High(samples, 200), midLP)
	trebleSamples := bp8High(samples, analysis.PreviewTrebleHz)

	const entrySize = 6
	buf := make([]byte, analysis.ColorPreviewPoints*entrySize)

	// First pass: per-segment RMS (overall + per-band).
	// Using RMS instead of peak amplitude for band bytes avoids IIR ringing
	// during sweeps inflating values past the steady-state in-band amplitude.
	rmsVals := make([]float64, analysis.ColorPreviewPoints)
	bassRMS := make([]float64, analysis.ColorPreviewPoints)
	lowRMS := make([]float64, analysis.ColorPreviewPoints)
	midRMS := make([]float64, analysis.ColorPreviewPoints)
	trebleRMS := make([]float64, analysis.ColorPreviewPoints)
	var maxRMS float64

	for i := 0; i < analysis.ColorPreviewPoints; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(samples) {
			end = len(samples)
		}
		if start >= len(samples) {
			continue
		}

		var sum, bSum, lSum, mSum, tSum float64
		for j := start; j < end; j++ {
			v := float64(samples[j])
			sum += v * v
			bv := float64(bassSamples[j])
			bSum += bv * bv
			lv := float64(lowSamples[j])
			lSum += lv * lv
			mv := float64(midSamples[j])
			mSum += mv * mv
			tv := float64(trebleSamples[j])
			tSum += tv * tv
		}
		n := float64(end - start)
		rms := math.Sqrt(sum / n)
		if rms > maxRMS {
			maxRMS = rms
		}
		rmsVals[i] = rms
		bassRMS[i] = math.Sqrt(bSum / n)
		lowRMS[i] = math.Sqrt(lSum / n)
		midRMS[i] = math.Sqrt(mSum / n)
		trebleRMS[i] = math.Sqrt(tSum / n)
	}

	if maxRMS < 1e-10 {
		return buf
	}

	// Per-band scales calibrated against the sweep reference
	// (maxima: d3≈11, d4≈14, d5≈8). The asymmetry is in the
	// encoder — each band of the log sweep carries equal energy. Likely
	// perceptual weighting baked into the CDJ pixel formula.
	//
	// Band bytes are LINEAR in band RMS, full 0-255 range (not 5-bit). The
	// per-band scales (PreviewBassScale/Mid/Treble, above) are calibrated so the
	// colour balance holds on actual music, not on isolated
	// tones — see tools/wavecompare. d2 (the narrow-low blue-mode detector) is
	// not part of the colour balance and keeps its own scale.
	const lowScale = 130.0 // d2

	for i := 0; i < analysis.ColorPreviewPoints; i++ {
		var d1 uint8
		if rmsNorm := rmsVals[i] / maxRMS; rmsNorm > 0 {
			d1 = uint8(math.Min(math.Pow(rmsNorm, 0.3)*240, 255))
		}
		d2 := uint8(math.Min(lowRMS[i]*lowScale, 255))
		d3 := uint8(math.Min(bassRMS[i]*analysis.PreviewBassScale, 255))
		d4 := uint8(math.Min(midRMS[i]*analysis.PreviewMidScale, 255))
		d5 := uint8(math.Min(trebleRMS[i]*analysis.PreviewTrebleScale, 255))

		off := i * entrySize
		buf[off+0] = 0 // d0: unknown — placeholder until characterised
		buf[off+1] = d1
		buf[off+2] = d2
		buf[off+3] = d3
		buf[off+4] = d4
		buf[off+5] = d5
	}
	return buf
}

// GenerateDetail computes a high-resolution color waveform detail (PWV5 format).
// Returns raw data bytes: 2 bytes per entry (big-endian), ~150 entries per second.
// Bit layout: R(3) G(3) B(3) H(5) unused(2).
// Uses time-domain IIR filters for band splitting — no FFT needed.
func GenerateDetail(samples []float32, sampleRate int) []byte {
	durationSec := float64(len(samples)) / float64(sampleRate)
	numPoints := int(durationSec * analysis.DetailEntriesPerSec)
	if numPoints < 1 {
		numPoints = 1
	}

	allBass, allMid, allTreble, allTotal := dsp.SplitBandsAndPeaks(samples, sampleRate, numPoints, analysis.BandBassMidHz, analysis.BandMidTrebleHz)

	// Find global maxes — overall (classic mode) and per-band (3-band mode).
	totalMax, bassMax, midMax, trebleMax := 0.0, 0.0, 0.0, 0.0
	for i := 0; i < numPoints; i++ {
		if allTotal[i] > totalMax {
			totalMax = allTotal[i]
		}
		if allBass[i] > bassMax {
			bassMax = allBass[i]
		}
		if allMid[i] > midMax {
			midMax = allMid[i]
		}
		if allTreble[i] > trebleMax {
			trebleMax = allTreble[i]
		}
	}

	// 2 bytes per entry. Buffer starts zero — quiet/silent mid-song entries
	// stay as 0x00 0x00 rather than the 0xff 0x80 padding pattern. The
	// format reserves 0xff 0x80 strictly for pre/post-roll silence; the
	// the CDJ appears to treat mid-song occurrences as "end of
	// waveform" and stops color rendering for the rest of the track.
	buf := make([]byte, numPoints*2)
	if totalMax < 1e-10 {
		// Fully-silent track: it's all pre/post-roll, so fill with the silence
		// padding pattern (0xff 0x80) rather than 0x00 0x00 — the latter is the
		// "no data, stop rendering" sentinel the CDJ reacts to. (The pre/post-
		// roll loop below would reach this result too, but it divides by
		// totalMax, so handle the all-zero case explicitly here.)
		for i := 0; i < numPoints; i++ {
			buf[i*2] = 0xff
			buf[i*2+1] = 0x80
		}
		return buf
	}

	// PWV5 format: 16 bits BE — R(3) G(3) B(3) H(5) unused(2)
	//
	// Always emit an entry per timestep — never leave 0x00 0x00 mid-song.
	// Exports never have all-zero entries; quiet sections
	// still carry colour bits (height may be 0, RGB non-zero). The CDJ
	// appears to treat 0x00 0x00 as "no data here" and stops color
	// rendering past the first occurrence.
	for i := 0; i < numPoints; i++ {
		var height, r, g, b uint8

		// Classic mode: H = (peak/trackMax)² × 31 — per-track normalised
		// with quadratic (energy) compression. This fits
		// exactly across the ramp test: ratio 1.0 → H=31, 0.7 → 15, 0.5 → 7,
		// 0.25 → 1, 0.125 → 0. And in the tones file (all tones at the same
		// amplitude) every entry has ratio=1 so H=31 throughout, which is
		// the expected result. Tracks where the loudest moment defines "31"
		// is what gives waveforms their punchy character.
		amp := allTotal[i] / totalMax
		compressed := amp * amp * 31.0
		if compressed > 31 {
			compressed = 31
		}
		height = uint8(compressed)

		bassE := allBass[i]
		midE := allMid[i]
		trebleE := allTreble[i]
		maxE := bassE
		if midE > maxE {
			maxE = midE
		}
		if trebleE > maxE {
			maxE = trebleE
		}
		if maxE > 1e-10 {
			r = uint8(bassE / maxE * 7)
			g = uint8(midE / maxE * 7)
			b = uint8(trebleE / maxE * 7)
		}

		// Floor to avoid emitting 0x00 0x00 mid-song. The all-zero pair
		// is never emitted; quiet entries always carry at least
		// one non-zero bit (most commonly r=7 from the bass band's
		// per-track normalisation).
		if r == 0 && g == 0 && b == 0 && height == 0 {
			r = 7
		}

		word := uint16(r&7)<<13 | uint16(g&7)<<10 | uint16(b&7)<<7 | uint16(height&0x1f)<<2
		buf[i*2] = byte(word >> 8)
		buf[i*2+1] = byte(word & 0xff)
	}

	// Pre/post-roll padding. The silent regions at
	// the start and end of the track are marked with the padding pattern 0xff 0x80
	// (the only place this pattern legitimately appears). Find the first
	// and last entries with meaningful height and overwrite outside those
	// with padding so the deck recognises the track boundaries.
	firstAudio, lastAudio := numPoints, -1
	for i := 0; i < numPoints; i++ {
		if allTotal[i]/totalMax >= 0.01 {
			firstAudio = i
			break
		}
	}
	for i := numPoints - 1; i >= 0; i-- {
		if allTotal[i]/totalMax >= 0.01 {
			lastAudio = i
			break
		}
	}
	for i := 0; i < firstAudio; i++ {
		buf[i*2] = 0xff
		buf[i*2+1] = 0x80
	}
	for i := lastAudio + 1; i < numPoints; i++ {
		buf[i*2] = 0xff
		buf[i*2+1] = 0x80
	}

	return buf
}

// GenerateDetailMono derives the monochrome waveform (0x2904 format) from color detail.
// Returns 1 byte per entry: (brightness << 5) | (height & 0x1f).
// brightness 0=dark blue, 7=near white.
func GenerateDetailMono(colorDetail []byte) []byte {
	n := len(colorDetail) / 2
	if n == 0 {
		return nil
	}
	mono := make([]byte, n)
	for i := 0; i < n; i++ {
		b0 := colorDetail[i*2]
		b1 := colorDetail[i*2+1]

		// Padding / silence check. PWV3 uses 0xe0
		// (brightness=7, height=0) for silent pre/post-roll
		// regions. Source PWV5 may carry either the official padding
		// pattern (0xff 0x80) or our "quiet floor" (0xe0 0x00) — both
		// represent silence and map to the same PWV3 byte. Falling
		// through to the luminance calc gives 0x40 instead, which
		// renders as a dark mid-line instead of the flat invisible
		// pre-roll the deck expects.
		if (b0 == 0xff && b1 == 0x80) || (b0 == 0xe0 && b1 == 0x00) {
			mono[i] = 0xe0
			continue
		}

		word := uint16(b0)<<8 | uint16(b1)
		r := (word >> 13) & 7
		g := (word >> 10) & 7
		b := (word >> 7) & 7
		h := (word >> 2) & 0x1f

		// Brightness = perceived luminance mapped to 0-7.
		// Higher brightness = whiter/lighter color on CDJ.
		lum := (r*3 + g*6 + b) / 10
		if lum > 7 {
			lum = 7
		}

		mono[i] = byte(lum<<5) | byte(h&0x1f)
	}
	return mono
}

// computeHeights divides samples into n segments and returns RMS amplitude per segment.
// Uses a two-pass approach: compute all RMS values, then normalize to 0-maxH.
func computeHeights(samples []float32, n int, maxH int) []byte {
	heights := make([]byte, n)
	segLen := len(samples) / n
	if segLen < 1 {
		segLen = 1
	}

	rmsVals := make([]float64, n)
	var maxRMS float64
	for i := 0; i < n; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(samples) {
			end = len(samples)
		}
		if start >= len(samples) {
			break
		}

		var sum float64
		for _, s := range samples[start:end] {
			sum += float64(s) * float64(s)
		}
		rms := math.Sqrt(sum / float64(end-start))
		rmsVals[i] = rms
		if rms > maxRMS {
			maxRMS = rms
		}
	}

	if maxRMS < 1e-10 {
		return heights
	}

	for i, rms := range rmsVals {
		normalized := rms / maxRMS
		// Power curve: aggressive compression that produces
		// most heights at 4-6 with only loud peaks reaching 16.
		// pow(2.3) matches the PWAV height distribution.
		scaled := math.Pow(normalized, 2.3) * float64(maxH)
		if scaled > float64(maxH) {
			scaled = float64(maxH)
		}
		h := uint8(scaled)
		if h == 0 && rmsVals[i] > 0 {
			h = 1 // never produce zero heights for non-silent segments
		}
		heights[i] = h
	}

	return heights
}

// GenerateDetail3Band computes a high-resolution 3-band detail waveform where
// each band (bass/mid/treble) is normalized to its own global peak across the
// entire track. Returns 3 bytes per entry (bass, mid, treble; 0-255 each) at
// ~150 entries per second — same time resolution as GenerateDetail.
//
// Unlike PWV5 where r/g/b are per-entry relative band proportions, this format
// stores absolute per-band amplitudes. Consumers can render each band as its
// own bar height for CDJ-style additive 3-band visualisation.
func GenerateDetail3Band(samples []float32, sampleRate int) []byte {
	durationSec := float64(len(samples)) / float64(sampleRate)
	numPoints := int(durationSec * analysis.DetailEntriesPerSec)
	if numPoints < 1 {
		numPoints = 1
	}
	// PWV7 uses ABSOLUTE per-band RMS × scale — rekordbox's detail 3-band is
	// bass-heavy / treble-light, calibrated via tools/wavecompare -pwv7.
	return pack3BandAbs(samples, sampleRate, numPoints, analysis.Detail3BassScale, analysis.Detail3MidScale, analysis.Detail3TrebleScale)
}

// pack3BandAbs packs each segment's absolute per-band RMS × scale into 3 bytes
// (bass, mid, treble; 0-255). Shared by PWV6/PWV7 — rekordbox's 3-band formats
// store absolute amplitudes (the band balance comes from the scales, not from
// per-band normalisation), which is what gives them a usable loudness envelope.
func pack3BandAbs(samples []float32, sampleRate, numPoints int, bScale, mScale, tScale float64) []byte {
	bass, mid, treble := splitBands3RMS(samples, sampleRate, numPoints)
	clamp := func(v float64) byte {
		if v > 255 {
			return 255
		}
		if v < 0 {
			return 0
		}
		return byte(v)
	}
	buf := make([]byte, numPoints*3)
	for i := 0; i < numPoints; i++ {
		buf[i*3] = clamp(bass[i] * bScale)
		buf[i*3+1] = clamp(mid[i] * mScale)
		buf[i*3+2] = clamp(treble[i] * tScale)
	}
	return buf
}

// splitBands3RMS filters into bass/mid/treble (same crossovers as the colour
// waveforms) and returns each band's per-segment RMS over numPoints segments.
func splitBands3RMS(samples []float32, sampleRate, numPoints int) (bass, mid, treble []float64) {
	if numPoints < 1 {
		numPoints = 1
	}
	sr := float64(sampleRate)
	bf := dsp.ApplyBiquad(samples, dsp.ButterworthLow(analysis.BandBassMidHz, sr))
	tf := dsp.ApplyBiquad(samples, dsp.ButterworthHigh(analysis.BandMidTrebleHz, sr))
	mf := dsp.ApplyBiquad(dsp.ApplyBiquad(samples, dsp.ButterworthHigh(analysis.BandBassMidHz, sr)), dsp.ButterworthLow(analysis.BandMidTrebleHz, sr))
	segLen := len(samples) / numPoints
	if segLen < 1 {
		segLen = 1
	}
	bass = make([]float64, numPoints)
	mid = make([]float64, numPoints)
	treble = make([]float64, numPoints)
	for i := 0; i < numPoints; i++ {
		start := i * segLen
		end := start + segLen
		if end > len(samples) {
			end = len(samples)
		}
		if start >= len(samples) {
			break
		}
		var bs, ms, ts float64
		for j := start; j < end; j++ {
			bs += float64(bf[j]) * float64(bf[j])
			ms += float64(mf[j]) * float64(mf[j])
			ts += float64(tf[j]) * float64(tf[j])
		}
		if n := float64(end - start); n > 0 {
			bass[i] = math.Sqrt(bs / n)
			mid[i] = math.Sqrt(ms / n)
			treble[i] = math.Sqrt(ts / n)
		}
	}
	return
}

// GeneratePreview3Band computes the fixed-size (1200-entry) 3-band overview
// waveform — the PWV6 analog of PWV4, and the preview counterpart to PWV7.
// 3 bytes per entry (bass, mid, treble; 0-255), absolute per-band RMS × scale,
// calibrated for PWV6 (tools/wavecompare -pwv6).
func GeneratePreview3Band(samples []float32, sampleRate int) []byte {
	return pack3BandAbs(samples, sampleRate, analysis.ColorPreviewPoints, analysis.Preview3BassScale, analysis.Preview3MidScale, analysis.Preview3TrebleScale)
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"math"
)

const (
	previewPoints      = 900  // mono preview for dbserver network response
	anlzPreviewPoints  = 400  // mono preview for ANLZ .DAT file (rekordbox uses 400)
	colorPreviewPoints = 1200 // color preview (PWV4) — rekordbox uses 1200
	maxHeight          = 31   // max height for PWV4/PWV5 color waveforms
	previewMaxHeight   = 16   // max height for PWAV mono preview (rekordbox uses 1-16)
	fftSize            = 2048 // larger window for better bass frequency resolution
)

// RGB3BandMode controls how GenerateDetail and GenerateColorPreview encode
// per-band amplitudes into the PWV5/PWV4 wire formats. When true, each band
// is normalized to its own global peak across the track (so quieter highs
// stay visible alongside loud bass); height is driven by the loudest band
// at each point. When false (default), the original behaviour: r/g/b are
// per-entry band proportions and h is overall amplitude / global max.
//
// Toggled via the --rgb-3band CLI flag at startup. Affects what CDJs render.
// Changing this requires regenerating the analysis cache.
var RGB3BandMode bool

// Band-split crossover frequencies (Hz) for the colour-detail waveform's
// bass/mid/treble → R/G/B mapping. Exposed as vars so the colour balance can be
// calibrated against rekordbox; the defaults are the calibrated values.
var (
	BandBassMidHz   = 200.0
	BandMidTrebleHz = 750.0
	// PreviewTrebleHz is the PWV4 overview treble HP cutoff (separate from the
	// detail crossover above — PWV4 has its own band structure).
	PreviewTrebleHz = 2000.0
	// PWV4 per-band byte scales (d3/d4/d5 = bass/mid/treble). Calibrated so the
	// overview colour balance matches rekordbox on actual music
	// (tools/wavecompare): mean d-values land within ~1 of rekordbox's
	// (bass~64 mid~41 treble~31, balance ~47/30/23). These are much higher than
	// a single-tone sweep would imply — rekordbox's real-music band levels run
	// higher than a linear extrapolation from isolated tones, so the broadband
	// balance is the calibration target, not the sweep.
	PreviewBassScale   = 240.0
	PreviewMidScale    = 420.0
	PreviewTrebleScale = 480.0
	// PWV7 (3-band detail) absolute per-band RMS scales — calibrated against
	// rekordbox PWV7 (tools/wavecompare -pwv7): bass-heavy, treble-light,
	// all channels within ~0.7 of rekordbox's means/balance over 120 tracks.
	Detail3BassScale   = 176.0
	Detail3MidScale    = 283.0
	Detail3TrebleScale = 115.0
	// PWV6 (3-band overview) absolute per-band RMS scales — calibrated against
	// rekordbox PWV6, which is balanced (treble-favouring scales) and low.
	Preview3BassScale   = 83.0
	Preview3MidScale    = 166.0
	Preview3TrebleScale = 198.0
)

// GeneratePreview computes the waveform preview for the dbserver 0x2004 response.
// Returns 904 bytes: 200×2 (PWAV) + 200×2 (second preview) + 100×1 (PWV2) + 4 (footer).
// Each PWAV entry is 2 bytes: height + whiteness.
// rekordbox uses max height 16 and whiteness ~5.
func GeneratePreview(samples []float32, sampleRate int) []byte {
	h200a := computeHeights(samples, 200, previewMaxHeight) // first half
	h200b := computeHeights(samples, 200, previewMaxHeight) // second half (same data, different view)
	h100 := computeHeights(samples, 100, 11)                // tiny waveform (PWV2, max ~11)

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
// the PWV2 section in ANLZ .DAT files. rekordbox PWV2 has brightness=0
// for every entry (each byte is just the raw height 0-11), unlike PWAV
// which packs brightness into the high 3 bits.
func GenerateTinyPreviewANLZ(samples []float32) []byte {
	return computeHeights(samples, 100, 11)
}

// GeneratePreviewANLZ computes a 400-point waveform preview for ANLZ .DAT files.
// Each byte is `(brightness << 5) | (height & 0x1f)`. rekordbox encodes
// brightness inversely to height: brighter pixels for quiet sections,
// darker for loud peaks (mode brightness ≈ 3 across a typical track, with
// 5 for low heights and 2 for high). We approximate with `5 - h/3`.
func GeneratePreviewANLZ(samples []float32, sampleRate int) []byte {
	heights := computeHeights(samples, anlzPreviewPoints, previewMaxHeight)
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
// V2 is the only encoder now — it targets the byte ranges observed in real
// rekordbox .EXT PWV4 sections (calibrated against the 20Hz-20kHz sine sweep
// reference). V1 was the FFT-based experimental encoder that over-saturated
// band bytes 10-20× vs rekordbox; it lived behind --pwv4-v2 until V2 was
// validated, then was removed.
func GenerateColorPreview(samples []float32, sampleRate int) []byte {
	return generateColorPreviewV2(samples, sampleRate)
}

// generateColorPreviewV2 targets the byte ranges observed in rekordbox
// .EXT PWV4 sections (verified against a 20Hz-20kHz sine sweep on 2026-05-16):
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
// clean separation matching the band response rekordbox shows.
//
// d_band = pow(peak_amplitude, 0.5) * 31 (sqrt compression), calibrated so
// A=0.126 (the test sweep) produces d≈11 — exactly what rekordbox emits.
func generateColorPreviewV2(samples []float32, sampleRate int) []byte {
	segLen := len(samples) / colorPreviewPoints
	if segLen < 1 {
		segLen = 1
	}

	sr := float64(sampleRate)
	// 8th-order Butterworth (4 cascaded biquads, 48 dB/oct rolloff). Lower
	// orders left ~50% of a 1.8 kHz tone in the treble band when cutoff was
	// at 2 kHz; rekordbox shows essentially zero in that transition zone,
	// implying a near-brick-wall response.
	bp8Low := func(s []float32, cutoff float64) []float32 {
		c := butterworthLow(cutoff, sr)
		return applyBiquad(applyBiquad(applyBiquad(applyBiquad(s, c), c), c), c)
	}
	bp8High := func(s []float32, cutoff float64) []float32 {
		c := butterworthHigh(cutoff, sr)
		return applyBiquad(applyBiquad(applyBiquad(applyBiquad(s, c), c), c), c)
	}
	// Mid uses an 8th-order HP@200 for sharp bass rejection followed by a
	// gentle 2nd-order LP@800 — rekordbox's mid has a humped response
	// peaking around 400-600 Hz then rolling off at ~3-6 dB/octave above.
	// A flat band-pass 200-2000 Hz over-emits the 1-2 kHz region by ~2×.
	midLP := butterworthLow(800, sr)
	// d2 is a 2nd-order LP@400 with gentle 12 dB/octave rolloff. Real shows
	// 16→8→2 from 100→500→1000 Hz which an 8th-order would overshoot at the
	// top end (still 8 at 1 kHz when real is at 2).
	lowLP := butterworthLow(400, sr)
	bassSamples := bp8Low(samples, 200)
	lowSamples := applyBiquad(samples, lowLP)
	midSamples := applyBiquad(bp8High(samples, 200), midLP)
	trebleSamples := bp8High(samples, PreviewTrebleHz)

	const entrySize = 6
	buf := make([]byte, colorPreviewPoints*entrySize)

	// First pass: per-segment RMS (overall + per-band).
	// Using RMS instead of peak amplitude for band bytes avoids IIR ringing
	// during sweeps inflating values past the steady-state in-band amplitude.
	rmsVals := make([]float64, colorPreviewPoints)
	bassRMS := make([]float64, colorPreviewPoints)
	lowRMS := make([]float64, colorPreviewPoints)
	midRMS := make([]float64, colorPreviewPoints)
	trebleRMS := make([]float64, colorPreviewPoints)
	var maxRMS float64

	for i := 0; i < colorPreviewPoints; i++ {
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

	// Per-band scales calibrated against rekordbox sweep capture
	// (real maxima: d3≈11, d4≈14, d5≈8). The asymmetry is in real's
	// encoder — each band of the log sweep carries equal energy. Likely
	// perceptual weighting baked into the CDJ pixel formula.
	//
	// Band bytes are LINEAR in band RMS, full 0-255 range (not 5-bit). The
	// per-band scales (PreviewBassScale/Mid/Treble, above) are calibrated so the
	// colour balance matches rekordbox on actual music, not on isolated
	// tones — see tools/wavecompare. d2 (the narrow-low blue-mode detector) is
	// not part of the colour balance and keeps its own scale.
	const lowScale = 130.0 // d2

	for i := 0; i < colorPreviewPoints; i++ {
		var d1 uint8
		if rmsNorm := rmsVals[i] / maxRMS; rmsNorm > 0 {
			d1 = uint8(math.Min(math.Pow(rmsNorm, 0.3)*240, 255))
		}
		d2 := uint8(math.Min(lowRMS[i]*lowScale, 255))
		d3 := uint8(math.Min(bassRMS[i]*PreviewBassScale, 255))
		d4 := uint8(math.Min(midRMS[i]*PreviewMidScale, 255))
		d5 := uint8(math.Min(trebleRMS[i]*PreviewTrebleScale, 255))

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

// biquadCoeffs holds coefficients for a 2nd-order IIR (biquad) filter.
type biquadCoeffs struct {
	b0, b1, b2, a1, a2 float64
}

// butterworthLow computes 2nd-order Butterworth low-pass filter coefficients.
// Butterworth Q = 1/√2 ≈ 0.707 (maximally flat passband, no resonant peak).
// alpha = sin(w0) / (2*Q) = sin(w0) / √2.
func butterworthLow(cutoff, sampleRate float64) biquadCoeffs {
	w0 := 2.0 * math.Pi * cutoff / sampleRate
	alpha := math.Sin(w0) / math.Sqrt2
	cosW0 := math.Cos(w0)
	a0 := 1.0 + alpha
	return biquadCoeffs{
		b0: (1.0 - cosW0) / 2.0 / a0,
		b1: (1.0 - cosW0) / a0,
		b2: (1.0 - cosW0) / 2.0 / a0,
		a1: -2.0 * cosW0 / a0,
		a2: (1.0 - alpha) / a0,
	}
}

// butterworthHigh computes 2nd-order Butterworth high-pass filter coefficients.
func butterworthHigh(cutoff, sampleRate float64) biquadCoeffs {
	w0 := 2.0 * math.Pi * cutoff / sampleRate
	alpha := math.Sin(w0) / math.Sqrt2
	cosW0 := math.Cos(w0)
	a0 := 1.0 + alpha
	return biquadCoeffs{
		b0: (1.0 + cosW0) / 2.0 / a0,
		b1: -(1.0 + cosW0) / a0,
		b2: (1.0 + cosW0) / 2.0 / a0,
		a1: -2.0 * cosW0 / a0,
		a2: (1.0 - alpha) / a0,
	}
}

// applyBiquad applies a biquad IIR filter to the samples (in-place would mutate, so returns new slice).
func applyBiquad(samples []float32, c biquadCoeffs) []float32 {
	out := make([]float32, len(samples))
	var x1, x2, y1, y2 float64
	for i, s := range samples {
		x0 := float64(s)
		y0 := c.b0*x0 + c.b1*x1 + c.b2*x2 - c.a1*y1 - c.a2*y2
		out[i] = float32(y0)
		x2 = x1
		x1 = x0
		y2 = y1
		y1 = y0
	}
	return out
}

// splitBandsAndPeaks runs IIR band-splitting on samples and returns
// per-segment peak amplitudes for bass, mid, treble, and the unfiltered overall.
// Used by PWV5 (per-entry relative color) and the 3-band JSON generator.
//
// Cutoffs ~200/2000 Hz, matching rekordbox's apparent crossover points
// (verified via ramp/tone tests). Mid uses a proper band-pass (HP@200 + LP@2000)
// rather than complementary subtraction, which had constructive-interference
// artefacts near the bass cutoff that rendered mid-range tones as bass-tinted
// on the CDJ (yellow/orange instead of green).
func splitBandsAndPeaks(samples []float32, sampleRate, numPoints int) (allBass, allMid, allTreble, allTotal []float64) {
	segLen := len(samples) / numPoints
	if segLen < 1 {
		segLen = 1
	}

	sr := float64(sampleRate)
	bassFiltered := applyBiquad(samples, butterworthLow(BandBassMidHz, sr))
	trebleFiltered := applyBiquad(samples, butterworthHigh(BandMidTrebleHz, sr))
	midFiltered := applyBiquad(
		applyBiquad(samples, butterworthHigh(BandBassMidHz, sr)),
		butterworthLow(BandMidTrebleHz, sr),
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

// detailEntriesPerSec is the entry rate for PWV3/PWV5 scrolling waveforms.
// rekordbox 6.x exports use ~150 entries/sec (verified: real Greece
// 2000 at 484s has 72,741 PWV5 entries → 150.3/s; real Waveform at 165s
// has 24,796 entries → 150.3/s). The 0x0096 in the format-flags header
// is literally this rate.
const detailEntriesPerSec = 150

// GenerateDetail computes a high-resolution color waveform detail (PWV5 format).
// Returns raw data bytes: 2 bytes per entry (big-endian), ~150 entries per second.
// Bit layout: R(3) G(3) B(3) H(5) unused(2).
// Uses time-domain IIR filters for band splitting — no FFT needed.
func GenerateDetail(samples []float32, sampleRate int) []byte {
	durationSec := float64(len(samples)) / float64(sampleRate)
	numPoints := int(durationSec * detailEntriesPerSec)
	if numPoints < 1 {
		numPoints = 1
	}

	allBass, allMid, allTreble, allTotal := splitBandsAndPeaks(samples, sampleRate, numPoints)

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
	// stay as 0x00 0x00 rather than the 0xff 0x80 padding pattern. Real
	// rekordbox reserves 0xff 0x80 strictly for pre/post-roll silence; the
	// CDJ appears to treat mid-song occurrences as "end of
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
	// rekordbox exports never have all-zero entries; quiet sections
	// still carry colour bits (height may be 0, RGB non-zero). The CDJ
	// appears to treat 0x00 0x00 as "no data here" and stops color
	// rendering past the first occurrence.
	for i := 0; i < numPoints; i++ {
		var height, r, g, b uint8

		if RGB3BandMode {
			// 3-band mode: each band normalized to its own global peak.
			// Height = loudest band at this point (so a quiet hi-hat over silent
			// bass still produces a visible bar).
			bn := 0.0
			if bassMax > 1e-10 {
				bn = allBass[i] / bassMax
			}
			mn := 0.0
			if midMax > 1e-10 {
				mn = allMid[i] / midMax
			}
			tn := 0.0
			if trebleMax > 1e-10 {
				tn = allTreble[i] / trebleMax
			}
			peak := bn
			if mn > peak {
				peak = mn
			}
			if tn > peak {
				peak = tn
			}
			compressed := math.Pow(peak, 0.75) * 31.0
			if compressed > 31 {
				compressed = 31
			}
			height = uint8(compressed)

			r = uint8(math.Pow(bn, 0.75) * 7)
			g = uint8(math.Pow(mn, 0.75) * 7)
			b = uint8(math.Pow(tn, 0.75) * 7)
		} else {
			// Classic mode: H = (peak/trackMax)² × 31 — per-track normalised
			// with quadratic (energy) compression. rekordbox fits this
			// exactly across the ramp test: ratio 1.0 → H=31, 0.7 → 15, 0.5 → 7,
			// 0.25 → 1, 0.125 → 0. And in the tones file (all tones at the same
			// amplitude) every entry has ratio=1 so H=31 throughout, which is
			// what real shows. Tracks where the loudest moment defines "31"
			// is what gives real waveforms their punchy character.
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
		}

		// Floor to avoid emitting 0x00 0x00 mid-song. rekordbox never
		// emits the all-zero pair; its quiet entries always carry at least
		// one non-zero bit (most commonly r=7 from the bass band's
		// per-track normalisation).
		if r == 0 && g == 0 && b == 0 && height == 0 {
			r = 7
		}

		word := uint16(r&7)<<13 | uint16(g&7)<<10 | uint16(b&7)<<7 | uint16(height&0x1f)<<2
		buf[i*2] = byte(word >> 8)
		buf[i*2+1] = byte(word & 0xff)
	}

	// Pre/post-roll padding. rekordbox marks the silent regions at
	// the start and end of the track with the padding pattern 0xff 0x80
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

		// Padding / silence check. rekordbox emits 0xe0
		// (brightness=7, height=0) in PWV3 for silent pre/post-roll
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

// WrapANLZ wraps raw data in an ANLZ tag structure for dbserver responses.
// fourcc is e.g. "PWV4", "PWV5", "PVB2", "PSSI". entrySize is bytes per entry.
// For PVB2, pass the file size as entrySize (used in the extended header).
func WrapANLZ(fourcc string, entrySize int, data []byte) []byte {
	numEntries := len(data) / entrySize

	// Header length is tag-specific (verified against rekordbox .EXT/.2EX):
	// PVB2/PVBR=32, PWV6=20 (entry_size+num), PWVC=14 (u2 pad), rest=24.
	var lenHeader uint32
	switch fourcc {
	case "PVB2", "PVBR":
		lenHeader = 32
	case "PWV6":
		lenHeader = 20
	case "PWVC":
		lenHeader = 14
	default:
		lenHeader = 24
	}

	lenTag := lenHeader + uint32(len(data))
	sectionLen := lenTag

	buf := make([]byte, 4+int(lenTag))

	// 4-byte LE prefix: section length
	binary.LittleEndian.PutUint32(buf[0:], sectionLen)

	// ANLZ section header
	copy(buf[4:8], fourcc)
	binary.BigEndian.PutUint32(buf[8:], lenHeader)
	binary.BigEndian.PutUint32(buf[12:], lenTag)

	// Extended header — tag-specific.
	switch fourcc {
	case "PWV5":
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00960305)
	case "PWV4":
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00000000)
	case "PWV7":
		// 3-band detail: entry_size(3), num_entries, u2 rate(150), u2 pad.
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00960000)
	case "PWV6":
		// 3-band preview: entry_size(3), num_entries. 20-byte header (no rate).
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
	case "PWVC":
		// 3-band colour metadata: 14-byte header (u2 pad), 6-byte body.
		binary.BigEndian.PutUint16(buf[16:], 0)
	case "PVB2", "PVBR":
		// rekordbox PVB2: 32-byte header.
		// ext bytes 12-15: 0 (u1)
		// ext bytes 16-19: 0 (u2)
		// ext bytes 20-23: file size (u3)
		// ext bytes 24-27: 0x190 = 400 (u4)
		// ext bytes 28-31: 0x14 = 20 (u5)
		binary.BigEndian.PutUint32(buf[16:], 0)
		binary.BigEndian.PutUint32(buf[20:], 0)
		binary.BigEndian.PutUint32(buf[24:], uint32(entrySize)) // file size passed as entrySize
		binary.BigEndian.PutUint32(buf[28:], 0x00000190)        // 400
		binary.BigEndian.PutUint32(buf[32:], 0x00000014)        // 20
	default:
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
	}

	// Data (starts after header)
	copy(buf[4+int(lenHeader):], data)

	return buf
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
		// Power curve: rekordbox uses aggressive compression that produces
		// most heights at 4-6 with only loud peaks reaching 16.
		// pow(2.3) matches the observed distribution from rekordbox PWAV data.
		scaled := math.Pow(normalized, 2.3) * float64(maxH)
		if scaled > float64(maxH) {
			scaled = float64(maxH)
		}
		h := uint8(scaled)
		if h == 0 && rmsVals[i] > 0 {
			h = 1 // rekordbox never produces zero heights for non-silent segments
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
	numPoints := int(durationSec * detailEntriesPerSec)
	if numPoints < 1 {
		numPoints = 1
	}
	// PWV7 uses ABSOLUTE per-band RMS × scale — rekordbox's detail 3-band is
	// bass-heavy / treble-light, calibrated via tools/wavecompare -pwv7.
	return pack3BandAbs(samples, sampleRate, numPoints, Detail3BassScale, Detail3MidScale, Detail3TrebleScale)
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
	bf := applyBiquad(samples, butterworthLow(BandBassMidHz, sr))
	tf := applyBiquad(samples, butterworthHigh(BandMidTrebleHz, sr))
	mf := applyBiquad(applyBiquad(samples, butterworthHigh(BandBassMidHz, sr)), butterworthLow(BandMidTrebleHz, sr))
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
// calibrated against rekordbox PWV6 (tools/wavecompare -pwv6).
func GeneratePreview3Band(samples []float32, sampleRate int) []byte {
	return pack3BandAbs(samples, sampleRate, colorPreviewPoints, Preview3BassScale, Preview3MidScale, Preview3TrebleScale)
}


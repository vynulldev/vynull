// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "math"

// Lightweight vocal-presence detection (our own). It's a cosmetic nice-to-have:
// mark which phrases contain singing so the UI can show it. Mono-only (DecodePCM gives mono,
// so no centre-channel trick), so it leans on three per-frame proxies for the
// human voice, each covering a failure mode of the others:
//
//   - vocal-band ratio: fraction of spectral energy in the ~300–3400 Hz voice
//     band (rejects bass-only drops / sub-heavy sections);
//   - voicing: 1 − spectral flatness of that band, i.e. harmonic/pitched rather
//     than noisy (rejects percussion / hats / white-noise risers);
//   - syllabic modulation: how much the band's energy fluctuates over ~1.2 s
//     (rejects sustained pads and steady lead synths, which don't articulate
//     like sung words).
//
// The product of the three is a per-frame vocal score; AnnotateVocals averages
// it per phrase and thresholds. It WILL confuse expressive melodic leads for
// vocals and miss heavily-processed/vocoded vocals — acceptable for a display
// hint, not a classifier.

var (
	VocalBandLoHz  = 300.0
	VocalBandHiHz  = 3400.0
	VocalThreshold = 0.12 // per-phrase mean score above this → has vocal
)

// detectVocals returns a per-frame vocal-presence score (≥0) and the frame
// interval in ms. nil if the track is too short.
func detectVocals(samples []float32, sampleRate int) ([]float64, float64) {
	const frameSize, hop = 2048, 512
	n := (len(samples) - frameSize) / hop
	if n < 8 {
		return nil, 0
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(frameSize-1)))
	}
	windows := make([][]float64, n)
	for k := 0; k < n; k++ {
		off := k * hop
		w := make([]float64, frameSize)
		for j := 0; j < frameSize; j++ {
			w[j] = float64(samples[off+j]) * win[j]
		}
		windows[k] = w
	}
	re, im := batchFFTGPU(windows, frameSize)

	freqPerBin := float64(sampleRate) / float64(frameSize)
	loBin := int(VocalBandLoHz / freqPerBin)
	if loBin < 1 {
		loBin = 1
	}
	hiBin := int(VocalBandHiHz / freqPerBin)
	if hiBin > frameSize/2 {
		hiBin = frameSize / 2
	}

	vbeRatio := make([]float64, n) // vocal-band energy fraction
	voicing := make([]float64, n)  // 1 − spectral flatness of the vocal band
	for k := 0; k < n && k < len(re); k++ {
		var total, vocal, logSum float64
		var cnt int
		for bin := 1; bin < frameSize/2; bin++ {
			mag := math.Sqrt(re[k][bin]*re[k][bin] + im[k][bin]*im[k][bin])
			total += mag
			if bin >= loBin && bin < hiBin {
				vocal += mag
				logSum += math.Log(mag + 1e-12)
				cnt++
			}
		}
		if total > 0 {
			vbeRatio[k] = vocal / total
		}
		if cnt > 0 && vocal > 0 {
			geo := math.Exp(logSum / float64(cnt))
			arith := vocal / float64(cnt)
			voicing[k] = 1 - geo/(arith+1e-12) // flat (noise)→0, peaky (pitched)→1
		}
	}

	// Syllabic modulation: coefficient of variation of vbeRatio over ~1.2 s.
	frameRate := float64(sampleRate) / float64(hop)
	w := int(1.2 * frameRate)
	if w < 2 {
		w = 2
	}
	score := make([]float64, n)
	for k := 0; k < n; k++ {
		a, b := k-w, k+w
		if a < 0 {
			a = 0
		}
		if b > n {
			b = n
		}
		var mean float64
		for i := a; i < b; i++ {
			mean += vbeRatio[i]
		}
		mean /= float64(b - a)
		var varr float64
		for i := a; i < b; i++ {
			d := vbeRatio[i] - mean
			varr += d * d
		}
		cv := math.Sqrt(varr/float64(b-a)) / (mean + 1e-9)
		if cv > 1 {
			cv = 1
		}
		score[k] = vbeRatio[k] * voicing[k] * (0.5 + 0.5*cv)
	}
	return score, 1000.0 * float64(hop) / float64(sampleRate)
}

// AnnotateVocals sets HasVocal on each phrase by averaging the vocal score over
// the phrase's time span and thresholding. downbeatMs is where beat 1 sits.
func AnnotateVocals(samples []float32, sampleRate int, bpm, downbeatMs float64, phrases []Phrase) {
	if bpm <= 0 || len(phrases) == 0 {
		return
	}
	score, msPerFrame := detectVocals(samples, sampleRate)
	if score == nil {
		return
	}
	msPerBeat := 60000.0 / bpm
	for i := range phrases {
		startMs := downbeatMs + float64(phrases[i].StartBeat-1)*msPerBeat
		endMs := downbeatMs + float64(phrases[i].EndBeat-1)*msPerBeat
		f0 := int(startMs / msPerFrame)
		f1 := int(endMs / msPerFrame)
		if f0 < 0 {
			f0 = 0
		}
		if f1 > len(score) {
			f1 = len(score)
		}
		if f1-f0 < 1 {
			continue
		}
		var sum float64
		for f := f0; f < f1; f++ {
			sum += score[f]
		}
		mean := sum / float64(f1-f0)
		phrases[i].VocalScore = mean
		phrases[i].HasVocal = mean > VocalThreshold
	}
}

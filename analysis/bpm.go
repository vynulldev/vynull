// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"sort"
)

// BeatResult holds the detected BPM and actual beat positions.
type BeatResult struct {
	BPM      float64   // detected tempo
	Beats    []float64 // beat positions in milliseconds
	Downbeat float64   // first downbeat (beat 1) in milliseconds
}

// tempoPriorCenter / tempoPriorSigma define the perceptual tempo prior used to
// resolve harmonic ambiguity in BPM detection — a log-Gaussian over BPM that
// favours the typical dance range. sigma is in octaves.
const (
	tempoPriorCenter = 130.0
	tempoPriorSigma  = 0.5
)

// tempoPrior weights a candidate BPM by perceptual likelihood. Without it,
// autocorrelation alone often locks onto a sub-/super-harmonic (most commonly
// the 2/3 sub-harmonic — a 124 BPM track also peaks at ~82.7); the prior pulls
// the choice back to the musically-correct tempo.
func tempoPrior(bpm float64) float64 {
	x := math.Log2(bpm/tempoPriorCenter) / tempoPriorSigma
	return math.Exp(-0.5 * x * x)
}

// DetectBeats finds individual beat positions and computes BPM from them.
// Uses onset peak detection with adaptive threshold, then computes BPM
// from the median inter-beat interval.
func DetectBeats(samples []float32, sampleRate int) *BeatResult {
	if len(samples) < sampleRate*2 {
		return &BeatResult{}
	}

	hopSize := 128
	frameSize := 1024

	// Regular low-pass at 500Hz for BPM detection + peak-scoring phase.
	filtered := lowPass(samples, float64(sampleRate), 500.0)

	// Zero-phase low-pass for DP beat tracker: eliminates the ~60ms transient
	// delay, giving more accurate beat positions at actual transient starts.
	zpForward := lowPass(samples, float64(sampleRate), 500.0)
	for i, j := 0, len(zpForward)-1; i < j; i, j = i+1, j-1 {
		zpForward[i], zpForward[j] = zpForward[j], zpForward[i]
	}
	zpFiltered := lowPass(zpForward, float64(sampleRate), 500.0)
	for i, j := 0, len(zpFiltered)-1; i < j; i, j = i+1, j-1 {
		zpFiltered[i], zpFiltered[j] = zpFiltered[j], zpFiltered[i]
	}

	numFrames := (len(filtered) - frameSize) / hopSize
	if numFrames < 2 {
		return &BeatResult{}
	}

	// Energy-based onset detection for BPM estimation (proven accurate).
	energy := make([]float64, numFrames)
	for i := 0; i < numFrames; i++ {
		off := i * hopSize
		var sum float64
		for j := 0; j < frameSize && off+j < len(filtered); j++ {
			v := float64(filtered[off+j])
			sum += v * v
		}
		energy[i] = math.Sqrt(sum / float64(frameSize))
	}

	onset := make([]float64, numFrames)
	for i := 1; i < numFrames; i++ {
		diff := energy[i] - energy[i-1]
		if diff > 0 {
			onset[i] = diff
		}
	}

	// Complex Spectral Difference onset for phase refinement (low-freq only).
	// Restricted to bins below 500Hz to focus on kick transients and ignore
	// hi-hats/tonal changes that confuse phase alignment.
	// FFTs are batched (GPU or parallel CPU), then CSD is computed sequentially.
	csdHop := 256
	csdFrameSize := 1024
	csdNumFrames := (len(samples) - csdFrameSize) / csdHop
	csdOnset := make([]float64, csdNumFrames)
	if csdNumFrames > 2 {
		freqPerBin := float64(sampleRate) / float64(csdFrameSize)
		maxBin := int(500.0 / freqPerBin)
		if maxBin < 1 {
			maxBin = 1
		}

		// Process CSD in chunks to limit memory usage.
		hannWindow := make([]float64, csdFrameSize)
		for i := range hannWindow {
			hannWindow[i] = 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(csdFrameSize-1)))
		}

		prevMag := make([]float64, maxBin+1)
		prevPhase := make([]float64, maxBin+1)
		prevPrevPhase := make([]float64, maxBin+1)

		const csdChunk = 8192
		for chunkStart := 0; chunkStart < csdNumFrames; chunkStart += csdChunk {
			chunkEnd := chunkStart + csdChunk
			if chunkEnd > csdNumFrames {
				chunkEnd = csdNumFrames
			}
			n := chunkEnd - chunkStart

			windows := make([][]float64, n)
			for ci := 0; ci < n; ci++ {
				off := (chunkStart + ci) * csdHop
				w := make([]float64, csdFrameSize)
				for j := 0; j < csdFrameSize && off+j < len(samples); j++ {
					w[j] = float64(samples[off+j]) * hannWindow[j]
				}
				windows[ci] = w
			}

			fftReals, fftImags := batchFFTGPU(windows, csdFrameSize)

			for ci := 0; ci < n && ci < len(fftReals); ci++ {
				fftR := fftReals[ci]
				fftI := fftImags[ci]
				var df float64
				limit := maxBin + 1
				if limit > len(fftR) {
					limit = len(fftR)
				}
				for bin := 1; bin < limit; bin++ {
					mag := math.Sqrt(fftR[bin]*fftR[bin] + fftI[bin]*fftI[bin])
					phase := math.Atan2(fftI[bin], fftR[bin])
					expectedPhase := 2*prevPhase[bin] - prevPrevPhase[bin]
					predR := prevMag[bin] * math.Cos(expectedPhase)
					predI := prevMag[bin] * math.Sin(expectedPhase)
					actR := mag * math.Cos(phase)
					actI := mag * math.Sin(phase)
					diffR := actR - predR
					diffI := actI - predI
					df += math.Sqrt(diffR*diffR + diffI*diffI)
					prevPrevPhase[bin] = prevPhase[bin]
					prevPhase[bin] = phase
					prevMag[bin] = mag
				}
				csdOnset[chunkStart+ci] = df
			}
		}
	}

	// Normalize onset to prevent numerical issues.
	var maxOnset float64
	for _, v := range onset {
		if v > maxOnset {
			maxOnset = v
		}
	}
	if maxOnset > 0 {
		for i := range onset {
			onset[i] /= maxOnset
		}
	}

	// Adaptive threshold: local mean over a window + offset.
	// Peaks above threshold are beat candidates.
	framesPerSec := float64(sampleRate) / float64(hopSize)
	windowFrames := int(framesPerSec * 0.5) // 500ms window
	if windowFrames < 3 {
		windowFrames = 3
	}

	threshold := make([]float64, numFrames)
	for i := range onset {
		start := i - windowFrames
		end := i + windowFrames
		if start < 0 {
			start = 0
		}
		if end > numFrames {
			end = numFrames
		}
		var sum float64
		for j := start; j < end; j++ {
			sum += onset[j]
		}
		mean := sum / float64(end-start)
		threshold[i] = mean * 1.5 // threshold = 1.5x local mean
	}

	// Find onset peaks above threshold with minimum spacing.
	minSpacingFrames := int(framesPerSec * 60.0 / 200.0) // max 200 BPM
	if minSpacingFrames < 1 {
		minSpacingFrames = 1
	}

	var peakFrames []int
	lastPeak := -minSpacingFrames
	for i := 1; i < numFrames-1; i++ {
		if onset[i] > threshold[i] &&
			onset[i] > onset[i-1] &&
			onset[i] >= onset[i+1] &&
			i-lastPeak >= minSpacingFrames {
			peakFrames = append(peakFrames, i)
			lastPeak = i
		}
	}

	if len(peakFrames) < 4 {
		return &BeatResult{}
	}

	// Compute inter-beat intervals (in ms).
	msPerFrame := float64(hopSize) / float64(sampleRate) * 1000.0
	var intervals []float64
	for i := 1; i < len(peakFrames); i++ {
		ibi := float64(peakFrames[i]-peakFrames[i-1]) * msPerFrame
		intervals = append(intervals, ibi)
	}

	// Use autocorrelation on onset to find the dominant periodicity.
	// This is more robust than raw median for tracks with off-beat hits.
	minLag := int(framesPerSec * 60.0 / 200.0)
	maxLag := int(framesPerSec * 60.0 / 60.0)
	if maxLag >= numFrames/2 {
		maxLag = numFrames/2 - 1
	}

	corrs := make([]float64, maxLag+1)
	bestLag := 0
	bestCorr := 0.0
	for lag := minLag; lag <= maxLag; lag++ {
		var corr float64
		count := numFrames - lag
		for i := 0; i < count; i++ {
			corr += onset[i] * onset[i+lag]
		}
		corr /= float64(count)
		corrs[lag] = corr
		if corr > bestCorr {
			bestCorr = corr
			bestLag = lag
		}
	}

	if bestLag == 0 {
		return &BeatResult{}
	}

	// Parabolic interpolation for sub-frame precision.
	refinedLag := float64(bestLag)
	if bestLag > minLag && bestLag < maxLag {
		alpha := corrs[bestLag-1]
		beta := corrs[bestLag]
		gamma := corrs[bestLag+1]
		denom := alpha - 2*beta + gamma
		if math.Abs(denom) > 1e-10 {
			refinedLag = float64(bestLag) - 0.5*(alpha-gamma)/denom
		}
	}

	rawBPM := 60.0 * framesPerSec / refinedLag

	// Multi-ratio correction: test the raw BPM and common multiples/divisions.
	// Autocorrelation can lock onto harmonics (2x, 3x) or subharmonics (1/2, 1/3)
	// as well as triplet ratios (3/4, 4/3). Score each candidate by checking
	// the autocorrelation strength at that specific lag.
	// The raw BPM from the interpolated peak is precise at the original harmonic.
	// Now find which ratio candidate is in the 80-170 range with the best
	// autocorrelation support.
	ratios := []float64{1, 0.5, 2, 1.0 / 3, 3, 0.75, 4.0 / 3, 1.5, 2.0 / 3}

	bpm := rawBPM
	bestCandScore := 0.0
	for _, r := range ratios {
		c := rawBPM * r
		if c < 80 || c > 170 {
			continue
		}
		// Check autocorrelation support at this candidate's lag.
		candLag := int(math.Round(framesPerSec * 60.0 / c))
		if candLag < minLag || candLag > maxLag {
			continue
		}
		// Weight the autocorrelation support by a perceptual tempo prior so a
		// strong sub-/super-harmonic (e.g. the 2/3 lag of a 124 BPM track at
		// 82.7) doesn't win over the musically-correct tempo.
		score := corrs[candLag] * tempoPrior(c)
		if score > bestCandScore {
			bestCandScore = score
			// Use the precisely interpolated raw BPM × ratio.
			// This preserves the sub-frame precision from the original peak.
			bpm = rawBPM * r
		}
	}

	bpm = math.Round(bpm*100) / 100

	// Now build a refined beat grid by snapping peaks to a regular grid.
	// Use the detected peaks to find the best phase alignment.
	msPerBeat := 60000.0 / bpm
	durationMs := float64(len(samples)) / float64(sampleRate) * 1000.0

	// Convert peak frames to ms.
	peakMs := make([]float64, len(peakFrames))
	for i, f := range peakFrames {
		peakMs[i] = float64(f) * msPerFrame
	}

	// Dynamic programming beat tracker implementing the method in
	// D. Ellis, "Beat Tracking by Dynamic Programming" (J. New Music
	// Research 36(1), 2007): a forward cumulative-score pass with a
	// log-Gaussian tempo-transition penalty plus a backtrace. This is an
	// independent implementation of that published algorithm.
	// Uses the CSD onset function for precise transient-start detection,
	// with dynamic programming to find the optimal sequence of beat positions
	// that follow the expected tempo while landing on actual onsets.
	//
	// Parameters (Ellis 2007 defaults):
	//   alpha = 0.9: weight of continuity vs local onset (90% continuity)
	//   tightness = 4.0: how strictly beats must follow expected period

	// DP beat tracker uses regular filtered onset (proven pattern tracking).
	trackOnset := make([]float64, len(onset))
	copy(trackOnset, onset)
	trackMsPerFrame := msPerFrame

	periodFrames := msPerBeat / trackMsPerFrame
	nTrackFrames := len(trackOnset)

	// Normalize onset function.
	var trackMax float64
	for _, v := range trackOnset {
		if v > trackMax {
			trackMax = v
		}
	}
	if trackMax > 0 {
		for i := range trackOnset {
			trackOnset[i] /= trackMax
		}
	}

	// DP forward pass.
	const alpha = 0.9
	const tightness = 4.0
	cumscore := make([]float64, nTrackFrames)
	backlink := make([]int, nTrackFrames)
	for i := range backlink {
		backlink[i] = -1
	}

	// Search range: look back between period/2 and 2*period frames.
	pMin := int(periodFrames * 0.5)
	pMax := int(periodFrames * 2.0)
	if pMin < 1 {
		pMin = 1
	}

	for i := 0; i < nTrackFrames; i++ {
		// Local score = onset strength at this frame.
		localScore := trackOnset[i]

		// Find best previous beat in range [i-pMax, i-pMin].
		bestPrev := -1.0
		bestIdx := -1
		for j := pMin; j <= pMax; j++ {
			prev := i - j
			if prev < 0 {
				continue
			}
			// Log-Gaussian penalty: how far is (i-prev) from the expected period?
			ratio := float64(j) / periodFrames
			logPenalty := -0.5 * tightness * tightness * math.Log(ratio) * math.Log(ratio)
			weight := math.Exp(logPenalty)

			score := cumscore[prev] + weight
			if score > bestPrev {
				bestPrev = score
				bestIdx = prev
			}
		}

		if bestIdx >= 0 {
			cumscore[i] = alpha*bestPrev + (1-alpha)*localScore
			backlink[i] = bestIdx
		} else {
			cumscore[i] = (1 - alpha) * localScore
		}
	}

	// Backtrack: start from the best score in the last beat-period of the track.
	startFrame := nTrackFrames - int(periodFrames)
	if startFrame < 0 {
		startFrame = 0
	}
	bestEnd := startFrame
	bestEndScore := cumscore[startFrame]
	for i := startFrame; i < nTrackFrames; i++ {
		if cumscore[i] > bestEndScore {
			bestEndScore = cumscore[i]
			bestEnd = i
		}
	}

	// Follow backlinks to collect beat frames.
	var beatFrames []int
	f := bestEnd
	for f >= 0 {
		beatFrames = append(beatFrames, f)
		f = backlink[f]
	}
	// Reverse to get chronological order.
	for i, j := 0, len(beatFrames)-1; i < j; i, j = i+1, j-1 {
		beatFrames[i], beatFrames[j] = beatFrames[j], beatFrames[i]
	}

	// Convert to milliseconds.
	var dpBeats []float64
	for _, bf := range beatFrames {
		dpBeats = append(dpBeats, math.Round(float64(bf)*trackMsPerFrame*10)/10)
	}

	// Compute two candidate phases:
	// 1. DP phase: from the dynamic programming beat tracker (better for soft intros)
	// 2. Peak-scoring phase: from onset-peak phase averaging (better for clear kicks)
	// Pick whichever aligns better with detected onset peaks.

	// DP phase: filter to strong beats, compute circular median.
	dpPhase := 0.0
	if len(dpBeats) >= 2 {
		var strongBeats []float64
		onsetThreshold := 0.05
		for _, b := range dpBeats {
			frame := int(b / trackMsPerFrame)
			if frame >= 0 && frame < len(trackOnset) && trackOnset[frame] > onsetThreshold {
				strongBeats = append(strongBeats, b)
			}
		}
		if len(strongBeats) < 4 {
			strongBeats = dpBeats
		}
		var offsets []float64
		for _, b := range strongBeats {
			offsets = append(offsets, math.Mod(b, msPerBeat))
		}
		sort.Float64s(offsets)
		bestCost := math.MaxFloat64
		for _, candidate := range offsets {
			var cost float64
			for _, off := range offsets {
				d := math.Abs(off - candidate)
				if d > msPerBeat/2 {
					d = msPerBeat - d
				}
				cost += d
			}
			if cost < bestCost {
				bestCost = cost
				dpPhase = candidate
			}
		}
	}

	// Peak-scoring phase: the old reliable method.
	peakPhase := 0.0
	{
		bestScore := 0.0
		snapWindow := msPerBeat * 0.15
		for phase := 0.0; phase < msPerBeat; phase += msPerFrame {
			var score float64
			for _, pk := range peakMs {
				beatPos := math.Mod(pk-phase, msPerBeat)
				if beatPos < 0 {
					beatPos += msPerBeat
				}
				if beatPos > msPerBeat/2 {
					beatPos = msPerBeat - beatPos
				}
				if beatPos < snapWindow {
					score += 1.0 - beatPos/snapWindow
				}
			}
			if score > bestScore {
				bestScore = score
				peakPhase = phase
			}
		}
		// Phase-averaging refinement: average the per-beat phase offsets.
		var offsetSum float64
		var offsetCount int
		for _, pk := range peakMs {
			offset := math.Mod(pk-peakPhase, msPerBeat)
			if offset < 0 {
				offset += msPerBeat
			}
			if offset > msPerBeat/2 {
				offset -= msPerBeat
			}
			if math.Abs(offset) <= 25.0 {
				offsetSum += offset
				offsetCount++
			}
		}
		if offsetCount > 0 {
			peakPhase += offsetSum / float64(offsetCount)
			peakPhase = math.Mod(peakPhase, msPerBeat)
			if peakPhase < 0 {
				peakPhase += msPerBeat
			}
		}
	}

	// Score both phases against zero-phase filtered energy at grid positions.
	scorePhase := func(phase float64) float64 {
		var score float64
		windowSamples := sampleRate / 100 // 10ms window
		for t := phase; t < durationMs; t += msPerBeat {
			center := int(t / 1000.0 * float64(sampleRate))
			start := center - windowSamples/2
			end := center + windowSamples/2
			if start < 0 {
				start = 0
			}
			if end > len(zpFiltered) {
				end = len(zpFiltered)
			}
			var energy float64
			for s := start; s < end; s++ {
				energy += float64(zpFiltered[s]) * float64(zpFiltered[s])
			}
			score += energy
		}
		return score
	}
	dpScore := scorePhase(dpPhase)
	peakScore := scorePhase(peakPhase)

	bestPhase := peakPhase
	if dpScore > peakScore*1.01 { // DP needs to be >1% better to override
		bestPhase = dpPhase
	}

	// Tempogram phase: the first-beat phase is the argument of the onset
	// envelope's complex DFT bin at the beat period (a 2π onset-envelope
	// tempogram, matching rekordbox's beat analysis). This replaces the
	// peak/DP phase, which only ever aligned to onset peaks in a fixed band
	// and matched rekordbox's grid ~30% of the time.
	if UseTempogramPhase {
		phaseOnset, phaseOnsetMs := onset, msPerFrame
		if UseMultiBandOnset {
			if mb, mbMs := multiBandOnset(samples, sampleRate); mb != nil {
				phaseOnset, phaseOnsetMs = mb, mbMs
			}
		}
		if tp, ok := tempogramPhase(phaseOnset, phaseOnsetMs, msPerBeat); ok {
			bestPhase = math.Mod(tp+TempogramLatencyMs, msPerBeat)
			if bestPhase < 0 {
				bestPhase += msPerBeat
			}
		}
	}

	// Extrapolate backward to time 0.
	var beats []float64
	startPhase := bestPhase
	for startPhase-msPerBeat >= 0 {
		startPhase -= msPerBeat
	}
	for t := startPhase; t < durationMs; t += msPerBeat {
		beats = append(beats, math.Round(t*10)/10)
	}

	// BPM rounding: snap to the nearest "nice" BPM value if within 0.1 BPM.
	// This prevents cumulative drift from sub-BPM precision errors.
	// Applied AFTER phase detection to avoid changing the DP tracker's period.
	roundedBPM := bpm
	if r := math.Round(bpm); math.Abs(bpm-r) < 0.15 {
		roundedBPM = r
	} else if r := math.Round(bpm*2) / 2; math.Abs(bpm-r) < 0.05 {
		roundedBPM = r
	}

	// If BPM was rounded, rebuild the grid with the rounded interval
	// but keep the same phase to prevent drift.
	if roundedBPM != bpm {
		roundedInterval := 60000.0 / roundedBPM
		phase := math.Mod(beats[0], msPerBeat)
		beats = beats[:0]
		startPhase := phase
		for startPhase-roundedInterval >= 0 {
			startPhase -= roundedInterval
		}
		for t := startPhase; t < durationMs; t += roundedInterval {
			beats = append(beats, math.Round(t*10)/10)
		}
		bpm = roundedBPM
		msPerBeat = roundedInterval
	}

	// Find downbeat (beat 1 of 4) using accent detection.
	downbeat := findDownbeat(beats)

	return &BeatResult{
		BPM:      bpm,
		Beats:    beats,
		Downbeat: downbeat,
	}
}

// Tempogram-phase calibration knobs, tuned to reproduce rekordbox's grids.
// Validated against imported rekordbox grids (n=400, near-exact-BPM subset
// n=208):
//
//	peak/DP 30% → tempogram 34% → multiband 42% → +band-norm 61%
//
// UseBandNorm is rekordbox's band "combination": adaptive per-band normalization
// (divide each band's flux by its own running EMA level, BandNormAlpha=0.99),
// then sum ALL bands. Equalizing the
// bands is what makes the full 25-band set usable; a raw sum is dominated by the
// bass and scores worse than a single band. MultiBandMaxHz is only the cutoff
// for the non-normalized fallback path. TempogramLatencyMs cancels the STFT
// framing+flux+EMA latency, centering the residual bias.
var (
	UseTempogramPhase  = true
	UseMultiBandOnset  = true
	UseBandNorm        = true
	BandNormAlpha      = 0.99
	MultiBandMaxHz     = 4096.0
	TempogramLatencyMs = 55.0
)

// rbBandEdgesHz is rekordbox's 25-band filterbank edge table (ascending Hz),
// used to build the multi-band onset novelty its tempogram runs on. Edges above
// Nyquist are clamped (empty bands) so the same table works at 44.1/48k.
var rbBandEdgesHz = []float64{
	917, 1507, 2195, 3113, 4096, 5079, 6062, 7045, 8028, 9011,
	9994, 10977, 11960, 12943, 13926, 14909, 15892, 17367, 19333, 21299,
	23265, 25886, 30802, 39322, 50790,
}

// multiBandOnset builds rekordbox's onset novelty: split the (mono) signal into
// the rbBandEdgesHz bands via STFT, take each band's half-wave-rectified
// magnitude flux, and sum across bands. A single broadband flux can't match
// rekordbox's phase because its novelty comes from this 25-band decomposition.
// Returns the novelty and its frame interval (ms); nil if the track is too short.
func multiBandOnset(samples []float32, sampleRate int) ([]float64, float64) {
	const frameSize, hop = 1024, 512
	n := (len(samples) - frameSize) / hop
	if n < 4 {
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
	nyq := float64(sampleRate) / 2
	nb := len(rbBandEdgesHz)
	// Precompute each band's [loBin,hiBin); band b spans (edge[b-1], edge[b]].
	hiBin := make([]int, nb)
	for b := 0; b < nb; b++ {
		hz := rbBandEdgesHz[b]
		if hz > nyq {
			hz = nyq
		}
		hb := int(hz / freqPerBin)
		if hb > frameSize/2 {
			hb = frameSize / 2
		}
		hiBin[b] = hb
	}

	onset := make([]float64, n)
	prevMag := make([]float64, nb)
	bandMag := make([]float64, nb)
	mean := make([]float64, nb) // per-band running level for adaptive normalization
	for k := 0; k < n && k < len(re); k++ {
		lo := 1
		for b := 0; b < nb; b++ {
			var s float64
			for bin := lo; bin < hiBin[b]; bin++ {
				s += math.Sqrt(re[k][bin]*re[k][bin] + im[k][bin]*im[k][bin])
			}
			bandMag[b] = s
			lo = hiBin[b]
		}
		if k > 0 {
			var flux float64
			for b := 0; b < nb; b++ {
				d := bandMag[b] - prevMag[b]
				if UseBandNorm {
					// Adaptive per-band normalization (rekordbox's combination):
					// divide each band's flux by its own EMA-tracked running
					// level so loud and quiet bands contribute comparably,
					// then sum ALL bands. Equalization makes the full band set
					// usable, unlike a raw low-band sum.
					if d > 0 {
						flux += d / (mean[b] + 1e-9)
					}
				} else {
					if rbBandEdgesHz[b] > MultiBandMaxHz {
						break
					}
					if d > 0 {
						flux += d
					}
				}
			}
			onset[k] = flux
		}
		for b := 0; b < nb; b++ {
			mean[b] = BandNormAlpha*mean[b] + (1-BandNormAlpha)*bandMag[b]
		}
		copy(prevMag, bandMag)
	}
	return onset, float64(hop) / float64(sampleRate) * 1000.0
}

// tempogramPhase returns the first-beat sub-beat offset (ms, in [0,msPerBeat))
// as the argument of the onset envelope's complex DFT bin at the beat period.
// Modeling the envelope's beat component as A·cos(ω·n − ψ), the first beat sits
// at frame ψ/ω where ψ = atan2(Σ e·sin(ωn), Σ e·cos(ωn)) and ω = 2π/period.
// Unlike peak/DP phase, this uses the whole-track periodic structure (valid
// because tempo is constant post-rounding), which is how rekordbox anchors.
func tempogramPhase(onset []float64, msPerFrame, msPerBeat float64) (float64, bool) {
	period := msPerBeat / msPerFrame // envelope frames per beat
	if period <= 1 || len(onset) < int(period*2) {
		return 0, false
	}
	w := 2 * math.Pi / period
	var re, im float64
	for n, e := range onset {
		re += e * math.Cos(w*float64(n))
		im += e * math.Sin(w*float64(n))
	}
	if re == 0 && im == 0 {
		return 0, false
	}
	n0 := math.Mod(math.Atan2(im, re)/w, period)
	if n0 < 0 {
		n0 += period
	}
	return n0 * msPerFrame, true
}

// findDownbeat picks which beat of the grid is beat 1 of the bar. It returns
// the first beat.
//
// This deliberately does NOT try to find the musical "1" by accent strength.
// In four-on-the-floor dance music every beat carries a kick, so onset accent is
// near-identical across the four rotations; the back-beat (snare/clap on 2 & 4)
// is a cleaner cue in principle, but in practice both approaches do worse than
// simply trusting the first beat. Measured against rekordbox's grids (tools/
// bpmcompare, phase-aligned tracks): accent-scoring scored 41%, a snare/back-beat
// model 21%, and the first-beat rule 48% — which is the ceiling set by how often
// our grid's bar parity matches rekordbox's at all. The remaining misses are
// off-by-±1-beat, i.e. our beat tracker counted a different number of beats in
// the intro than rekordbox did. That is a beat-grid alignment problem upstream
// of this function, not one a downbeat selector can recover, so anything fancier
// here just adds risk without headroom.
func findDownbeat(beats []float64) float64 {
	if len(beats) > 0 {
		return beats[0]
	}
	return 0
}

// DetectBPM is a convenience wrapper that returns just the BPM.
func DetectBPM(samples []float32, sampleRate int) float64 {
	return DetectBeats(samples, sampleRate).BPM
}

// DetectDownbeat is a convenience wrapper that returns just the downbeat position.
func DetectDownbeat(samples []float32, sampleRate int, bpm float64) float64 {
	result := DetectBeats(samples, sampleRate)
	if result.Downbeat > 0 {
		return result.Downbeat
	}
	// Fallback: use the first beat.
	if len(result.Beats) > 0 {
		return result.Beats[0]
	}
	return 0
}

// lowPass applies a simple single-pole IIR low-pass filter.
func lowPass(samples []float32, sampleRate, cutoff float64) []float32 {
	alpha := cutoff / (sampleRate/2.0 + cutoff)
	out := make([]float32, len(samples))
	if len(samples) == 0 {
		return out
	}
	out[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		out[i] = out[i-1] + float32(alpha)*(samples[i]-out[i-1])
	}
	return out
}

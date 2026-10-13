// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"sort"
)

// Harmonic downbeat parity — a port of the shape of rekordbox's dedicated
// downbeat analyzer (rekordbox 6.6.4, reverse-engineered; see
// docs/reference/rekordbox-downbeat-analysis.md in vynull-tools for the evidence).
//
// After its tempo/phase tracker has produced a constant-period grid, rekordbox
// runs a separate analyzer over the audio that decides which beat PARITY
// carries beat 1. It does not score per-beat accent (in four-on-the-floor every
// beat has a kick). It looks for where the HARMONY changes:
//
//  1. mono, 8th-order Butterworth low-pass at 0.4·(SR/8), decimate by 8
//     (44.1 kHz → 5512.5 Hz), first 210 s only;
//  2. a semitone pitch spectrum, 73 bins from A0 = 27.5 Hz (27.5·2^(k/12));
//  3. beat-synchronous change events: the strongest change point in each
//     2-bar (8-beat) segment, kept only where the music is active around it;
//  4. fold each event onto a 2-beat cycle and vote: fewer than 7 on-beat
//     events → keep the tracker's downbeat; >88.6% on one parity → that parity;
//     ≤15% → the other; otherwise the parity carrying more change energy.
//
// Chord and bass-note changes land on bar lines (beat 1, and beat 3 for
// half-bar changes) far more than on beats 2 and 4, so the parity of the
// change events is the parity of the downbeat. rekordbox resolves only this
// parity: its second stage (which half of the bar) is unreachable in 6.6.4, so
// a whole-bar-half (±2 beat) choice still comes from the beat tracker.

// Constants recovered from the binary.
const (
	dbDecimate      = 8     // +0x74
	dbLPFraction    = 0.4   // DAT_143804bf0: LPF cutoff as a fraction of SR/8
	dbMaxSeconds    = 210.0 // DAT_1438054c0: analyze the first 210 s
	dbPitchBase     = 27.5  // DAT_143806e40: A0
	dbPitchBins     = 73    // 0x49 semitones, A0..A6
	dbMinEvents     = 7     // fewer on-beat events → keep the tracker's parity
	dbRatioHigh     = 0.886 // DAT_143804e10
	dbRatioLow      = 0.15  // DAT_143804aa0
	dbSegmentBeats  = 8     // events are picked per 2-bar segment
	dbContextBeats  = 2     // change measured between ±2-beat neighbourhoods
	dbActiveFrac    = 0.3   // a beat is "active" above this fraction of median energy
	dbActiveReachBe = 4     // an event needs active music this many beats each side
)

// DownbeatHarmonic toggles the harmonic parity correction in findDownbeat. Off
// by default: against 1572 phase-matched rekordbox grids it scores 78.4%
// against the first beat's 86.7%, because our change feature does not yet
// reproduce rekordbox's events closely (see
// docs/reference/rekordbox-downbeat-analysis.md in vynull-tools).
var DownbeatHarmonic = false

// DownbeatVote is the diagnostic outcome of the harmonic parity vote.
type DownbeatVote struct {
	Events     int     // on-beat change events counted
	EvenFrac   float64 // share landing on the current downbeat's parity
	EvenEnergy float64
	OddEnergy  float64
	Flip       bool // true: beat 1 moves one beat later
	Decided    bool // false: too few events, tracker's choice kept
}

// harmonicParity votes on whether beat 1 belongs on the parity of downIdx or
// the other one, and returns the (possibly shifted by one) downbeat index.
func harmonicParity(samples []float32, sampleRate int, beats []float64, downIdx int) (int, DownbeatVote) {
	var vote DownbeatVote
	if len(beats) < dbSegmentBeats*2 || sampleRate <= 0 {
		return downIdx, vote
	}
	limit := len(samples)
	if max := int(dbMaxSeconds * float64(sampleRate)); limit > max {
		limit = max
	}
	low := butterworthLowpass8(samples[:limit], float64(sampleRate), dbLPFraction*float64(sampleRate)/dbDecimate)
	dec := make([]float64, len(low)/dbDecimate)
	for i := range dec {
		dec[i] = low[i*dbDecimate]
	}
	rate := float64(sampleRate) / dbDecimate

	frames, frameE, frameMs, centerMs := pitchFrames(dec, rate)
	if len(frames) == 0 {
		return downIdx, vote
	}
	nf := len(frames)
	period := beats[1] - beats[0]
	toFrame := func(ms float64) int { return int(math.Round((ms - centerMs) / frameMs)) }

	// Harmonic change per frame: cosine distance between the mean pitch
	// spectrum over the preceding and following beat.
	K := int(math.Round(period / frameMs))
	if K < 2 {
		return downIdx, vote
	}
	nov := make([]float64, nf)
	for f := K; f+K <= nf; f++ {
		nov[f] = 1 - cosine(meanVec(frames[f-K:f]), meanVec(frames[f:f+K]))
	}

	// Per-beat energy for the activity gate.
	nb := 0
	energy := make([]float64, 0, len(beats))
	for b := 0; b+1 < len(beats); b++ {
		lo, hi := toFrame(beats[b]), toFrame(beats[b+1])
		if lo < 0 {
			lo = 0
		}
		if hi > nf {
			break
		}
		var e float64
		for f := lo; f < hi; f++ {
			e += frameE[f]
		}
		energy = append(energy, e/float64(max(1, hi-lo)))
		nb++
	}
	if nb < dbSegmentBeats*2 {
		return downIdx, vote
	}
	med := median64(energy)
	isActive := func(i int) bool { return i >= 0 && i < nb && energy[i] > dbActiveFrac*med }

	// One event per 2-bar segment: its strongest change frame. Like
	// rekordbox, an event that does not fall within a quarter beat of a beat
	// is off-beat and ignored, and an event needs active music around it.
	tolMs := period / 4
	for s := 0; s+dbSegmentBeats < nb; s += dbSegmentBeats {
		lo, hi := toFrame(beats[s]-tolMs), toFrame(beats[s+dbSegmentBeats]-tolMs)
		if lo < K {
			lo = K
		}
		if hi > nf-K {
			hi = nf - K
		}
		best, bestV := -1, 0.0
		for f := lo; f < hi; f++ {
			if nov[f] > bestV {
				best, bestV = f, nov[f]
			}
		}
		if best < 0 {
			continue
		}
		t := centerMs + float64(best)*frameMs
		j := int(math.Round((t - beats[0]) / period))
		if j < 0 || j >= nb || math.Abs(t-beats[j]) > tolMs {
			continue // off-beat change
		}
		if !isActive(j) || !isActive(j-dbActiveReachBe) || !isActive(j+dbActiveReachBe) {
			continue
		}
		vote.Events++
		if ((j-downIdx)%2+2)%2 == 0 {
			vote.EvenFrac++
			vote.EvenEnergy += bestV
		} else {
			vote.OddEnergy += bestV
		}
	}
	if vote.Events < dbMinEvents {
		return downIdx, vote
	}
	vote.EvenFrac /= float64(vote.Events)
	vote.Decided = true
	switch {
	case vote.EvenFrac > dbRatioHigh:
		vote.Flip = false
	case vote.EvenFrac <= dbRatioLow:
		vote.Flip = true
	default:
		vote.Flip = vote.OddEnergy > vote.EvenEnergy
	}
	if vote.Flip {
		// rekordbox maps a parity flip to "beat 1 is the next beat"
		// (downbeat 2 → table [1,4,3,2] → renumber from the following beat).
		return (downIdx + 1) % 4, vote
	}
	return downIdx, vote
}

// pitchFrames returns the log semitone pitch spectrum (73 bins from A0) of
// the decimated signal per STFT frame, each frame's energy, the frame hop in
// ms, and the time of frame 0's centre in ms.
func pitchFrames(x []float64, rate float64) ([][]float64, []float64, float64, float64) {
	const n = 2048 // ~0.37 s at 5512.5 Hz, 2.7 Hz resolution
	const hop = 256
	win := make([]float64, n)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	type span struct{ lo, hi int }
	bins := make([]span, dbPitchBins)
	binHz := rate / n
	for k := range bins {
		f := dbPitchBase * math.Pow(2, float64(k)/12)
		lo := int(math.Floor(f * math.Pow(2, -1.0/24) / binHz))
		hi := int(math.Ceil(f * math.Pow(2, 1.0/24) / binHz))
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n/2 {
			hi = n / 2
		}
		bins[k] = span{lo, hi}
	}
	nFrames := (len(x) - n) / hop
	if nFrames < 1 {
		return nil, nil, 0, 0
	}
	frames := make([][]float64, nFrames)
	frameE := make([]float64, nFrames)
	re := make([]float64, n)
	im := make([]float64, n)
	for f := 0; f < nFrames; f++ {
		off := f * hop
		for i := 0; i < n; i++ {
			re[i] = x[off+i] * win[i]
			im[i] = 0
		}
		fft(re, im)
		v := make([]float64, dbPitchBins)
		var e float64
		for k, s := range bins {
			var p float64
			for b := s.lo; b < s.hi; b++ {
				p += re[b]*re[b] + im[b]*im[b]
			}
			v[k] = math.Log1p(p)
			e += p
		}
		frames[f] = v
		frameE[f] = e
	}
	return frames, frameE, hop / rate * 1000, float64(n) / 2 / rate * 1000
}

// butterworthLowpass8 is an 8th-order Butterworth low-pass built as four
// bilinear-transformed biquads with analog pole angles π/16 + k·π/8 (the
// DAT_143804b00 / DAT_143804be0 constants in rekordbox's filter design).
func butterworthLowpass8(x []float32, rate, cutoff float64) []float64 {
	y := make([]float64, len(x))
	for i, v := range x {
		y[i] = float64(v)
	}
	k := math.Tan(math.Pi * cutoff / rate)
	for s := 0; s < 4; s++ {
		theta := math.Pi/16 + float64(s)*math.Pi/8
		q := 1 / (2 * math.Sin(theta)) // analog section Q for this pole pair
		norm := 1 / (1 + k/q + k*k)
		b0 := k * k * norm
		b1 := 2 * b0
		b2 := b0
		a1 := 2 * (k*k - 1) * norm
		a2 := (1 - k/q + k*k) * norm
		var x1, x2, y1, y2 float64
		for i, in := range y {
			out := b0*in + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1 = x1, in
			y2, y1 = y1, out
			y[i] = out
		}
	}
	return y
}

func meanVec(rows [][]float64) []float64 {
	m := make([]float64, len(rows[0]))
	for _, r := range rows {
		for i, v := range r {
			m[i] += v
		}
	}
	for i := range m {
		m[i] /= float64(len(rows))
	}
	return m
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 1
	}
	return dot / math.Sqrt(na*nb)
}

func median64(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

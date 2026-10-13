// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "math"

// rekordbox places each beat on the kick's attack: across ~1700 reference
// grids its beat sits a few ms after the point where the beat-averaged
// full-band energy has risen 10% of the way from its trough to its peak, with
// a per-track spread of ~2ms. The tempogram phase scatters ~3x wider around
// that point, so the final grid is moved onto the measured attack.
var (
	// AttackPlacement enables moving the grid onto the measured attack.
	AttackPlacement = true
	// AttackLeadMs is where rekordbox's beat sits relative to the 10%-rise
	// point: +3.0 (FLAC), +4.1 (MP3), +4.4ms (MP3 without gapless metadata).
	AttackLeadMs = 3.5
	// AttackGuardMs: a measured shift larger than this is treated as a
	// misread (another element dominating the beat average) and the
	// detector's grid is kept. 30 moves ~86% of tracks; no guard gains a
	// little fine alignment but adds ~1 point of half-beat errors.
	AttackGuardMs = 30.0
	// AttackRiseFrac is the fraction of the trough-to-peak rise that marks the attack.
	AttackRiseFrac = 0.1
)

const (
	attackWinMs    = 100.0 // beat-synchronous window: +-attackWinMs around each beat
	attackMarginMs = 20.0  // extra context so the smoothing settles before the window
	attackPeakMs   = 60.0  // search the peak within +-attackPeakMs of the beat
	attackRiseMs   = 80.0  // look back this far from the peak for the trough
	attackSmoothMs = 2.0   // envelope smoothing time constant (zero phase)
)

// attackShift returns how far to move the grid (ms) so its beats land on the
// kick's attack, and false when there is too little to measure or the shift
// fails AttackGuardMs. beats are grid times; grid time minus encoderDelayMs is
// the decoded audio's time.
func attackShift(samples []float32, sampleRate int, beats []float64, encoderDelayMs float64) (float64, bool) {
	avg := beatEnergyAverage(samples, sampleRate, beats, encoderDelayMs)
	if avg == nil {
		return 0, false
	}
	shift := attackOffsetMs(avg, sampleRate, AttackRiseFrac) + AttackLeadMs
	if math.Abs(shift) > AttackGuardMs {
		return 0, false
	}
	return shift, true
}

// beatEnergyAverage averages the smoothed full-band energy over
// +-attackWinMs around every beat, each window normalized to unit mean so loud
// sections don't dominate. It returns nil when fewer than 8 beats fit.
func beatEnergyAverage(samples []float32, sampleRate int, beats []float64, encoderDelayMs float64) []float64 {
	sr := float64(sampleRate)
	half := int(attackWinMs * sr / 1000)
	margin := int(attackMarginMs * sr / 1000)
	a := math.Exp(-1 / (attackSmoothMs / 1000 * sr))
	avg := make([]float64, 2*half+1)
	w := make([]float64, 2*(half+margin)+1)
	n := 0
	for _, b := range beats {
		c := int(math.Round((b - encoderDelayMs) * sr / 1000))
		lo := c - half - margin
		if lo < 0 || c+half+margin >= len(samples) {
			continue
		}
		for i := range w {
			v := float64(samples[lo+i])
			w[i] = v * v
		}
		var s float64
		for i := range w {
			s = a*s + (1-a)*w[i]
			w[i] = s
		}
		s = 0
		for i := len(w) - 1; i >= 0; i-- {
			s = a*s + (1-a)*w[i]
			w[i] = s
		}
		win := w[margin : margin+len(avg)]
		var m float64
		for _, v := range win {
			m += v
		}
		m /= float64(len(win))
		if m <= 0 {
			continue
		}
		for i, v := range win {
			avg[i] += v / m
		}
		n++
	}
	if n < 8 {
		return nil
	}
	return avg
}

// attackOffsetMs locates the attack in a beat average, in ms relative to the
// beat: the main peak within +-attackPeakMs, the trough before it within
// attackRiseMs, then the last point before the peak still below
// trough + frac*(peak-trough).
func attackOffsetMs(avg []float64, sampleRate int, frac float64) float64 {
	sr := float64(sampleRate)
	half := (len(avg) - 1) / 2
	ms := func(i int) float64 { return float64(i-half) * 1000 / sr }
	lim := int(attackPeakMs * sr / 1000)
	pk := half - lim
	for i := half - lim; i <= half+lim; i++ {
		if avg[i] > avg[pk] {
			pk = i
		}
	}
	lo := max(0, pk-int(attackRiseMs*sr/1000))
	tr := pk
	for i := pk; i >= lo; i-- {
		if avg[i] < avg[tr] {
			tr = i
		}
	}
	th := avg[tr] + frac*(avg[pk]-avg[tr])
	for i := pk; i > tr; i-- {
		if avg[i-1] < th {
			return ms(i)
		}
	}
	return ms(tr)
}

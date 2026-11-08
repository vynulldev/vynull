// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "math"

// structuralDownbeat uses repeated section entrances to resolve all four bar
// positions. A loud beat alone is ambiguous (a snare often wins); a sustained
// return of both bass and overall energy is stronger evidence of a bar line.
// Require at least two separate entrances and 80% agreement before replacing
// the opening-beat fallback. Tracks without that evidence keep their choice.
func structuralDownbeat(samples []float32, sampleRate int, beats []float64, fallback int) int {
	return downbeatFromEntrances(beatEntranceEnergy(samples, sampleRate, beats), fallback)
}

// beatEntranceEnergy measures bass and full-band energy in the first half of
// each beat. Work on the unshifted detector grid, before the user's grid offset.
// The streaming low-pass needs only two sums per beat, not another track-sized
// filtered buffer or STFT. As with harmonicParity, cap work at 210 seconds.
func beatEntranceEnergy(samples []float32, sampleRate int, beats []float64) [][2]float64 {
	if sampleRate <= 0 || len(beats) < 16 || len(samples) == 0 {
		return nil
	}
	period := (beats[len(beats)-1] - beats[0]) / float64(len(beats)-1)
	if period <= 0 || math.IsNaN(period) || math.IsInf(period, 0) {
		return nil
	}
	energy := make([][2]float64, 0, min(len(beats), int(dbMaxSeconds*1000/period)))
	alpha := 1 - math.Exp(-2*math.Pi*180/float64(sampleRate))
	var low float64
	var sums [2]float64
	beat, count := 0, 0
	limit := min(len(samples), int(dbMaxSeconds*float64(sampleRate)))
	for i, sample := range samples[:limit] {
		ms := float64(i) * 1000 / float64(sampleRate)
		v := float64(sample)
		low += alpha * (v - low)
		if beat+1 >= len(beats) {
			break
		}
		if ms >= beats[beat+1] {
			for k := range sums {
				sums[k] /= float64(max(1, count))
			}
			energy = append(energy, sums)
			beat++
			sums, count = [2]float64{}, 0
		}
		if ms >= beats[beat] && ms < beats[beat]+period/2 {
			sums[0] += low * low
			sums[1] += v * v
			count++
		}
	}
	return energy
}

// DownbeatEntrances exposes the section-entrance vote (structuralDownbeat) for
// tools: how many entrances fall on each beat of the bar, counted from the
// grid's first beat, and how many there are in all.
func DownbeatEntrances(samples []float32, sampleRate int, beats []float64) (votes [4]int, total int) {
	return entranceVotes(beatEntranceEnergy(samples, sampleRate, beats))
}

func downbeatFromEntrances(energy [][2]float64, fallback int) int {
	votes, total := entranceVotes(energy)
	best := 0
	for i := 1; i < len(votes); i++ {
		if votes[i] > votes[best] {
			best = i
		}
	}
	if votes[best] >= 2 && votes[best]*5 >= total*4 {
		return best
	}
	return fallback
}

// entranceVotes counts the section entrances on each beat of the bar.
func entranceVotes(energy [][2]float64) (votes [4]int, total int) {
	if len(energy) < 16 {
		return votes, 0
	}
	var floor [2]float64
	for _, e := range energy {
		for k, v := range e {
			floor[k] = max(floor[k], v)
		}
	}
	for k := range floor {
		floor[k] = max(floor[k]*0.001, 1e-12)
	}

	// Compare complete bars so a repeating kick/snare pattern cancels out.
	// Also require an attack on this beat, rather than the gradual rise of a
	// fade. Both conditions must hold in both bands: a bass-note change or a
	// bright crash on its own must not renumber the bar.
	scores := make([]float64, len(energy))
	for i := 4; i+4 < len(energy); i++ {
		score := math.Inf(1)
		for k := range floor {
			var before, after float64
			for j := 0; j < 4; j++ {
				before += energy[i-4+j][k]
				after += energy[i+j][k]
			}
			rise := after / 4 / max(floor[k], before/4)
			attack := energy[i][k] / max(floor[k], energy[i-1][k])
			score = min(score, rise, attack)
		}
		scores[i] = score
	}

	for i := 4; i+4 < len(scores); i++ {
		if scores[i] < 4 {
			continue
		}
		// One vote per entrance, separated by at least two bars. Resolve
		// equal peaks toward the earlier beat instead of counting both.
		peak := true
		for j := max(4, i-8); j < min(len(scores)-4, i+9); j++ {
			if scores[j] > scores[i] || (j < i && scores[j] == scores[i]) {
				peak = false
				break
			}
		}
		if peak {
			votes[i%4]++
			total++
		}
	}
	return votes, total
}

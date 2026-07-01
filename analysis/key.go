// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"strings"
)

// DetectKey estimates the musical key of audio samples.
// Returns the key in Camelot notation (e.g., "8A", "11B") used by Pioneer CDJs,
// and the standard name (e.g., "Am", "Eb").
func DetectKey(samples []float32, sampleRate int) (camelot, standard string) {
	if len(samples) < sampleRate*4 {
		return "", ""
	}

	chroma := computeChromagram(samples, sampleRate)

	// Krumhansl-Kessler key profiles (Krumhansl & Kessler, 1982 — published
	// probe-tone weights; academic data, not third-party code).
	major := [12]float64{6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88}
	minor := [12]float64{6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17}

	bestCorr := -999.0
	bestKey := 0
	bestMode := 0 // 0=major, 1=minor

	for key := 0; key < 12; key++ {
		// Rotate chroma to align with this key.
		var rotated [12]float64
		for i := 0; i < 12; i++ {
			rotated[i] = chroma[(i+key)%12]
		}

		majCorr := pearson(rotated[:], major[:])
		minCorr := pearson(rotated[:], minor[:])

		// Minor bias: electronic/dance music is predominantly minor key.
		// When major and minor correlations are close (relative major/minor pair),
		// prefer minor. This matches rekordbox's behavior.

		if majCorr > bestCorr {
			bestCorr = majCorr
			bestKey = key
			bestMode = 0
		}
		if minCorr > bestCorr {
			bestCorr = minCorr
			bestKey = key
			bestMode = 1
		}
	}

	return toCamelot(bestKey, bestMode), toStandard(bestKey, bestMode)
}

// computeChromagram builds a 12-bin pitch class energy profile.
// Uses FFT per window and reads energy at pitch-class frequency bins directly,
// replacing per-frequency Goertzel with a single FFT that gives all bins at once.
func computeChromagram(samples []float32, sampleRate int) [12]float64 {
	var chroma [12]float64

	windowSize := 4096
	hopSize := 2048

	// Pre-compute which FFT bins correspond to each pitch class (octaves 4-7).
	// Octaves 2-3 excluded: sub-bass/bass (65-250Hz) has broadband kick energy
	// that biases the chromagram toward C/C# regardless of actual key.
	freqPerBin := float64(sampleRate) / float64(windowSize)
	pitchBins := make([][]int, 12)
	for pc := 0; pc < 12; pc++ {
		for octave := 4; octave <= 7; octave++ {
			midi := float64((octave+1)*12 + pc)
			freq := 440.0 * math.Pow(2.0, (midi-69.0)/12.0)
			if freq > 0 && freq < float64(sampleRate)/2 {
				bin := int(math.Round(freq / freqPerBin))
				if bin > 0 && bin < windowSize/2 {
					pitchBins[pc] = append(pitchBins[pc], bin)
				}
			}
		}
	}

	// Pre-compute Hann window coefficients.
	hannWindow := make([]float64, windowSize)
	for i := range hannWindow {
		hannWindow[i] = 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(windowSize-1)))
	}

	// Prepare all windowed frames for batch FFT.
	numWindows := 0
	for start := 0; start+windowSize <= len(samples); start += hopSize {
		numWindows++
	}
	windows := make([][]float64, numWindows)
	idx := 0
	for start := 0; start+windowSize <= len(samples); start += hopSize {
		w := make([]float64, windowSize)
		for i := 0; i < windowSize; i++ {
			w[i] = float64(samples[start+i]) * hannWindow[i]
		}
		windows[idx] = w
		idx++
	}

	// Batch FFT (parallel CPU or GPU).
	realParts, imagParts := batchFFTGPU(windows, windowSize)

	// Accumulate chroma from FFT results.
	for i := 0; i < numWindows && i < len(realParts); i++ {
		fftR := realParts[i]
		fftI := imagParts[i]
		for pc := 0; pc < 12; pc++ {
			for _, bin := range pitchBins[pc] {
				if bin < len(fftR) {
					mag := math.Sqrt(fftR[bin]*fftR[bin] + fftI[bin]*fftI[bin])
					chroma[pc] += mag * mag
				}
			}
		}
	}

	if numWindows > 0 {
		for i := range chroma {
			chroma[i] /= float64(numWindows)
		}
	}

	return chroma
}

// pearson computes Pearson correlation between two vectors.
func pearson(a, b []float64) float64 {
	n := len(a)
	if n == 0 {
		return 0
	}

	var sumA, sumB, sumAB, sumA2, sumB2 float64
	for i := 0; i < n; i++ {
		sumA += a[i]
		sumB += b[i]
		sumAB += a[i] * b[i]
		sumA2 += a[i] * a[i]
		sumB2 += b[i] * b[i]
	}

	num := float64(n)*sumAB - sumA*sumB
	den := math.Sqrt((float64(n)*sumA2 - sumA*sumA) * (float64(n)*sumB2 - sumB*sumB))
	if den < 1e-10 {
		return 0
	}
	return num / den
}

// Note names indexed by pitch class (0=C, 1=C#, ..., 11=B).
var majorNames = [12]string{"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
var minorNames = [12]string{"Cm", "C#m", "Dm", "Ebm", "Em", "Fm", "F#m", "Gm", "G#m", "Am", "Bbm", "Bm"}

// Camelot wheel mapping:
// Major (B): C=8B, Db=3B, D=10B, Eb=5B, E=12B, F=7B, F#=2B, G=9B, Ab=4B, A=11B, Bb=6B, B=1B
// Minor (A): Cm=5A, C#m=12A, Dm=7A, Ebm=2A, Em=9A, Fm=4A, F#m=11A, Gm=6A, G#m=1A, Am=8A, Bbm=3A, Bm=10A
var camelotMajor = [12]string{"8B", "3B", "10B", "5B", "12B", "7B", "2B", "9B", "4B", "11B", "6B", "1B"}
var camelotMinor = [12]string{"5A", "12A", "7A", "2A", "9A", "4A", "11A", "6A", "1A", "8A", "3A", "10A"}

func toCamelot(key, mode int) string {
	if mode == 0 {
		return camelotMajor[key]
	}
	return camelotMinor[key]
}

func toStandard(key, mode int) string {
	if mode == 0 {
		return majorNames[key]
	}
	return minorNames[key]
}

// keyPairs maps any key name (Camelot like "8A" or standard like "Am") to
// both notations, built once from the pitch-class tables. Lookups are
// case-insensitive on the upper-cased name.
var keyPairs = func() map[string][2]string {
	m := make(map[string][2]string, 48)
	for k := 0; k < 12; k++ {
		for _, p := range [][2]string{
			{camelotMajor[k], majorNames[k]},
			{camelotMinor[k], minorNames[k]},
		} {
			cam, std := p[0], p[1]
			m[strings.ToUpper(cam)] = [2]string{cam, std}
			m[strings.ToUpper(std)] = [2]string{cam, std}
		}
	}
	// Enharmonic spellings rekordbox sometimes uses that differ from our
	// canonical note names (e.g. it writes "Abm" where we use "G#m", "Dbm"
	// for "C#m"). Map each to its pitch class's canonical pair so they
	// normalize too.
	for _, e := range []struct {
		alias string
		pc    int
		minor bool
	}{
		{"C#", 1, false}, {"D#", 3, false}, {"Gb", 6, false}, {"G#", 8, false}, {"A#", 10, false},
		{"Dbm", 1, true}, {"D#m", 3, true}, {"Gbm", 6, true}, {"Abm", 8, true}, {"A#m", 10, true},
	} {
		cam, std := camelotMajor[e.pc], majorNames[e.pc]
		if e.minor {
			cam, std = camelotMinor[e.pc], minorNames[e.pc]
		}
		m[strings.ToUpper(e.alias)] = [2]string{cam, std}
	}
	return m
}()

// KeyNamesFrom resolves a key string in either Camelot ("8A") or standard
// ("Am") notation to both forms. Used to backfill the Camelot/standard key
// fields when importing a rekordbox library (rekordbox stores whichever
// notation the user configured). Returns ("","") for an unrecognized key.
func KeyNamesFrom(key string) (camelot, standard string) {
	if p, ok := keyPairs[strings.ToUpper(strings.TrimSpace(key))]; ok {
		return p[0], p[1]
	}
	return "", ""
}

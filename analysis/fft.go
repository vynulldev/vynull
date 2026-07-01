// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "math"

// fft performs an in-place radix-2 Cooley-Tukey FFT on complex data.
// len(real) and len(imag) must be equal and a power of 2.
func fft(real, imag []float64) {
	n := len(real)
	if n <= 1 {
		return
	}

	// Bit-reversal permutation
	j := 0
	for i := 0; i < n-1; i++ {
		if i < j {
			real[i], real[j] = real[j], real[i]
			imag[i], imag[j] = imag[j], imag[i]
		}
		k := n >> 1
		for k <= j {
			j -= k
			k >>= 1
		}
		j += k
	}

	// Cooley-Tukey butterfly
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		angle := -2.0 * math.Pi / float64(size)
		wR := math.Cos(angle)
		wI := math.Sin(angle)
		for start := 0; start < n; start += size {
			curR, curI := 1.0, 0.0
			for k := 0; k < half; k++ {
				a := start + k
				b := start + k + half
				tR := curR*real[b] - curI*imag[b]
				tI := curR*imag[b] + curI*real[b]
				real[b] = real[a] - tR
				imag[b] = imag[a] - tI
				real[a] += tR
				imag[a] += tI
				curR, curI = curR*wR-curI*wI, curR*wI+curI*wR
			}
		}
	}
}

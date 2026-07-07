// SPDX-License-Identifier: GPL-3.0-or-later

// Package dsp holds the low-level signal-processing primitives shared by the
// analyzer and the per-ecosystem waveform encoders — IIR (biquad) filtering and
// band splitting. It depends only on the standard library.
package dsp

import "math"

// BiquadCoeffs holds coefficients for a 2nd-order IIR (biquad) filter. The
// fields are unexported: callers treat it as an opaque token produced by the
// Butterworth constructors and consumed by ApplyBiquad.
type BiquadCoeffs struct {
	b0, b1, b2, a1, a2 float64
}

// ButterworthLow computes 2nd-order Butterworth low-pass filter coefficients.
// Butterworth Q = 1/√2 ≈ 0.707 (maximally flat passband, no resonant peak).
// alpha = sin(w0) / (2*Q) = sin(w0) / √2.
func ButterworthLow(cutoff, sampleRate float64) BiquadCoeffs {
	w0 := 2.0 * math.Pi * cutoff / sampleRate
	alpha := math.Sin(w0) / math.Sqrt2
	cosW0 := math.Cos(w0)
	a0 := 1.0 + alpha
	return BiquadCoeffs{
		b0: (1.0 - cosW0) / 2.0 / a0,
		b1: (1.0 - cosW0) / a0,
		b2: (1.0 - cosW0) / 2.0 / a0,
		a1: -2.0 * cosW0 / a0,
		a2: (1.0 - alpha) / a0,
	}
}

// ButterworthHigh computes 2nd-order Butterworth high-pass filter coefficients.
func ButterworthHigh(cutoff, sampleRate float64) BiquadCoeffs {
	w0 := 2.0 * math.Pi * cutoff / sampleRate
	alpha := math.Sin(w0) / math.Sqrt2
	cosW0 := math.Cos(w0)
	a0 := 1.0 + alpha
	return BiquadCoeffs{
		b0: (1.0 + cosW0) / 2.0 / a0,
		b1: -(1.0 + cosW0) / a0,
		b2: (1.0 + cosW0) / 2.0 / a0,
		a1: -2.0 * cosW0 / a0,
		a2: (1.0 - alpha) / a0,
	}
}

// ApplyBiquad applies a biquad IIR filter to samples, returning a new slice
// (the input is not mutated).
func ApplyBiquad(samples []float32, c BiquadCoeffs) []float32 {
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

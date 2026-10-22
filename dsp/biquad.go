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
	f := Biquad{c: c}
	for i, s := range samples {
		out[i] = f.Next(s)
	}
	return out
}

// Biquad runs a biquad IIR filter one sample at a time: fed a signal's samples
// in order, Next returns what ApplyBiquad puts at each position. A caller that
// reads the filtered signal once, front to back, can filter as it reads
// instead of holding a filtered copy of the whole track.
type Biquad struct {
	c              BiquadCoeffs
	x1, x2, y1, y2 float64
}

// Next filters the next sample.
func (f *Biquad) Next(s float32) float32 {
	x0 := float64(s)
	y0 := f.c.b0*x0 + f.c.b1*f.x1 + f.c.b2*f.x2 - f.c.a1*f.y1 - f.c.a2*f.y2
	f.x2 = f.x1
	f.x1 = x0
	f.y2 = f.y1
	f.y1 = y0
	return float32(y0)
}

// Cascade is biquads in series, run one sample at a time: Next returns what
// applying them one after another with ApplyBiquad gives at each position.
type Cascade []Biquad

// NewCascade returns the filters for the given coefficients, in order.
func NewCascade(cs ...BiquadCoeffs) Cascade {
	f := make(Cascade, len(cs))
	for i, c := range cs {
		f[i].c = c
	}
	return f
}

// Next filters the next sample through every stage.
func (f Cascade) Next(s float32) float32 {
	for i := range f {
		s = f[i].Next(s)
	}
	return s
}

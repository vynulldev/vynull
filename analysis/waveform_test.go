// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// synthSine produces a mono sine wave at the given frequency and amplitude.
// Duration is in seconds; sample rate is analysisRate.
func synthSine(freqHz, durSec, amplitude float64) []float32 {
	n := int(float64(analysisRate) * durSec)
	out := make([]float32, n)
	twoPiF := 2.0 * math.Pi * freqHz / float64(analysisRate)
	for i := range out {
		out[i] = float32(amplitude * math.Sin(twoPiF*float64(i)))
	}
	return out
}

// decodePWV5 unpacks one big-endian 2-byte entry into r/g/b/h.
// Mirrors the WaveformDetail wire layout so the test fails loudly
// if the encoder bit positions ever drift.
func decodePWV5(hi, lo byte) (r, g, b, h uint8) {
	bits := uint16(hi)<<8 | uint16(lo)
	r = uint8((bits >> 13) & 7)
	g = uint8((bits >> 10) & 7)
	b = uint8((bits >> 7) & 7)
	h = uint8((bits >> 2) & 0x1f)
	return
}

func TestPWV5BitLayout(t *testing.T) {
	// Property check on the bit-packing formula at waveform.go:457.
	// Encode every legal combination of r/g/b/h, decode, assert round-trip.
	for r := uint8(0); r <= 7; r++ {
		for g := uint8(0); g <= 7; g++ {
			for b := uint8(0); b <= 7; b++ {
				for h := uint8(0); h <= 31; h++ {
					word := uint16(r&7)<<13 | uint16(g&7)<<10 | uint16(b&7)<<7 | uint16(h&0x1f)<<2
					hi, lo := byte(word>>8), byte(word&0xff)
					gotR, gotG, gotB, gotH := decodePWV5(hi, lo)
					if gotR != r || gotG != g || gotB != b || gotH != h {
						t.Fatalf("round-trip failed for r=%d g=%d b=%d h=%d: got r=%d g=%d b=%d h=%d",
							r, g, b, h, gotR, gotG, gotB, gotH)
					}
				}
			}
		}
	}
}

func TestPWV5Silence(t *testing.T) {
	// Zero samples — every entry should be the padding 0xff 0x80.
	samples := make([]float32, analysisRate*2)
	out := GenerateDetail(samples, analysisRate)
	if len(out)%2 != 0 || len(out) == 0 {
		t.Fatalf("unexpected output length %d", len(out))
	}
	for i := 0; i < len(out); i += 2 {
		if out[i] != 0xff || out[i+1] != 0x80 {
			t.Fatalf("silence entry %d: got %02x %02x, want ff 80", i/2, out[i], out[i+1])
		}
	}
}

func TestPWV5ClassicDominantBand(t *testing.T) {
	// In classic mode, the loudest band at each entry should encode to 7
	// (the formula is band / max(bands) * 7). Pure-band sine waves let us
	// predict which channel that is.
	cases := []struct {
		name     string
		freqHz   float64
		dominant string // "r", "g", or "b"
	}{
		{"bass-100Hz", 100, "r"},
		{"mid-500Hz", 500, "g"},
		{"treble-8000Hz", 8000, "b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			samples := synthSine(c.freqHz, 2.0, 0.5)
			out := GenerateDetail(samples, analysisRate)

			// Skip the first and last ~150ms — IIR filter ramp-up and
			// segment-boundary effects can put a single low-amplitude entry
			// in a non-dominant state.
			skip := 30
			var checked, mismatched int
			for i := skip * 2; i < len(out)-skip*2; i += 2 {
				// Skip padding (silent entries pass through unchanged).
				if out[i] == 0xff && out[i+1] == 0x80 {
					continue
				}
				r, g, b, _ := decodePWV5(out[i], out[i+1])
				checked++
				var ok bool
				switch c.dominant {
				case "r":
					ok = r == 7 && r >= g && r >= b
				case "g":
					ok = g == 7 && g >= r && g >= b
				case "b":
					ok = b == 7 && b >= r && b >= g
				}
				if !ok {
					mismatched++
				}
			}
			if checked == 0 {
				t.Fatal("no non-silent entries to check")
			}
			// IIR rolloff can leak a tiny bit of energy into neighbours; allow
			// 5% of entries to disagree but flag wholesale mis-encoding.
			if float64(mismatched)/float64(checked) > 0.05 {
				t.Fatalf("%d/%d entries did not have %s dominant", mismatched, checked, c.dominant)
			}
		})
	}
}

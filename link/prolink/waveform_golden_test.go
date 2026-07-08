// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// goldenSignal is the deterministic 3-second bass+mid+treble tone used to lock
// the colour-waveform encoders byte-for-byte. math.Sin is bit-stable in Go, so
// the output hashes are reproducible across machines. (Matches the signal in
// TestPWV5GoldenHash.)
func goldenSignal() []float32 {
	n := analysis.AnalysisRate * 3
	s := make([]float32, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(analysis.AnalysisRate)
		s[i] = float32(
			0.6*math.Sin(2*math.Pi*200*t) +
				0.3*math.Sin(2*math.Pi*2000*t) +
				0.15*math.Sin(2*math.Pi*8000*t),
		)
	}
	return s
}

// checkGolden hashes an encoder's output and compares it to want. These guard
// the wire bytes CDJs render from, so the encoders can be moved/refactored (the
// modular-backends split) without silently changing what a deck displays.
//
// To (re)seed after an intentional encoder change: run
//
//	go test ./analysis -run GoldenHash -v
//
// and paste the printed hash into the matching want.
func checkGolden(t *testing.T, name string, out []byte, want string) {
	t.Helper()
	sum := sha256.Sum256(out)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("%s encoder output changed.\n  got:  %s\n  want: %s\n"+
			"If intentional, update the want constant.", name, got, want)
	}
}

func TestPWV4GoldenHash(t *testing.T) { // color overview preview
	checkGolden(t, "PWV4 (colour preview)",
		GenerateColorPreview(goldenSignal(), analysis.AnalysisRate),
		"44bce394255ddcf45e1df0f0b978ada68c665fe645d5bfbd9c6019c05130f5bb")
}

// PWV5 is the served detail scrolling waveform. If this hash changes on an
// intentional encoder tweak, also bump cacheVersion in analysis.go so existing
// caches re-analyze.
func TestPWV5GoldenHash(t *testing.T) { // detail scrolling waveform
	checkGolden(t, "PWV5 (detail)",
		GenerateDetail(goldenSignal(), analysis.AnalysisRate),
		"053db4c3aa8762da39e4757f1309d9c379b92aae343cab2261a482287dd5ab20")
}

func TestPWV6GoldenHash(t *testing.T) { // 3-band overview preview
	checkGolden(t, "PWV6 (3-band preview)",
		GeneratePreview3Band(goldenSignal(), analysis.AnalysisRate),
		"26994eff66433fca98c0b6a3685c8739aa489bb09214be21b0a8279046bc3f59")
}

func TestPWV7GoldenHash(t *testing.T) { // 3-band detail
	checkGolden(t, "PWV7 (3-band detail)",
		GenerateDetail3Band(goldenSignal(), analysis.AnalysisRate),
		"5cbfe40515092522a120d8b03aaa80ed9deb07b3ae7cada41bc28d1cddb409c0")
}

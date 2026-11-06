// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"math/rand"
	"testing"
)

// subBassOffset finds the kick on the grid when it's there, half a beat off
// when it's on the off-beats, even under hats on the grid, and reports
// nothing for silence.
func TestSubBassOffset(t *testing.T) {
	const sr, bpm, secs = 44100, 125.0, 30.0
	period := 60000 / bpm
	// track lays a kick (a falling sine) every beat from kickMs and, when
	// hats, a short noise burst every beat from hatMs.
	track := func(kickMs float64, hats bool, hatMs float64) []float32 {
		buf := make([]float32, int(secs*sr))
		rng := rand.New(rand.NewSource(1))
		for at := kickMs; at < secs*1000; at += period {
			s0 := int(at * sr / 1000)
			for i := 0; i < sr*3/10 && s0+i < len(buf); i++ {
				x := float64(i) / sr
				buf[s0+i] += float32(0.8 * math.Sin(2*math.Pi*(50+70*math.Exp(-30*x))*x) * math.Exp(-12*x))
			}
		}
		for at := hatMs; hats && at < secs*1000; at += period {
			s0 := int(at * sr / 1000)
			for i := 0; i < sr/20 && s0+i < len(buf); i++ {
				buf[s0+i] += float32(0.5 * (rng.Float64()*2 - 1) * math.Exp(-90*float64(i)/sr))
			}
		}
		return buf
	}
	for _, c := range []struct {
		name    string
		samples []float32
		phase   float64
		half    bool // want the kick about half a beat from the grid
	}{
		{"kicks on the grid", track(100, false, 0), 100, false},
		{"kicks on the off-beats", track(100+period/2, false, 0), 100, true},
		{"kicks off-beat under hats on the grid", track(100+period/2, true, 100), 100, true},
		{"kicks on the grid under off-beat hats", track(100, true, 100+period/2), 100, false},
	} {
		off, ok := subBassOffset(c.samples, sr, c.phase, period)
		if !ok {
			t.Errorf("%s: no offset", c.name)
			continue
		}
		if got := math.Abs(off) > SubBassFlipFrac; got != c.half {
			t.Errorf("%s: offset %.2f beat, want half a beat %v", c.name, off, c.half)
		}
	}
	if _, ok := subBassOffset(make([]float32, sr*10), sr, 0, period); ok {
		t.Error("silence: got an offset")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// Section entrances repeat at the same position in the bar even when the
// opening is cut on another beat. The first beat still contains a kick.
func TestStructuralDownbeat(t *testing.T) {
	const rate = 8000
	for phase := 0; phase < 4; phase++ {
		for _, gain := range []float64{1, 0.01} {
			beats := make([]float64, 128)
			samples := make([]float32, rate*64)
			for b := range beats {
				beats[b] = 100 + float64(b)*500
				level := 0.05 * gain
				if (b-phase+32)%32 < 16 {
					level = 0.8 * gain
				}
				start := int(beats[b] * rate / 1000)
				for j := 0; j < rate/4 && start+j < len(samples); j++ {
					tm := float64(j) / rate
					samples[start+j] = float32(level * math.Sin(2*math.Pi*60*tm) * math.Exp(-tm*25))
				}
			}
			if got := structuralDownbeat(samples, rate, beats, 0); got != phase {
				t.Errorf("phase %d gain %g: got %d", phase, gain, got)
			}
			if gain == 1 {
				if got := findDownbeat(beats, samples, rate); got != beats[phase] {
					t.Errorf("default selector, phase %d: beat 1 at %g ms, want %g", phase, got, beats[phase])
				}
			}
		}
	}
}

func TestDownbeatEntranceFallback(t *testing.T) {
	flat := func() [][2]float64 {
		e := make([][2]float64, 128)
		for i := range e {
			e[i] = [2]float64{1, 1}
		}
		return e
	}
	entrance := func(e [][2]float64, at int, bands ...int) {
		for i := at - 8; i < at; i++ {
			for _, band := range bands {
				e[i][band] = 0.01
			}
		}
	}
	for _, name := range []string{"silence", "short", "equal kicks", "backbeat accents", "single entrance", "conflicting entrances", "bass only", "full band only", "fade in"} {
		t.Run(name, func(t *testing.T) {
			e := flat()
			switch name {
			case "silence":
				clear(e)
			case "short":
				e = e[:8]
			case "backbeat accents":
				for i := range e {
					if i%2 == 1 {
						e[i] = [2]float64{10, 100}
					}
				}
			case "single entrance":
				entrance(e, 33, 0, 1)
			case "conflicting entrances":
				entrance(e, 33, 0, 1)
				entrance(e, 65, 0, 1)
				entrance(e, 98, 0, 1)
			case "bass only":
				entrance(e, 33, 0)
				entrance(e, 65, 0)
			case "full band only":
				entrance(e, 33, 1)
				entrance(e, 65, 1)
			case "fade in":
				for i := range e {
					v := float64(i+1) / float64(len(e))
					e[i] = [2]float64{v * v, v * v}
				}
			}
			if got := downbeatFromEntrances(e, 2); got != 2 {
				t.Errorf("ambiguous evidence changed beat 1 to %d, want fallback 2", got)
			}
		})
	}
}

func TestDownbeatEmptyInput(t *testing.T) {
	if got := findDownbeat(nil, nil, 44100); got != 0 {
		t.Errorf("empty grid: %g", got)
	}
	if got := findDownbeat([]float64{100}, nil, 44100); got != 100 {
		t.Errorf("empty audio: %g", got)
	}
	if got := structuralDownbeat(nil, 0, nil, 2); got != 2 {
		t.Errorf("no evidence: %d", got)
	}
}

func BenchmarkStructuralDownbeat(b *testing.B) {
	samples := kickTrack(AnalysisRate, 100, 210)
	beats := make([]float64, 420)
	for i := range beats {
		beats[i] = 100 + float64(i)*500
	}
	b.ReportAllocs()
	for b.Loop() {
		structuralDownbeat(samples, AnalysisRate, beats, 0)
	}
}

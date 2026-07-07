// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestBandWaveformDetail(t *testing.T) {
	samples := synthSine(100, 2.0, 0.5) // 2s bass tone
	w := BandWaveformDetail(samples, analysisRate)

	if w.PointsPerSec != DetailEntriesPerSec {
		t.Errorf("PointsPerSec = %v, want %v", w.PointsPerSec, float64(DetailEntriesPerSec))
	}
	want := int(2.0 * DetailEntriesPerSec)
	if len(w.Bass) != want || len(w.Mid) != want || len(w.Treble) != want {
		t.Fatalf("point counts: bass=%d mid=%d treble=%d want=%d", len(w.Bass), len(w.Mid), len(w.Treble), want)
	}

	// Every value in [0,1], and the loudest band-point normalises to ~1.
	var peak float32
	for i := range w.Bass {
		for _, v := range []float32{w.Bass[i], w.Mid[i], w.Treble[i]} {
			if v < 0 || v > 1.0001 {
				t.Fatalf("value out of range: %v", v)
			}
			if v > peak {
				peak = v
			}
		}
	}
	if peak < 0.99 {
		t.Errorf("expected a normalised peak near 1.0, got %v", peak)
	}

	// A 100 Hz tone is bass-dominant mid-track.
	m := want / 2
	if w.Bass[m] <= w.Treble[m] {
		t.Errorf("100Hz tone should be bass-dominant: bass=%v treble=%v", w.Bass[m], w.Treble[m])
	}
}

func TestBandWaveformOverview(t *testing.T) {
	w := BandWaveformOverview(synthSine(100, 2.0, 0.5), analysisRate)
	if len(w.Bass) != overviewPoints {
		t.Errorf("overview points = %d, want %d", len(w.Bass), overviewPoints)
	}
	if w.PointsPerSec != float64(overviewPoints)/2.0 {
		t.Errorf("PointsPerSec = %v, want %v", w.PointsPerSec, float64(overviewPoints)/2.0)
	}
}

func TestCoreWithBands(t *testing.T) {
	samples := synthSine(100, 2.0, 0.5)
	r := &Result{BPM: 120, KeyCamelot: "8A", KeyStandard: "Am"}
	a := r.CoreWithBands(samples, analysisRate)
	if a.BPM != 120 {
		t.Errorf("BPM = %v, want 120", a.BPM)
	}
	if len(a.Detail.Bass) == 0 || len(a.Overview.Bass) == 0 {
		t.Errorf("expected detail + overview band data, got detail=%d overview=%d",
			len(a.Detail.Bass), len(a.Overview.Bass))
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestResultCore(t *testing.T) {
	r := &Result{
		BPM:           128,
		KeyCamelot:    "8A",
		KeyStandard:   "Am",
		Beats:         []float64{0, 500, 1000, 1500, 2000},
		DownbeatIndex: 0, // beats 0 and 4 are downbeats (every 4th)
		Phrases: []Phrase{
			{Kind: 1, StartMs: 0, EndMs: 1000},
			{Kind: 5, StartMs: 1000, EndMs: 2000},
		},
	}
	a := r.Core()

	if a.BPM != 128 {
		t.Errorf("BPM = %v, want 128", a.BPM)
	}
	if a.Key.Camelot != "8A" || a.Key.Standard != "Am" {
		t.Errorf("Key = %+v, want {8A Am}", a.Key)
	}
	if len(a.Beats) != 5 {
		t.Fatalf("len(Beats) = %d, want 5", len(a.Beats))
	}
	if !a.Beats[0].Downbeat || !a.Beats[4].Downbeat || a.Beats[1].Downbeat {
		t.Errorf("downbeat flags wrong: %+v", a.Beats)
	}
	if a.Beats[2].TimeMs != 1000 {
		t.Errorf("Beats[2].TimeMs = %v, want 1000", a.Beats[2].TimeMs)
	}
	if len(a.Phrases) != 2 || a.Phrases[0].Kind != "intro" || a.Phrases[1].Kind != "chorus" {
		t.Errorf("Phrases = %+v, want intro/chorus", a.Phrases)
	}
}

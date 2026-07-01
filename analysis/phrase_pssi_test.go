// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestPSSIRoundTrip(t *testing.T) {
	in := []Phrase{
		{StartBeat: 1, EndBeat: 33, Kind: 1},    // intro
		{StartBeat: 33, EndBeat: 97, Kind: 2},   // up
		{StartBeat: 97, EndBeat: 161, Kind: 5},  // chorus
		{StartBeat: 161, EndBeat: 193, Kind: 6}, // outro
	}
	blob := GeneratePSSI(in, 128.0)
	if blob == nil {
		t.Fatal("GeneratePSSI returned nil")
	}
	out := ParsePSSI(blob)
	if len(out) != len(in) {
		t.Fatalf("got %d phrases, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].StartBeat != in[i].StartBeat || out[i].EndBeat != in[i].EndBeat || out[i].Kind != in[i].Kind {
			t.Errorf("phrase %d: got %+v want %+v", i, out[i], in[i])
		}
	}
}

func TestPSSIEmpty(t *testing.T) {
	if ParsePSSI(nil) != nil {
		t.Error("nil blob should yield nil")
	}
	if ParsePSSI([]byte{0, 0, 0, 24, 0, 0}) != nil {
		t.Error("zero-entry blob should yield nil")
	}
	if ParsePSSI([]byte{1, 2, 3}) != nil {
		t.Error("too-short blob should yield nil")
	}
}

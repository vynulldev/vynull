// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"testing"
)

func TestGenerateBeatGrid_HappyPath(t *testing.T) {
	// 128 BPM for 60 seconds, downbeat at 0 → expect 128 beats.
	blob := GenerateBeatGrid(128.0, 60_000, 0)
	if blob == nil {
		t.Fatalf("expected non-nil blob")
	}
	if len(blob) < 20 {
		t.Fatalf("blob too short: %d bytes", len(blob))
	}
	numBeats := binary.LittleEndian.Uint32(blob[4:])
	if numBeats != 128 {
		t.Errorf("expected 128 beats, got %d", numBeats)
	}
	// First beat should be at t=0 with beatNum=1, tempo=12800.
	const preamble = 20
	beatNum := binary.LittleEndian.Uint16(blob[preamble:])
	tempo := binary.LittleEndian.Uint16(blob[preamble+2:])
	timeMs := binary.LittleEndian.Uint32(blob[preamble+4:])
	if beatNum != 1 || tempo != 12800 || timeMs != 0 {
		t.Errorf("first beat = (num=%d, tempo=%d, t=%d), want (1, 12800, 0)", beatNum, tempo, timeMs)
	}
}

func TestGenerateBeatGrid_BeatCountClamp(t *testing.T) {
	// Regression: beatGridForTrack used to pass durationMs in ns-as-ms
	// (10^14) which made numBeats ~6e11, causing an 880GB allocation.
	// The clamp in GenerateBeatGrid returns nil for any input that
	// would produce more than 100k beats — that's well beyond any
	// real track and a clear signal of a unit error upstream.
	bigDuration := 2.91e14 // what 291s in ns looked like as ms
	if got := GenerateBeatGrid(128.0, bigDuration, 0); got != nil {
		t.Errorf("expected nil for absurd duration, got %d-byte blob", len(got))
	}
}

func TestGenerateBeatGrid_InvalidInputs(t *testing.T) {
	cases := []struct {
		name             string
		bpm, dur, downMs float64
	}{
		{"zero bpm", 0, 60_000, 0},
		{"negative bpm", -128, 60_000, 0},
		{"zero duration", 128, 0, 0},
		{"downbeat past end", 128, 60_000, 70_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerateBeatGrid(tc.bpm, tc.dur, tc.downMs); got != nil {
				t.Errorf("expected nil, got %d bytes", len(got))
			}
		})
	}
}

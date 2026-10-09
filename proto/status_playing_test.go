// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"math"
	"testing"
)

func TestPitchRoundTrip(t *testing.T) {
	for _, pct := range []float64{0, 6, -6, 16, -16, 50, -100, 100} {
		got := WireToPitch(PitchToWire(pct))
		if math.Abs(got-pct) > 0.001 {
			t.Errorf("pitch %.2f%% round-tripped to %.4f%%", pct, got)
		}
	}
	if PitchToWire(0) != PitchZero {
		t.Errorf("0%% pitch = %#x, want %#x", PitchToWire(0), PitchZero)
	}
	if PitchToWire(-100) != 0 {
		t.Errorf("-100%% pitch = %#x, want 0", PitchToWire(-100))
	}
}

// TestMarshalStatusCDJPlayingRoundTrip proves a built playing status parses
// back to the fields we put in; this is what lets it drive our own listeners.
func TestMarshalStatusCDJPlayingRoundTrip(t *testing.T) {
	p := CDJPlayState{
		PlayState:   PlayStatePlaying,
		TrackDevice: 17,
		TrackSlot:   SlotUSB,
		TrackType:   1,
		TrackID:     42,
		TrackNum:    7,
		BPM:         12800, // 128.00 BPM
		PitchPct:    -3.5,
		BeatInTrack: 513,
		BeatInBar:   2,
		Sync:        true,
	}
	buf := MarshalStatusCDJPlaying("vynull", 3, SlotUSB, 1, nil, p)

	if len(buf) != 292 {
		t.Fatalf("status length = %d, want 292", len(buf))
	}

	s, ok := ParseCDJStatus(buf)
	if !ok {
		t.Fatal("ParseCDJStatus rejected a status we built")
	}

	if s.PlayState != p.PlayState {
		t.Errorf("PlayState = %#x, want %#x", s.PlayState, p.PlayState)
	}
	if !s.Active {
		t.Error("Active = false, want true while playing")
	}
	if s.TrackDevice != p.TrackDevice || s.TrackSlot != p.TrackSlot || s.TrackType != p.TrackType {
		t.Errorf("track source = (%d,%d,%d), want (%d,%d,%d)",
			s.TrackDevice, s.TrackSlot, s.TrackType, p.TrackDevice, p.TrackSlot, p.TrackType)
	}
	if s.TrackID != p.TrackID {
		t.Errorf("TrackID = %d, want %d", s.TrackID, p.TrackID)
	}
	if s.TrackNum != p.TrackNum {
		t.Errorf("TrackNum = %d, want %d", s.TrackNum, p.TrackNum)
	}
	if s.BPM != p.BPM {
		t.Errorf("BPM = %d, want %d", s.BPM, p.BPM)
	}
	if s.BeatInTrack != p.BeatInTrack {
		t.Errorf("BeatInTrack = %d, want %d", s.BeatInTrack, p.BeatInTrack)
	}
	if s.BeatInBar != p.BeatInBar {
		t.Errorf("BeatInBar = %d, want %d", s.BeatInBar, p.BeatInBar)
	}
	if !s.IsPlaying {
		t.Error("IsPlaying flag = false, want true")
	}
	if !s.IsSync {
		t.Error("IsSync flag = false, want true")
	}
	if s.IsMaster {
		t.Error("IsMaster flag = true, want false (safe profile)")
	}
	if s.IsOnAir {
		t.Error("IsOnAir flag = true, want false (safe profile)")
	}
	if got := WireToPitch(uint32(s.Pitch)); math.Abs(got-p.PitchPct) > 0.01 {
		t.Errorf("Pitch = %.3f%%, want %.3f%%", got, p.PitchPct)
	}
	if s.DeviceNumber != 3 {
		t.Errorf("DeviceNumber = %d, want 3 (from the idle template)", s.DeviceNumber)
	}
}

// TestMarshalStatusCDJPlayingPaused checks a paused deck is not marked active
// or playing.
func TestMarshalStatusCDJPlayingPaused(t *testing.T) {
	buf := MarshalStatusCDJPlaying("vynull", 3, SlotUSB, 1, nil, CDJPlayState{
		PlayState: PlayStatePaused,
		TrackID:   1,
		BPM:       12000,
	})
	s, ok := ParseCDJStatus(buf)
	if !ok {
		t.Fatal("ParseCDJStatus rejected a paused status")
	}
	if s.Active {
		t.Error("Active = true, want false while paused")
	}
	if s.IsPlaying {
		t.Error("IsPlaying = true, want false while paused")
	}
}

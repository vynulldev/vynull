// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// TestBeatChangedAtPairing covers the beat-clock anchor the API's beat_age_ms
// is built on: a beat-counter advance pairs with the deck's most recent 0x28
// beat tick (exact beat time), a repeated counter keeps its original anchor so
// the age keeps growing between beats, and a stale tick is not paired.
func TestBeatChangedAtPairing(t *testing.T) {
	m := NewPlayerMonitor(nil, nil)
	st := func(beat uint32) *proto.CDJStatus {
		return &proto.CDJStatus{DeviceNumber: 2, TrackID: 7, BeatInTrack: beat, IsPlaying: true}
	}

	// Beat 10 fires (tick), status carrying beat 10 arrives ~150ms later:
	// the anchor must be the tick, not the status arrival.
	tick := time.Now().Add(-150 * time.Millisecond)
	m.BeatTick(2, tick)
	m.Update(st(10))
	ps := m.States()[2]
	if ps == nil || !ps.BeatChangedAt.Equal(tick) {
		t.Fatalf("beat advance did not pair with the tick: got %v, want %v", ps.BeatChangedAt, tick)
	}

	// Another status with the SAME beat counter: anchor unchanged.
	m.Update(st(10))
	if ps = m.States()[2]; !ps.BeatChangedAt.Equal(tick) {
		t.Fatalf("same-beat update moved the anchor: got %v, want %v", ps.BeatChangedAt, tick)
	}

	// Counter advances but the last tick is stale (>700ms, e.g. lost packet):
	// fall back to the status arrival (recent), never the stale tick.
	m.BeatTick(2, time.Now().Add(-2*time.Second))
	before := time.Now()
	m.Update(st(11))
	ps = m.States()[2]
	if ps.BeatChangedAt.Before(before) {
		t.Fatalf("stale tick was paired: anchor %v predates the update", ps.BeatChangedAt)
	}

	// Track change with the same counter value: this is a different beat 11,
	// so the anchor must move (the same-beat carry-forward requires the same
	// track).
	prev := ps.BeatChangedAt
	time.Sleep(2 * time.Millisecond)
	m.Update(&proto.CDJStatus{DeviceNumber: 2, TrackID: 8, BeatInTrack: 11, IsPlaying: true})
	if ps = m.States()[2]; !ps.BeatChangedAt.After(prev) {
		t.Fatalf("track change kept the old track's beat anchor")
	}
}

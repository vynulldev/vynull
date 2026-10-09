// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// fakeClock drives a SimDeck deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

// grid builds a steady beat grid: n beats at the given BPM starting at 0.
func grid(n int, bpm float64) []float64 {
	step := 60000.0 / bpm
	beats := make([]float64, n)
	for i := range beats {
		beats[i] = float64(i) * step
	}
	return beats
}

func newTestDeck() (*SimDeck, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d := NewSimDeck()
	d.now = clk.now
	return d, clk
}

func TestSimDeckIdle(t *testing.T) {
	d, _ := newTestDeck()
	s := d.Snapshot()
	if s.PlayState != proto.PlayStateNoTrack {
		t.Errorf("idle PlayState = %#x, want NoTrack", s.PlayState)
	}
	if s.BeatInTrack != 0xFFFFFFFF {
		t.Errorf("idle BeatInTrack = %#x, want unknown", s.BeatInTrack)
	}
	// Play with nothing loaded is a no-op.
	d.Play()
	if d.Snapshot().PlayState != proto.PlayStateNoTrack {
		t.Error("Play with no track changed state")
	}
}

func TestSimDeckLoadCues(t *testing.T) {
	d, _ := newTestDeck()
	d.Load(42, 17, proto.SlotUSB, 1, grid(600, 128), 0, 300000, 128)
	s := d.Snapshot()
	if s.PlayState != proto.PlayStateCued {
		t.Errorf("after Load, PlayState = %#x, want Cued", s.PlayState)
	}
	if s.TrackID != 42 {
		t.Errorf("TrackID = %d, want 42", s.TrackID)
	}
	if s.BPM != 12800 {
		t.Errorf("BPM = %d, want 12800", s.BPM)
	}
	if s.BeatInBar != 1 {
		t.Errorf("at cue, BeatInBar = %d, want 1", s.BeatInBar)
	}
}

func TestSimDeckPlayheadAdvances(t *testing.T) {
	d, clk := newTestDeck()
	// 128 BPM => 468.75 ms/beat. 1s playback => ~2.13 beats.
	d.Load(1, 17, proto.SlotUSB, 1, grid(600, 128), 0, 300000, 128)
	d.Play()
	clk.advance(1 * time.Second)
	st := d.Status()
	if st.PositionMs < 990 || st.PositionMs > 1010 {
		t.Errorf("after 1s, position = %.1fms, want ~1000", st.PositionMs)
	}
	// Beat 3 is at 937.5ms, beat 4 at 1406.25ms; at 1000ms we are on beat 3.
	s := d.Snapshot()
	if s.BeatInTrack != 3 {
		t.Errorf("BeatInTrack = %d, want 3", s.BeatInTrack)
	}
	if s.BeatInBar != 3 {
		t.Errorf("BeatInBar = %d, want 3", s.BeatInBar)
	}
}

func TestSimDeckPitchChangesRate(t *testing.T) {
	d, clk := newTestDeck()
	d.Load(1, 17, proto.SlotUSB, 1, grid(600, 120), 0, 600000, 120)
	d.SetPitch(50) // 1.5x
	d.Play()
	clk.advance(2 * time.Second)
	st := d.Status()
	if st.PositionMs < 2990 || st.PositionMs > 3010 {
		t.Errorf("2s at +50%% = %.1fms, want ~3000", st.PositionMs)
	}
	if st.EffectiveBPM < 179 || st.EffectiveBPM > 181 {
		t.Errorf("effective BPM = %.2f, want ~180", st.EffectiveBPM)
	}
}

func TestSimDeckPauseFreezes(t *testing.T) {
	d, clk := newTestDeck()
	d.Load(1, 17, proto.SlotUSB, 1, grid(600, 120), 0, 600000, 120)
	d.Play()
	clk.advance(500 * time.Millisecond)
	d.Pause()
	clk.advance(10 * time.Second) // time passes while paused
	st := d.Status()
	if st.PositionMs < 490 || st.PositionMs > 510 {
		t.Errorf("paused position drifted to %.1fms, want ~500", st.PositionMs)
	}
	if st.State != "PAUSED" {
		t.Errorf("state = %q, want PAUSED", st.State)
	}
}

func TestSimDeckReachesEnd(t *testing.T) {
	d, clk := newTestDeck()
	d.Load(1, 17, proto.SlotUSB, 1, grid(10, 120), 0, 2000, 120)
	d.Play()
	clk.advance(5 * time.Second)
	s := d.Snapshot()
	if s.PlayState != proto.PlayStateEnded {
		t.Errorf("past end, PlayState = %#x (%s), want Ended", s.PlayState, proto.PlayStateName(s.PlayState))
	}
	if d.Status().PositionMs != 2000 {
		t.Errorf("end position = %.1f, want clamped to 2000", d.Status().PositionMs)
	}
}

func TestSimDeckSeekFromEnd(t *testing.T) {
	d, clk := newTestDeck()
	d.Load(1, 17, proto.SlotUSB, 1, grid(10, 120), 0, 2000, 120)
	d.Play()
	clk.advance(5 * time.Second)
	d.Seek(500)
	if d.Status().State != "PAUSED" {
		t.Errorf("after seeking back from end, state = %q, want PAUSED", d.Status().State)
	}
	d.Play()
	clk.advance(200 * time.Millisecond)
	if st := d.Status(); st.PositionMs < 690 || st.PositionMs > 710 {
		t.Errorf("resumed position = %.1f, want ~700", st.PositionMs)
	}
}

func TestSimDeckEject(t *testing.T) {
	d, clk := newTestDeck()
	d.Load(1, 17, proto.SlotUSB, 1, grid(600, 128), 0, 300000, 128)
	d.SetPitch(4)
	d.Play()
	clk.advance(2 * time.Second)

	// Eject must not panic (it previously reassigned *d while holding the
	// mutex, panicking on the deferred unlock) and must clear the track.
	d.Eject()
	s := d.Snapshot()
	if s.PlayState != proto.PlayStateNoTrack {
		t.Errorf("after eject, PlayState = %#x, want NoTrack", s.PlayState)
	}
	if s.TrackID != 0 {
		t.Errorf("after eject, TrackID = %d, want 0", s.TrackID)
	}
	if d.Status().Loaded {
		t.Error("after eject, Loaded = true")
	}
	// The deck is still usable after an eject.
	d.Eject() // idempotent, still no panic
	d.Load(2, 17, proto.SlotUSB, 1, grid(100, 174), 0, 60000, 174)
	if d.Snapshot().TrackID != 2 {
		t.Error("could not load after eject")
	}
}

func TestSimDeckDownbeatOffset(t *testing.T) {
	d, _ := newTestDeck()
	// Downbeat at index 2: beats 2,6,10,... are beat 1 of a bar.
	d.Load(1, 17, proto.SlotUSB, 1, grid(20, 120), 2, 10000, 120)
	// At cue (pos 0, beat index 0): rel = 0-2 = -2 => bar beat 3.
	if s := d.Snapshot(); s.BeatInBar != 3 {
		t.Errorf("BeatInBar at index 0 with downbeat 2 = %d, want 3", s.BeatInBar)
	}
}

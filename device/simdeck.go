// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// SimDeck is a virtual playing deck: it advances a playhead through a loaded
// track's beat grid at a set pitch and reports the result as a
// proto.CDJPlayState. It holds no audio; it is a clock over a beat grid.
//
// It is the engine behind the CDJ emulator (see docs/design/cdj-emulator.md):
// a status broadcaster calls Snapshot at the status rate, and the whole thing
// can drive Vynull's own monitor/overlay/MPRIS/history as a software test rig.
//
// All methods are safe for concurrent use. The time source is injectable so
// the clock can be driven deterministically in tests.
type SimDeck struct {
	now func() time.Time

	mu sync.Mutex

	// Loaded track.
	trackID     uint32
	trackDevice uint8
	trackSlot   uint8
	trackType   uint8
	beats       []float64 // beat positions in ms, ascending
	downbeat    int       // index into beats of the first downbeat (beat 1 of a bar)
	durationMs  float64
	bpm         float64 // track base BPM (unpitched); 0 = unknown

	// Transport.
	state    uint8   // proto.PlayState*
	pitchPct float64 // tempo adjustment, clamped to [-100, 100]
	playing  bool
	master   bool
	sync     bool
	onAir    bool

	// Playhead. posMs is the position as of anchor; while playing, the live
	// position is posMs + (now-anchor) * rate. normalizeLocked folds elapsed
	// wall-clock into posMs and resets anchor, so repeated reads never
	// double-count.
	posMs  float64
	anchor time.Time
}

// NewSimDeck returns an idle SimDeck with no track loaded.
func NewSimDeck() *SimDeck {
	return &SimDeck{
		now:       time.Now,
		trackSlot: proto.SlotUSB,
		trackType: 1,
		state:     proto.PlayStateNoTrack,
	}
}

// Load places a track at the cue point (position 0, cued, not playing). The
// beat grid (ms positions, ascending) and downbeat index come from analysis;
// pitch carries over, as it would on a real deck.
func (d *SimDeck) Load(trackID uint32, device, slot, typ uint8, beats []float64, downbeat int, durationMs, bpm float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trackID = trackID
	d.trackDevice = device
	d.trackSlot = slot
	d.trackType = typ
	d.beats = beats
	if downbeat < 0 {
		downbeat = 0
	}
	d.downbeat = downbeat
	d.durationMs = durationMs
	d.bpm = bpm
	d.posMs = 0
	d.playing = false
	d.state = proto.PlayStateCued
	d.anchor = d.now()
}

// Eject clears the loaded track. It resets the track, grid, and transport in
// place; pitch and the sync/master/on-air flags carry over, as a physical
// fader and switches would. (Resetting the fields individually rather than
// reassigning *d is deliberate: overwriting the struct while holding d.mu
// would replace the locked mutex and panic on the deferred unlock.)
func (d *SimDeck) Eject() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trackID = 0
	d.trackDevice = 0
	d.trackSlot = proto.SlotUSB
	d.trackType = 1
	d.beats = nil
	d.downbeat = 0
	d.durationMs = 0
	d.bpm = 0
	d.posMs = 0
	d.anchor = d.now()
	d.playing = false
	d.state = proto.PlayStateNoTrack
}

// Play starts (or resumes) playback. No-op with no track loaded or at the end.
func (d *SimDeck) Play() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == proto.PlayStateNoTrack || d.state == proto.PlayStateEnded {
		return
	}
	d.playing = true
	d.state = proto.PlayStatePlaying
	d.anchor = d.now()
}

// Pause freezes the playhead where it is.
func (d *SimDeck) Pause() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizeLocked(d.now())
	if d.state == proto.PlayStateNoTrack {
		return
	}
	d.playing = false
	d.state = proto.PlayStatePaused
}

// Cue returns the playhead to the start (the cue point) and pauses.
func (d *SimDeck) Cue() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == proto.PlayStateNoTrack {
		return
	}
	d.playing = false
	d.posMs = 0
	d.state = proto.PlayStateCued
}

// Seek jumps the playhead to ms (clamped to the track). Playback continues if
// it was playing. Seeking back from the end re-arms the deck.
func (d *SimDeck) Seek(ms float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizeLocked(d.now())
	if d.state == proto.PlayStateNoTrack {
		return
	}
	if ms < 0 {
		ms = 0
	}
	if d.durationMs > 0 && ms > d.durationMs {
		ms = d.durationMs
	}
	d.posMs = ms
	d.anchor = d.now()
	if d.state == proto.PlayStateEnded && ms < d.durationMs {
		d.state = proto.PlayStatePaused
	}
}

// SetPitch sets the tempo adjustment in percent (clamped to [-100, 100]). The
// rate changes without jumping the playhead.
func (d *SimDeck) SetPitch(pct float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizeLocked(d.now())
	if pct < -100 {
		pct = -100
	} else if pct > 100 {
		pct = 100
	}
	d.pitchPct = pct
	d.anchor = d.now()
}

// SetFlags sets the sync / master / on-air flags. The emulator's safe profile
// leaves all three off; this is here for the live-rig opt-in.
func (d *SimDeck) SetFlags(master, sync, onAir bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.master, d.sync, d.onAir = master, sync, onAir
}

func (d *SimDeck) rate() float64 { return 1 + d.pitchPct/100 }

// normalizeLocked folds elapsed wall-clock time into posMs and handles
// end-of-track. Caller holds d.mu.
func (d *SimDeck) normalizeLocked(t time.Time) {
	if d.playing {
		d.posMs += t.Sub(d.anchor).Seconds() * 1000 * d.rate()
	}
	d.anchor = t
	if d.posMs < 0 {
		d.posMs = 0
	}
	if d.durationMs > 0 && d.posMs >= d.durationMs {
		d.posMs = d.durationMs
		d.playing = false
		d.state = proto.PlayStateEnded
	}
}

// beatPositionLocked derives the beat-in-track (1-based) and beat-in-bar (1-4)
// from the playhead. Returns (0xFFFFFFFF, 0) when there is no grid. Caller
// holds d.mu.
func (d *SimDeck) beatPositionLocked() (uint32, uint8) {
	if len(d.beats) == 0 {
		return 0xFFFFFFFF, 0
	}
	// Count of beats at or before the playhead.
	n := sort.Search(len(d.beats), func(k int) bool { return d.beats[k] > d.posMs })
	var beatIdx int
	var beatInTrack uint32
	if n == 0 {
		beatIdx = 0
		beatInTrack = 1
	} else {
		beatIdx = n - 1
		beatInTrack = uint32(n)
	}
	rel := beatIdx - d.downbeat
	beatInBar := uint8(((rel%4)+4)%4) + 1
	return beatInTrack, beatInBar
}

// Snapshot returns the current dynamic status fields. PacketNum is left zero
// for the broadcaster to fill.
func (d *SimDeck) Snapshot() proto.CDJPlayState {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizeLocked(d.now())

	bpm := uint16(0xFFFF)
	if d.bpm > 0 {
		bpm = uint16(math.Round(d.bpm * 100))
	}
	beatInTrack, beatInBar := d.beatPositionLocked()

	return proto.CDJPlayState{
		PlayState:   d.state,
		TrackDevice: d.trackDevice,
		TrackSlot:   d.trackSlot,
		TrackType:   d.trackType,
		TrackID:     d.trackID,
		BPM:         bpm,
		PitchPct:    d.pitchPct,
		BeatInTrack: beatInTrack,
		BeatInBar:   beatInBar,
		Master:      d.master,
		Sync:        d.sync,
		OnAir:       d.onAir,
	}
}

// SimDeckStatus is a human-friendly view of the deck for the API/CLI.
type SimDeckStatus struct {
	Loaded       bool    `json:"loaded"`
	TrackID      uint32  `json:"track_id"`
	State        string  `json:"state"`
	Playing      bool    `json:"playing"`
	PositionMs   float64 `json:"position_ms"`
	DurationMs   float64 `json:"duration_ms"`
	BPM          float64 `json:"bpm"`
	EffectiveBPM float64 `json:"effective_bpm"`
	PitchPct     float64 `json:"pitch_pct"`
	BeatInTrack  uint32  `json:"beat_in_track"`
	BeatInBar    uint8   `json:"beat_in_bar"`
}

// Status returns the deck's current state for display.
func (d *SimDeck) Status() SimDeckStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.normalizeLocked(d.now())
	beatInTrack, beatInBar := d.beatPositionLocked()
	eff := d.bpm * d.rate()
	return SimDeckStatus{
		Loaded:       d.state != proto.PlayStateNoTrack,
		TrackID:      d.trackID,
		State:        proto.PlayStateName(d.state),
		Playing:      d.playing,
		PositionMs:   d.posMs,
		DurationMs:   d.durationMs,
		BPM:          d.bpm,
		EffectiveBPM: eff,
		PitchPct:     d.pitchPct,
		BeatInTrack:  beatInTrack,
		BeatInBar:    beatInBar,
	}
}

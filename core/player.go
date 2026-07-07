// SPDX-License-Identifier: GPL-3.0-or-later

package core

import "time"

// PlayerState is the live state of one deck on a link, as the UI and TUI show
// it — brand-agnostic, so a CDJ and a Denon player look the same here.
type PlayerState struct {
	DeckID    int
	Name      string  // "CDJ-3000", "Prime 4"
	TrackID   TrackID // 0 if unknown/none
	TrackName string
	Artist    string
	Key       string
	BPM       float64
	Pitch     float64 // tempo adjust as a fraction (+0.02 = +2%)
	Playing   bool
	OnAir     bool
	Master    bool
	BeatInBar int // 1..4, 0 if unknown
	LastSeen  time.Time
}

// MixerState is the live state of a mixer on the link.
type MixerState struct {
	DeviceID     int
	Name         string
	MasterBPM    float64
	MasterDevice int
	BeatInBar    int
	ChannelOnAir []bool
	LastSeen     time.Time
}

// EventKind classifies a change a Backend pushes to the UI.
type EventKind uint8

const (
	EventPlayer EventKind = iota // a player's state changed
	EventMixer                   // a mixer's state changed
	EventBeat                    // a beat tick
	EventPeer                    // a device joined or left
)

// Event is a change a Backend streams to the UI/TUI via Backend.Events.
type Event struct {
	Kind   EventKind
	DeckID int
	Player *PlayerState
	Mixer  *MixerState
}

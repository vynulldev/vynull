// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"context"
	"io"
)

// Source is the neutral data a Backend serves to players: the library, its
// analysis, and the audio bytes. It is implemented once over the library +
// analysis cache and consumed by every backend.
type Source interface {
	Track(TrackID) (*Track, bool)
	Tracks() []*Track
	Playlists() []*Playlist
	Analysis(TrackID) (*Analysis, bool)
	// Open returns the audio stream for a track and its size in bytes; the
	// backend's file transport (NFS, HTTP) streams from it.
	Open(TrackID) (io.ReadSeekCloser, int64, error)
}

// Backend is a live link/source protocol — Pro DJ Link, StageLinq, …. A process
// may run several at once, so one library is visible to multiple ecosystems.
type Backend interface {
	// Name is a short identifier, e.g. "prolink" or "stagelinq".
	Name() string
	// Start announces on the network and serves src until ctx is cancelled.
	Start(ctx context.Context, src Source) error
	// Players and Mixers report the devices currently seen on the link.
	Players() []PlayerState
	Mixers() []MixerState
	// Load asks a deck to load a track, where the protocol supports it.
	Load(deck int, id TrackID) error
	// Events streams player/mixer/beat changes for the UI and TUI.
	Events() <-chan Event
}

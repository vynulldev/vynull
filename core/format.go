// SPDX-License-Identifier: GPL-3.0-or-later

package core

// Library is a neutral snapshot of tracks and playlists — what the export
// adapters consume. (Import adapters live in package library, working against
// the concrete library.Library; see docs/design/import-layer.md.)
type Library struct {
	Tracks    []*Track
	Playlists []*Playlist
}

// ExportOptions carries adapter-agnostic export knobs.
type ExportOptions struct {
	// CopyFiles copies audio into the destination instead of referencing it.
	CopyFiles bool
}

// Exporter writes the neutral model out in a foreign format (rekordbox USB/PDB,
// Engine Library, M3U, …).
type Exporter interface {
	Name() string
	Export(lib *Library, dst string, opts ExportOptions) error
}

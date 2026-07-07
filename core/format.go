// SPDX-License-Identifier: GPL-3.0-or-later

package core

// Library is a neutral snapshot of tracks and playlists — the currency of the
// import/export adapters.
type Library struct {
	Tracks    []*Track
	Playlists []*Playlist
}

// ImportOptions carries adapter-agnostic import knobs; a specific importer may
// define its own richer options.
type ImportOptions struct {
	// RemapPath, when set, rewrites an imported absolute path onto a local file.
	RemapPath func(string) string
}

// ExportOptions carries adapter-agnostic export knobs.
type ExportOptions struct {
	// CopyFiles copies audio into the destination instead of referencing it.
	CopyFiles bool
}

// Importer reads a foreign library (rekordbox XML/master.db, Engine, Serato, …)
// into the neutral model.
type Importer interface {
	Name() string
	// Detect reports whether path looks like this format (a directory or file).
	Detect(path string) bool
	Import(path string, opts ImportOptions) (*Library, error)
}

// Exporter writes the neutral model out in a foreign format (rekordbox USB/PDB,
// Engine Library, M3U, …).
type Exporter interface {
	Name() string
	Export(lib *Library, dst string, opts ExportOptions) error
}

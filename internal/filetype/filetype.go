// SPDX-License-Identifier: GPL-3.0-or-later

// Package filetype is the one table of the audio file types Vynull serves:
// each type's file extensions, the name the library stores for it,
// rekordbox's ID for it, and whether it is lossless.
package filetype

import (
	"path/filepath"
	"strings"
)

// Type is an audio file type Vynull serves.
type Type struct {
	// Name is the library's name for the type: "mp3", "m4a", "flac", "wav"
	// or "aiff".
	Name string
	// RekordboxID is rekordbox's FileType for it, which a deck picks its
	// decoder by: MP3 1, M4A (AAC) 4, FLAC 5, WAV 11, AIFF 12.
	RekordboxID int
	// Lossless is true for FLAC, WAV and AIFF: no encoder delay, and the NFS
	// server serves them transcoded.
	Lossless bool
}

var (
	mp3  = Type{Name: "mp3", RekordboxID: 1}
	m4a  = Type{Name: "m4a", RekordboxID: 4}
	flac = Type{Name: "flac", RekordboxID: 5, Lossless: true}
	wav  = Type{Name: "wav", RekordboxID: 11, Lossless: true}
	aiff = Type{Name: "aiff", RekordboxID: 12, Lossless: true}
)

var byExt = map[string]Type{".mp3": mp3, ".m4a": m4a, ".flac": flac, ".wav": wav, ".aiff": aiff, ".aif": aiff}

var byName = map[string]Type{"mp3": mp3, "m4a": m4a, "flac": flac, "wav": wav, "aiff": aiff}

// ForPath returns the type of the audio file at path, by its extension in any
// case (".aif" is AIFF), and whether Vynull serves that type.
func ForPath(path string) (Type, bool) {
	t, ok := byExt[strings.ToLower(filepath.Ext(path))]
	return t, ok
}

// ByName returns the type the library names name ("flac"), and whether there
// is one.
func ByName(name string) (Type, bool) {
	t, ok := byName[name]
	return t, ok
}

// SPDX-License-Identifier: GPL-3.0-or-later

package core

import "time"

// TrackID identifies a track within a Library for the life of the process.
// Backends map it onto their own on-wire identifiers.
type TrackID uint32

// DurationSec is a track length in whole seconds.
type DurationSec uint32

// Track is one library entry: brand-agnostic metadata plus the beat-grid
// override fields the UI can edit. Format encoders (ANLZ/PDB, Engine) map it
// onto their own representations. The field set mirrors what vynull already
// tracks so existing packages can migrate onto this type.
type Track struct {
	ID       TrackID     `json:"id"`
	Title    string      `json:"title"`
	Artist   string      `json:"artist"`
	Album    string      `json:"album"`
	Genre    string      `json:"genre"`
	Duration DurationSec `json:"duration"`
	BPM      float64     `json:"bpm"`
	Key      string      `json:"key"` // display key; Analysis carries both notations
	Rating   uint8       `json:"rating"`
	Year     int         `json:"year"`
	TrackNum int         `json:"track_num"`
	DiscNum  int         `json:"disc_num"`

	FilePath string `json:"file_path"` // absolute path on disk
	FileType string `json:"file_type"` // "mp3", "m4a", "flac", "wav", "aiff"
	FileSize int64  `json:"file_size"`
	ArtID    uint32 `json:"art_id"` // artwork lookup ID, 0 if none

	Comment        string `json:"comment"`
	Label          string `json:"label"`
	OriginalArtist string `json:"original_artist"`
	Remixer        string `json:"remixer"`
	MixName        string `json:"mix_name"`

	DateAdded   time.Time `json:"date_added"`
	Bitrate     int       `json:"bitrate"`      // kbps
	SampleRate  int       `json:"sample_rate"`  // Hz
	SampleDepth int       `json:"sample_depth"` // bits
	PlayCount   int       `json:"play_count"`
	ColorID     uint8     `json:"color_id"`

	FileMissing bool `json:"file_missing,omitempty"`

	// Beat-grid overrides. DetectedBPM snapshots the auto-detected value;
	// BPM (above) is the effective value used downstream. BeatPhaseShift
	// rotates which detected beat is the downbeat (0 = detector's choice).
	DetectedBPM    float64 `json:"detected_bpm,omitempty"`
	BeatPhaseShift int     `json:"beat_phase_shift,omitempty"`
}

// Playlist is an ordered list of tracks, or a folder of child playlists. A
// smart playlist evaluates rules at read time; the rules are opaque here and a
// format adapter interprets them.
type Playlist struct {
	ID       uint32    `json:"id"`
	Name     string    `json:"name"`
	ParentID uint32    `json:"parent_id"` // 0 = root
	IsFolder bool      `json:"is_folder"`
	IsSmart  bool      `json:"is_smart"`
	TrackIDs []TrackID `json:"track_ids,omitempty"`
}

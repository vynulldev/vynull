// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"fmt"
	"time"
)

// DurationSec is a time.Duration that serializes as float seconds in JSON.
type DurationSec time.Duration

func (d DurationSec) Seconds() float64        { return time.Duration(d).Seconds() }
func (d DurationSec) Duration() time.Duration { return time.Duration(d) }

func (d DurationSec) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%.3f", time.Duration(d).Seconds())), nil
}

func (d *DurationSec) UnmarshalJSON(b []byte) error {
	var val float64
	if err := json.Unmarshal(b, &val); err != nil {
		return err
	}
	if val > 86400 {
		// Old format: nanoseconds as integer (e.g., 178000000000).
		*d = DurationSec(time.Duration(int64(val)))
	} else {
		// Float seconds (e.g., 178.432).
		*d = DurationSec(time.Duration(val * float64(time.Second)))
	}
	return nil
}

// Track represents a music file in the library.
// Fields match the rekordbox PDB track entry format.
type Track struct {
	ID             uint32      `json:"id"`
	Title          string      `json:"title"`
	Artist         string      `json:"artist"`
	Album          string      `json:"album"`
	Genre          string      `json:"genre"`
	Duration       DurationSec `json:"duration"`
	BPM            float64     `json:"bpm"`
	Key            string      `json:"key"`
	Rating         uint8       `json:"rating"`
	Year           int         `json:"year"`
	TrackNum       int         `json:"track_num"`
	DiscNum        int         `json:"disc_num"`
	FilePath       string      `json:"file_path"`                  // absolute path on disk
	FileType       string      `json:"file_type"`                  // "mp3", "m4a", "flac", "wav", "aiff"
	FileTypeID     int         `json:"file_type_id,omitempty"`     // rekordbox FileType enum, when imported (authoritative over the extension)
	AutoLoadHotCue bool        `json:"auto_load_hotcue,omitempty"` // deck auto-loads this track's hot cues
	FileSize       int64       `json:"file_size"`
	ArtID          uint32      `json:"art_id"` // artwork lookup ID, 0 if none
	Comment        string      `json:"comment"`
	Label          string      `json:"label"` // record label
	OriginalArtist string      `json:"original_artist"`
	Remixer        string      `json:"remixer"`
	MixName        string      `json:"mix_name"` // mix/version name
	// AlbumArtist and Composer come from a rekordbox import (the album's
	// artist; ComposerID, which rekordbox's smart playlists call producer).
	AlbumArtist string `json:"album_artist,omitempty"`
	Composer    string `json:"composer,omitempty"`
	// KeyID is the track's rekordbox key row ID, from an import (0 when the
	// track has no key relation). It tells an empty-named key from no key.
	KeyID     uint32    `json:"key_id,omitempty"`
	DateAdded time.Time `json:"date_added"`
	// DateCreated is rekordbox's separate DateCreated, from an import; smart
	// playlist rules on dateCreated test it (zero when unknown).
	DateCreated time.Time `json:"date_created,omitempty"`
	Bitrate     int       `json:"bitrate"`      // kbps
	SampleRate  int       `json:"sample_rate"`  // Hz
	SampleDepth int       `json:"sample_depth"` // bits
	PlayCount   int       `json:"play_count"`
	ColorID     uint8     `json:"color_id"`               // 0=none, 1=pink, 2=red, etc.
	ArtChecked  bool      `json:"art_checked,omitempty"`  // true after artwork extraction attempted
	FileMissing bool      `json:"file_missing,omitempty"` // FilePath doesn't exist on disk — unplayable (e.g. an imported path not remapped to a local file)

	// Beat-grid override fields. DetectedBPM is the auto-detected value
	// snapshotted before any manual override; BPM (above) is the
	// effective value used everywhere downstream (CDJ menus, beat grid
	// generation, smart playlists). BeatPhaseShift rotates which beat
	// in the detected grid is labelled as the downbeat (beat 1) — 0
	// keeps the detector's choice, 1-3 shift forward N beats.
	DetectedBPM    float64 `json:"detected_bpm,omitempty"`
	BeatPhaseShift int     `json:"beat_phase_shift,omitempty"`

	// AnalysisLocked protects a hand-corrected grid: while set, the daemon
	// refuses calls that would re-run analysis or rewrite the beat grid for
	// this track, and will not auto/re-analyze it (serving the existing grid
	// even across a cache-version bump). Omitted from JSON when false.
	AnalysisLocked bool `json:"analysis_locked,omitempty"`

	// Related lists the tracks rekordbox recommends alongside this one
	// (djmdRecommendLike from a master.db import, both directions). It backs
	// the CDJ's MATCHING browse and persists with the library.
	Related []uint32 `json:"related,omitempty"`
}

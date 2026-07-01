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
	FilePath       string      `json:"file_path"` // absolute path on disk
	FileType       string      `json:"file_type"` // "mp3", "m4a", "flac", "wav", "aiff"
	FileSize       int64       `json:"file_size"`
	ArtID          uint32      `json:"art_id"` // artwork lookup ID, 0 if none
	Comment        string      `json:"comment"`
	Label          string      `json:"label"` // record label
	OriginalArtist string      `json:"original_artist"`
	Remixer        string      `json:"remixer"`
	MixName        string      `json:"mix_name"` // mix/version name
	DateAdded      time.Time   `json:"date_added"`
	Bitrate        int         `json:"bitrate"`      // kbps
	SampleRate     int         `json:"sample_rate"`  // Hz
	SampleDepth    int         `json:"sample_depth"` // bits
	PlayCount      int         `json:"play_count"`
	ColorID        uint8       `json:"color_id"`               // 0=none, 1=pink, 2=red, etc.
	ArtChecked     bool        `json:"art_checked,omitempty"`  // true after artwork extraction attempted
	FileMissing    bool        `json:"file_missing,omitempty"` // FilePath doesn't exist on disk — unplayable (e.g. an imported path not remapped to a local file)

	// Audio-decode health, populated by CheckDecode when the track is
	// added (or via /api/scan-decode for backfill). DecodeStatus is one
	// of "ok" / "warning" / "error"; "error" means ffmpeg hit hard
	// frame errors that empirically freeze the CDJ mid-
	// playback. DecodeIssue carries the first complaint for display.
	DecodeStatus string `json:"decode_status,omitempty"`
	DecodeIssue  string `json:"decode_issue,omitempty"`

	// Beat-grid override fields. DetectedBPM is the auto-detected value
	// snapshotted before any manual override; BPM (above) is the
	// effective value used everywhere downstream (CDJ menus, beat grid
	// generation, smart playlists). BeatPhaseShift rotates which beat
	// in the detected grid is labelled as the downbeat (beat 1) — 0
	// keeps the detector's choice, 1-3 shift forward N beats.
	DetectedBPM    float64 `json:"detected_bpm,omitempty"`
	BeatPhaseShift int     `json:"beat_phase_shift,omitempty"`
}

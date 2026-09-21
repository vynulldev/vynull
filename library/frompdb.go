// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/vynulldev/vynull/pdb"
)

// FromPDB builds a Library over a served rekordbox export: the same track
// IDs the decks see (the PDB's), rekordbox's own titles/keys/tempos, and
// absolute file paths under musicDir. Serving a USB must keep the web
// UI/API and the decks in ONE track-ID space — with a separately scanned
// library the two sides number the same files independently, so a
// web-initiated load lands on a different deck track than the row the
// user clicked, and cues/analysis key against the wrong track.
func FromPDB(tracks []*pdb.Track, musicDir string) *Library {
	out := make([]*Track, 0, len(tracks))
	for _, t := range tracks {
		abs := filepath.Join(musicDir, filepath.FromSlash(t.FilePath))
		out = append(out, &Track{
			ID:       t.ID,
			Title:    t.Title,
			Artist:   t.Artist,
			Album:    t.Album,
			Genre:    t.Genre,
			Key:      t.Key,
			Label:    t.Label,
			Comment:  t.Comment,
			BPM:      float64(t.Tempo) / 100,
			Duration: DurationSec(time.Duration(t.Duration) * time.Second),
			Year:     int(t.Year),
			TrackNum: int(t.TrackNum),
			Bitrate:  int(t.Bitrate),
			FilePath: abs,
			FileType: supportedExtensions[strings.ToLower(filepath.Ext(abs))],
			ArtID:    t.ArtworkID,
		})
	}
	return NewLibrary(out, NewArtworkCache())
}

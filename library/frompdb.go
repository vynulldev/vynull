// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"time"

	"github.com/vynulldev/vynull/pdb"
)

// FromPDB builds a Library over a served rekordbox export: the same track
// IDs the decks see (the PDB's), rekordbox's own fields (titles, keys,
// tempos, ratings, colours, play counts, dates, ANLZ paths) and category
// IDs, and absolute file paths under musicDir. The dbserver serves the
// export from it, as it serves any library. Serving a USB must keep the web
// UI/API and the decks in ONE track-ID space — with a separately scanned
// library the two sides number the same files independently, so a
// web-initiated load lands on a different deck track than the row the
// user clicked, and cues/analysis key against the wrong track.
func FromPDB(tracks []*pdb.Track, musicDir string) *Library {
	out := make([]*Track, 0, len(tracks))
	for _, t := range tracks {
		abs := filepath.Join(musicDir, filepath.FromSlash(t.FilePath))
		var added time.Time
		if t.DateAdded != "" {
			added, _ = time.Parse("2006-01-02", t.DateAdded)
		}
		out = append(out, &Track{
			ID:          t.ID,
			Title:       t.Title,
			Artist:      t.Artist,
			Album:       t.Album,
			Genre:       t.Genre,
			Key:         t.Key,
			KeyID:       t.KeyID,
			Label:       t.Label,
			Comment:     t.Comment,
			BPM:         float64(t.Tempo) / 100,
			Duration:    DurationSec(time.Duration(t.Duration) * time.Second),
			Year:        int(t.Year),
			TrackNum:    int(t.TrackNum),
			DiscNum:     int(t.DiscNumber),
			Bitrate:     int(t.Bitrate),
			Rating:      t.Rating,
			ColorID:     t.ColorID,
			PlayCount:   int(t.PlayCount),
			DateAdded:   added,
			FilePath:    abs,
			FileType:    fileTypeOrEmpty(abs),
			FileSize:    int64(t.FileSize),
			SampleRate:  int(t.SampleRate),
			SampleDepth: int(t.SampleDepth),
			ArtID:       t.ArtworkID,
			AnalyzePath: t.AnalyzePath,
			// A USB export's tracks carry hot-cue autoload, as the decks
			// were served them from the PDB.
			AutoLoadHotCue: true,
		})
	}
	lib := NewLibrary(out, NewArtworkCache())
	// The decks browse by the export's own category IDs, as they would the
	// export itself.
	for _, t := range tracks {
		lib.RegisterCategoryID("artist", t.Artist, t.ArtistID)
		lib.RegisterCategoryID("album", t.Album, t.AlbumID)
		lib.RegisterCategoryID("genre", t.Genre, t.GenreID)
		lib.RegisterCategoryID("key", t.Key, t.KeyID)
		lib.RegisterCategoryID("label", t.Label, t.LabelID)
	}
	return lib
}

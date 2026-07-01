// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
)

// supportedExtensions maps file extensions to their type name.
var supportedExtensions = map[string]string{
	".mp3":  "mp3",
	".m4a":  "m4a",
	".flac": "flac",
	".wav":  "wav",
	".aiff": "aiff",
	".aif":  "aiff",
}

// Scan walks the given directory tree and returns a Library containing
// all recognized music files with their metadata.
func Scan(root string) (*Library, error) {
	artwork := NewArtworkCache()
	var tracks []*Track
	var nextID uint32 = 1

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("scan: skipping %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		fileType, ok := supportedExtensions[ext]
		if !ok {
			return nil
		}

		track := &Track{
			ID:       nextID,
			FilePath: path,
			FileType: fileType,
		}
		nextID++

		info, err := d.Info()
		if err == nil {
			track.FileSize = info.Size()
		}

		if err := readTags(track, artwork); err != nil {
			log.Printf("scan: tags for %s: %v", path, err)
		}

		// Fall back to filename as title.
		if track.Title == "" {
			track.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}

		tracks = append(tracks, track)
		return nil
	})
	if err != nil {
		return nil, err
	}

	lib := NewLibrary(tracks, artwork)
	log.Printf("scanned %d tracks (%d unique artworks)", len(tracks), len(artwork.byHash))
	return lib, nil
}

func readTags(track *Track, artwork *ArtworkCache) error {
	f, err := os.Open(track.FilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return err
	}

	track.Title = m.Title()
	track.Artist = m.Artist()
	track.Album = m.Album()
	track.Genre = m.Genre()
	track.Year = m.Year()
	trackNum, _ := m.Track()
	track.TrackNum = trackNum

	if pic := m.Picture(); pic != nil && len(pic.Data) > 0 {
		track.ArtID = artwork.Add(pic.MIMEType, pic.Data)
	}

	return nil
}

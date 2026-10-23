// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"github.com/vynulldev/vynull/internal/filetype"
)

// fileTypeOrExt is a file's type name for imports, which keep an unsupported
// file's own extension rather than dropping the track.
func fileTypeOrExt(path string) string {
	if ft, ok := filetype.ForPath(path); ok {
		return ft.Name
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

func fileTypeOrEmpty(path string) string {
	ft, _ := filetype.ForPath(path)
	return ft.Name
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

		fileType, ok := filetype.ForPath(path)
		if !ok {
			return nil
		}

		track := &Track{
			ID:       nextID,
			FilePath: path,
			FileType: fileType.Name,
		}
		nextID++

		info, err := d.Info()
		if err == nil {
			track.FileSize = info.Size()
		}

		if ft, err := ReadFileTags(path); err != nil {
			log.Printf("scan: tags for %s: %v", path, err)
		} else {
			ft.ApplyTo(track)
			if len(ft.Picture) > 0 {
				track.ArtID = artwork.Add(ft.PictureMIME, ft.Picture)
			}
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

// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"fmt"
	"os"
	"path/filepath"
)

// ArtworkPath returns the USB-relative path rekordbox uses for an
// artwork JPEG (e.g. `/PIONEER/Artwork/00001/a42.jpg`). Same scheme
// for the matching thumbnail `_m.jpg`.
//
// Verified against a rekordbox USB export: every JPEG
// lives under the bucket `00001`; the bucket appears to be a 5-digit
// page index that increments past ~1000 artworks per bucket. We use
// the same 1000-per-bucket scheme here — fine for typical libraries
// and easy to revisit if a CDJ trips on it.
func ArtworkPath(id uint32) string {
	return fmt.Sprintf("/PIONEER/Artwork/%05d/a%d.jpg", artworkBucket(id), id)
}

// ArtworkThumbPath is the matching thumbnail path (a<id>_m.jpg).
func ArtworkThumbPath(id uint32) string {
	return fmt.Sprintf("/PIONEER/Artwork/%05d/a%d_m.jpg", artworkBucket(id), id)
}

func artworkBucket(id uint32) uint32 {
	if id == 0 {
		return 1
	}
	return (id-1)/1000 + 1
}

// ArtworkLookup resolves an artwork ID to its raw JPEG bytes. Returns
// nil when the ID isn't known. Implementations typically wrap
// library.ArtworkCache.Get.
type ArtworkLookup func(id uint32) []byte

// WriteArtworkFiles materialises each artwork ID under destDir using
// the rekordbox path scheme (full + thumbnail). For each ID present in
// the lookup, two files are written: `a<id>.jpg` and `a<id>_m.jpg`.
// The thumbnail currently mirrors the full image — adequate for the
// CDJ track list at the cost of disk space; we can plug a JPEG
// downscaler in later without changing callers.
//
// IDs that don't resolve via the lookup are silently skipped — a
// track row with a stale ArtworkID will just not display art on the
// CDJ rather than blocking the whole export.
func WriteArtworkFiles(destDir string, ids []uint32, lookup ArtworkLookup) (int, error) {
	if lookup == nil || len(ids) == 0 {
		return 0, nil
	}
	written := 0
	for _, id := range ids {
		if id == 0 {
			continue
		}
		data := lookup(id)
		if len(data) == 0 {
			continue
		}
		fullRel := ArtworkPath(id)
		thumbRel := ArtworkThumbPath(id)
		fullAbs := filepath.Join(destDir, fullRel)
		thumbAbs := filepath.Join(destDir, thumbRel)
		if err := os.MkdirAll(filepath.Dir(fullAbs), 0o755); err != nil {
			return written, fmt.Errorf("create artwork dir: %w", err)
		}
		if err := os.WriteFile(fullAbs, data, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", fullAbs, err)
		}
		if err := os.WriteFile(thumbAbs, data, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", thumbAbs, err)
		}
		written++
	}
	return written, nil
}

// encodeArtworkRow builds a row for the artwork table. The row is the
// simple "genres/labels" shape: id (uint32 LE) + DeviceSQL string of
// the JPEG path. The path is the USB-relative one a CDJ resolves via
// its mounted root.
func encodeArtworkRow(id uint32, path string) []byte {
	pathBytes := encodeString(path)
	row := make([]byte, 4+len(pathBytes))
	le32put(row, 0, id)
	copy(row[4:], pathBytes)
	return row
}

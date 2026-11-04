// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"bytes"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/pdb"
)

// sameFileEnds is how much of each end of two files of the same size must
// match for keepOnStick to take them as one file.
const sameFileEnds = 64 << 10

// keepOnStick finds the tracks of a merge export that the stick already
// holds (a file with the same bytes as the track's source) and points them at
// that file instead of laying them out and copying them again. It returns the
// tracks still to lay out.
//
// rekordbox does the same: a track already on a stick keeps its file and ID
// when it is exported again. Laying it out again instead gives it a new path
// whenever its tags changed, so it is copied twice and the stick's playlists
// keep the old copy. Files are matched by size, then by their first and last
// sameFileEnds bytes.
func keepOnStick(tracks []*pdb.Track, destDir string, existing []*pdb.Track) []*pdb.Track {
	bySize := map[int64][]*pdb.Track{}
	for _, e := range existing {
		if info, err := os.Stat(stickFile(destDir, e)); err == nil && info.Mode().IsRegular() {
			bySize[info.Size()] = append(bySize[info.Size()], e)
		}
	}
	claimed := map[*pdb.Track]bool{}
	var rest []*pdb.Track
	for _, t := range tracks {
		e := findOnStick(t, destDir, bySize, claimed)
		if e == nil {
			rest = append(rest, t)
			continue
		}
		claimed[e] = true
		t.FilePath, t.FileName = e.FilePath, path.Base(e.FilePath)
		if t.DateAdded == "" {
			t.DateAdded = e.DateAdded
		}
	}
	if kept := len(tracks) - len(rest); kept > 0 {
		dlog.Infof("export: merge: %d tracks already on the stick keep their files", kept)
	}
	return rest
}

// findOnStick returns the unclaimed stick track whose file has t's source
// bytes, or nil. A track the export re-encodes never matches: the stick
// holds the re-encode, not the source.
func findOnStick(t *pdb.Track, destDir string, bySize map[int64][]*pdb.Track, claimed map[*pdb.Track]bool) *pdb.Track {
	if t.NeedsReencode {
		return nil
	}
	info, err := os.Stat(t.FilePath)
	if err != nil {
		return nil
	}
	for _, e := range bySize[info.Size()] {
		if !claimed[e] && sameEnds(t.FilePath, stickFile(destDir, e), info.Size()) {
			return e
		}
	}
	return nil
}

// stickFile is the file a stick track's row points at.
func stickFile(destDir string, t *pdb.Track) string {
	return filepath.Join(destDir, filepath.FromSlash(t.FilePath))
}

// sameEnds reports whether two files of the given size have the same first
// and last sameFileEnds bytes (all of them, for a file that short).
func sameEnds(a, b string, size int64) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	n := min(size, sameFileEnds)
	for _, off := range []int64{0, size - n} {
		ba, bb := make([]byte, n), make([]byte, n)
		if _, err := fa.ReadAt(ba, off); err != nil && err != io.EOF {
			return false
		}
		if _, err := fb.ReadAt(bb, off); err != nil && err != io.EOF {
			return false
		}
		if !bytes.Equal(ba, bb) {
			return false
		}
	}
	return true
}

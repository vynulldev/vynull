// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/pdb"
)

// TestMergeRenumbersPlaylistEntries merges an export whose library IDs clash
// with the stick's existing rows. The merge has to renumber those tracks, and
// the new playlist must follow them: before the fix it kept the old numbers and
// played the stick's own tracks 1 and 2 instead, and the renumbering could hand
// a track the number another new track had already kept.
func TestMergeRenumbersPlaylistEntries(t *testing.T) {
	dir := t.TempDir()
	existing := []*pdb.Track{
		{ID: 1, Title: "Stick A", FilePath: "/Contents/a.mp3"},
		{ID: 2, Title: "Stick B", FilePath: "/Contents/b.mp3"},
		{ID: 3, Title: "Stick C", FilePath: "/Contents/c.mp3"},
	}
	if err := pdb.GenerateWithOptions(existing, SinglePlaylist("Old", []uint32{1, 2, 3}), nil, nil, dir); err != nil {
		t.Fatal(err)
	}

	added := []*pdb.Track{
		{ID: 5, Title: "New X", FilePath: "/Contents/x.mp3"}, // free number: kept
		{ID: 1, Title: "New Y", FilePath: "/Contents/y.mp3"}, // taken by Stick A: renumbered
		{ID: 2, Title: "New Z", FilePath: "/Contents/z.mp3"}, // taken by Stick B: renumbered, but not to X's 5
		{ID: 9, Title: "Stick C again", FilePath: "/Contents/c.mp3"},
	}
	tracks, playlists := mergeWithExisting(filepath.Join(dir, "PIONEER", "rekordbox", "export.pdb"),
		added, SinglePlaylist("New", []uint32{5, 1, 2, 9}))

	byID := map[uint32]string{}
	for _, tr := range tracks {
		if other, dup := byID[tr.ID]; dup {
			t.Fatalf("track ID %d used twice: %q and %q", tr.ID, other, tr.Title)
		}
		byID[tr.ID] = tr.Title
	}
	var got []string
	for _, p := range playlists {
		if p.Name == "New" {
			for _, id := range p.TrackIDs {
				got = append(got, byID[id])
			}
		}
	}
	want := []string{"New X", "New Y", "New Z", "Stick C again"}
	if len(got) != len(want) {
		t.Fatalf(`playlist "New" plays %q, want %q`, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf(`playlist "New" plays %q, want %q`, got, want)
		}
	}
}

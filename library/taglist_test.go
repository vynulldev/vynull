// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"slices"
	"testing"
)

// The tag list keeps its order, adds a track at its end once, never adds a
// track the library doesn't hold, and survives a reload from disk.
func TestTagListEdits(t *testing.T) {
	dir := t.TempDir()
	lib := New()
	lib.SetDBPath(filepath.Join(dir, "library.json"))
	for id := uint32(1); id <= 4; id++ {
		lib.AddTrack(&Track{ID: id, FilePath: filepath.Join("/music", string(rune('a'+id))+".mp3")})
	}
	want := func(lib *Library, ids ...uint32) {
		t.Helper()
		if got := lib.TagList(); !slices.Equal(got, ids) {
			t.Fatalf("tag list %v, want %v", got, ids)
		}
		for id := uint32(1); id <= 5; id++ {
			if lib.InTagList(id) != slices.Contains(ids, id) {
				t.Fatalf("InTagList(%d) = %v with list %v", id, !slices.Contains(ids, id), ids)
			}
		}
	}

	for _, id := range []uint32{3, 1, 4} {
		if !lib.SetTagged(id, true) {
			t.Fatalf("adding %d: no change", id)
		}
	}
	if lib.SetTagged(1, true) || lib.SetTagged(5, true) || lib.SetTagged(2, false) {
		t.Fatal("adding a listed or unknown track, or removing an unlisted one, changed the list")
	}
	want(lib, 3, 1, 4)
	if !lib.SetTagged(1, false) || !lib.SetTagged(1, true) {
		t.Fatal("removing and re-adding 1: no change")
	}
	want(lib, 3, 4, 1) // re-added at the end, as on rekordbox

	reloaded := New()
	reloaded.SetDBPath(filepath.Join(dir, "library.json"))
	want(reloaded, 3, 4, 1)

	if lib.RemoveTracks([]uint32{4}) != 1 {
		t.Fatal("track 4 not removed")
	}
	want(lib, 3, 1)
	if !lib.ClearTagList() || lib.ClearTagList() {
		t.Fatal("clearing: want one change, then none")
	}
	want(lib)
	reloaded = New()
	reloaded.SetDBPath(filepath.Join(dir, "library.json"))
	want(reloaded)
}

// An import takes rekordbox's tag list in its order, and leaves the
// library's own list alone when the dump has none.
func TestImportTagList(t *testing.T) {
	lib := New()
	dump := &MasterDBDump{
		Tracks: []masterDBTrack{
			{ID: "101", Title: "One", FilePath: "/music/one.mp3"},
			{ID: "102", Title: "Two", FilePath: "/music/two.mp3"},
			{ID: "103", Title: "Three", FilePath: "/music/three.mp3"},
		},
		TagList: []string{"103", "999", "101"},
	}
	if _, err := ImportMasterDBDump(lib, dump); err != nil {
		t.Fatal(err)
	}
	three, one := lib.TrackByPath("/music/three.mp3"), lib.TrackByPath("/music/one.mp3")
	if got := lib.TagList(); !slices.Equal(got, []uint32{three.ID, one.ID}) {
		t.Fatalf("tag list %v, want [%d %d] (unknown content left out)", got, three.ID, one.ID)
	}

	dump.TagList = nil
	if _, err := ImportMasterDBDump(lib, dump); err != nil {
		t.Fatal(err)
	}
	if got := lib.TagList(); !slices.Equal(got, []uint32{three.ID, one.ID}) {
		t.Fatalf("after an import without a tag list: %v", got)
	}
}

// A library built from a music dir keeps its tag list beside its overlay of
// edits, so the list survives the rebuild at the next start.
func TestTagListMusicDir(t *testing.T) {
	dir, music := t.TempDir(), "/music"
	build := func() *Library {
		lib := New()
		for id := uint32(1); id <= 3; id++ {
			lib.AddTrack(&Track{ID: id, FilePath: filepath.Join(music, string(rune('a'+id))+".mp3")})
		}
		lib.UseOverlay(filepath.Join(dir, "music-dir-edits.json"), music)
		return lib
	}
	lib := build()
	lib.SetTagged(3, true)
	lib.SetTagged(1, true)
	lib.Close()
	if got := build().TagList(); !slices.Equal(got, []uint32{3, 1}) {
		t.Fatalf("tag list after a rebuild %v, want [3 1]", got)
	}
}

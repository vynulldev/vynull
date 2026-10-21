// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func ids(ts []*library.Track) []uint32 {
	out := make([]uint32, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func newLib(tracks ...*library.Track) *library.Library {
	lib := library.New()
	for _, t := range tracks {
		lib.AddTrackBulk(t)
	}
	lib.FinalizeBulk()
	return lib
}

// An update is saved and moves the revision; reloading shows it.
func TestUpdateSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	lib := library.New()
	lib.SetDBPath(path)
	lib.AddTrack(&library.Track{ID: 1, Title: "Old", FilePath: "/a.mp3"})
	rev := lib.Rev()
	if err := lib.Update(1, func(tr *library.Track) error { tr.Title, tr.Rating = "New", 4; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := lib.Track(1); got.Title != "New" || got.Rating != 4 || lib.Rev() == rev {
		t.Errorf("after update: %q rating %d, rev %d (was %d)", got.Title, got.Rating, lib.Rev(), rev)
	}
	lib.Flush()
	reloaded := library.New()
	reloaded.SetDBPath(path)
	if got := reloaded.Track(1); got == nil || got.Title != "New" {
		t.Errorf("reloaded track %+v, want the update saved", got)
	}
}

// When fn fails, nothing it changed sticks (not even inside Related), the
// error comes back, and the revision stays put.
func TestUpdateErrorLeavesTrack(t *testing.T) {
	lib := newLib(&library.Track{ID: 1, Title: "Keep", Artist: "A", FilePath: "/a.mp3", Related: []uint32{7}})
	rev := lib.Rev()
	bad := errors.New("bad edit")
	err := lib.Update(1, func(tr *library.Track) error {
		tr.Title, tr.Artist, tr.FilePath = "Lost", "B", "/b.mp3"
		tr.Related[0] = 9
		return bad
	})
	got := lib.Track(1)
	if !errors.Is(err, bad) || got.Title != "Keep" || got.Artist != "A" || got.FilePath != "/a.mp3" || got.Related[0] != 7 {
		t.Errorf("err %v, track %+v; want the error and the track unchanged", err, got)
	}
	if lib.Rev() != rev || lib.TrackByPath("/a.mp3") != got || len(lib.TracksByArtist("B")) != 0 {
		t.Errorf("a failed update changed the revision or the indexes")
	}
	if err := lib.Update(99, func(*library.Track) error { return nil }); !errors.Is(err, library.ErrTrackNotFound) {
		t.Errorf("unknown track: %v, want ErrTrackNotFound", err)
	}
	lib.Update(1, func(tr *library.Track) error { tr.ID = 5; return nil })
	if lib.Track(1) == nil || lib.Track(5) != nil {
		t.Errorf("Update changed the track's ID")
	}
}

// Changing a track's artist, album or genre moves it between the indexes,
// keeping each index in the order tracks were added, and the sorted name
// lists follow, including a change of case alone.
func TestUpdateReindexesNames(t *testing.T) {
	lib := newLib(
		&library.Track{ID: 1, Artist: "X", Album: "One", Genre: "House"},
		&library.Track{ID: 2, Artist: "X", Album: "One", Genre: "House"},
		&library.Track{ID: 3, Artist: "Y", Album: "Two", Genre: "Techno"},
	)
	lib.Update(2, func(tr *library.Track) error { tr.Artist, tr.Album, tr.Genre = "Y", "Two", "Techno"; return nil })
	if got := ids(lib.TracksByArtist("x")); !slices.Equal(got, []uint32{1}) {
		t.Errorf("artist X: %v, want [1]", got)
	}
	if got := ids(lib.TracksByArtist("Y")); !slices.Equal(got, []uint32{2, 3}) {
		t.Errorf("artist Y: %v, want [2 3] (library order)", got)
	}
	if got := ids(lib.TracksByAlbum("two")); !slices.Equal(got, []uint32{2, 3}) {
		t.Errorf("album Two: %v, want [2 3]", got)
	}
	if got := ids(lib.TracksByGenre("techno")); !slices.Equal(got, []uint32{2, 3}) {
		t.Errorf("genre Techno: %v, want [2 3]", got)
	}
	lib.Update(1, func(tr *library.Track) error { tr.Artist = ""; return nil })
	if got := lib.Artists(); !slices.Equal(got, []string{"Y"}) {
		t.Errorf("artists after clearing the last X: %v, want [Y]", got)
	}
	lib.Update(2, func(tr *library.Track) error { tr.Artist = "y"; return nil })
	if got := lib.Artists(); !slices.Equal(got, []string{"y"}) || !slices.Equal(ids(lib.TracksByArtist("Y")), []uint32{2, 3}) {
		t.Errorf("after a case change: artists %v, artist Y %v; want [y] and [2 3]", got, ids(lib.TracksByArtist("Y")))
	}
}

// The path index follows path changes and removals, and with several tracks
// on one path TrackByPath returns the one added first, as the old linear
// search did.
func TestTrackByPathIndex(t *testing.T) {
	lib := newLib(
		&library.Track{ID: 1, FilePath: "/a.mp3"},
		&library.Track{ID: 2, FilePath: "/a.mp3"},
		&library.Track{ID: 3, FilePath: "/c.mp3"},
	)
	at := func(path string) uint32 {
		if tr := lib.TrackByPath(path); tr != nil {
			return tr.ID
		}
		return 0
	}
	if at("/a.mp3") != 1 || at("/c.mp3") != 3 || at("/none.mp3") != 0 {
		t.Fatalf("start: /a %d, /c %d, /none %d", at("/a.mp3"), at("/c.mp3"), at("/none.mp3"))
	}
	lib.Update(1, func(tr *library.Track) error { tr.FilePath = "/b.mp3"; return nil })
	if at("/a.mp3") != 2 || at("/b.mp3") != 1 {
		t.Errorf("after moving 1: /a %d, /b %d; want 2, 1", at("/a.mp3"), at("/b.mp3"))
	}
	lib.Update(1, func(tr *library.Track) error { tr.FilePath = "/a.mp3"; return nil })
	if at("/a.mp3") != 1 || at("/b.mp3") != 0 {
		t.Errorf("after moving 1 back: /a %d, /b %d; want 1 (added first), 0", at("/a.mp3"), at("/b.mp3"))
	}
	lib.RemoveTrack(1)
	if at("/a.mp3") != 2 {
		t.Errorf("after removing 1: /a %d, want 2", at("/a.mp3"))
	}
	lib.RemapPaths("/a", "/z")
	if at("/a.mp3") != 0 || at("/z.mp3") != 2 {
		t.Errorf("after remapping: /a %d, /z %d; want 0, 2", at("/a.mp3"), at("/z.mp3"))
	}
}

// Slices handed out by Tracks and TracksBy* keep their contents when a track
// is removed, and appending to one can't reach into the library.
func TestSnapshotsSurviveChanges(t *testing.T) {
	lib := newLib(
		&library.Track{ID: 1, Artist: "X"},
		&library.Track{ID: 2, Artist: "X"},
		&library.Track{ID: 3, Artist: "X"},
	)
	all, byArtist := lib.Tracks(), lib.TracksByArtist("X")
	lib.RemoveTrack(1)
	if !slices.Equal(ids(all), []uint32{1, 2, 3}) || !slices.Equal(ids(byArtist), []uint32{1, 2, 3}) {
		t.Errorf("held slices changed: %v, %v; want [1 2 3] for both", ids(all), ids(byArtist))
	}
	_ = append(lib.Tracks(), &library.Track{ID: 99})
	lib.AddTrack(&library.Track{ID: 4, Artist: "X"})
	if got := ids(lib.Tracks()); !slices.Equal(got, []uint32{2, 3, 4}) {
		t.Errorf("tracks: %v, want [2 3 4]", got)
	}
}

// UpdateBulk reindexes at once but leaves the name lists and the save to
// FinalizeBulk.
func TestUpdateBulk(t *testing.T) {
	lib := newLib(&library.Track{ID: 1, Artist: "X"})
	rev := lib.Rev()
	if err := lib.UpdateBulk(1, func(tr *library.Track) error { tr.Artist = "Y"; return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(lib.TracksByArtist("Y")), []uint32{1}) || lib.Rev() != rev || !slices.Equal(lib.Artists(), []string{"X"}) {
		t.Errorf("before FinalizeBulk: artist Y %v, rev moved %v, artists %v", ids(lib.TracksByArtist("Y")), lib.Rev() != rev, lib.Artists())
	}
	lib.FinalizeBulk()
	if lib.Rev() == rev || !slices.Equal(lib.Artists(), []string{"Y"}) {
		t.Errorf("after FinalizeBulk: rev moved %v, artists %v", lib.Rev() != rev, lib.Artists())
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// listLabels asks for a list and returns its rows' labels, [ALL] left out.
func listLabels(h *Handler, typ uint16, args ...uint32) []string {
	a := []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(0)}
	for _, v := range args {
		a = append(a, proto.ArgI32(v))
	}
	h.Handle(&proto.DBMessage{TxID: 1, Type: typ, Args: a})
	var labels []string
	for _, m := range h.pendingItems {
		if m.ID != 0xffffffff {
			labels = append(labels, m.Label1)
		}
	}
	return labels
}

// TestAlbumTracksByTrackNumber checks that an album's tracks are listed in
// track-number order, then title, as rekordbox lists an album; an artist's
// tracks across all albums stay in title order.
func TestAlbumTracksByTrackNumber(t *testing.T) {
	lib := library.New()
	for _, tr := range []struct {
		title string
		no    int
	}{{"Opening", 1}, {"Westward", 6}, {"Tide Line", 2}, {"Cobalt", 3}, {"Bonus", 3}} {
		lib.AddTrack(&library.Track{Title: tr.title, Artist: "Artist", Album: "Album", TrackNum: tr.no})
	}
	h := &Handler{lib: lib}
	album, artist := h.catID("album", "Album"), h.catID("artist", "Artist")
	want := []string{"Opening", "Tide Line", "Bonus", "Cobalt", "Westward"}
	if got := listLabels(h, proto.DBMsgGetByAlbum, album); !slices.Equal(got, want) {
		t.Errorf("ALBUM tracks %q, want %q", got, want)
	}
	if got := listLabels(h, 0x1202, artist, album); !slices.Equal(got, want) {
		t.Errorf("artist's album tracks %q, want %q", got, want)
	}
	titles := []string{"Bonus", "Cobalt", "Opening", "Tide Line", "Westward"}
	if got := listLabels(h, 0x1202, artist, 0xffffffff); !slices.Equal(got, titles) {
		t.Errorf("artist's tracks on all albums %q, want title order %q", got, titles)
	}
}

// TestNameListsCollated checks the category lists against the patterns of
// the lists a CDJ-2000NXS2 got from rekordbox 7 (with made-up names), which
// keep spaces and punctuation and sort them after letters and digits.
func TestNameListsCollated(t *testing.T) {
	lib := library.New()
	for _, tr := range []library.Track{
		{Artist: "Rio & Vale, Kade, Lumen", Genre: "A Side", Label: " Aurora (Sublabel)", FilePath: "/m/click.wav"},
		{Artist: "Rio & Vale with Nell", Genre: "Ambient & Drone", Label: "45 Degrees", FilePath: "/m/Demo 1.mp3"},
		{Artist: "Rio & Vale", Genre: "Ambient", Label: "Axis Deep", FilePath: "/m/beat.wav"},
		{Artist: "Ria Moss", Genre: "Afro Funk", Label: "A sound of Mars", FilePath: "/m/cosmos.mp3"},
	} {
		lib.AddTrack(&tr)
	}
	h := &Handler{lib: lib}
	for _, c := range []struct {
		name string
		typ  uint16
		want []string
	}{
		{"ARTIST", proto.DBMsgGetArtists, []string{"Ria Moss", "Rio & Vale", "Rio & Vale with Nell", "Rio & Vale, Kade, Lumen"}},
		{"GENRE", 0x1001, []string{"Afro Funk", "Ambient", "Ambient & Drone", "A Side"}},
		{"LABEL", 0x100a, []string{"Axis Deep", "A sound of Mars", "45 Degrees", " Aurora (Sublabel)"}},
		{"FILENAME", 0x1013, []string{"beat.wav", "click.wav", "cosmos.mp3", "Demo 1.mp3"}},
	} {
		if got := listLabels(h, c.typ); !slices.Equal(got, c.want) {
			t.Errorf("%s list %q, want %q", c.name, got, c.want)
		}
	}
}

// TestNamesCollatingEqual checks that names that collate equal (differing
// only in case) come out in ID order, not map order.
func TestNamesCollatingEqual(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{Label: "A Sound of Mars"})
	lib.AddTrack(&library.Track{Label: "A sound of Mars"})
	h := &Handler{lib: lib}
	a, b := h.catID("label", "A Sound of Mars"), h.catID("label", "A sound of Mars")
	want := []string{"A Sound of Mars", "A sound of Mars"}
	if b < a {
		want = []string{"A sound of Mars", "A Sound of Mars"}
	}
	for range 20 {
		if got := listLabels(h, 0x100a); !slices.Equal(got, want) {
			t.Fatalf("LABEL list %q, want %q (ID order)", got, want)
		}
	}
}

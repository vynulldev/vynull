// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// The contexts a CDJ-2000NXS2 sent rekordbox in the TAG LIST captures:
// player 2, slot 4, track type 1, at menu location 6 (the tag list), 8 (the
// track it tags) and 1 (the browse list it tags from).
const (
	tagListCtx = 0x02060401
	tagCtx     = 0x02080401
	browseCtx  = 0x02010401
)

func tagListHandler() *Handler {
	lib := library.NewLibrary([]*library.Track{
		{ID: 10001, Title: "Alpha One", FilePath: "/music/a/one.mp3"},
		{ID: 10002, Title: "Alpha Two", FilePath: "/music/a/two.mp3"},
		{ID: 10003, Title: "Alpha Three", FilePath: "/music/a/three.mp3"},
	}, nil)
	return &Handler{lib: lib}
}

// tagListRows asks for the tag list and returns its rows' (track ID,
// position) pairs, checking each row's argument 7 is rekordbox's 0x01000001:
// the context track type and the tag-list bit.
func tagListRows(t *testing.T, h *Handler, sort uint32) [][2]uint32 {
	t.Helper()
	_, rows := ask(t, h, 0x100f, tagListCtx, sort)
	var got [][2]uint32
	for _, r := range rows {
		if f := r[7].Int(); f != 0x01000001 {
			t.Errorf("tag list row %d: arg 7 %#x, want 0x1000001", r[1].Int(), f)
		}
		got = append(got, [2]uint32{r[1].Int(), r[9].Int()})
	}
	return got
}

// tagged lists the tracks whose browse rows carry the tag-list bit.
func tagged(t *testing.T, h *Handler) []uint32 {
	t.Helper()
	_, rows := ask(t, h, 0x1004, browseCtx, 0)
	var got []uint32
	for _, r := range rows {
		if r[7].Int()&0x1 != 0 {
			got = append(got, r[1].Int())
		}
	}
	slices.Sort(got)
	return got
}

// change sends a TAG LIST change and checks rekordbox's 0x4000 [kind, 0].
func change(t *testing.T, h *Handler, kind uint16, args ...proto.DBArg) {
	t.Helper()
	r := h.Handle(&proto.DBMessage{TxID: 7, Type: kind, Args: args})
	if len(r) != 1 || r[0].Type != proto.DBMsgSuccess || len(r[0].Args) != 2 || r[0].Args[0].Int() != uint32(kind) || r[0].Args[1].Int() != 0 {
		t.Fatalf("%#04x reply %+v, want 4000 [%#04x, 0]", kind, r, kind)
	}
}

// The deck reads the tag list with 0x100f at location 6, in list order with
// each row's position in argument 9; a sort reorders the rows but keeps
// their positions. rekordbox answers the same at another location.
func TestTagListServed(t *testing.T) {
	h := tagListHandler()
	h.lib.SetTagList([]uint32{10003, 10001})
	if got := tagListRows(t, h, 0); !slices.Equal(got, [][2]uint32{{10003, 1}, {10001, 2}}) {
		t.Errorf("tag list %v, want [10003 1] [10001 2]", got)
	}
	if got := tagListRows(t, h, 1); !slices.Equal(got, [][2]uint32{{10001, 2}, {10003, 1}}) { // ALPHABET
		t.Errorf("tag list by title %v, want [10001 2] [10003 1]", got)
	}
	if _, rows := ask(t, h, 0x100f, browseCtx, 0); len(rows) != 2 || rows[0][1].Int() != 10003 {
		t.Errorf("0x100f at location 1: %v, want the tag list", rows)
	}
}

// A deck adds a track to the end of the tag list and removes it with 0x3002,
// and removes every track with 0x3202; browse rows mark the listed tracks.
func TestTagListEditsFromDeck(t *testing.T) {
	h := tagListHandler()
	change(t, h, 0x3002, proto.ArgI32(tagCtx), proto.ArgI32(10002), proto.ArgI32(1))
	change(t, h, 0x3002, proto.ArgI32(tagCtx), proto.ArgI32(10001), proto.ArgI32(1))
	if got := tagListRows(t, h, 0); !slices.Equal(got, [][2]uint32{{10002, 1}, {10001, 2}}) {
		t.Errorf("after two adds: %v", got)
	}
	if got := tagged(t, h); !slices.Equal(got, []uint32{10001, 10002}) {
		t.Errorf("browse rows marked %v, want [10001 10002]", got)
	}
	change(t, h, 0x3002, proto.ArgI32(tagCtx), proto.ArgI32(10002), proto.ArgI32(0))
	change(t, h, 0x3002, proto.ArgI32(tagCtx), proto.ArgI32(10002), proto.ArgI32(1))
	if got := tagListRows(t, h, 0); !slices.Equal(got, [][2]uint32{{10001, 1}, {10002, 2}}) {
		t.Errorf("after removing and re-adding 10002: %v, want it at the end", got)
	}
	change(t, h, 0x3202, proto.ArgI32(tagListCtx))
	if total, _ := ask(t, h, 0x100f, tagListCtx, 0); total != 0 {
		t.Errorf("after 0x3202 the tag list holds %d tracks", total)
	}
	if got := tagged(t, h); len(got) != 0 {
		t.Errorf("browse rows still marked %v", got)
	}
}

type fakePlaylists struct {
	created [][]any
}

func (f *fakePlaylists) Children(uint32) []PlaylistEntry { return nil }
func (f *fakePlaylists) Tracks(uint32) []uint32          { return nil }
func (f *fakePlaylists) HistoryFolderID() uint32         { return 0 }
func (f *fakePlaylists) CreatePlaylist(name string, parentID uint32, ids []uint32) error {
	f.created = append(f.created, []any{name, parentID, ids})
	return nil
}

// utf16Arg is a string argument as the server's reader hands it over: the
// raw UTF-16BE bytes, with the NUL.
func utf16Arg(s string) proto.DBArg {
	u := utf16.Encode([]rune(s + "\x00"))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.BigEndian.PutUint16(b[2*i:], c)
	}
	return proto.DBArg{Tag: proto.ArgString, Str: string(b)}
}

// 0x3102 makes a playlist of the tag list under the name the deck sends,
// keeps the list, and reports the change so the link can be told.
func TestTagListToPlaylist(t *testing.T) {
	h := tagListHandler()
	pl := &fakePlaylists{}
	changed := 0
	h.playlists, h.onPlaylistsChanged = pl, func() { changed++ }
	h.lib.SetTagList([]uint32{10003, 10001})
	change(t, h, 0x3102, proto.ArgI32(tagListCtx), proto.ArgI32(18), utf16Arg("TAG LIST"), proto.ArgI32(0))
	if len(pl.created) != 1 || pl.created[0][0] != "TAG LIST" || pl.created[0][1] != uint32(0) ||
		!slices.Equal(pl.created[0][2].([]uint32), []uint32{10003, 10001}) || changed != 1 {
		t.Fatalf("created %v, %d change notices; want one TAG LIST playlist of [10003 10001] and one notice", pl.created, changed)
	}
	if got := tagListRows(t, h, 0); len(got) != 2 {
		t.Errorf("tag list after making the playlist: %v, want it kept", got)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

func searchTrackHandler() *Handler {
	lib := library.NewLibrary([]*library.Track{
		{ID: 1, Title: "Precomposed Éclair", Artist: "Alpha Artist", Key: "Am", FilePath: "/m/text-01.wav"},
		{ID: 2, Title: "Emoji 🙂 Search", Artist: "Beta Artist", Remixer: "Fixture Remixer", FilePath: "/m/text-02.wav"},
		{ID: 3, Title: "Another", Artist: "Beta Artist", Album: "Fixture Album", FilePath: "/m/fixture-03.wav"},
		{ID: 4, Title: "Zeta", Artist: "Gamma", FilePath: "/m/z.wav"},
	}, nil)
	return &Handler{lib: lib, extended: true}
}

// searchTrack sends 0x1500 [ctx, sort, declared length, query] (the query's
// declared length is its UTF-16 bytes with the NUL when declared is -1).
func searchTrack(h *Handler, sort uint32, declared int, query string) []*proto.DBMessage {
	q := utf16Arg(query)
	if declared < 0 {
		declared = len(q.Str)
	}
	return h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1500, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(sort), proto.ArgI32(uint32(declared)), q}})
}

// searchTrackRows renders a Search Track result.
func searchTrackRows(t *testing.T, h *Handler, sort uint32, query string) [][]proto.DBArg {
	t.Helper()
	r := searchTrack(h, sort, -1, query)
	total := r[0].Args[1].Int()
	if total == 0 {
		return nil
	}
	page := h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0)}})
	var rows [][]proto.DBArg
	for _, m := range page[1 : len(page)-1] {
		rows = append(rows, m.Args)
	}
	return rows
}

// Search Track returns only tracks: those whose title, file name, artists
// or album match, as SEARCH's content rows (key alone, item type 0x0f04),
// in title order, with the context type only under an explicit sort.
func TestSearchTrack(t *testing.T) {
	h := searchTrackHandler()
	if got := ids(searchTrackRows(t, h, 0, "fixture")); !slices.Equal(got, []uint32{3, 2}) {
		t.Errorf("FIXTURE: %v, want [3 2] (album, remixer and file name; title order)", got)
	}
	if got := ids(searchTrackRows(t, h, 0, "beta search")); got != nil {
		t.Errorf("BETA SEARCH: %v, want none (every token has to be in one field)", got)
	}
	if got := ids(searchTrackRows(t, h, 0, "éclair")); got != nil {
		t.Errorf("éclair: %v, want none (non-ASCII letters aren't folded)", got)
	}
	rows := searchTrackRows(t, h, 0, "Éclair")
	if len(rows) != 1 || rows[0][6].Int() != 0x0f04 || rows[0][5].Str != "Am" || rows[0][7].Int() != 0 {
		t.Fatalf("Éclair row %v, want one key-alone 0x0f04 row without the context type", rows)
	}
	if rows := searchTrackRows(t, h, 2, "fixture"); len(rows) != 2 || rows[0][7].Int()>>24 != 1 {
		t.Errorf("explicit sort rows %v, want the context type in argument 7", rows)
	}
	// "Emoji 🙂 Search" is 15 UTF-16 units: 32 bytes with the NUL.
	if rows := searchTrackRows(t, h, 0, "emoji"); len(rows) != 1 || rows[0][2].Int() != 32 {
		t.Errorf("emoji title length %v, want 32", rows)
	}
}

// Requests rekordbox answers specially: an unavailable sort, a short or zero
// declared length, no query at all, and SEARCH's fifth argument.
func TestSearchTrackRequests(t *testing.T) {
	h := searchTrackHandler()
	for _, sort := range []uint32{18, 127, 128, 255} {
		if r := searchTrack(h, sort, -1, "fixture"); len(r) != 1 || r[0].Type != proto.DBMsgSuccess || r[0].Args[1].Int() != 0xffffffff {
			t.Errorf("sort %d: %+v, want total 0xffffffff", sort, r)
		}
	}
	if r := searchTrack(h, 256, -1, "fixture"); r[0].Args[1].Int() != 2 {
		t.Errorf("sort 256 (sort 0): total %d, want 2", r[0].Args[1].Int())
	}
	for _, declared := range []int{0, 4} {
		if r := searchTrack(h, 1, declared, "fixture"); len(r) != 1 || r[0].Type != 0x0100 || len(r[0].Args) != 0 {
			t.Errorf("declared length %d: %+v, want an argumentless 0x0100", declared, r)
		}
	}
	r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1500, Args: []proto.DBArg{proto.ArgI32(rootCtx), proto.ArgI32(1), proto.ArgI32(2)}})
	if r[0].Type != proto.DBMsgSuccess || r[0].Args[1].Int() != 0 {
		t.Errorf("no query: %+v, want an empty list", r)
	}
	q := utf16Arg("fixture")
	r = h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1500, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(0), proto.ArgI32(uint32(len(q.Str))), q, proto.ArgI32(0)}})
	if r[0].Args[1].Int() != 2 {
		t.Errorf("five arguments: total %d, want 2", r[0].Args[1].Int())
	}
}

// The default sort lists at most 5,000 tracks; an explicit sort lists all.
func TestSearchTrackCeiling(t *testing.T) {
	var tracks []*library.Track
	for i := range searchTrackCeiling + 5 {
		tracks = append(tracks, &library.Track{ID: uint32(i + 1), Title: fmt.Sprintf("Ceiling %04d", i+1), FilePath: fmt.Sprintf("/m/%d.wav", i)})
	}
	h := &Handler{lib: library.NewLibrary(tracks, nil)}
	if got := searchTrack(h, 0, -1, "ceiling")[0].Args[1].Int(); got != searchTrackCeiling {
		t.Errorf("default sort total %d, want %d", got, searchTrackCeiling)
	}
	if got := searchTrack(h, 17, -1, "ceiling")[0].Args[1].Int(); got != searchTrackCeiling+5 {
		t.Errorf("sort 17 total %d, want %d", got, searchTrackCeiling+5)
	}
}

func TestUTF16Units(t *testing.T) {
	for s, want := range map[string]int{"": 0, "Am": 2, "Éclair": 6, "🙂": 2, "a🙂b": 4} {
		if got := utf16Units(s); got != want {
			t.Errorf("utf16Units(%q) = %d, want %d", s, got, want)
		}
	}
}

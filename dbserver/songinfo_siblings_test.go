// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

func siblingsFixture() *Handler {
	lib := library.New()
	lib.AddTrack(&library.Track{
		ID: 10001, Title: "Alpha One", Artist: "Alpha Artist", Album: "Album One", Genre: "Fixture House",
		Label: "Fixture Label One", Key: "Am", BPM: 120, Comment: "comment-1", FileTypeID: 1,
		FilePath: "/Z:/tracks/link-export-fixture/fixture-01.wav", Duration: library.DurationSec(59 * time.Second),
	})
	lib.AddTrack(&library.Track{ID: 10002, Title: "Local", Key: "Am", FilePath: "/music/local.flac"})
	lib.RegisterCategoryID("key", "Am", 5001)
	lib.RegisterCategoryID("genre", "Fixture House", 3001)
	lib.RegisterCategoryID("label", "Fixture Label One", 4001)
	lib.RegisterCategoryID("artist", "Alpha Artist", 1001)
	lib.RegisterCategoryID("album", "Album One", 2001)
	return &Handler{lib: lib}
}

// Play Song Info (0x2102) carries the track's comment, rekordbox's key ID,
// and a Windows drive path as rekordbox sends it (link-spec
// SONG_INFO_SIBLINGS_ORACLE, "Play Song Info rows").
func TestPlaySongInfoRows(t *testing.T) {
	h := siblingsFixture()
	h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2102, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(10001)}})
	rows := h.pendingItems
	if len(rows) != 7 {
		t.Fatalf("%d rows, want 7", len(rows))
	}
	if rows[3].Label1 != "comment-1" {
		t.Errorf("comment row = %q, want comment-1", rows[3].Label1)
	}
	if rows[4].Label1 != "Z:/tracks/link-export-fixture/fixture-01.wav" {
		t.Errorf("path row = %q, want the drive path without a leading slash", rows[4].Label1)
	}
	if rows[6].ID != 5001 || rows[6].Label1 != "Am" {
		t.Errorf("key row = %d %q, want 5001 Am", rows[6].ID, rows[6].Label1)
	}

	// A local path keeps its leading slash: the deck opens it over NFS.
	h.Handle(&proto.DBMessage{TxID: 2, Type: 0x2102, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(10002)}})
	if got := h.pendingItems[4].Label1; got != "/music/local.flac" {
		t.Errorf("local path row = %q, want /music/local.flac", got)
	}
}

// Delivery Info (0x2602) returns rekordbox's 13 rows in its base order.
func TestDeliveryInfoRows(t *testing.T) {
	h := siblingsFixture()
	resp := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2602, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(10001)}})
	if len(resp) != 1 || resp[0].Args[1].Int() != 13 {
		t.Fatalf("header = %+v, want 13 rows", resp)
	}
	rows := h.pendingItems
	var types []uint32
	for _, r := range rows {
		types = append(types, r.ItemType)
	}
	want := []uint32{0x36, 0x0d, 0x23, 0x0f, 0x12, 0x06, 0x0e, 0x07, 0x37, 0x4f, rows[10].ItemType, 0x02, 0x0b}
	if !slices.Equal(types, want) || rows[10].ID != 10001 {
		t.Fatalf("row types = %x, want rekordbox's base order with the track row 11th", types)
	}
	if k := rows[3]; k.ParentID != 5001 || k.ID != 15 || k.Label2 != "Am" {
		t.Errorf("key row = parent %d, id %d, %q; want 5001, 15, Am", k.ParentID, k.ID, k.Label2)
	}
	if rows[5].ID != 3001 || rows[7].ID != 1001 || rows[11].ID != 2001 || rows[12].ID != 59 {
		t.Errorf("genre/artist/album/length = %d/%d/%d/%d", rows[5].ID, rows[7].ID, rows[11].ID, rows[12].ID)
	}
	if rows[9].ParentID != 10001 || rows[8].ID != 10001 {
		t.Errorf("DeliveryControl/lyricist rows should carry the track ID where rekordbox does")
	}

	resp = h.Handle(&proto.DBMessage{TxID: 2, Type: 0x2602, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(99999)}})
	if resp[0].Args[1].Int() != 0 {
		t.Errorf("missing track: %d rows, want 0", resp[0].Args[1].Int())
	}
}

// Render windows follow rekordbox's normalization (link-spec
// PROTOCOL_REFERENCE, "Render ranges and pagination"), here over its
// eight-row sweep.
func TestRenderWindowNormalization(t *testing.T) {
	h := &Handler{}
	var items []*menuItem
	for i := uint32(1); i <= 8; i++ {
		items = append(items, &menuItem{ID: i, Label1: "row", ItemType: 0x0004})
	}
	req := &proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(0)}}
	render := func(offset, count uint32) []uint32 {
		h.setPendingAll(req, items)
		resp := h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			proto.ArgI32(0x01020401), proto.ArgI32(offset), proto.ArgI32(count), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0)}})
		if resp == nil {
			return nil
		}
		var ids []uint32
		for _, m := range resp {
			if m.Type == proto.DBMsgMenuItem {
				ids = append(ids, m.Args[1].Int())
			}
		}
		return ids
	}
	for _, tc := range []struct {
		offset, count uint32
		want          []uint32
	}{
		{0, 8, []uint32{1, 2, 3, 4, 5, 6, 7, 8}},
		{7, 1, []uint32{8}},
		{0, 0, []uint32{1}},                       // zero count reads one row
		{8, 1, []uint32{8}},                       // at the end: the final row
		{9, 1, []uint32{8}},                       // past the end: the final row
		{7, 10, []uint32{1, 2, 3, 4, 5, 6, 7, 8}}, // overrun larger than the list: all of it
		{6, 3, []uint32{6, 7, 8}},                 // overrun: slid back to end on the last row
	} {
		if got := render(tc.offset, tc.count); !slices.Equal(got, tc.want) {
			t.Errorf("render offset %d count %d = %v, want %v", tc.offset, tc.count, got, tc.want)
		}
	}
	if got := render(0xffffffff, 1); got != nil {
		t.Errorf("render at 0xffffffff = %v, want no reply", got)
	}
}

// Play Song Info and Delivery Info answer malformed arguments as rekordbox
// does (link-spec song-info-sibling-errors): no arguments get no reply, a
// string or blob context and a blob content ID a bare 0x0100, and a string
// content ID a zero-row menu.
func TestMalformedSongInfoRequests(t *testing.T) {
	ctx, track := proto.ArgI32(0x01010301), proto.ArgI32(10001)
	str, blob := proto.ArgStr("wrong-type"), proto.ArgBlob([]byte{1, 2, 3, 4})
	for _, kind := range []uint16{0x2102, 0x2602} {
		rows := map[uint16]uint32{0x2102: 7, 0x2602: 13}[kind]
		for _, c := range []struct {
			name string
			args []proto.DBArg
			want string // "none", "0100", or the menu total
		}{
			{"no arguments", nil, "none"},
			{"string context", []proto.DBArg{str, track}, "0100"},
			{"blob context", []proto.DBArg{blob, track}, "0100"},
			{"blob content ID", []proto.DBArg{ctx, blob}, "0100"},
			{"string content ID", []proto.DBArg{ctx, str}, "0"},
			{"context only", []proto.DBArg{ctx}, "0"},
			{"well formed", []proto.DBArg{ctx, track}, fmt.Sprint(rows)},
			{"an extra argument", []proto.DBArg{ctx, track, proto.ArgI32(0xdeadbeef)}, fmt.Sprint(rows)},
		} {
			r := siblingsFixture().Handle(&proto.DBMessage{TxID: 1, Type: kind, Args: c.args})
			var got string
			switch {
			case len(r) == 0:
				got = "none"
			case r[0].Type == 0x0100 && len(r[0].Args) == 0:
				got = "0100"
			case r[0].Type == proto.DBMsgSuccess && r[0].Args[0].Int() == uint32(kind):
				got = fmt.Sprint(r[0].Args[1].Int())
			default:
				got = fmt.Sprintf("%+v", r[0])
			}
			if got != c.want {
				t.Errorf("%#04x %s: got %s, want %s", kind, c.name, got, c.want)
			}
		}
	}
}

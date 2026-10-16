// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// Display Song Info's category rows carry the category's ID, as rekordbox's
// do: the deck's TRACK INFO jump opens ARTIST, ALBUM and the rest by it. They
// carried the track ID, so the jump opened an empty menu.
func TestDisplaySongInfoCategoryIDs(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 10001, Title: "Alpha One", Artist: "Alpha Artist", Album: "Album One",
		Genre: "Fixture House", Label: "Fixture Label One", Key: "Am", Remixer: "Fixture Remixer",
		OriginalArtist: "Fixture Original"})
	for kind, v := range map[string]struct {
		name string
		id   uint32
	}{
		"artist": {"Alpha Artist", 1001}, "album": {"Album One", 2001}, "genre": {"Fixture House", 3001},
		"label": {"Fixture Label One", 4001}, "key": {"Am", 5001}, "remixer": {"Fixture Remixer", 1003},
		"origartist": {"Fixture Original", 1004},
	} {
		lib.RegisterCategoryID(kind, v.name, v.id)
	}
	h := &Handler{lib: lib}
	h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(10001)}})

	want := map[uint32]struct {
		id    uint32
		label string
	}{
		0x07: {1001, "Alpha Artist"}, 0x02: {2001, "Album One"}, 0x06: {3001, "Fixture House"},
		0x0e: {4001, "Fixture Label One"}, 0x0f: {5001, "Am"}, 0x29: {1003, "Fixture Remixer"},
		0x28: {1004, "Fixture Original"},
	}
	seen := 0
	for _, it := range h.pendingItems {
		if w, ok := want[it.ItemType]; ok {
			seen++
			if it.ID != w.id || it.Label1 != w.label {
				t.Errorf("row %#x = %d %q, want %d %q", it.ItemType, it.ID, it.Label1, w.id, w.label)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of %d category rows", seen, len(want))
	}
}

// 0x3100 returns an ID's position in the list loaded at the request's menu
// location, so a TRACK INFO jump lands on the loaded track; it answered 0
// (the top) for anything but the root menu.
func TestListOffsetForInfoJump(t *testing.T) {
	h := &Handler{}
	var items []*menuItem
	for _, id := range []uint32{11, 22, 1648969, 44} {
		items = append(items, &menuItem{ID: id, ItemType: 0x0004})
	}
	h.setPendingAll(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(0x01010401)}}, items)
	offset := func(id uint32) uint32 {
		return h.Handle(&proto.DBMessage{TxID: 2, Type: 0x3100, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(id)}})[0].Args[1].Int()
	}
	if got := offset(1648969); got != 2 {
		t.Errorf("offset of the loaded track = %d, want 2", got)
	}
	if got := offset(999); got != 0 {
		t.Errorf("offset of an ID not in the list = %d, want 0", got)
	}
}

// Display Song Info follows rekordbox's sixteen-row layout: its order, the
// argument-0 flags, the colour type (0x13 + colour ID), and a title row whose
// second column the render request picks.
func TestDisplaySongInfoLayout(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 10001, Title: "Alpha One", Artist: "Alpha Artist", Album: "Album One",
		Key: "Am", BPM: 120, ColorID: 1, Comment: "comment-1"})
	lib.RegisterCategoryID("artist", "Alpha Artist", 1001)
	lib.RegisterCategoryID("key", "Am", 5001)
	h := &Handler{lib: lib}
	h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(10001)}})
	rows := h.pendingItems

	var types, parents []uint32
	for _, r := range rows[1:] {
		types = append(types, r.ItemType)
		parents = append(parents, r.ParentID)
	}
	wantTypes := []uint32{0x07, 0x02, 0x0b, 0x0d, 0x0f, 0x0a, 0x14, 0x06, 0x2e, 0x23, 0x10, 0x11, 0x0e, 0x28, 0x29}
	wantParents := []uint32{1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 1, 1, 1, 1, 1}
	if !slices.Equal(types, wantTypes) {
		t.Errorf("row types = %x, want %x", types, wantTypes)
	}
	if !slices.Equal(parents, wantParents) {
		t.Errorf("argument 0 = %v, want %v", parents, wantParents)
	}

	title := rows[0]
	render := func(args ...uint32) *menuItem {
		a := []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(0), proto.ArgI32(16)}
		for _, v := range args {
			a = append(a, proto.ArgI32(v))
		}
		return title.songInfoTitle.render(title, &proto.DBMessage{Type: proto.DBMsgRenderMenu, Args: a})
	}
	for _, tc := range []struct {
		name   string
		args   []uint32
		arg0   uint32
		label2 string
		typ    uint32
	}{
		{"six-argument render: plain title", []uint32{0, 16, 12}, 1, "", 0x0004},
		{"gate 0: plain title", []uint32{0, 16, 12, 0, 7}, 1, "", 0x0004},
		{"gate 1, column 12: key", []uint32{0, 16, 12, 1, 0}, 5001, "Am", 0x0f04},
		{"override 2: artist", []uint32{0, 16, 12, 1, 2}, 1001, "Alpha Artist", 0x0704},
		{"override 4: BPM", []uint32{0, 16, 12, 1, 4}, 12000, "", 0x0d04},
		{"override 15: colour", []uint32{0, 16, 12, 1, 15}, 1, "Pink", 0x1404},
	} {
		got := render(tc.args...)
		if got.ParentID != tc.arg0 || got.Label2 != tc.label2 || got.ItemType != tc.typ {
			t.Errorf("%s: %d %q %#x, want %d %q %#x", tc.name, got.ParentID, got.Label2, got.ItemType, tc.arg0, tc.label2, tc.typ)
		}
	}
	if title.ItemType == 0x0004 && title.Label2 == "" && title.ParentID == 1 {
		t.Errorf("render must not modify the pending title row")
	}
}

// Song-info strings are cut to rekordbox's limits in UTF-16 units; a cut
// through a surrogate pair leaves U+FFFD.
func TestTruncateUTF16(t *testing.T) {
	if got := truncateUTF16("abcdef", 4); got != "abcd" {
		t.Errorf("ascii: %q", got)
	}
	if got := truncateUTF16("a🙂b", 2); got != "a�" {
		t.Errorf("split pair: %q", got)
	}
	if got := truncateUTF16("short", 127); got != "short" {
		t.Errorf("under the limit: %q", got)
	}
}

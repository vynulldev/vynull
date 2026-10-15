// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
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

// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// The deck keeps its main list (location 1) on screen while it fills the
// preview pane (location 2) for the highlighted row. A preview with as many
// rows as the main list must not stand in for it when the deck scrolls the
// main list: on a CDJ-2000NXS2, scrolling the 27-entry playlist list past a
// 27-track playlist showed that playlist's tracks in the list.
func TestRenderMainListAfterSameSizePreview(t *testing.T) {
	h := &Handler{}
	rows := func(first uint32) []*menuItem {
		var items []*menuItem
		for id := first; id < first+27; id++ {
			items = append(items, &menuItem{ID: id, Label1: "row", ItemType: 0x0004})
		}
		return items
	}
	const main, preview = 0x01010401, 0x01020401
	query := func(ctx, txid uint32, items []*menuItem) {
		h.setPendingAll(&proto.DBMessage{TxID: txid, Type: 0x1105, Args: []proto.DBArg{proto.ArgI32(ctx)}}, items)
	}
	render := func(ctx, txid, offset uint32) []uint32 {
		resp := h.Handle(&proto.DBMessage{TxID: txid, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			proto.ArgI32(ctx), proto.ArgI32(offset), proto.ArgI32(8), proto.ArgI32(0), proto.ArgI32(27), proto.ArgI32(0)}})
		var ids []uint32
		for _, m := range resp {
			if m.Type == proto.DBMsgMenuItem {
				ids = append(ids, m.Args[1].Int())
			}
		}
		return ids
	}

	query(main, 1, rows(1)) // the playlist list: IDs 1-27
	render(main, 1, 0)
	query(preview, 2, rows(1000)) // the highlighted playlist's 27 tracks
	render(preview, 2, 0)
	if got, want := render(main, 3, 5), []uint32{6, 7, 8, 9, 10, 11, 12, 13}; !slices.Equal(got, want) {
		t.Fatalf("main list scrolled after a preview: rows %v, want %v", got, want)
	}

	// A root-menu query clears the count match, as before; the main list's
	// own pending rows still serve its next page.
	h.Handle(&proto.DBMessage{TxID: 4, Type: 0x1000, Args: []proto.DBArg{proto.ArgI32(main), proto.ArgI32(0)}})
	if got, want := render(main, 5, 10), []uint32{11, 12, 13, 14, 15, 16, 17, 18}; !slices.Equal(got, want) {
		t.Errorf("main list after a root-menu query: rows %v, want %v", got, want)
	}
}

// The lists are the server's, as rekordbox keeps them: a new session that
// renders a context another session queried gets that list (link-spec
// contexts suite: query in location 2, render in location 1), and a deck's
// root-menu query drops only that deck's lists.
func TestRecentListsSharedAcrossSessions(t *testing.T) {
	shared := &recentLists{}
	session := func() *Handler { return &Handler{recent: shared} }
	tracks := []*menuItem{{ID: 1, ItemType: 0x0004}, {ID: 2, ItemType: 0x0004}, {ID: 3, ItemType: 0x0004}}
	session().setPendingAll(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(0x01010301)}}, tracks)
	session().setPendingAll(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(0x02010301)}}, tracks)

	h := session()
	h.setPendingAll(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(0x01020301)}}, tracks)
	resp := h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(0x01010301), proto.ArgI32(0), proto.ArgI32(3), proto.ArgI32(0), proto.ArgI32(3), proto.ArgI32(0)}})
	if n := len(resp) - 2; n != 3 {
		t.Fatalf("render in a context another session queried: %d rows, want 3", n)
	}

	session().Handle(&proto.DBMessage{TxID: 3, Type: 0x1000, Args: []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(0)}})
	if shared.get(0x01010301) != nil || shared.get(0x01020301) != nil {
		t.Error("player 1's root-menu query kept its lists")
	}
	if shared.get(0x02010301) == nil {
		t.Error("player 1's root-menu query dropped player 2's list")
	}
}

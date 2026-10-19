// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// historyCtx is the request context of rekordbox's history-lifecycle golden:
// player 1, menu location 1, slot 3, track type 1.
const historyCtx = 0x01010301

func historyHandler() *Handler {
	lib := library.NewLibrary([]*library.Track{
		{ID: 10001, Title: "Alpha One", Artist: "A"},
		{ID: 10002, Title: "Alpha Two", Artist: "A"},
	}, nil)
	day := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	return &Handler{lib: lib, extended: true, clock: func() time.Time { return day }}
}

// ask sends a request and returns its count reply's total and, for a
// non-empty list, the rendered rows' arguments.
func ask(t *testing.T, h *Handler, kind uint16, args ...uint32) (total uint32, rows [][]proto.DBArg) {
	t.Helper()
	msg := &proto.DBMessage{TxID: uint32(kind), Type: kind}
	for _, a := range args {
		msg.Args = append(msg.Args, proto.ArgI32(a))
	}
	r := h.Handle(msg)
	if len(r) != 1 || r[0].Type != proto.DBMsgSuccess || r[0].Args[0].Int() != uint32(kind) {
		t.Fatalf("%#04x reply %+v, want 4000 [%#04x, n]", kind, r, kind)
	}
	total = r[0].Args[1].Int()
	if total == 0 {
		return 0, nil
	}
	page := h.Handle(&proto.DBMessage{TxID: uint32(kind), Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(historyCtx), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0)}})
	for _, m := range page[1 : len(page)-1] {
		rows = append(rows, m.Args)
	}
	return total, rows
}

// send sends a request that rekordbox doesn't answer.
func send(t *testing.T, h *Handler, kind uint16, args ...uint32) {
	t.Helper()
	msg := &proto.DBMessage{TxID: 1, Type: kind}
	for _, a := range args {
		msg.Args = append(msg.Args, proto.ArgI32(a))
	}
	if r := h.Handle(msg); len(r) != 0 {
		t.Fatalf("%#04x got a reply %+v, want none", kind, r)
	}
}

// historyTracks lists a history's tracks as (track ID, position) pairs and
// checks each row carries the played bit.
func historyTracks(t *testing.T, h *Handler, id uint32) [][2]uint32 {
	t.Helper()
	_, rows := ask(t, h, 0x1112, historyCtx, 0, id)
	var got [][2]uint32
	for _, r := range rows {
		if r[7].Int()&linkPlayedFlag == 0 {
			t.Errorf("history row %d: arg 7 %#x, want the played bit %#x", r[1].Int(), r[7].Int(), linkPlayedFlag)
		}
		got = append(got, [2]uint32{r[1].Int(), r[9].Int()})
	}
	return got
}

func playState(h *Handler, track uint32) uint32 {
	r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x3b03, Args: []proto.DBArg{proto.ArgI32(historyCtx), proto.ArgI32(track)}})
	return r[0].Args[1].Int()
}

// The Link history follows rekordbox's history-lifecycle golden: empty at
// first; the first insert creates "LINK HISTORY <day>" (a 0x24 row); its
// tracks list in insertion order numbered from 1; a removal closes the gap;
// deleting the history empties the HISTORY menu.
func TestLinkHistoryLifecycle(t *testing.T) {
	h := historyHandler()
	if n, _ := ask(t, h, 0x1012, historyCtx, 0); n != 0 {
		t.Fatalf("initial HISTORY lists %d histories, want 0", n)
	}

	send(t, h, 0x3001, historyCtx, 10001)
	n, rows := ask(t, h, 0x1012, historyCtx, 0)
	if n != 1 || len(rows) != 1 {
		t.Fatalf("HISTORY after an insert: %d rows, want 1", n)
	}
	row := rows[0]
	id := row[1].Int()
	if id == 0 || row[3].Str != "LINK HISTORY 2026-09-30" || row[2].Int() != 48 || row[6].Int() != historyItemType {
		t.Errorf("history row: id %d, label %q (%d bytes), type %#x; want LINK HISTORY 2026-09-30 (48), 0x24",
			id, row[3].Str, row[2].Int(), row[6].Int())
	}
	if got, want := historyTracks(t, h, id), [][2]uint32{{10001, 1}}; !slices.Equal(got, want) {
		t.Errorf("after the first insert: %v, want %v", got, want)
	}

	send(t, h, 0x3001, historyCtx, 10002)
	if got, want := historyTracks(t, h, id), [][2]uint32{{10001, 1}, {10002, 2}}; !slices.Equal(got, want) {
		t.Errorf("after the second insert: %v, want %v", got, want)
	}

	if n, _ := ask(t, h, 0x3401, historyCtx, 10001); n != 0 {
		t.Errorf("0x3401 reply total %d, want 0", n)
	}
	if got, want := historyTracks(t, h, id), [][2]uint32{{10002, 1}}; !slices.Equal(got, want) {
		t.Errorf("after removing the first: %v, want %v", got, want)
	}
	if n, _ := ask(t, h, 0x1112, historyCtx, 0, id+1); n != 0 {
		t.Errorf("an unknown history lists %d tracks, want 0", n)
	}

	send(t, h, 0x3101, historyCtx, id)
	if n, _ := ask(t, h, 0x1012, historyCtx, 0); n != 0 {
		t.Errorf("HISTORY after deleting the history lists %d, want 0", n)
	}
}

// A track in the Link history is played on the link: 0x3b03 answers 2 for
// it and 0 otherwise, and its rows in other lists carry the played bit.
func TestLinkHistoryPlayedState(t *testing.T) {
	h := historyHandler()
	trackRows := func() map[uint32]uint32 {
		_, rows := ask(t, h, proto.DBMsgGetTracks, historyCtx, 0)
		arg7 := map[uint32]uint32{}
		for _, r := range rows {
			arg7[r[1].Int()] = r[7].Int()
		}
		return arg7
	}
	if playState(h, 10001) != 0 || trackRows()[10001]&linkPlayedFlag != 0 {
		t.Fatalf("a track starts played")
	}
	send(t, h, 0x3001, historyCtx, 10001)
	if playState(h, 10001) != playStatePlayed || playState(h, 10002) != 0 {
		t.Errorf("after an insert: states %d, %d; want 2, 0", playState(h, 10001), playState(h, 10002))
	}
	if rows := trackRows(); rows[10001]&linkPlayedFlag == 0 || rows[10002]&linkPlayedFlag != 0 {
		t.Errorf("TRACK rows' arg 7: %#x, %#x; want only the inserted track played", rows[10001], rows[10002])
	}
	ask(t, h, 0x3401, historyCtx, 10001)
	if playState(h, 10001) != 0 {
		t.Errorf("a removed track is still played")
	}
	send(t, h, 0x3001, historyCtx, 10002)
	id, _ := h.linkHistory().current()
	send(t, h, 0x3101, historyCtx, id)
	if playState(h, 10002) != 0 {
		t.Errorf("a track in a deleted history is still played")
	}
}

// The decks' sessions share the server's Link history, and a track a deck
// adds twice is listed twice and removed in one go.
func TestLinkHistorySharedAndRepeated(t *testing.T) {
	shared := &linkHistory{}
	a, b := historyHandler(), historyHandler()
	a.history, b.history = shared, shared
	send(t, a, 0x3001, historyCtx, 10001)
	send(t, b, 0x3001, historyCtx, 10002)
	send(t, a, 0x3001, historyCtx, 10001)
	id, _ := b.linkHistory().current()
	if got, want := historyTracks(t, b, id), [][2]uint32{{10001, 1}, {10002, 2}, {10001, 3}}; !slices.Equal(got, want) {
		t.Errorf("shared history: %v, want %v", got, want)
	}
	ask(t, b, 0x3401, historyCtx, 10001)
	if got, want := historyTracks(t, a, id), [][2]uint32{{10002, 1}}; !slices.Equal(got, want) {
		t.Errorf("after removing the repeated track: %v, want %v", got, want)
	}
}

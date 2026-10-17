// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The TRACK FILTER commands, replayed from a real rekordbox capture of a
// CDJ-2000NXS2: each 0x3207 sets a condition, and the next 0x3107 reply's
// condition blob must match rekordbox's byte for byte.
func TestTrackFilterConditionsMatchRekordbox(t *testing.T) {
	h := &Handler{}
	const ctx = 0x01010401
	set := func(prop uint32, blob string) {
		r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x3207, Args: []proto.DBArg{
			proto.ArgI32(ctx), proto.ArgI32(prop), proto.ArgI32(0), proto.ArgI32(uint32(len(blob) / 2)), proto.ArgBlob(mustHex(t, blob))}})
		if len(r) != 1 || r[0].Type != proto.DBMsgSuccess || r[0].Args[0].Int() != 0x3207 {
			t.Fatalf("0x3207 reply = %+v", r)
		}
	}
	get := func() *proto.DBMessage {
		r := h.Handle(&proto.DBMessage{TxID: 2, Type: 0x3107, Args: []proto.DBArg{proto.ArgI32(ctx)}})
		if len(r) != 1 {
			t.Fatalf("0x3107: %d replies", len(r))
		}
		return r[0]
	}
	rest := "2c0000000100000046434e4400000014000000240000000c00110004000000100000000f0000000e00000012" +
		"240000000100000046434e44000000140000001c00000007000000020000000500000000" +
		"200000000100000046434e4400000014000000180000000f0011000100000000"
	for _, step := range []struct{ set, want string }{
		{"00060200ba300000b6310000", "240000000100000046434e44000000140000001c0000000600060002000030ba000031b6"},
		{"01060200ba300000b6310000", "240000000100000046434e44000000140000001c0000000601060002000030ba000031b6"},
		{"01060200402f000030330000", "240000000100000046434e44000000140000001c000000060106000200002f4000003330"},
		{"00060200402f000030330000", "240000000100000046434e44000000140000001c000000060006000200002f4000003330"},
	} {
		set(filterPropBPM, step.set)
		r := get()
		if r.Type != 0x4004 || r.Args[0].Int() != 0x3107 || r.Args[2].Int() != 148 || r.Args[4].Int() != 4 {
			t.Fatalf("0x3107 envelope = %+v", r)
		}
		if got := hex.EncodeToString(r.Args[3].Bytes); got != step.want+rest {
			t.Errorf("after 0x3207 %s:\n got %s\nwant %s", step.set, got, step.want+rest)
		}
	}
}

// With the filter on, the TRACK list at menu location 1 keeps only tracks in
// the BPM range and the key list; location 2 and an off filter are unfiltered.
func TestTrackFilterNarrowsTrackList(t *testing.T) {
	lib := library.New()
	for i, tr := range []struct {
		bpm float64
		key string
	}{{126, "Am"}, {124.74, "C"}, {127.26, "Fm"}, {130, "Am"}, {120, "Em"}} {
		lib.AddTrack(&library.Track{ID: uint32(i + 1), Title: string(rune('A' + i)), BPM: tr.bpm, Key: tr.key})
	}
	h := &Handler{lib: lib}
	count := func(ctx uint32) int {
		h.Handle(&proto.DBMessage{TxID: 3, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0)}})
		return len(h.pendingItems)
	}
	send := func(kind uint16, args ...proto.DBArg) {
		h.Handle(&proto.DBMessage{TxID: 4, Type: kind, Args: append([]proto.DBArg{proto.ArgI32(0x01010401)}, args...)})
	}
	if n := count(0x01010401); n != 5 {
		t.Fatalf("unfiltered = %d, want 5", n)
	}
	// BPM 126 ±1% (124.74-127.26), as the deck sent it; filter on.
	send(0x3207, proto.ArgI32(filterPropBPM), proto.ArgI32(0), proto.ArgI32(12), proto.ArgBlob(mustHex(t, "01060200ba300000b6310000")))
	send(0x3007, proto.ArgI32(1))
	if n := count(0x01010401); n != 3 {
		t.Errorf("BPM 124.74-127.26 = %d tracks, want 3 (bounds inclusive)", n)
	}
	if n := count(0x01020401); n != 5 {
		t.Errorf("location 2 = %d tracks, want 5 (unfiltered)", n)
	}
	// Add "Am related" keys (15,16,13,17 = 8A, 8B, 7A, 9A): Am and C stay.
	send(0x3207, proto.ArgI32(filterPropKey), proto.ArgI32(0), proto.ArgI32(20), proto.ArgBlob(mustHex(t, "011104000f000000100000000d00000011000000")))
	if n := count(0x01010401); n != 2 {
		t.Errorf("BPM and Am-related keys = %d tracks, want 2", n)
	}
	send(0x3007, proto.ArgI32(0))
	if n := count(0x01010401); n != 5 {
		t.Errorf("filter off = %d tracks, want 5", n)
	}
}

// Rating [n, 0] keeps tracks rated exactly n stars; colour is a bitmask of
// colours (bit colour-1: yellow, colour 4, is 8), and a zero mask keeps all.
// From real rekordbox captures: "5 stars exact" sends [5, 0], yellow sends
// [8], red [2].
func TestTrackFilterRatingAndColour(t *testing.T) {
	lib := library.New()
	for i, tr := range []struct {
		rating uint8
		color  uint8
	}{{5, 4}, {5, 0}, {4, 4}, {2, 2}, {0, 4}, {0, 8}} {
		lib.AddTrack(&library.Track{ID: uint32(i + 1), Title: string(rune('A' + i)), Rating: tr.rating, ColorID: tr.color})
	}
	h := &Handler{lib: lib}
	const ctx = 0x01010401
	send := func(kind uint16, args ...proto.DBArg) {
		h.Handle(&proto.DBMessage{TxID: 4, Type: kind, Args: append([]proto.DBArg{proto.ArgI32(ctx)}, args...)})
	}
	count := func() int {
		h.Handle(&proto.DBMessage{TxID: 3, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0)}})
		return len(h.pendingItems)
	}
	setCond := func(prop uint32, blob string) {
		send(0x3207, proto.ArgI32(prop), proto.ArgI32(0), proto.ArgI32(uint32(len(blob)/2)), proto.ArgBlob(mustHex(t, blob)))
	}
	send(0x3007, proto.ArgI32(1))
	for _, tc := range []struct {
		prop       uint32
		blob, name string
		want       int
	}{
		{filterPropRating, "010002000500000000000000", "rating 5 exact", 2},
		{filterPropRating, "010002000400000000000000", "rating 4 exact", 1},
		{filterPropRating, "010002000100000000000000", "rating 1 exact", 0},
		{filterPropRating, "000002000100000000000000", "rating off", 6},
		{filterPropColor, "0111010008000000", "yellow", 3},
		{filterPropColor, "0111010002000000", "red", 1},
		{filterPropColor, "011101000a000000", "yellow or red", 4},
		{filterPropColor, "0111010000000000", "no colour chosen", 6},
	} {
		setCond(tc.prop, tc.blob)
		if n := count(); n != tc.want {
			t.Errorf("%s: %d tracks, want %d", tc.name, n, tc.want)
		}
	}
}

// A 0x3207 carries its condition blob as a trailing binary argument, which
// the reader must wait for: skipping it left the blob in the stream, the
// next read failed ("unexpected byte 0x14"), the session dropped, and the
// deck unticked the filter it had just ticked.
func TestReadSetFilterConditionBlob(t *testing.T) {
	blob := mustHex(t, "01060200bc2f0000d4350000")
	frame := proto.MarshalDBMessage(&proto.DBMessage{TxID: 9, Type: 0x3207, Args: []proto.DBArg{
		proto.ArgI32(0x01010401), proto.ArgI32(filterPropBPM), proto.ArgI32(0), proto.ArgI32(uint32(len(blob))), proto.ArgBlob(blob)}})
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go client.Write(frame)
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := readMessage(server)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Args) != 5 || !bytes.Equal(msg.Args[4].Bytes, blob) {
		t.Fatalf("0x3207 read as %+v, want 5 arguments ending in the condition blob", msg.Args)
	}
}

// fakeMyTags is a MyTagSource for the MyTag filter tests.
type fakeMyTags struct {
	columns []MyTagEntry
	tags    map[uint32][]MyTagEntry
	track   map[uint32][]uint32
}

func (f fakeMyTags) MyTagColumns() []MyTagEntry          { return f.columns }
func (f fakeMyTags) MyTagsIn(c uint32) []MyTagEntry      { return f.tags[c] }
func (f fakeMyTags) TrackMyTags(trackID uint32) []uint32 { return f.track[trackID] }

// The MyTag filter, replayed from a real rekordbox capture: 0x1015 lists the
// columns and a column's tags (ticked rows typed 0x4b, a column's AND/OR mode
// in argument 8), 0x3307 turns a column's condition on with its mode, and
// 0x3407 ticks and unticks tags. Trap and Deep House kept 1, 0, 26, 26, 27
// and 0 tracks along the way; here the library is scaled down.
func TestTrackFilterMyTag(t *testing.T) {
	const trap, deep = 1740876187, 1937762829
	lib := library.New()
	src := fakeMyTags{
		columns: []MyTagEntry{{1, "Genre"}, {2, "Components"}},
		tags:    map[uint32][]MyTagEntry{1: {{trap, "Trap"}, {deep, "Deep House"}}},
		track:   map[uint32][]uint32{1: {trap}, 2: {deep}, 3: {deep}},
	}
	for i := uint32(1); i <= 4; i++ {
		lib.AddTrack(&library.Track{ID: i, Title: string(rune('A' + i))})
	}
	h := &Handler{lib: lib, mytags: src}
	send := func(kind uint16, ctx uint32, args ...uint32) []*proto.DBMessage {
		a := []proto.DBArg{proto.ArgI32(ctx)}
		for _, v := range args {
			a = append(a, proto.ArgI32(v))
		}
		return h.Handle(&proto.DBMessage{TxID: 5, Type: kind, Args: a})
	}
	count := func() int {
		send(proto.DBMsgGetTracks, 0x01010401, 0)
		return len(h.pendingItems)
	}
	rows := func(ctx, parent uint32) [][]uint32 {
		send(0x1015, ctx, 0, parent, 0)
		var out [][]uint32
		for _, it := range h.pendingItems {
			out = append(out, []uint32{it.ParentID, it.ID, it.ItemType, it.ArtID, it.trackPos})
		}
		return out
	}

	if got := rows(0x01070401, 0); len(got) != 2 || got[0][1] != 1 || got[0][2] != 0x4a || got[0][3] != mytagAnd || got[1][4] != 1 {
		t.Fatalf("columns = %v", got)
	}
	send(0x3007, 0x01010401, 1)
	send(0x3407, 0x01010401, 0, 1, trap, 1)
	send(0x3307, 0x01010401, 0, 1, 1, mytagAnd)
	if got := rows(0x01050401, 1); got[0][2] != 0x4b || got[1][2] != 0x4a {
		t.Errorf("Genre tags after ticking Trap = %v, want Trap 0x4b, Deep House 0x4a", got)
	}
	if got := rows(0x01070401, 0); got[0][2] != 0x4b {
		t.Errorf("Genre column with its condition on = %v, want 0x4b", got[0])
	}
	for _, step := range []struct {
		name string
		kind uint16
		args []uint32
		want int
	}{
		{"Trap, AND", 0, nil, 1},
		{"Trap and Deep House, AND", 0x3407, []uint32{0, 1, deep, 1}, 0},
		{"Deep House, AND", 0x3407, []uint32{0, 1, trap, 0}, 2},
		{"Deep House, OR", 0x3307, []uint32{0, 1, 1, mytagOr}, 2},
		{"Trap or Deep House, OR", 0x3407, []uint32{0, 1, trap, 1}, 3},
		{"Trap and Deep House, AND again", 0x3307, []uint32{0, 1, 1, mytagAnd}, 0},
		{"Genre condition off", 0x3307, []uint32{0, 1, 0, mytagAnd}, 4},
	} {
		if step.kind != 0 {
			send(step.kind, 0x01010401, step.args...)
		}
		if n := count(); n != step.want {
			t.Errorf("%s: %d tracks, want %d", step.name, n, step.want)
		}
	}
}

// The MyTag screen rows render like rekordbox's (first 12 arguments, from
// the capture): [0, id, len, name, 2, "", type, 0, mode, index, 0, 0].
func TestTrackFilterMyTagRows(t *testing.T) {
	const trap, deep = 1740876187, 1937762829
	h := &Handler{mytags: fakeMyTags{
		columns: []MyTagEntry{{1, "Genre"}, {2, "Components"}},
		tags:    map[uint32][]MyTagEntry{1: {{trap, "Trap"}, {deep, "Deep House"}}},
	}}
	render := func(ctx, parent uint32) [][]any {
		h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1015, Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0), proto.ArgI32(parent), proto.ArgI32(0)}})
		resp := h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			proto.ArgI32(ctx), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0)}})
		var out [][]any
		for _, m := range resp {
			if m.Type != proto.DBMsgMenuItem {
				continue
			}
			var row []any
			for i, a := range m.Args {
				if i >= 12 {
					break
				}
				if a.Tag == 0x26 {
					row = append(row, a.Str)
				} else {
					row = append(row, a.Int())
				}
			}
			out = append(out, row)
		}
		return out
	}
	want := func(id uint32, name string, typ, mode, idx uint32) []any {
		return []any{uint32(0), id, uint32(2 * (len([]rune(name)) + 1)), name, uint32(2), "", typ, uint32(0), mode, idx, uint32(0), uint32(0)}
	}
	cols := render(0x01070401, 0)
	if len(cols) != 2 || fmt.Sprint(cols[0]) != fmt.Sprint(want(1, "Genre", 0x4a, mytagAnd, 0)) {
		t.Errorf("column row = %v\nwant       %v", cols, want(1, "Genre", 0x4a, mytagAnd, 0))
	}
	tags := render(0x01050401, 1)
	if len(tags) != 2 || fmt.Sprint(tags[1]) != fmt.Sprint(want(deep, "Deep House", 0x4a, 0, 1)) {
		t.Errorf("tag row = %v\nwant    %v", tags, want(deep, "Deep House", 0x4a, 0, 1))
	}
}

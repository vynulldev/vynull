// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/hex"
	"testing"

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

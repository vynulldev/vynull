// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// rootCtx is the context of link-spec's extended-families suite: player 1,
// menu location 1, slot 3, track type 1.
const rootCtx = 0x01010301

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func extendedHandler() *Handler {
	lib := library.NewLibrary([]*library.Track{
		{ID: 10001, Title: "Alpha One", Key: "Am", BPM: 120, TrackNum: 1, PlayCount: 0, DateAdded: day(2021, 2, 2)},
		{ID: 10002, Title: "Alpha Two", Key: "C", BPM: 120.5, TrackNum: 2, PlayCount: 1, DateAdded: day(2022, 3, 3)},
		{ID: 10003, Title: "Beta", Key: strings.Repeat("k", 128), BPM: 121, TrackNum: 3, PlayCount: 255, DateAdded: day(2021, 7, 14)},
		{ID: 10004, Title: "Gamma", TrackNum: 4, PlayCount: 256, DateAdded: day(2021, 2, 20)},
		{ID: 10005, Title: "Delta", TrackNum: 5, PlayCount: 65535},
	}, nil)
	return &Handler{lib: lib, extended: true}
}

// ids lists rows' argument 1 (their ID).
func ids(rows [][]proto.DBArg) []uint32 {
	var out []uint32
	for _, r := range rows {
		out = append(out, r[1].Int())
	}
	return out
}

// DATE ADDED: years newest first, then ALL and the months, ALL and the days,
// and the tracks of a day or, with ALL as the day, of the whole month; ALL
// as the month leaves only ALL among the days. Rows of numbers have no label
// and type 0x2e.
func TestDateAdded(t *testing.T) {
	h := extendedHandler()
	check := func(name string, kind uint16, want []uint32, args ...uint32) [][]proto.DBArg {
		t.Helper()
		_, rows := ask(t, h, kind, append([]uint32{rootCtx, 0}, args...)...)
		if got := ids(rows); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
		return rows
	}
	years := check("years", 0x1708, []uint32{2022, 2021})
	if years[0][6].Int() != dateRowType || years[0][3].Str != "" {
		t.Errorf("year row %v, want type 0x2e and no label", years[0])
	}
	months := check("months of 2021", 0x1808, []uint32{allSelector, 2, 7}, 2021)
	if months[0][6].Int() != 0xa0 {
		t.Errorf("ALL row type %#x, want 0xa0", months[0][6].Int())
	}
	check("days of February 2021", 0x1908, []uint32{allSelector, 2, 20}, 2021, 2)
	check("days of an empty month", 0x1908, []uint32{allSelector}, 2021, 5)
	check("days of ALL months", 0x1908, []uint32{allSelector}, 2021, allSelector)
	check("tracks of 2021-02-02", 0x1a08, []uint32{10001}, 2021, 2, 2)
	check("tracks of February 2021", 0x1a08, []uint32{10001, 10004}, 2021, 2, allSelector)
	check("tracks of a day without any", 0x1a08, nil, 2021, 2, 3)
}

// DJ PLAY COUNT lists the counts as signed 16-bit values; a track request
// matches its selector's low byte against counts up to 255, and its rows
// carry the track number, the key alone and a key cut to 127 units.
func TestPlayCount(t *testing.T) {
	h := extendedHandler()
	if _, rows := ask(t, h, 0x100e, rootCtx, 0); !slices.Equal(ids(rows), []uint32{0xffffffff, 0, 1, 255, 256}) || rows[0][6].Int() != countRowType {
		t.Errorf("counts %v (type %#x), want [0xffffffff 0 1 255 256] of type 0x2a", ids(rows), rows[0][6].Int())
	}
	for sel, want := range map[uint32][]uint32{0: {10001}, 256: {10001}, 1: {10002}, 255: {10003}, 0xffffffff: {10003}, 65535: {10003}, 2: nil} {
		if _, rows := ask(t, h, 0x110e, rootCtx, 0, sel); !slices.Equal(ids(rows), want) {
			t.Errorf("tracks of count %d: %v, want %v", sel, ids(rows), want)
		}
	}
	h.menu = fakeMenu{detail: "key"}
	_, rows := ask(t, h, 0x110e, rootCtx, 0, 255)
	if r := rows[0]; r[9].Int() != 3 || r[5].Str != strings.Repeat("k", 127) || r[14].Str != strings.Repeat("k", 127) {
		t.Errorf("play count row: arg 9 %d, key %d and %d units; want 3 and 127", r[9].Int(), len(r[5].Str), len(r[14].Str))
	}
	_, rows = ask(t, h, 0x110e, rootCtx, 0, 0)
	if r := rows[0]; r[5].Str != "Am" || r[9].Int() != 1 {
		t.Errorf("play count row label %q, arg 9 %d; want the key alone and 1", r[5].Str, r[9].Int())
	}
}

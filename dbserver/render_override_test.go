// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

const overrideCtx = 0x01010301

// listRows requests the TRACK list with a sort ID, then renders all of it:
// with six arguments, or eight when override holds the gate and selector. It
// returns the rows' arguments, or nil when the render goes unanswered.
func listRows(t *testing.T, h *Handler, sort uint32, override ...uint32) [][]proto.DBArg {
	t.Helper()
	r := h.Handle(&proto.DBMessage{TxID: 9, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(overrideCtx), proto.ArgI32(sort)}})
	if len(r) != 1 || r[0].Type != proto.DBMsgSuccess {
		t.Fatalf("sort %d: reply %+v", sort, r)
	}
	total := r[0].Args[1].Int()
	args := []proto.DBArg{proto.ArgI32(overrideCtx), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(12)}
	for _, v := range override {
		args = append(args, proto.ArgI32(v))
	}
	page := h.Handle(&proto.DBMessage{TxID: 10, Type: proto.DBMsgRenderMenu, Args: args})
	if page == nil {
		return nil
	}
	var rows [][]proto.DBArg
	for _, m := range page[1 : len(page)-1] {
		rows = append(rows, m.Args)
	}
	return rows
}

type columnRow struct {
	arg0 uint32
	text string
	typ  uint32
}

func rowColumn(rows [][]proto.DBArg, id uint32) columnRow {
	for _, a := range rows {
		if a[1].Int() == id {
			return columnRow{a[0].Int(), a[5].Str, a[6].Int()}
		}
	}
	return columnRow{text: "(missing)"}
}

// An eight-argument render's override gate and selector choose the track
// rows' second column as rekordbox 7.2.19 does (link-spec
// secondary-column-semantics: explicit_render_8 and
// active_sort_render_8_persisted_fallback, track 10001 of the full fixture
// with the Key column configured).
func TestRenderOverrideColumns(t *testing.T) {
	h := &Handler{lib: fixtureLibrary(), menu: keyColumnMenu{}}
	key := h.catID("key", "Am")
	keyBPM := columnRow{key, "Am - 120.0 bpm", 0x0f04}
	for _, c := range []struct {
		name     string
		sort     uint32
		override []uint32
		track    uint32
		want     columnRow
	}{
		{"six-argument render", 0, nil, 10001, keyBPM},
		{"zero gate ignores the selector", 0, []uint32{0, 7}, 10001, keyBPM},
		{"selector 0 is the configured column", 0, []uint32{1, 0}, 10001, keyBPM},
		{"artist", 0, []uint32{1, 2}, 10001, columnRow{h.catID("artist", "Alpha Artist"), "Alpha Artist", 0x0704}},
		{"album", 0, []uint32{1, 3}, 10001, columnRow{h.catID("album", "Album One"), "Album One", 0x0204}},
		{"no album", 0, []uint32{1, 3}, 10007, columnRow{0, "", 0x0204}},
		{"BPM", 0, []uint32{1, 4}, 10001, columnRow{12000, "", 0x0d04}},
		{"rating", 0, []uint32{1, 5}, 10002, columnRow{1, "", 0x0a04}},
		{"genre", 0, []uint32{1, 6}, 10001, columnRow{h.catID("genre", "Fixture House"), "Fixture House", 0x0604}},
		{"comment", 0, []uint32{1, 7}, 10001, columnRow{10001, "comment-1", 0x2304}},
		{"time", 0, []uint32{1, 8}, 10001, columnRow{59, "", 0x0b04}},
		{"remixer", 0, []uint32{1, 9}, 10001, columnRow{h.catID("remixer", "Fixture Remixer"), "Fixture Remixer", 0x2904}},
		{"label", 0, []uint32{1, 10}, 10001, columnRow{h.catID("label", "Fixture Label One"), "Fixture Label One", 0x0e04}},
		{"original artist", 0, []uint32{1, 11}, 10001, columnRow{h.catID("origartist", "Fixture Original"), "Fixture Original", 0x2804}},
		{"key, already shown", 0, []uint32{1, 12}, 10001, keyBPM},
		{"bitrate", 0, []uint32{1, 13}, 10002, columnRow{32, "", 0x1004}},
		{"reserved", 0, []uint32{1, 14}, 10001, columnRow{0, "", 0x0004}},
		{"colour", 0, []uint32{1, 15}, 10002, columnRow{2, "Red", 0x1504}},
		{"play count", 0, []uint32{1, 16}, 10002, columnRow{1, "", 0x2a04}},
		{"date added", 0, []uint32{1, 17}, 10001, columnRow{10001, "2021-02-02", 0x2e04}},
		{"selector 1 is outside the extractor", 0, []uint32{1, 1}, 10001, keyBPM},
		{"selector 18 is outside the extractor", 0, []uint32{1, 18}, 10001, keyBPM},
		{"ARTIST sort, configured key: the key alone", 2, []uint32{1, 0}, 10001, columnRow{key, "Am", 0x0f04}},
		{"KEY sort keeps its own text", 12, []uint32{1, 0}, 10001, columnRow{14, "Am - 120.0 bpm", 0x0f04}},
		{"sort 14 shows the reserved column", 14, nil, 10001, columnRow{0, "", 0x0004}},
		{"sort 14, configured key: the key alone", 14, []uint32{1, 0}, 10001, columnRow{key, "Am", 0x0f04}},
	} {
		if got := rowColumn(listRows(t, h, c.sort, c.override...), c.track); got != c.want {
			t.Errorf("%s: track %d got %+v, want %+v", c.name, c.track, got, c.want)
		}
	}
}

// rekordbox cuts a lookup or key column to 127 characters, the KEY column's
// whole "key - BPM" text included, and a comment to 255; a comment that still
// takes more than 255 UTF-16 units leaves the render unanswered (link-spec
// secondary-string-boundaries and secondary-*-thresholds).
func TestRenderOverrideStringLimits(t *testing.T) {
	lib := library.New()
	long := strings.Repeat("k", 126)
	lib.AddTrack(&library.Track{ID: 1, Title: "One", Artist: strings.Repeat("a", 128), Key: long, BPM: 120,
		Comment: strings.Repeat("c", 256)})
	h := &Handler{lib: lib, menu: keyColumnMenu{}}

	if got := rowColumn(listRows(t, h, 0), 1).text; got != long+" " {
		t.Errorf("KEY column %q (%d chars), want the key and a space, 127 characters", got, len(got))
	}
	if got := rowColumn(listRows(t, h, 0, 1, 2), 1).text; got != strings.Repeat("a", 127) {
		t.Errorf("artist %d chars, want 127", len(got))
	}
	if got := rowColumn(listRows(t, h, 0, 1, 7), 1).text; got != strings.Repeat("c", 255) {
		t.Errorf("comment %d chars, want 255", len(got))
	}

	emoji := strings.Repeat("🙂", 127)
	lib.AddTrack(&library.Track{ID: 2, Title: "Two", Comment: emoji + "a"})
	if got := rowColumn(listRows(t, h, 0, 1, 7), 2).text; got != emoji+"a" {
		t.Errorf("a 255-unit comment was changed: %d runes", len([]rune(got)))
	}
	lib.AddTrack(&library.Track{ID: 3, Title: "Three", Comment: emoji + "🙂"})
	if rows := listRows(t, h, 0, 1, 7); rows != nil {
		t.Errorf("a 256-unit comment was rendered, want no reply")
	}
	if rows := listRows(t, h, 0); rows == nil {
		t.Errorf("the six-argument render went unanswered")
	}
}

// A date added stored as text that isn't a date is served as stored, as
// rekordbox serves its StockDate: in the Date Added column, by the render
// override (cut to 255 characters, and a render whose text still takes more
// than 255 UTF-16 units goes unanswered, as for a comment), and in TRACK
// INFO (link-spec secondary-*-thresholds and secondary-date-added-boundaries).
func TestDateAddedStoredText(t *testing.T) {
	lib := library.New()
	text := strings.Repeat("d", 126)
	lib.AddTrack(&library.Track{ID: 1, Title: "One", DateAddedText: text})
	lib.AddTrack(&library.Track{ID: 2, Title: "Two", DateAddedText: strings.Repeat("e", 300)})
	col := "date_added"
	h := &Handler{lib: lib, menu: detailMenu{detail: &col}}

	if got := rowColumn(listRows(t, h, 0), 1).text; got != text {
		t.Errorf("Date Added column %q, want the stored text", got)
	}
	// The override from another column (KEY) extracts the date added.
	hk := &Handler{lib: lib, menu: keyColumnMenu{}}
	if got := rowColumn(listRows(t, hk, 0, 1, 17), 2).text; got != strings.Repeat("e", 255) {
		t.Errorf("override: %d characters, want 255", len(got))
	}
	lib.AddTrack(&library.Track{ID: 3, Title: "Three", DateAddedText: strings.Repeat("🙂", 128)})
	if rows := listRows(t, hk, 0, 1, 17); rows != nil {
		t.Errorf("a 256-unit date added was rendered, want no reply")
	}

	h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(1)}})
	for _, r := range h.pendingItems {
		if r.ItemType == 0x2e && r.Label1 != text {
			t.Errorf("TRACK INFO date added %q, want the stored text", r.Label1)
		}
	}
}

// A library's own colour names are served wherever a colour is named: the
// COLOR menu, a colour second column (configured or by the render override,
// cut to 127 characters) and TRACK INFO (link-spec custom-colors,
// secondary-string-boundaries and display-song-info-boundaries).
func TestCustomColorNames(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "One", ColorID: 1})
	lib.AddTrack(&library.Track{ID: 2, Title: "Two", ColorID: 8})
	lib.AddTrack(&library.Track{ID: 3, Title: "Three", ColorID: 2})
	long := strings.Repeat("o", 128)
	lib.SetColorNames(map[uint8]string{1: "Fixture Magenta", 8: long})
	col := "color"
	h := &Handler{lib: lib, menu: detailMenu{detail: &col}}

	h.Handle(&proto.DBMessage{TxID: 1, Type: 0x100d, Args: []proto.DBArg{proto.ArgI32(overrideCtx), proto.ArgI32(0)}})
	var colors []string
	for _, m := range h.pendingItems {
		colors = append(colors, m.Label1)
	}
	if colors[0] != "Fixture Magenta" || colors[1] != "Red" || colors[7] != long {
		t.Errorf("COLOR menu %q", colors)
	}

	rows := listRows(t, h, 0)
	if got := rowColumn(rows, 1).text; got != "Fixture Magenta" {
		t.Errorf("colour column %q, want Fixture Magenta", got)
	}
	if got := rowColumn(rows, 2).text; got != long[:127] {
		t.Errorf("colour column %d characters, want 127", len(got))
	}
	hk := &Handler{lib: lib, menu: keyColumnMenu{}}
	if got := rowColumn(listRows(t, hk, 0, 1, 15), 2).text; got != long[:127] {
		t.Errorf("override: %d characters, want 127", len(got))
	}

	h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(1)}})
	for _, m := range h.pendingItems {
		if m.ItemType == 0x14 && m.Label1 != "Fixture Magenta" {
			t.Errorf("TRACK INFO colour %q, want Fixture Magenta", m.Label1)
		}
	}
}

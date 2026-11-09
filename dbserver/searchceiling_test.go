// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// search sends SEARCH 0x1300 [ctx, sort, declared length, query, 0] (the
// query's real length when declared is -1) and returns its reply.
func search(h *Handler, declared int, query string) []*proto.DBMessage {
	q := utf16Arg(query)
	if declared < 0 {
		declared = len(q.Str)
	}
	return h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1300, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(0), proto.ArgI32(uint32(declared)), q, proto.ArgI32(0)}})
}

func total(r []*proto.DBMessage) uint32 { return r[0].Args[1].Int() }

// SEARCH lists at most 1,000 rows.
func TestSearchCeiling(t *testing.T) {
	var tracks []*library.Track
	for i := range searchCeiling + 5 {
		tracks = append(tracks, &library.Track{ID: uint32(i + 1), Title: fmt.Sprintf("Ceiling Match %04d", i+1), FilePath: fmt.Sprintf("/m/%d.wav", i)})
	}
	h := &Handler{lib: library.NewLibrary(tracks, nil)}
	if got := total(search(h, -1, "ceiling")); got != searchCeiling {
		t.Errorf("CEILING total %d, want %d", got, searchCeiling)
	}
	if got := total(search(h, -1, "1005")); got != 1 {
		t.Errorf("a narrow query past the ceiling: total %d, want 1", got)
	}
}

// Both searches refuse a declared length of zero or shorter than the query
// and its NUL, read the query up to its first NUL, and match nothing for a
// token with an emoji.
func TestSearchQueryRules(t *testing.T) {
	h := searchTrackHandler()
	for _, declared := range []int{0, 2, 14} {
		if r := search(h, declared, "fixture"); len(r) != 1 || r[0].Type != 0x0100 || len(r[0].Args) != 0 {
			t.Errorf("declared length %d: %+v, want an argumentless 0x0100", declared, r)
		}
	}
	if got := total(search(h, 256, "fixture")); got == 0 {
		t.Error("a declared length longer than the query was refused")
	}
	for query, want := range map[string]uint32{"emoji\x00🙂": 1, "🙂": 0, "emoji 🙂": 0, "🙂\x00": 0} {
		if got := total(search(h, -1, query)); got != want {
			t.Errorf("SEARCH %q: total %d, want %d", query, got, want)
		}
		if got := total(searchTrack(h, 0, -1, query)); got != want {
			t.Errorf("Search Track %q: total %d, want %d", query, got, want)
		}
	}
}

// A zero-count render at or past the end reads the final row but keeps its
// offset in the header; a one-row render there shows the final row's.
func TestRenderZeroCountPastEnd(t *testing.T) {
	h := searchTrackHandler()
	n := total(search(h, -1, "a")) // every title and file name has an A
	render := func(offset, count uint32) (uint32, uint32) {
		page := h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			proto.ArgI32(rootCtx), proto.ArgI32(offset), proto.ArgI32(count), proto.ArgI32(0), proto.ArgI32(n), proto.ArgI32(0)}})
		return page[0].Args[1].Int(), uint32(len(page) - 2)
	}
	if off, rows := render(n, 0); off != n || rows != 1 {
		t.Errorf("zero count at the end: header offset %d, %d rows; want %d and 1", off, rows, n)
	}
	if off, rows := render(n, 1); off != n-1 || rows != 1 {
		t.Errorf("one row at the end: header offset %d, %d rows; want %d and 1", off, rows, n-1)
	}
	if off, rows := render(0, 0); off != 0 || rows != 1 {
		t.Errorf("zero count at the start: header offset %d, %d rows; want 0 and 1", off, rows)
	}
}

// The declared length is checked against the query up to its first NUL, so
// a string field running on past the NUL doesn't make it too short.
func TestSearchLengthPadded(t *testing.T) {
	h := searchTrackHandler()
	q := utf16Arg("fixture")
	padded := proto.DBArg{Tag: proto.ArgString, Str: q.Str + string(make([]byte, 16))}
	r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1300, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(0), proto.ArgI32(uint32(len(q.Str))), padded, proto.ArgI32(0)}})
	if r[0].Type != proto.DBMsgSuccess || total(r) == 0 {
		t.Errorf("padded query: %+v, want its matches", r)
	}
}

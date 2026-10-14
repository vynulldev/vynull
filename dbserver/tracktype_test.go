// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// Context track-type rules, from the link-spec conformance suites
// context-track-types and context-track-type-families (rekordbox 7.2.19):
// rekordbox ignores browse requests whose context track type is 0x03 or 0x04,
// and sets the hot-cue auto-load bit (arg 10, 0x100) on track rows only when
// the context track type is 0x01.
func TestContextTrackTypeRules(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{Title: "Alpha", Artist: "A", AutoLoadHotCue: true, FileType: "mp3"})
	lib.AddTrack(&library.Track{Title: "Beta", Artist: "B", AutoLoadHotCue: true, FileType: "mp3"})

	ctx := func(tt uint32) proto.DBArg { return proto.ArgI32(0x01010300 | tt) }
	list := func(h *Handler, tt uint32) []*proto.DBMessage {
		return h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{ctx(tt), proto.ArgI32(0)}})
	}
	render := func(h *Handler, tt uint32) []*proto.DBMessage {
		return h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			ctx(tt), proto.ArgI32(0), proto.ArgI32(2), proto.ArgI32(0), proto.ArgI32(2), proto.ArgI32(0)}})
	}

	for _, tt := range []uint32{0x03, 0x04} {
		if got := list(&Handler{lib: lib}, tt); got != nil {
			t.Errorf("track type 0x%02x: got %d replies, want none", tt, len(got))
		}
	}

	for _, c := range []struct {
		tt   uint32
		want uint32
	}{{0x01, 0x100}, {0x00, 0}, {0x02, 0}, {0x05, 0}, {0xff, 0}} {
		h := &Handler{lib: lib}
		if got := list(h, c.tt); len(got) == 0 {
			t.Fatalf("track type 0x%02x: no reply to the track list", c.tt)
		}
		rows := 0
		for _, m := range render(h, c.tt) {
			if m.Type != 0x4101 { // menu item
				continue
			}
			rows++
			if got := m.Args[10].Int() & 0x100; got != c.want {
				t.Errorf("track type 0x%02x: row arg 10 bit 0x100 = %#x, want %#x", c.tt, got, c.want)
			}
		}
		if rows != 2 {
			t.Errorf("track type 0x%02x: rendered %d rows, want 2", c.tt, rows)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// A track row carries its converted key index in argument 11 only when the
// render's sixth argument is the KEY category (12), as decks send for their
// main lists. rekordbox leaves it zero for the 0 decks send for the preview
// locations and for a render with no sixth argument (link-spec
// device-capabilities render-5/6/8, and real-rekordbox captures of an NXS2).
func TestRenderKeyIndexOnlyForKeyCategory(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{Title: "One", Key: "Am", BPM: 120, FileType: "mp3"})
	h := &Handler{lib: lib}
	const ctx = 0x01010301

	for _, c := range []struct {
		name  string
		extra []uint32 // render arguments after [ctx, offset, count, 0, total]
		want  uint32
	}{
		{"five arguments", nil, 0},
		{"sixth argument 12", []uint32{12}, 15},
		{"sixth argument 0", []uint32{0}, 0},
		{"sixth argument 12 in its low word", []uint32{0x10000 | 12}, 15},
		{"another category", []uint32{1}, 0},
		{"eight arguments", []uint32{12, 1, 0}, 15},
	} {
		h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1004, Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0)}})
		args := []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0), proto.ArgI32(1), proto.ArgI32(0), proto.ArgI32(1)}
		for _, v := range c.extra {
			args = append(args, proto.ArgI32(v))
		}
		var rows []*proto.DBMessage
		for _, m := range h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: args}) {
			if m.Type == 0x4101 {
				rows = append(rows, m)
			}
		}
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", c.name, len(rows))
		}
		if got := rows[0].Args[11].Int(); got != c.want {
			t.Errorf("%s: argument 11 = %d, want %d", c.name, got, c.want)
		}
	}
}

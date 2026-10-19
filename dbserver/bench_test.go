// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/internal/benchlib"
	"github.com/vynulldev/vynull/proto"
)

// trackListCtx is a TRACK request's context: player 1, menu location 1,
// slot 3 (rekordbox), track type 1.
const trackListCtx = 0x01010301

// BenchmarkTrackList measures a deck opening TRACK on a 20k-track library:
// the 0x1004 request (which builds, sorts and filters the whole list) and
// the render of its first page of 12 rows. It runs once per sort a deck
// commonly picks: the default title order, a text sort and a numeric one.
func BenchmarkTrackList(b *testing.B) {
	lib := benchlib.Library(benchlib.Size)
	for _, bc := range []struct {
		name string
		sort uint32
	}{{"default", 0}, {"artist", 2}, {"bpm", 4}} {
		b.Run(bc.name, func(b *testing.B) {
			h := &Handler{lib: lib}
			request := &proto.DBMessage{TxID: 1, Type: proto.DBMsgGetTracks,
				Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(bc.sort)}}
			render := &proto.DBMessage{TxID: 1, Type: proto.DBMsgRenderMenu,
				Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(12),
					proto.ArgI32(0), proto.ArgI32(benchlib.Size), proto.ArgI32(0)}}
			h.Handle(request)
			if got := len(h.Handle(render)); got != 12+2 { // header, rows, footer
				b.Fatalf("render returned %d messages, want 14", got)
			}
			b.ReportAllocs()
			for b.Loop() {
				h.Handle(request)
				h.Handle(render)
			}
		})
	}
}

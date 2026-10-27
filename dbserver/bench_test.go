// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
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

// BenchmarkRenderPage measures a deck scrolling a list it has already
// queried: one 8-row page of a 27-entry playlist list, which the deck asks
// for at every step past the visible window.
func BenchmarkRenderPage(b *testing.B) {
	const ctx = 0x01010401 // player 1, main list, rekordbox slot
	h := &Handler{}
	var items []*menuItem
	for id := uint32(1); id <= 27; id++ {
		items = append(items, &menuItem{ID: id, Label1: "Playlist", ItemType: 0x0008})
	}
	h.setPendingAll(&proto.DBMessage{TxID: 1, Type: 0x1105,
		Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(0)}}, items)
	render := &proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu,
		Args: []proto.DBArg{proto.ArgI32(ctx), proto.ArgI32(5), proto.ArgI32(8),
			proto.ArgI32(0), proto.ArgI32(27), proto.ArgI32(0)}}
	if got := len(h.Handle(render)); got != 8+2 { // header, rows, footer
		b.Fatalf("render returned %d messages, want 10", got)
	}
	b.ReportAllocs()
	for b.Loop() {
		h.Handle(render)
	}
}

// BenchmarkKeyTracks measures a deck opening a key's tracks in the KEY menu
// (0x1214) on a 20k-track library, for 8A exact and its widest group.
func BenchmarkKeyTracks(b *testing.B) {
	h := &Handler{lib: benchlib.Library(benchlib.Size)}
	for _, bc := range []struct {
		name     string
		distance uint32
	}{{"exact", 0}, {"widest", 2}} {
		b.Run(bc.name, func(b *testing.B) {
			request := &proto.DBMessage{TxID: 1, Type: 0x1214, Args: []proto.DBArg{
				proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(15), proto.ArgI32(bc.distance)}}
			b.ReportAllocs()
			for b.Loop() {
				h.Handle(request)
			}
		})
	}
}

// BenchmarkWavePreview measures a deck's 0x2004 preview request for a track
// Vynull analysed, which carries its preview, and for an imported one, whose
// preview is built from its .DAT previews.
func BenchmarkWavePreview(b *testing.B) {
	store := analysis.NewStore()
	store.Set(1, &analysis.Result{WavePreview: make([]byte, 904)})
	store.Set(2, &analysis.Result{Imported: true, WavePreviewANLZ: make([]byte, 400), WaveTinyANLZ: make([]byte, 100)})
	h := &Handler{analysis: store}
	for _, bc := range []struct {
		name  string
		track uint32
	}{{"analysed", 1}, {"imported", 2}} {
		b.Run(bc.name, func(b *testing.B) {
			request := &proto.DBMessage{TxID: 1, Type: proto.DBMsgGetWavePreview,
				Args: []proto.DBArg{proto.ArgI32(0x01080401), proto.ArgI32(4), proto.ArgI32(bc.track), proto.ArgI32(0)}}
			b.ReportAllocs()
			for b.Loop() {
				h.Handle(request)
			}
		})
	}
}

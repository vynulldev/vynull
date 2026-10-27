// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// 0x2d04 is rekordbox's second specified-atom request: XDJ-AZ and XDJ-RX3
// decks use it for the .2EX 3-band waveforms (PWV6/PWV7). It is answered like
// 0x2c04, and every reply echoes the request's own kind, as rekordbox's do
// (link-spec ADJACENT_PAYLOAD_SERVICES).
func TestSpecifiedAtom2d04(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 48, Title: "Track"})
	store := analysis.NewStore()
	store.Set(48, &analysis.Result{BPM: 120, WavePreview3Band: make([]byte, 1200*3)})
	h := &Handler{lib: lib, analysis: store}

	fourcc := func(tag string) uint32 { // the deck sends the tag reversed
		return uint32(tag[3])<<24 | uint32(tag[2])<<16 | uint32(tag[1])<<8 | uint32(tag[0])
	}
	ask := func(kind uint16, tag string) *proto.DBMessage {
		resp := h.Handle(&proto.DBMessage{TxID: 0x2d, Type: kind, Args: []proto.DBArg{
			proto.ArgI32(0x01080401), proto.ArgI32(48), proto.ArgI32(fourcc(tag)), proto.ArgI32(0x00584532)}})
		if len(resp) != 1 {
			t.Fatalf("0x%04x %s: %d replies, want 1", kind, tag, len(resp))
		}
		return resp[0]
	}

	for _, kind := range []uint16{0x2c04, 0x2d04} {
		r := ask(kind, "PWV6")
		if r.Type != 0x4f02 || r.Args[0].Int() != uint32(kind) || r.Args[1].Int() != 0 || len(r.Args) < 4 || len(r.Args[3].Bytes) == 0 {
			t.Errorf("0x%04x PWV6: got %+v, want 0x4f02 echoing 0x%04x with the waveform", kind, r, kind)
		}
		if nf := ask(kind, "PQZZ"); nf.Args[0].Int() != uint32(kind) || nf.Args[1].Int() != 0x32 {
			t.Errorf("0x%04x unknown tag: got %+v, want not-found echoing 0x%04x", kind, nf, kind)
		}
	}
}

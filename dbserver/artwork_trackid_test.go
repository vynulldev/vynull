// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// 0x2103 asks for artwork by track ID. In library mode it must resolve to the
// track's artwork, not the artwork that happens to have the same number.
func TestArtworkByTrackIDLibrary(t *testing.T) {
	lib := library.New()
	other := []byte{0xff, 0xd8, 'o', 't', 'h', 'e', 'r'}
	mine := []byte{0xff, 0xd8, 'm', 'i', 'n', 'e'}
	otherID := lib.Artwork.Add("image/jpeg", other) // artwork 1
	mineID := lib.Artwork.Add("image/jpeg", mine)   // artwork 2
	lib.AddTrack(&library.Track{ID: otherID, Title: "Track 1", ArtID: mineID})
	h := &Handler{lib: lib}

	resp := h.Handle(&proto.DBMessage{TxID: 3, Type: 0x2103, Args: []proto.DBArg{proto.ArgI32(0x01080401), proto.ArgI32(otherID)}})
	if len(resp) != 1 || len(resp[0].Args) < 4 || !bytes.Equal(resp[0].Args[3].Bytes, mine) {
		t.Fatalf("0x2103 for track %d: want the track's artwork %d, got %+v", otherID, mineID, resp)
	}
}

// A track without artwork must get "not found" for 0x2103, never the artwork
// whose number equals its track ID (the deck would show another track's cover).
func TestArtworkByTrackIDWithoutCover(t *testing.T) {
	lib := library.New()
	strayID := lib.Artwork.Add("image/jpeg", []byte{0xff, 0xd8, 's', 't', 'r', 'a', 'y'}) // artwork 1
	lib.AddTrack(&library.Track{ID: strayID, Title: "No cover"})                          // track 1, ArtID 0
	h := &Handler{lib: lib}

	for _, id := range []uint32{strayID, 99} { // the coverless track, and an unknown one
		resp := h.Handle(&proto.DBMessage{TxID: 3, Type: 0x2103, Args: []proto.DBArg{proto.ArgI32(0x01080401), proto.ArgI32(id)}})
		if len(resp) != 1 || len(resp[0].Args) < 2 || resp[0].Args[1].Int() != 0x32 {
			t.Fatalf("0x2103 for track %d: want not-found (0x32), got %+v", id, resp)
		}
	}
}

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

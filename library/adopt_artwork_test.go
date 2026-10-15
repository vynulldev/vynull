// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"bytes"
	"testing"
)

// Artwork that analysis finds for a track must get its own artwork ID, not
// the track's: track 1's art was stored as artwork 1, overwriting (or, with
// the existence check, standing in for) whatever track owned artwork 1.
func TestAdoptArtworkUsesOwnID(t *testing.T) {
	lib := New()
	owned := []byte{0xff, 0xd8, 'o', 'w', 'n', 'e', 'd'}
	ownedID := lib.Artwork.Add("image/jpeg", owned) // artwork 1
	lib.AddTrack(&Track{ID: 2, Title: "Owner", ArtID: ownedID})
	lib.AddTrack(&Track{ID: ownedID, Title: "Analyzed"}) // track ID equals the artwork ID
	lib.AddTrack(&Track{ID: 3, Title: "Checked", ArtChecked: true})

	found := []byte{0xff, 0xd8, 'f', 'o', 'u', 'n', 'd'}
	lib.AdoptArtwork(ownedID, "image/jpeg", found)

	if got := lib.Artwork.Get(ownedID); got == nil || !bytes.Equal(got.Data, owned) {
		t.Fatalf("artwork %d was overwritten", ownedID)
	}
	tr := lib.Track(ownedID)
	if tr.ArtID == 0 || tr.ArtID == ownedID {
		t.Fatalf("analyzed track ArtID = %d, want a new artwork ID", tr.ArtID)
	}
	if got := lib.Artwork.Get(tr.ArtID); got == nil || !bytes.Equal(got.Data, found) {
		t.Errorf("analyzed track's artwork is not the found image")
	}

	// A track that already has artwork, or was checked and has none, keeps it.
	lib.AdoptArtwork(2, "image/jpeg", found)
	if lib.Track(2).ArtID != ownedID {
		t.Errorf("track with artwork was relinked to %d", lib.Track(2).ArtID)
	}
	lib.AdoptArtwork(3, "image/jpeg", found)
	if lib.Track(3).ArtID != 0 {
		t.Errorf("checked track got artwork %d", lib.Track(3).ArtID)
	}
}

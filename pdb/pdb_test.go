// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"testing"
)

func TestOpenPDB(t *testing.T) {
	path := "/media/usb/PIONEER/rekordbox/export.pdb"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("USB not mounted")
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if len(db.Tracks) == 0 {
		t.Fatal("no tracks found")
	}

	for _, track := range db.Tracks {
		t.Logf("Track %d: %q by %q [%s] path=%s", track.ID, track.Title, track.Artist, track.Album, track.FilePath)
	}

	t.Logf("Artists: %v", db.Artists)
	t.Logf("Albums: %v", db.Albums)
	t.Logf("Genres: %v", db.Genres)
	t.Logf("Keys: %v", db.Keys)
}

// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Reimporting a track whose artist changed files it under the new artist: an
// importer's merge into an existing track goes through the library's indexes.
func TestReimportMovesTrackToNewArtist(t *testing.T) {
	dir := t.TempDir()
	importXML := func(lib *library.Library, artist string) {
		t.Helper()
		doc := `<?xml version="1.0" encoding="UTF-8"?>
<DJ_PLAYLISTS Version="1.0.0"><PRODUCT Name="rekordbox" Version="7.2.19"/>
<COLLECTION Entries="1"><TRACK TrackID="1" Name="Song" Artist="` + artist + `" Location="file://localhost/music/a.mp3"/></COLLECTION>
<PLAYLISTS><NODE Type="0" Name="ROOT" Count="0"/></PLAYLISTS></DJ_PLAYLISTS>`
		path := filepath.Join(dir, artist+".xml")
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := library.ImportRekordboxXML(lib, path); err != nil {
			t.Fatal(err)
		}
	}
	lib := library.New()
	importXML(lib, "Old")
	importXML(lib, "New")
	if len(lib.Tracks()) != 1 {
		t.Fatalf("%d tracks after the reimport, want 1", len(lib.Tracks()))
	}
	if len(lib.TracksByArtist("Old")) != 0 || len(lib.TracksByArtist("New")) != 1 || !slices.Equal(lib.Artists(), []string{"New"}) {
		t.Errorf("artist Old %d, New %d, artists %v; want 0, 1, [New]",
			len(lib.TracksByArtist("Old")), len(lib.TracksByArtist("New")), lib.Artists())
	}
}

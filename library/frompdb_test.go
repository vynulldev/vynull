// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/pdb"
)

// FromPDB carries every field the dbserver serves an export's tracks with,
// and the export's own category IDs.
func TestFromPDBCarriesTheExport(t *testing.T) {
	lib := FromPDB([]*pdb.Track{{
		ID: 7, Title: "T", Artist: "A", Album: "B", Genre: "G", Label: "L", Key: "8A",
		ArtistID: 11, AlbumID: 12, GenreID: 13, LabelID: 14, KeyID: 15,
		Rating: 4, ColorID: 3, PlayCount: 9, DateAdded: "2022-07-20", FileSize: 1234,
		SampleRate: 44100, SampleDepth: 16, DiscNumber: 2, Tempo: 12800, Duration: 300,
		FilePath: "/Contents/t.mp3", AnalyzePath: "/PIONEER/USBANLZ/P000/0000/ANLZ0000.DAT",
	}}, "/usb")
	tr := lib.Track(7)
	if tr == nil {
		t.Fatal("track 7 missing")
	}
	type fields struct {
		Rating, ColorID                             uint8
		PlayCount, SampleRate, SampleDepth, DiscNum int
		KeyID                                       uint32
		FileSize                                    int64
		BPM                                         float64
		FilePath, FileType, AnalyzePath             string
		AutoLoadHotCue                              bool
	}
	want := fields{4, 3, 9, 44100, 16, 2, 15, 1234, 128, "/usb/Contents/t.mp3", "mp3", "/PIONEER/USBANLZ/P000/0000/ANLZ0000.DAT", true}
	got := fields{tr.Rating, tr.ColorID, tr.PlayCount, tr.SampleRate, tr.SampleDepth, tr.DiscNum, tr.KeyID, tr.FileSize, tr.BPM,
		tr.FilePath, tr.FileType, tr.AnalyzePath, tr.AutoLoadHotCue}
	if got != want {
		t.Errorf("track %+v\nwant  %+v", got, want)
	}
	if !tr.DateAdded.Equal(time.Date(2022, 7, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date added %v", tr.DateAdded)
	}
	for kind, id := range map[string]uint32{"artist": 11, "album": 12, "genre": 13, "label": 14, "key": 15} {
		name := map[string]string{"artist": "A", "album": "B", "genre": "G", "label": "L", "key": "8A"}[kind]
		if got := lib.CategoryID(kind, name); got != id {
			t.Errorf("%s ID %d, want %d", kind, got, id)
		}
	}
}

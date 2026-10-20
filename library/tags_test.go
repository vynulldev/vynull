// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/internal/tagtest"
	"github.com/vynulldev/vynull/library"
)

var fullTags = map[string]string{
	"TIT2": "Song", "TPE1": "Artist", "TALB": "Album", "TCON": "House", "TYER": "2024",
	"TRCK": "3", "TPOS": "2", "COMM": "A comment", "TPUB": "Label", "TPE4": "Remixer",
	"TOPE": "Original", "TSST": "Extended Mix",
}

// ApplyTo replaces the main fields, sets the optional ones only when the
// file has them, and falls back to the file name for a missing title.
func TestFileTagsApplyTo(t *testing.T) {
	dir := t.TempDir()
	full, sparse := filepath.Join(dir, "full.mp3"), filepath.Join(dir, "Sparse Name.mp3")
	tagtest.WriteMP3(t, full, fullTags)
	tagtest.WriteMP3(t, sparse, map[string]string{"TPE1": "Only Artist"})

	ft, err := library.ReadFileTags(full)
	if err != nil {
		t.Fatal(err)
	}
	var tr library.Track
	ft.ApplyTo(&tr)
	want := library.Track{Title: "Song", Artist: "Artist", Album: "Album", Genre: "House", Year: 2024,
		TrackNum: 3, DiscNum: 2, Comment: "A comment", Label: "Label", Remixer: "Remixer",
		OriginalArtist: "Original", MixName: "Extended Mix"}
	if tr.Title != want.Title || tr.Artist != want.Artist || tr.Album != want.Album || tr.Genre != want.Genre ||
		tr.Year != want.Year || tr.TrackNum != want.TrackNum || tr.DiscNum != want.DiscNum || tr.Comment != want.Comment ||
		tr.Label != want.Label || tr.Remixer != want.Remixer || tr.OriginalArtist != want.OriginalArtist || tr.MixName != want.MixName {
		t.Errorf("applied %+v\nwant     %+v", tr, want)
	}

	ft, err = library.ReadFileTags(sparse)
	if err != nil {
		t.Fatal(err)
	}
	tr = library.Track{FilePath: sparse, Title: "Old", Album: "Old Album", Label: "Kept", MixName: "Kept Mix"}
	ft.ApplyTo(&tr)
	if tr.Artist != "Only Artist" || tr.Album != "" || tr.Label != "Kept" || tr.MixName != "Kept Mix" || tr.Title != "Sparse Name" {
		t.Errorf("sparse tags: %+v; want artist set, album cleared, label and mix kept, title from the file name", tr)
	}
	if _, err := library.ReadFileTags(filepath.Join(dir, "missing.mp3")); err == nil {
		t.Error("a missing file read without error")
	}
}

// Scanning a folder reads the same tags as adding a file: label, remixer,
// original artist, mix name, comment and disc number included.
func TestScanReadsAllTags(t *testing.T) {
	dir := t.TempDir()
	tagtest.WriteMP3(t, filepath.Join(dir, "a.mp3"), fullTags)
	lib, err := library.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := lib.Tracks()
	if len(ts) != 1 {
		t.Fatalf("%d tracks scanned, want 1", len(ts))
	}
	if tr := ts[0]; tr.Label != "Label" || tr.Remixer != "Remixer" || tr.OriginalArtist != "Original" ||
		tr.MixName != "Extended Mix" || tr.Comment != "A comment" || tr.DiscNum != 2 {
		t.Errorf("scanned %+v; want every tag read", tr)
	}
}

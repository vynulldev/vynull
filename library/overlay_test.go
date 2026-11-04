// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
)

// export is a served USB's tracks, with the genre a fresh export may change.
func export(genre string) []*pdb.Track {
	return []*pdb.Track{
		{ID: 1, Title: "One", Artist: "A", Genre: genre, Comment: "first", FilePath: "/Contents/A/one.mp3", Tempo: 12000},
		{ID: 2, Title: "Two", Artist: "B", Genre: genre, Comment: "second", FilePath: "/Contents/B/two.mp3"},
		{ID: 3, Title: "Three", Artist: "C", Genre: genre, FilePath: "/Contents/C/three.mp3"},
	}
}

// start builds the library from the export and applies the overlay, as the
// daemon does at each start.
func start(tracks []*pdb.Track, music, overlay string) *library.Library {
	lib := library.FromPDB(tracks, music)
	lib.UseOverlay(overlay, music)
	return lib
}

func readOverlay(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]json.RawMessage
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Edits to a served USB's tracks, a relink, a track added on top and a
// deletion all survive a restart, while the fields nobody edited follow a
// fresh export.
func TestOverlayKeepsEditsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	music, overlay := filepath.Join(dir, "usb"), filepath.Join(dir, "data", "music-dir-edits.json")
	relinked := filepath.Join(dir, "elsewhere", "three.flac")

	lib := start(export("House"), music, overlay)
	lib.Update(1, func(tr *library.Track) error { tr.Title, tr.Rating = "One (edit)", 5; return nil })
	lib.Update(2, func(tr *library.Track) error { tr.Comment = ""; return nil })
	lib.Update(3, func(tr *library.Track) error { tr.FilePath = relinked; return nil })
	added := lib.AddTrack(&library.Track{Title: "Added", Artist: "D", FilePath: filepath.Join(dir, "elsewhere", "added.mp3")})
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}

	var edits map[string]map[string]json.RawMessage
	json.Unmarshal(readOverlay(t, overlay)["edits"], &edits)
	if got := slices.Sorted(func(yield func(string) bool) {
		for k := range edits["Contents/A/one.mp3"] {
			if !yield(k) {
				return
			}
		}
	}); !slices.Equal(got, []string{"rating", "title"}) {
		t.Errorf("track one's recorded fields %v, want only [rating title]", got)
	}

	lib = start(export("Techno"), music, overlay)
	one, two, three, add := lib.Track(1), lib.Track(2), lib.Track(3), lib.Track(added)
	if one.Title != "One (edit)" || one.Rating != 5 || one.Genre != "Techno" || one.Comment != "first" {
		t.Errorf("track one: %q rating %d genre %q comment %q; want the edits, and the export's genre and comment",
			one.Title, one.Rating, one.Genre, one.Comment)
	}
	if two.Comment != "" || two.Genre != "Techno" {
		t.Errorf("track two: comment %q genre %q; want the comment cleared and the export's genre", two.Comment, two.Genre)
	}
	if three.FilePath != relinked {
		t.Errorf("track three at %q, want the relink %q", three.FilePath, relinked)
	}
	if add == nil || add.Title != "Added" {
		t.Fatalf("added track %d: %+v, want it back", added, add)
	}
	if !slices.Contains(lib.Artists(), "D") {
		t.Errorf("artists %v: the added track's artist is missing", lib.Artists())
	}

	lib.RemoveTracks([]uint32{2})
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}
	lib = start(export("Techno"), music, overlay)
	defer lib.Close()
	if lib.Track(2) != nil || lib.Track(1) == nil || lib.Track(added) == nil {
		t.Errorf("after deleting track two: tracks %v, want 1, 3 and the added one", ids(lib.Tracks()))
	}
}

// Entries for paths the music dir doesn't hold (another stick served from
// the same data dir) are kept when the overlay is saved, and apply once the
// path is back. Unknown fields are skipped.
func TestOverlayKeepsOtherSticksEntries(t *testing.T) {
	dir := t.TempDir()
	music, overlay := filepath.Join(dir, "usb"), filepath.Join(dir, "music-dir-edits.json")
	os.WriteFile(overlay, []byte(`{
		"edits": {"Contents/Z/other.mp3": {"title": "Other (edit)", "no_such_field": 1}},
		"removed": ["Contents/Z/gone.mp3"]}`), 0o644)

	lib := start(export("House"), music, overlay)
	lib.Update(1, func(tr *library.Track) error { tr.Rating = 3; return nil })
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}
	f := readOverlay(t, overlay)
	var edits map[string]map[string]json.RawMessage
	var removed []string
	json.Unmarshal(f["edits"], &edits)
	json.Unmarshal(f["removed"], &removed)
	if edits["Contents/Z/other.mp3"] == nil || edits["Contents/A/one.mp3"] == nil || !slices.Equal(removed, []string{"Contents/Z/gone.mp3"}) {
		t.Fatalf("saved edits %v, removed %v; want the other stick's entries kept beside this one's", edits, removed)
	}

	other := append(export("House"),
		&pdb.Track{ID: 7, Title: "Other", FilePath: "/Contents/Z/other.mp3"},
		&pdb.Track{ID: 8, Title: "Gone", FilePath: "/Contents/Z/gone.mp3"})
	lib = start(other, music, overlay)
	defer lib.Close()
	if got := lib.Track(7); got == nil || got.Title != "Other (edit)" {
		t.Errorf("track 7: %+v, want the kept edit applied", got)
	}
	if lib.Track(8) != nil {
		t.Error("track 8 is back, want the kept removal applied")
	}
}

// A track added on top keeps its ID unless the music dir now holds that ID
// (a fresh export renumbered), and is dropped once its file is in the music
// dir itself.
func TestOverlayAddedTracks(t *testing.T) {
	dir := t.TempDir()
	music, overlay := filepath.Join(dir, "usb"), filepath.Join(dir, "music-dir-edits.json")
	f, _ := json.Marshal(map[string]any{"added": []*library.Track{
		{ID: 2, Title: "Clashing", FilePath: filepath.Join(dir, "elsewhere", "clash.mp3")},
		{ID: 9, Title: "Now On The Stick", FilePath: filepath.Join(music, "Contents", "A", "one.mp3")},
		{ID: 10, Title: "Kept", FilePath: filepath.Join(dir, "elsewhere", "kept.mp3")},
	}})
	os.WriteFile(overlay, f, 0o644)

	lib := start(export("House"), music, overlay)
	defer lib.Close()
	if two := lib.Track(2); two == nil || two.Title != "Two" {
		t.Errorf("track 2: %+v, want the export's", two)
	}
	clash := lib.TrackByPath(filepath.Join(dir, "elsewhere", "clash.mp3"))
	if clash == nil || clash.ID == 2 {
		t.Errorf("clashing added track: %+v, want it under a new ID", clash)
	}
	if one := lib.TrackByPath(filepath.Join(music, "Contents", "A", "one.mp3")); one == nil || one.ID != 1 || lib.Track(9) != nil {
		t.Errorf("the added copy of a stick file: track %+v, track 9 %+v; want only the stick's", one, lib.Track(9))
	}
	if kept := lib.Track(10); kept == nil || kept.Title != "Kept" {
		t.Errorf("track 10: %+v, want it kept under its ID", kept)
	}
}

// What analysis fills in (a BPM, a duration, a key) is the music dir's, not
// an edit: the overlay doesn't record it, so a fresh export's values apply.
// A user's BPM override is an edit, and is kept.
func TestOverlaySkipsAnalysis(t *testing.T) {
	dir := t.TempDir()
	music, overlay := filepath.Join(dir, "usb"), filepath.Join(dir, "music-dir-edits.json")
	lib := start(export("House"), music, overlay)
	lib.ApplyAnalysis(2, library.AnalysisFacts{BPM: 96, Duration: 180, KeyCamelot: "8A"})
	lib.ApplyAnalysis(1, library.AnalysisFacts{BPM: 120, Duration: 200, KeyCamelot: "5A"})
	lib.Update(1, func(tr *library.Track) error { tr.DetectedBPM, tr.BPM = tr.BPM, 60; return nil })
	if two := lib.Track(2); two.BPM != 96 || two.Key != "8A" {
		t.Fatalf("track two: BPM %g key %q, want the analysis applied", two.BPM, two.Key)
	}
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}
	var edits map[string]map[string]json.RawMessage
	json.Unmarshal(readOverlay(t, overlay)["edits"], &edits)
	if edits["Contents/B/two.mp3"] != nil {
		t.Errorf("analysis recorded as an edit: %s", edits["Contents/B/two.mp3"])
	}
	if one := edits["Contents/A/one.mp3"]; string(one["bpm"]) != "60" || string(one["detected_bpm"]) != "120" {
		t.Errorf("track one's override recorded as %v, want bpm 60 and detected_bpm 120", one)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func sortIDs(ms *MenuStore) []uint32 {
	var ids []uint32
	for _, e := range ms.SortMenu() {
		ids = append(ids, e.ID)
	}
	return ids
}

// The SORT menu starts as rekordbox's default, adopts an imported library's
// order and second column, and survives a reload.
func TestMenuStoreSorts(t *testing.T) {
	dir := t.TempDir()
	ms := NewMenuStore(dir)
	if want := []uint32{0, 1, 2, 3, 4, 5, 12, 10, 6, 17, 16}; !slices.Equal(sortIDs(ms), want) {
		t.Errorf("default sort menu %v, want %v", sortIDs(ms), want)
	}
	if got := len(ms.Sorts()); got != 17 {
		t.Errorf("%d sorts stored, want all 17", got)
	}

	ms.ImportSorts([]library.SortImport{
		{ID: 0, Visible: true}, {ID: 17, Visible: true}, {ID: 2, Visible: true},
		{ID: 12, Visible: true, Selected: true}, {ID: 7},
	})
	if want := []uint32{0, 17, 2, 12}; !slices.Equal(sortIDs(ms), want) {
		t.Errorf("imported sort menu %v, want %v", sortIDs(ms), want)
	}
	if ms.TrackDetail() != "key" {
		t.Errorf("track detail %q, want key (the selected sort)", ms.TrackDetail())
	}

	if again := NewMenuStore(dir); !slices.Equal(sortIDs(again), []uint32{0, 17, 2, 12}) || again.TrackDetail() != "key" {
		t.Errorf("after reload: sorts %v, detail %q", sortIDs(again), again.TrackDetail())
	}

	if err := ms.ReplaceSorts([]SortItem{{ID: 7}, {ID: 99, Visible: true}}); err == nil {
		t.Errorf("a sort menu with nothing shown was accepted")
	}
	if err := ms.ReplaceSorts([]SortItem{{ID: 5, Visible: true}, {ID: 4, Visible: true}}); err != nil || !slices.Equal(sortIDs(ms), []uint32{5, 4}) {
		t.Errorf("ReplaceSorts: %v, menu %v", err, sortIDs(ms))
	}
}

// A library with no selected column gets title-only rows, as rekordbox
// serves them.
func TestMenuStoreImportWithoutSelection(t *testing.T) {
	ms := NewMenuStore(t.TempDir())
	ms.ImportSorts([]library.SortImport{{ID: 0, Visible: true}, {ID: 1, Visible: true}})
	if ms.TrackDetail() != "not_specified" {
		t.Errorf("track detail %q, want not_specified", ms.TrackDetail())
	}
}

// An imported SORT menu's selected row names the track lists' second column,
// every column rekordbox can select included; with two selected rows the
// first in table order wins, as in rekordbox (link-spec
// multiple-secondary-selections), and a selected sort that names no column
// leaves the rows title-only.
func TestImportedTrackColumn(t *testing.T) {
	for _, c := range []struct {
		selected []uint32
		want     string
	}{
		{[]uint32{7}, "comments"}, {[]uint32{8}, "time"}, {[]uint32{11}, "original_artist"},
		{[]uint32{16}, "dj_play_count"}, {[]uint32{17}, "date_added"}, {[]uint32{12}, "key"},
		{[]uint32{7, 12}, "comments"}, {nil, "not_specified"}, {[]uint32{0}, "not_specified"},
	} {
		var imp []library.SortImport
		for _, id := range []uint32{0, 7, 8, 11, 12, 16, 17} {
			imp = append(imp, library.SortImport{ID: id, Visible: true, Selected: slices.Contains(c.selected, id)})
		}
		if got := ImportedTrackColumn(imp); got != c.want {
			t.Errorf("selected %v: column %q, want %q", c.selected, got, c.want)
		}
	}
}

func visibleKeys(ms *MenuStore) []string {
	var keys []string
	for _, m := range ms.Visible() {
		keys = append(keys, m.Key)
	}
	return keys
}

// An imported root menu takes the library's order and shown categories,
// keeps Locked ones shown and a label the user changed, follows with the
// categories the library has no row for, hidden, and survives a reload.
func TestMenuStoreImportCategories(t *testing.T) {
	dir := t.TempDir()
	ms := NewMenuStore(dir)
	items := ms.All()
	for i := range items {
		if items[i].Key == "bpm" {
			items[i].Label = "TEMPO"
		}
	}
	if err := ms.Replace(items); err != nil {
		t.Fatal(err)
	}
	ms.ImportCategories([]library.CategoryImport{
		{MenuItem: 5, Visible: true},  // BPM
		{MenuItem: 27, Visible: true}, // MATCHING
		{MenuItem: 4, Visible: false}, // TRACK: locked, stays shown
		{MenuItem: 2, Visible: false}, // ARTIST
		{MenuItem: 11, Visible: true}, // KEY
		{MenuItem: 99, Visible: true}, // unknown
	})
	want := []string{"bpm", "matching", "track", "key"}
	if got := visibleKeys(ms); !slices.Equal(got[:4], want) {
		t.Errorf("shown %v, want %v first", got, want)
	}
	for _, m := range ms.All() {
		switch {
		case m.Key == "artist" && m.Visible:
			t.Errorf("artist shown, want hidden")
		case m.Key == "bpm" && m.Label != "TEMPO":
			t.Errorf("bpm label %q, want the user's TEMPO", m.Label)
		case m.Key == "genre" && m.Visible:
			t.Errorf("genre (no row in the import) shown, want hidden")
		}
	}
	if again := NewMenuStore(dir); !slices.Equal(visibleKeys(again), visibleKeys(ms)) {
		t.Errorf("after reload %v, want %v", visibleKeys(again), visibleKeys(ms))
	}
}

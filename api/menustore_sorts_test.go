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

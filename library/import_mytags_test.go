// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"slices"
	"testing"
)

// MyTag import keeps every live column and tag in rekordbox's order (columns
// by Seq, then each column's tags by Seq), including tags no track carries and
// empty columns. Unused tags were dropped: the importer took "no tracks" to
// mean "a column".
func TestImportMyTags(t *testing.T) {
	lib := New()
	dump := &MasterDBDump{
		Tracks: []masterDBTrack{{ID: "100", Title: "One", FilePath: "/music/one.mp3"}},
		Tags: []MasterDBTag{
			{ID: "2", Name: "Situation", ParentID: "root", Seq: 2, IsCategory: true},
			{ID: "1", Name: "Genre", ParentID: "root", Seq: 1, IsCategory: true},
			{ID: "4", Name: "Untitled Column", ParentID: "root", Seq: 4}, // older dump: no flag
			{ID: "11", Name: "Deep House", ParentID: "1", Seq: 4, TrackIDs: []string{"100"}},
			{ID: "12", Name: "Acid House", ParentID: "1", Seq: 3}, // unused
			{ID: "13", Name: "Trap", ParentID: "1", Seq: 1, TrackIDs: []string{"100"}},
			{ID: "21", Name: "Intro", ParentID: "2", Seq: 9},
		},
	}
	bundle, err := ImportMasterDBDump(lib, dump)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Genre", "Situation", "Untitled Column"}; !slices.Equal(bundle.TagCategories, want) {
		t.Errorf("columns = %v, want %v", bundle.TagCategories, want)
	}
	var names, cats []string
	for _, tg := range bundle.Tags {
		names = append(names, tg.Name)
		cats = append(cats, tg.Category)
	}
	if want := []string{"Trap", "Acid House", "Deep House", "Intro"}; !slices.Equal(names, want) {
		t.Errorf("tags = %v, want %v", names, want)
	}
	if want := []string{"Genre", "Genre", "Genre", "Situation"}; !slices.Equal(cats, want) {
		t.Errorf("tag columns = %v, want %v", cats, want)
	}
	if len(bundle.Tags[1].TrackIDs) != 0 || len(bundle.Tags[0].TrackIDs) != 1 {
		t.Errorf("track assignments: %+v", bundle.Tags)
	}
}

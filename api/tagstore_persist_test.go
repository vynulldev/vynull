// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"slices"
	"testing"
)

// Tags, categories and assignments round-trip through Flush, and a deleted
// tag leaves the tracks that carried it.
func TestTagStorePersists(t *testing.T) {
	dir := t.TempDir()
	ts := NewTagStore(dir)
	cat, _ := ts.CreateCategory("Genre")
	house, _ := ts.CreateTag("House", cat)
	techno, _ := ts.CreateTag("Techno", cat)
	ts.SetTagsForTrack(1, []uint32{house, techno})
	ts.SetTagsForTrack(2, []uint32{techno})
	ts.DeleteTag(techno)
	if err := ts.Close(); err != nil {
		t.Fatal(err)
	}

	got := NewTagStore(dir)
	if tags := got.GetAllTags(); len(tags) != 1 || tags[0].Name != "House" || tags[0].CategoryID != cat {
		t.Errorf("tags after reload: %+v", tags)
	}
	if ids := got.TrackTagIDs(1); !slices.Equal(ids, []uint32{house}) {
		t.Errorf("track 1 tags after reload: %v, want [%d]", ids, house)
	}
	if ids := got.TrackTagIDs(2); len(ids) != 0 {
		t.Errorf("track 2 kept the deleted tag: %v", ids)
	}
	if next, _ := got.CreateTag("Disco", 0); next != techno+1 {
		t.Errorf("next tag ID %d, want %d", next, techno+1)
	}
}

// The store keeps its own copy of a track's tag list: the writer encodes it
// outside the lock, so neither the caller nor DeleteTag may edit it in place.
func TestTagStoreTrackTagsNotShared(t *testing.T) {
	ts := NewTagStore(t.TempDir())
	defer ts.Close()
	a, _ := ts.CreateTag("A", 0)
	b, _ := ts.CreateTag("B", 0)
	ids := []uint32{a, b}
	ts.SetTagsForTrack(1, ids)
	ids[0] = 99
	if got := ts.TrackTagIDs(1); !slices.Equal(got, []uint32{a, b}) {
		t.Fatalf("the caller's later edit reached the store: %v", got)
	}

	ts.mu.RLock()
	before := ts.trackTags[1]
	ts.mu.RUnlock()
	ts.DeleteTag(a)
	if !slices.Equal(before, []uint32{a, b}) {
		t.Errorf("DeleteTag edited the stored list in place: %v", before)
	}
}

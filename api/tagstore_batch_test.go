// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// BeginBatch/EndBatch must defer all tag writes to a single flush, so a
// bulk import doesn't rewrite the JSON files once per assignment (review #4).
func TestTagStoreBatchSave(t *testing.T) {
	dir := t.TempDir()
	ts := NewTagStore(dir)
	id, err := ts.CreateTag("House", 0) // persists tags.json before the batch
	if err != nil {
		t.Fatal(err)
	}

	ts.BeginBatch()
	ts.SetTagsForTrack(1, []uint32{id})

	// Mid-batch: a fresh store loaded from disk must NOT see the assignments —
	// proves the per-mutation writes were deferred.
	mid := NewTagStore(dir)
	if got := len(mid.GetTagsForTrack(1)); got != 0 {
		t.Errorf("mid-batch: track tags should not be persisted yet, got %d", got)
	}

	ts.EndBatch()

	// After EndBatch: a fresh store sees everything (one flush persisted all).
	after := NewTagStore(dir)
	if got := after.GetTagsForTrack(1); len(got) != 1 || got[0].Name != "House" {
		t.Errorf("after EndBatch: expected [House], got %+v", got)
	}
}

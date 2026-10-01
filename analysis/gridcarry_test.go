// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/gob"
	"os"
	"testing"
)

// TestGridEditSurvivesCacheBump pins the contract that a user-edited beat
// grid survives a cacheVersion bump: the stale entry is re-analyzed for
// fresh waveforms, but the hand-fixed grid is carried over instead of
// being reverted to the detector's (wrong) phase. Before this, every bump
// silently deleted all grid edits.
func TestGridEditSurvivesCacheBump(t *testing.T) {
	dir := t.TempDir()
	s := NewStoreWithCache(dir)
	const fp = "/music/some-track.flac"
	s.SetPath(7, fp)

	// A previous-version cache entry carrying a user-edited grid.
	stale := &Result{
		CacheVersion:  cacheVersion - 1,
		GridEdited:    true,
		BPM:           122,
		Beats:         []float64{0, 491.8, 983.6},
		DownbeatIndex: 0,
	}
	f, err := os.Create(s.cacheFile(fp))
	if err != nil {
		t.Fatal(err)
	}
	if err := gob.NewEncoder(f).Encode(stale); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Get must treat it as a miss (forcing re-analysis), but keep the file
	// so the edit also survives a restart before re-analysis happens.
	if r := s.Get(7); r != nil {
		t.Fatalf("stale entry served: %+v", r)
	}
	if _, err := os.Stat(s.cacheFile(fp)); err != nil {
		t.Fatal("stale grid-edited cache file was deleted — edit would not survive a restart")
	}

	// Fresh analysis arrives with the detector's (wrong) grid: the edit
	// must win.
	fresh := &Result{CacheVersion: cacheVersion, BPM: 122, Beats: []float64{302.4, 794.2}, DownbeatIndex: 1}
	s.Set(7, fresh)
	got := s.Get(7)
	if got == nil || !got.GridEdited || got.Beats[0] != 0 || got.DownbeatIndex != 0 {
		t.Fatalf("edited grid not re-applied: %+v", got)
	}

	// And the merged result must be what persisted: a second store sees it.
	s2 := NewStoreWithCache(dir)
	s2.SetPath(7, fp)
	got2 := s2.Get(7)
	if got2 == nil || !got2.GridEdited || got2.Beats[0] != 0 {
		t.Fatalf("persisted result lost the edit: %+v", got2)
	}
}

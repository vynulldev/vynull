// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/gob"
	"os"
	"testing"
)

// writeCache writes r as track id's on-disk cache entry and returns a fresh
// store (nothing in memory) that will read it.
func writeCache(t *testing.T, dir string, id uint32, r *Result) *Store {
	t.Helper()
	s := NewStoreWithCache(dir)
	fp := "/music/track.flac"
	s.SetPath(id, fp)
	f, err := os.Create(s.cacheFile(fp))
	if err != nil {
		t.Fatal(err)
	}
	if err := gob.NewEncoder(f).Encode(r); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return s
}

// Imported rekordbox analysis and locked tracks survive a cache-version bump
// and a tempo-range change; a plain detected grid is still discarded so it
// re-analyzes.
func TestStaleCacheKeepsImportedAndLocked(t *testing.T) {
	defer SetTempoRange(0, 0)
	grid := []float64{20, 488.8, 957.5}
	for _, tc := range []struct {
		name   string
		r      Result
		locked bool
		tempo  bool // also change the tempo range
		keep   bool
	}{
		{"imported, version bump", Result{CacheVersion: cacheVersion - 1, Imported: true, BPM: 128, Beats: grid}, false, false, true},
		{"imported, tempo range set", Result{CacheVersion: cacheVersion, Imported: true, BPM: 128, Beats: grid}, false, true, true},
		{"locked detected grid, version bump", Result{CacheVersion: cacheVersion - 1, GridDetected: true, BPM: 128, Beats: grid}, true, false, true},
		{"unlocked detected grid, version bump", Result{CacheVersion: cacheVersion - 1, GridDetected: true, BPM: 128, Beats: grid}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetTempoRange(0, 0)
			if tc.tempo {
				SetTempoRange(160, 185)
			}
			dir := t.TempDir()
			r := tc.r
			s := writeCache(t, dir, 7, &r)
			s.SetKeep(func(id uint32) bool { return tc.locked && id == 7 })
			got := s.Get(7)
			if tc.keep {
				if got == nil || got.Beats[0] != 20 {
					t.Fatalf("cached analysis not kept: %+v", got)
				}
				if _, err := os.Stat(s.cacheFile("/music/track.flac")); err != nil {
					t.Fatal("kept cache file was deleted")
				}
			} else if got != nil {
				t.Fatalf("stale detected grid served: %+v", got)
			}
		})
	}
}

// Results cached since version 30 but before the Imported flag are still
// recognised as imported: their grid is neither detected nor hand-edited.
func TestIsImportedFallback(t *testing.T) {
	grid := []float64{20, 488.8}
	for _, tc := range []struct {
		name string
		r    Result
		want bool
	}{
		{"flagged", Result{Imported: true}, true},
		{"v30 grid, not detected or edited", Result{CacheVersion: 30, Beats: grid}, true},
		{"v30 detected grid", Result{CacheVersion: 30, GridDetected: true, Beats: grid}, false},
		{"v30 hand-edited grid", Result{CacheVersion: 30, GridEdited: true, Beats: grid}, false},
		{"v30 without a grid", Result{CacheVersion: 30}, false},
		{"pre-v30 grid (ambiguous)", Result{CacheVersion: 29, Beats: grid}, false},
	} {
		if got := tc.r.isImported(); got != tc.want {
			t.Errorf("%s: isImported = %v, want %v", tc.name, got, tc.want)
		}
	}
}

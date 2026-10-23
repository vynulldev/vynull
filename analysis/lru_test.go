// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"fmt"
	"testing"
)

// lruStore returns a store with a cache directory and a budget of about three
// of the results bigResult makes.
func lruStore(t *testing.T) *Store {
	s := NewStoreWithCache(t.TempDir())
	s.MaxResultBytes = 3*resultSize(bigResult(0)) + 100
	return s
}

func bigResult(bpm float64) *Result {
	return &Result{CacheVersion: cacheVersion, BPM: bpm, WaveDetail: make([]byte, 100_000)}
}

// put stores a result for a track that has a cache file.
func put(s *Store, id uint32) {
	s.SetPath(id, fmt.Sprintf("/music/%d.flac", id))
	s.Set(id, bigResult(float64(id)))
}

func inMemory(s *Store, id uint32) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.results[id] != nil
}

// Past the budget the least recently used results leave memory, and come back
// from their cache file when asked for.
func TestResultsStayWithinBudget(t *testing.T) {
	s := lruStore(t)
	for id := uint32(1); id <= 5; id++ {
		put(s, id)
	}
	if n := s.Count(); n != 3 {
		t.Fatalf("%d results in memory, want 3", n)
	}
	if inMemory(s, 1) || inMemory(s, 2) || !inMemory(s, 5) {
		t.Fatal("not the least recently used results that left")
	}
	if r := s.Get(1); r == nil || r.BPM != 1 {
		t.Fatalf("evicted result reloaded as %+v, want track 1's", r)
	}
	if !inMemory(s, 1) || inMemory(s, 3) {
		t.Error("the reloaded result should be most recent, and the oldest left")
	}
}

// Reading a result makes it recent, so it outlasts results stored after it.
func TestRecentlyReadResultStays(t *testing.T) {
	s := lruStore(t)
	for id := uint32(1); id <= 3; id++ {
		put(s, id)
	}
	s.Get(1)
	put(s, 4)
	if !inMemory(s, 1) || inMemory(s, 2) {
		t.Error("track 1 was read last, so track 2 should have left instead")
	}
}

// A result that has no cache file can't be loaded again, so it stays even
// over the budget.
func TestResultWithoutCacheFileStays(t *testing.T) {
	s := lruStore(t)
	s.Set(100, bigResult(100)) // no path: nowhere to save it
	for id := uint32(1); id <= 5; id++ {
		put(s, id)
	}
	if !inMemory(s, 100) {
		t.Error("the result without a cache file left memory")
	}
	if s.Get(100) == nil {
		t.Error("the result without a cache file was lost")
	}
}

// Removing results gives their bytes back to the budget.
func TestRemovedResultsLeaveTheBudget(t *testing.T) {
	s := lruStore(t)
	for id := uint32(1); id <= 3; id++ {
		put(s, id)
	}
	s.Remove(1)
	s.Invalidate(2)
	s.mu.RLock()
	want := resultSize(bigResult(3))
	got := s.lru.bytes
	s.mu.RUnlock()
	if got != want {
		t.Errorf("%d bytes counted, want %d (one result left)", got, want)
	}
	s.InvalidateAll()
	if s.Count() != 0 || s.lru.bytes != 0 {
		t.Errorf("after InvalidateAll: %d results, %d bytes", s.Count(), s.lru.bytes)
	}
}

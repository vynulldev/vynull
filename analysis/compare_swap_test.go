// SPDX-License-Identifier: GPL-3.0-or-later
package analysis

import "testing"

// CompareAndSwap replaces only the result it was given, and leaves the old
// one untouched for anyone still reading it.
func TestCompareAndSwapRefusesChangedOrMissingAnalysis(t *testing.T) {
	s := NewStore()
	first := &Result{BPM: 120, Beats: []float64{0, 500}}
	s.Set(1, first)
	newer := &Result{BPM: 125, Beats: []float64{0, 480}}
	s.Set(1, newer)
	restored := &Result{BPM: 100, Beats: []float64{0, 600}, GridEdited: true}
	if s.CompareAndSwap(1, first, restored) || s.Get(1) != newer {
		t.Fatal("overwrote concurrent analysis")
	}
	if s.CompareAndSwap(2, first, restored) {
		t.Fatal("created missing analysis")
	}
	if !s.CompareAndSwap(1, newer, restored) || s.Get(1) != restored {
		t.Fatal("matching replacement failed")
	}
	if newer.BPM != 125 || first.Beats[1] != 500 {
		t.Fatal("changed result held by old reader")
	}
}

// CompareAndSwap leaves the disk cache to Save.
func TestCompareAndSwapSavesOnlyOnSave(t *testing.T) {
	dir := t.TempDir()
	const fp = "/music/swap.flac"
	s := NewStoreWithCache(dir)
	s.SetPath(1, fp)
	first := &Result{CacheVersion: cacheVersion, BPM: 120, Beats: []float64{0, 500}}
	s.Set(1, first)
	cachedBPM := func() float64 {
		r := NewStoreWithCache(dir)
		r.SetPath(1, fp)
		return r.Get(1).BPM
	}
	if !s.CompareAndSwap(1, first, &Result{CacheVersion: cacheVersion, BPM: 100, Beats: []float64{0, 600}}) {
		t.Fatal("swap refused")
	}
	if got := cachedBPM(); got != 120 {
		t.Fatalf("cache holds BPM %v before Save, want 120", got)
	}
	s.Save(1)
	if got := cachedBPM(); got != 100 {
		t.Fatalf("cache holds BPM %v after Save, want 100", got)
	}
}

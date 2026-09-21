// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"testing"
	"time"
)

// TestStoreImporterPreemptsDSP pins the Importer contract: when the hook
// returns a Result (e.g. parsed from a served rekordbox USB's ANLZ files),
// the store must use it — stored, delivered to onDone — without running our
// own DSP. The file path is deliberately nonexistent: if the DSP fallback
// ran instead, analysis would fail and no result would ever arrive.
func TestStoreImporterPreemptsDSP(t *testing.T) {
	s := NewStore()
	want := &Result{CacheVersion: cacheVersion, BPM: 123.4, Duration: 300}
	var gotID uint32
	s.Importer = func(trackID uint32, filePath string) *Result {
		gotID = trackID
		return want
	}

	done := make(chan *Result, 1)
	s.AnalyzeInBackground(7, "/nonexistent/dir/file.mp3", func(r *Result) { done <- r })

	select {
	case r := <-done:
		if r != want {
			t.Fatalf("onDone got %+v, want the imported result", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("imported result never arrived — the DSP fallback probably ran (and failed) instead")
	}
	if gotID != 7 {
		t.Fatalf("importer called with track %d, want 7", gotID)
	}
	if s.Get(7) != want {
		t.Fatal("imported result was not stored")
	}
}

// TestStoreImporterNilFallsThrough: an Importer returning nil must fall
// through to real analysis (which here fails on the missing file, storing
// nothing) — a track without ANLZ data still gets our own DSP.
func TestStoreImporterNilFallsThrough(t *testing.T) {
	s := NewStore()
	called := make(chan struct{}, 1)
	s.Importer = func(trackID uint32, filePath string) *Result {
		called <- struct{}{}
		return nil
	}

	s.AnalyzeInBackground(9, "/nonexistent/dir/file.mp3", func(r *Result) {
		t.Error("onDone fired — nothing should be produced for a missing file")
	})

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("importer was never consulted")
	}
	// Give the fallback path a moment to (fail to) analyze, then confirm
	// nothing was stored.
	time.Sleep(300 * time.Millisecond)
	if s.Get(9) != nil {
		t.Fatal("a result was stored for a missing file")
	}
}

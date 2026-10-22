// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// A track that gains an analysis before its queued request runs keeps it.
// An import adds its tracks before storing (and locking) rekordbox's
// analysis, and the web UI requests waveforms for the rows on screen in
// between; those requests used to overwrite the imported grids. (The queue's
// own test covers the request that waits behind others.)
func TestQueuedAnalysisKeepsImportedResult(t *testing.T) {
	dir := t.TempDir()
	one, two := audioFile(t, dir, "one.flac"), audioFile(t, dir, "two.flac")
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Imported", FilePath: one})
	lib.AddTrack(&library.Track{ID: 2, Title: "New", FilePath: two})
	store := analysis.NewStore()
	analyzed := map[uint32]int{}
	var mu sync.Mutex
	store.Importer = func(id uint32, _ string) *analysis.Result {
		mu.Lock()
		analyzed[id]++
		mu.Unlock()
		return &analysis.Result{BPM: 99}
	}
	srv := &Server{Library: lib, Analysis: store}
	store.OnAnalyzed = srv.applyAnalysisToTrack // main wires the dbserver's, a superset

	// The import stores rekordbox's analysis for track 1 and locks it.
	store.Set(1, &analysis.Result{BPM: 128})
	lib.Track(1).AnalysisLocked = true

	if srv.tryQueueAnalysis(1, "") {
		t.Error("track 1 was queued though it has an analysis")
	}
	if !srv.tryQueueAnalysis(2, "") {
		t.Fatal("track 2 was not queued")
	}
	waitIdle(t, store)

	mu.Lock()
	defer mu.Unlock()
	if got := store.Get(1); got == nil || got.BPM != 128 || analyzed[1] != 0 {
		t.Errorf("track 1: got %+v after %d run(s), want the imported analysis and none", got, analyzed[1])
	}
	if got := store.Get(2); got == nil || got.BPM != 99 || analyzed[2] != 1 {
		t.Errorf("track 2 (no analysis): want analyzed once, got %+v after %d run(s)", got, analyzed[2])
	}
	if lib.Track(2).BPM != 99 {
		t.Errorf("track 2's library BPM %v, want the analysis written back", lib.Track(2).BPM)
	}
}

// waitIdle waits for the store's queued and running analyses to finish.
func waitIdle(t *testing.T, store *analysis.Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for store.Pending() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("analyses never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// audioFile creates an (empty) audio file for a queue test; only its
// presence matters, since the tests' Importer stands in for analysis.
func audioFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A track whose file is missing is not queued: the web UI asks for its
// waveform on every render, and each request used to start another ffmpeg
// run that failed. The track is flagged as missing, and queued again once
// its file is back.
func TestMissingFileIsNotQueuedForAnalysis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.mp3")
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Gone", FilePath: path})
	store := analysis.NewStore()
	store.Importer = func(uint32, string) *analysis.Result { return &analysis.Result{BPM: 99} }
	srv := &Server{Library: lib, Analysis: store}

	for i := 0; i < 3; i++ {
		if srv.tryQueueAnalysis(1, "") {
			t.Fatalf("request %d queued a track whose file is missing", i+1)
		}
	}
	if n := store.Pending(); n != 0 {
		t.Errorf("%d analyses queued, want 0", n)
	}
	if !lib.Track(1).FileMissing {
		t.Errorf("track not flagged as missing")
	}

	audioFile(t, dir, "gone.mp3")
	if !srv.tryQueueAnalysis(1, "") {
		t.Errorf("track not queued once its file is back")
	}
	waitIdle(t, store)
}

// On-demand analysis (the track drawer's waveform and grid requests) skips a
// missing file instead of running ffmpeg for it each time.
func TestMissingFileIsNotAnalyzedOnDemand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.mp3")
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Gone", FilePath: path})
	store := analysis.NewStore()
	ran := 0
	store.Importer = func(uint32, string) *analysis.Result { ran++; return nil }
	srv := &Server{Library: lib, Analysis: store}
	if r := srv.getOrAnalyze(context.Background(), 1); r != nil || ran != 0 {
		t.Errorf("getOrAnalyze = %v after %d analysis attempt(s), want nil and none", r, ran)
	}
	if !lib.Track(1).FileMissing {
		t.Errorf("track not flagged as missing")
	}
}

// An import can also land while an analysis is running, not only while it
// waits: neither the queued nor the on-demand analysis may replace it when
// it finishes.
func TestRunningAnalysisKeepsImportedResult(t *testing.T) {
	path := audioFile(t, t.TempDir(), "one.flac")
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Imported", FilePath: path})
	store := analysis.NewStore()
	store.Importer = func(id uint32, _ string) *analysis.Result {
		store.Set(id, &analysis.Result{BPM: 128}) // rekordbox's, stored mid-run
		return &analysis.Result{BPM: 99}
	}
	srv := &Server{Library: lib, Analysis: store}
	if !srv.tryQueueAnalysis(1, "") {
		t.Fatal("track 1 was not queued")
	}
	waitIdle(t, store)
	if got := store.Get(1); got == nil || got.BPM != 128 {
		t.Errorf("queued: imported analysis replaced, got %+v", got)
	}

	store.Remove(1)
	store.SetPath(1, path)
	if got := srv.getOrAnalyze(context.Background(), 1); got == nil || got.BPM != 128 || store.Get(1).BPM != 128 {
		t.Errorf("on demand: served %+v, stored %+v; want the imported analysis", got, store.Get(1))
	}
}

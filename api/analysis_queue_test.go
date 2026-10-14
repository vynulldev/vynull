// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// A track queued for analysis that gains one before the job runs must keep
// it. An import adds its tracks before storing and locking rekordbox's
// analysis, and the web UI queues the rows on screen in between; those queued
// jobs used to overwrite the imported grids.
func TestQueuedAnalysisKeepsImportedResult(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Imported", FilePath: "/music/one.flac"})
	lib.AddTrack(&library.Track{ID: 2, Title: "New", FilePath: "/music/two.flac"})
	store := analysis.NewStore()
	analyzed := map[uint32]int{}
	store.Importer = func(id uint32, _ string) *analysis.Result {
		analyzed[id]++
		return &analysis.Result{BPM: 99}
	}
	srv := &Server{Library: lib, Analysis: store}

	// Queue both without starting the workers, as the web UI's waveform
	// requests do while an import is still running.
	srv.analyzeOnce.Do(func() { srv.analyzeCh = make(chan analyzeJob, 4) })
	for _, id := range []uint32{1, 2} {
		if !srv.tryQueueAnalysis(id, "") {
			t.Fatalf("track %d was not queued", id)
		}
	}

	// The import stores rekordbox's analysis for track 1 and locks it.
	store.Set(1, &analysis.Result{BPM: 128})
	lib.Track(1).AnalysisLocked = true

	close(srv.analyzeCh)
	srv.analysisWorker()

	if got := store.Get(1); got == nil || got.BPM != 128 {
		t.Errorf("track 1: imported analysis replaced, got %+v", got)
	}
	if analyzed[1] != 0 {
		t.Errorf("track 1 was analyzed %d time(s), want 0", analyzed[1])
	}
	if got := store.Get(2); got == nil || got.BPM != 99 || analyzed[2] != 1 {
		t.Errorf("track 2 (no analysis): want analyzed once, got %+v after %d run(s)", got, analyzed[2])
	}
}

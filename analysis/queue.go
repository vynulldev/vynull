// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// queue.go runs the analyses the daemon starts by itself: a deck loading a
// track that has none yet, the web UI showing a waveform or a track's details,
// a track being added. A request for a track already queued or running joins
// it. At most maxRunning analyses run at once, because one holds the decoded
// track and its spectrum (about 1.5 GB at peak for a four-minute track). A
// deck or a person waiting goes ahead of background work, and background work
// never takes the last slot, so a waiting request starts at once unless other
// waiting requests fill both.

// Priority orders analysis requests.
type Priority int

const (
	// Background is work nobody waits on: the web UI's list thumbnails, a
	// newly added track.
	Background Priority = iota
	// Foreground is a deck loading the track or a person in the web UI.
	Foreground
)

const (
	maxRunning    = 2 // analyses at once
	maxBackground = 1 // of which background work, leaving one for Foreground
)

type job struct {
	trackID  uint32
	filePath string
	prio     Priority
	slot     Priority // the priority it was started under, while running
	running  bool
	waiters  []func(*Result)
}

type queue struct {
	mu      sync.Mutex
	jobs    map[uint32]*job // queued or running, by track
	waiting [2][]*job       // queued, oldest first, by priority
	running [2]int          // running, by the priority they started under
	closed  bool
}

// Request asks for a track's analysis: rekordbox's through the Importer when
// it has one, otherwise our own, stored like any filled-in result (see
// keepCurrent). done, if not nil, is called once with the track's result, or
// nil if there is none (a missing file, a failed analysis, Close). Never
// blocks.
func (s *Store) Request(trackID uint32, filePath string, prio Priority, done func(*Result)) {
	q := &s.queue
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		if done != nil {
			done(nil)
		}
		return
	}
	if q.jobs == nil {
		q.jobs = make(map[uint32]*job)
	}
	j := q.jobs[trackID]
	switch {
	case j == nil:
		j = &job{trackID: trackID, filePath: filePath, prio: prio}
		q.jobs[trackID] = j
		q.waiting[prio] = append(q.waiting[prio], j)
	case prio > j.prio && !j.running:
		// Someone is waiting for queued background work now: move it up.
		q.waiting[j.prio] = slices.DeleteFunc(q.waiting[j.prio], func(w *job) bool { return w == j })
		j.prio = prio
		q.waiting[prio] = append(q.waiting[prio], j)
	}
	if done != nil {
		j.waiters = append(j.waiters, done)
	}
	s.dispatchLocked()
	q.mu.Unlock()
}

// Analyze requests a track's analysis with Foreground priority and waits for
// it. It returns nil if there is none, or when ctx ends first; the analysis
// carries on for the track either way.
func (s *Store) Analyze(ctx context.Context, trackID uint32, filePath string) *Result {
	ch := make(chan *Result, 1)
	s.Request(trackID, filePath, Foreground, func(r *Result) { ch <- r })
	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		return nil
	}
}

// Close stops starting analyses: queued requests get nil, and later ones get
// nil at once. Analyses already running finish.
func (s *Store) Close() {
	q := &s.queue
	q.mu.Lock()
	q.closed = true
	var dropped []*job
	for p := range q.waiting {
		dropped = append(dropped, q.waiting[p]...)
		q.waiting[p] = nil
	}
	for _, j := range dropped {
		delete(q.jobs, j.trackID)
	}
	q.mu.Unlock()
	for _, j := range dropped {
		for _, done := range j.waiters {
			done(nil)
		}
	}
}

// dispatchLocked starts queued analyses while there are free slots,
// Foreground first. Callers hold q.mu.
func (s *Store) dispatchLocked() {
	q := &s.queue
	for q.running[Background]+q.running[Foreground] < maxRunning {
		p := Foreground
		if len(q.waiting[Foreground]) == 0 {
			if len(q.waiting[Background]) == 0 || q.running[Background] >= maxBackground {
				return
			}
			p = Background
		}
		j := q.waiting[p][0]
		q.waiting[p] = q.waiting[p][1:]
		j.running, j.slot = true, p
		q.running[p]++
		go s.run(j)
	}
}

// run analyzes a started job's track, then hands the result to its waiters.
func (s *Store) run(j *job) {
	r := s.analyze(j.trackID, j.filePath)
	q := &s.queue
	q.mu.Lock()
	delete(q.jobs, j.trackID)
	q.running[j.slot]--
	waiters := j.waiters
	if !q.closed {
		s.dispatchLocked()
	}
	q.mu.Unlock()
	for _, done := range waiters {
		done(r)
	}
}

// analyze produces a track's result: one that arrived while the request was
// queued (an import storing rekordbox's), the Importer's, or our own.
func (s *Store) analyze(trackID uint32, filePath string) *Result {
	if r := s.Get(trackID); r != nil {
		return r
	}
	if s.Importer != nil {
		if r := s.Importer(trackID, filePath); r != nil {
			r = s.keepCurrent(trackID, r)
			log.Printf("analysis: track %d imported from ANLZ (BPM=%.1f dur=%ds)", trackID, r.BPM, r.Duration)
			return r
		}
	}
	if _, err := os.Stat(filePath); err != nil {
		log.Printf("analysis: track %d not analyzed, its file is missing: %s", trackID, filePath)
		return nil
	}
	log.Printf("analysis: analyzing track %d (%s)...", trackID, filepath.Base(filePath))
	r, err := AnalyzeTrack(filePath)
	if err != nil {
		log.Printf("analysis: track %d failed: %v", trackID, err)
		return nil
	}
	r = s.keepCurrent(trackID, r)
	log.Printf("analysis: track %d done (BPM=%.1f key=%s dur=%ds)", trackID, r.BPM, r.KeyCamelot, r.Duration)
	return r
}

// forget drops a track's queued request, as when the track is deleted.
func (s *Store) forget(trackID uint32) {
	q := &s.queue
	q.mu.Lock()
	j := q.jobs[trackID]
	if j == nil || j.running {
		q.mu.Unlock()
		return
	}
	q.waiting[j.prio] = slices.DeleteFunc(q.waiting[j.prio], func(w *job) bool { return w == j })
	delete(q.jobs, trackID)
	q.mu.Unlock()
	for _, done := range j.waiters {
		done(nil)
	}
}

// Pending returns how many analyses are queued or running.
func (s *Store) Pending() int32 {
	q := &s.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	return int32(len(q.jobs))
}

// activity names a running analysis for the status line, or "" if none.
func (s *Store) activity() string {
	q := &s.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.running {
			return "Analyzing: " + filepath.Base(j.filePath)
		}
	}
	return ""
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeAnalysis stands in for analysis through the store's Importer: a track's
// run reports its start and then waits until the test releases it.
type fakeAnalysis struct {
	started chan uint32
	mu      sync.Mutex
	release map[uint32]chan struct{}
	runs    map[uint32]int
	running int
	peak    int
}

func newFake(s *Store) *fakeAnalysis {
	f := &fakeAnalysis{started: make(chan uint32, 16), release: map[uint32]chan struct{}{}, runs: map[uint32]int{}}
	s.Importer = func(id uint32, _ string) *Result {
		f.mu.Lock()
		f.runs[id]++
		f.running++
		f.peak = max(f.peak, f.running)
		ch := f.gate(id)
		f.mu.Unlock()
		f.started <- id
		<-ch
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
		return &Result{BPM: float64(id)}
	}
	return f
}

func (f *fakeAnalysis) gate(id uint32) chan struct{} {
	if f.release[id] == nil {
		f.release[id] = make(chan struct{})
	}
	return f.release[id]
}

func (f *fakeAnalysis) finish(id uint32) {
	f.mu.Lock()
	ch := f.gate(id)
	f.mu.Unlock()
	close(ch)
}

// expectStart waits for the next run to start and checks which track it is.
func (f *fakeAnalysis) expectStart(t *testing.T, want uint32) {
	t.Helper()
	select {
	case id := <-f.started:
		if id != want {
			t.Fatalf("track %d started, want %d", id, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("track %d never started", want)
	}
}

// expectNoStart checks no further run starts for a moment.
func (f *fakeAnalysis) expectNoStart(t *testing.T) {
	t.Helper()
	select {
	case id := <-f.started:
		t.Fatalf("track %d started, want none", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func result(t *testing.T, ch <-chan *Result) *Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(time.Second):
		t.Fatal("no result")
		return nil
	}
}

func request(s *Store, id uint32, p Priority) <-chan *Result {
	ch := make(chan *Result, 1)
	s.Request(id, "/music/track.flac", p, func(r *Result) { ch <- r })
	return ch
}

// Requests for a track already queued or running join it: one run, and every
// request gets its result.
func TestQueueJoinsRequestsForATrack(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	a := request(s, 1, Background)
	f.expectStart(t, 1)
	b := request(s, 1, Foreground)
	c := request(s, 1, Background)
	f.expectNoStart(t)
	f.finish(1)
	for _, ch := range []<-chan *Result{a, b, c} {
		if r := result(t, ch); r == nil || r.BPM != 1 {
			t.Fatalf("got %+v, want track 1's result", r)
		}
	}
	if f.runs[1] != 1 || s.Get(1) == nil {
		t.Errorf("%d runs, stored %v; want one run, stored", f.runs[1], s.Get(1))
	}
}

// Two analyses run at once at most, background work in only one of them, and
// waiting requests go first.
func TestQueueLimitsAndPriority(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	request(s, 1, Background)
	request(s, 2, Background)
	f.expectStart(t, 1)
	f.expectNoStart(t) // background keeps the second slot free

	request(s, 10, Foreground)
	f.expectStart(t, 10) // takes the free slot at once
	request(s, 11, Foreground)
	f.expectNoStart(t) // both slots busy

	f.finish(1)
	f.expectStart(t, 11) // a waiting request goes before background track 2
	f.finish(10)
	f.expectStart(t, 2) // the background slot is free again
	f.finish(11)
	f.finish(2)
	if f.peak > maxRunning {
		t.Errorf("%d analyses ran at once, want at most %d", f.peak, maxRunning)
	}
}

// A queued background request that someone starts waiting for moves up.
func TestQueuePromotesWaitedForBackgroundWork(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	request(s, 1, Background)
	f.expectStart(t, 1)
	request(s, 2, Background) // queued behind the background slot
	request(s, 20, Foreground)
	f.expectStart(t, 20)
	b := request(s, 2, Foreground) // a deck loads track 2
	f.finish(20)
	f.expectStart(t, 2) // starts in the freed slot though track 1 still runs
	f.finish(2)
	if r := result(t, b); r == nil || r.BPM != 2 {
		t.Errorf("got %+v, want track 2's result", r)
	}
	f.finish(1)
}

// A result stored while the request waited (an import storing rekordbox's) is
// the request's answer, without another analysis.
func TestQueueKeepsResultStoredWhileQueued(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	request(s, 1, Background)
	f.expectStart(t, 1)
	b := request(s, 2, Background)
	imported := &Result{BPM: 128}
	s.Set(2, imported)
	f.finish(1)
	if r := result(t, b); r != imported {
		t.Errorf("got %+v, want the imported result", r)
	}
	if f.runs[2] != 0 {
		t.Errorf("track 2 analyzed %d time(s), want 0", f.runs[2])
	}
}

// Close drops queued requests (nil) and refuses new ones; a running analysis
// finishes.
func TestQueueClose(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	a := request(s, 1, Background)
	f.expectStart(t, 1)
	b := request(s, 2, Background)
	s.Close()
	if r := result(t, b); r != nil {
		t.Errorf("queued request after Close: %+v, want nil", r)
	}
	if r := result(t, request(s, 3, Foreground)); r != nil {
		t.Errorf("request after Close: %+v, want nil", r)
	}
	f.finish(1)
	if r := result(t, a); r == nil || r.BPM != 1 {
		t.Errorf("running analysis after Close: %+v, want its result", r)
	}
	f.expectNoStart(t)
}

// Removing a track drops its queued request.
func TestQueueRemoveDropsQueuedRequest(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	request(s, 1, Background)
	f.expectStart(t, 1)
	b := request(s, 2, Background)
	s.Remove(2)
	if r := result(t, b); r != nil {
		t.Errorf("removed track's request: %+v, want nil", r)
	}
	if n := s.Pending(); n != 1 {
		t.Errorf("Pending = %d, want 1 (track 1 running)", n)
	}
	f.finish(1)
	f.expectNoStart(t)
}

// Analyze stops waiting when its context ends; the analysis carries on and is
// stored.
func TestAnalyzeStopsWaitingOnContext(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *Result, 1)
	go func() { got <- s.Analyze(ctx, 1, "/music/track.flac") }()
	f.expectStart(t, 1)
	cancel()
	if r := result(t, got); r != nil {
		t.Errorf("Analyze after cancel = %+v, want nil", r)
	}
	f.finish(1)
	deadline := time.Now().Add(time.Second)
	for s.Get(1) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned analysis was not stored")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A finished analysis is written back once, however many requests joined it:
// a deck asks for a track's waveforms, grid and cues many times while it
// loads, and each request used to bring its own write-back.
func TestQueueWritesBackOnce(t *testing.T) {
	s := NewStore()
	f := newFake(s)
	var mu sync.Mutex
	writes := map[uint32]int{}
	s.OnAnalyzed = func(id uint32, r *Result) {
		mu.Lock()
		writes[id]++
		mu.Unlock()
	}
	for range 20 {
		s.Request(1, "/music/track.flac", Foreground, nil)
	}
	f.expectStart(t, 1)
	last := request(s, 1, Foreground)
	f.finish(1)
	result(t, last)
	mu.Lock()
	defer mu.Unlock()
	if writes[1] != 1 {
		t.Errorf("written back %d times, want once", writes[1])
	}

	s.Importer = func(uint32, string) *Result { return nil } // a missing file: no result
	result(t, request(s, 2, Foreground))
	if writes[2] != 0 {
		t.Errorf("a request with no result was written back %d times", writes[2])
	}
}

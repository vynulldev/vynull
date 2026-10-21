// SPDX-License-Identifier: GPL-3.0-or-later

// Package saver writes a store's state in the background. A change marks the
// store dirty and returns at once; one goroutine writes the latest state at
// most once per interval, so a burst of changes costs one write and no
// change waits for the disk. Flush writes now; Close stops the goroutine and
// writes what's pending, on shutdown.
package saver

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Interval is how often, at most, the stores write: long enough to fold a
// burst of changes into one write, short enough that a crash loses little.
const Interval = 2 * time.Second

// Saver runs one store's background writes.
type Saver struct {
	name     string // for the log
	interval time.Duration
	write    func() error // writes the store's current state

	start, stopOnce sync.Once
	kick            chan struct{} // a change is waiting (buffered: one is enough)
	stop, done      chan struct{}

	dirty atomic.Uint64 // generation of the latest change

	mu    sync.Mutex // serializes writes
	saved uint64     // the generation last written
}

// New returns a Saver that calls write, at most once per interval, after a
// change. write must take its own snapshot of the state (under the store's
// lock) and may encode and write it outside the lock; it is never called
// concurrently with itself. The goroutine starts on the first change.
func New(name string, interval time.Duration, write func() error) *Saver {
	return &Saver{
		name:     name,
		interval: interval,
		write:    write,
		kick:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Mark records a change. Call it after the change is in place (under the
// store's lock is fine: Mark never blocks), so the next write includes it.
func (s *Saver) Mark() {
	s.dirty.Add(1)
	s.start.Do(func() { go s.run() })
	select {
	case s.kick <- struct{}{}:
	default: // a write is already due
	}
}

func (s *Saver) run() {
	defer close(s.done)
	for {
		select {
		case <-s.kick:
		case <-s.stop:
			return
		}
		// Let a burst of changes settle into one write.
		select {
		case <-time.After(s.interval):
		case <-s.stop:
			return // Close writes the final state
		}
		s.Flush()
	}
}

// Flush writes now if anything changed since the last write. A failed write
// is logged and returned, and tried again on the next change or Flush.
func (s *Saver) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	gen := s.dirty.Load()
	if gen == s.saved {
		return nil
	}
	if err := s.write(); err != nil {
		log.Printf("%s: save: %v", s.name, err)
		return err
	}
	s.saved = gen
	return nil
}

// Close stops the background writes and writes any pending change. Call it
// once the store won't change any more, on shutdown; after it, only Flush
// writes.
func (s *Saver) Close() error {
	s.stopOnce.Do(func() {
		close(s.stop)
		s.start.Do(func() { close(s.done) }) // never started: nothing to wait for
		<-s.done
	})
	return s.Flush()
}

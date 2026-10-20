// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vynulldev/vynull/internal/fsutil"
)

// saveInterval is how often, at most, the background writer saves the track
// list: a burst of changes within it costs one write. (A variable so tests can
// shorten it.)
var saveInterval = 2 * time.Second

// writer saves the track list in the background, so a change (a play count,
// a rating, an analysis result) no longer waits for the whole library.json to
// be rewritten. Save marks the list dirty; one goroutine writes the latest
// state, from a copy taken under the lock and encoded outside it.
type writer struct {
	start, stopOnce sync.Once
	kick            chan struct{} // a change is waiting (buffered: one is enough)
	stop, done      chan struct{}
	interval        time.Duration // saveInterval when the writer started

	dirty atomic.Uint64 // generation of the latest change

	mu    sync.Mutex // serializes writes
	saved uint64     // the generation last written
}

// Save records a change to the track list. The revision moves at once, so
// pollers see it; the background writer saves the list within saveInterval.
// Flush writes it now, and Close does on shutdown.
func (l *Library) Save() {
	l.rev.Add(1)
	if l.dbPath == "" {
		return
	}
	l.writer.dirty.Add(1)
	l.startWriter()
	select {
	case l.writer.kick <- struct{}{}:
	default: // a write is already due
	}
}

func (l *Library) startWriter() {
	w := &l.writer
	w.start.Do(func() {
		w.kick = make(chan struct{}, 1)
		w.stop = make(chan struct{})
		w.done = make(chan struct{})
		w.interval = saveInterval
		go l.runWriter()
	})
}

func (l *Library) runWriter() {
	w := &l.writer
	defer close(w.done)
	for {
		select {
		case <-w.kick:
		case <-w.stop:
			return
		}
		// Let a burst of changes settle into one write.
		select {
		case <-time.After(w.interval):
		case <-w.stop:
			return // Close writes the final state
		}
		l.Flush()
	}
}

// Flush writes the track list now if it has unsaved changes. A failed write
// is logged and returned, and tried again on the next change or Flush.
func (l *Library) Flush() error {
	if l.dbPath == "" {
		return nil
	}
	w := &l.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	gen := w.dirty.Load()
	if gen == w.saved {
		return nil
	}
	l.mu.RLock()
	tracks := make([]Track, len(l.tracks))
	for i, t := range l.tracks {
		tracks[i] = *t
	}
	l.mu.RUnlock()
	data, err := json.Marshal(tracks)
	if err == nil {
		err = fsutil.WriteFile(l.dbPath, data, 0644)
	}
	if err != nil {
		log.Printf("library: save %s: %v", l.dbPath, err)
		return err
	}
	w.saved = gen
	return nil
}

// Close stops the background writer and writes any unsaved changes. Call it
// once the library won't change any more, on shutdown; after it, only Flush
// writes.
func (l *Library) Close() error {
	l.startWriter()
	w := &l.writer
	w.stopOnce.Do(func() {
		close(w.stop)
		<-w.done
	})
	return l.Flush()
}

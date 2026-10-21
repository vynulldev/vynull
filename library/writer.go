// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"

	"github.com/vynulldev/vynull/internal/fsutil"
)

// Save records a change to the track list. The revision moves at once, so
// pollers see it; the background writer saves the list within
// saver.Interval. Flush writes it now, and Close does on shutdown.
func (l *Library) Save() {
	l.rev.Add(1)
	if l.saver != nil {
		l.saver.Mark()
	}
}

// Flush writes the track list now if it has unsaved changes. A failed write
// is logged and returned, and tried again on the next change or Flush.
func (l *Library) Flush() error {
	if l.saver == nil {
		return nil
	}
	return l.saver.Flush()
}

// Close stops the background writer and writes any unsaved changes. Call it
// once the library won't change any more, on shutdown; after it, only Flush
// writes.
func (l *Library) Close() error {
	if l.saver == nil {
		return nil
	}
	return l.saver.Close()
}

// writeTracks writes the track list from a copy taken under the lock,
// encoded outside it.
func (l *Library) writeTracks() error {
	l.mu.RLock()
	tracks := make([]Track, len(l.tracks))
	for i, t := range l.tracks {
		tracks[i] = *t
	}
	l.mu.RUnlock()
	data, err := json.Marshal(tracks)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(l.dbPath, data, 0644)
}

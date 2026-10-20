// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os"
	"sync"
)

// SetFileMissing records whether a track's audio file is missing, saving the
// library only when that changes the flag. It reports whether it did.
func (l *Library) SetFileMissing(id uint32, missing bool) bool {
	l.mu.Lock()
	t := l.byID[id]
	changed := t != nil && t.FileMissing != missing
	if changed {
		t.FileMissing = missing
	}
	l.mu.Unlock()
	if changed {
		l.Save()
	}
	return changed
}

// RefreshMissing checks every track's audio file on disk and sets its
// FileMissing flag to match (a track without a path counts as missing),
// saving the library if any flag changed. It returns how many tracks are
// missing. The checks run in parallel and outside the library's lock, since a
// serial pass over a large or network-backed library is a latency cliff; a
// track removed or relinked meanwhile is left alone.
func (l *Library) RefreshMissing() int {
	type check struct {
		id      uint32
		path    string
		missing bool
	}
	l.mu.RLock()
	checks := make([]check, len(l.tracks))
	for i, t := range l.tracks {
		checks[i] = check{id: t.ID, path: t.FilePath}
	}
	l.mu.RUnlock()

	next := make(chan int)
	var wg sync.WaitGroup
	for range min(16, len(checks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				c := &checks[i]
				if c.path == "" {
					c.missing = true
					continue
				}
				_, err := os.Stat(c.path)
				c.missing = err != nil
			}
		}()
	}
	for i := range checks {
		next <- i
	}
	close(next)
	wg.Wait()

	changed, missing := false, 0
	l.mu.Lock()
	for _, c := range checks {
		if t := l.byID[c.id]; t != nil && t.FilePath == c.path && t.FileMissing != c.missing {
			t.FileMissing = c.missing
			changed = true
		}
	}
	for _, t := range l.tracks {
		if t.FileMissing {
			missing++
		}
	}
	l.mu.Unlock()
	if changed {
		l.Save()
	}
	return missing
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"slices"
	"sync"
	"time"

	"github.com/vynulldev/vynull/library"
)

// smart_cache.go matches smart playlists against the library, keeping a
// playlist's result until the library, the tags, its rules or the day change.

// matchTracks returns the IDs of the tracks the rules select, in order.
func matchTracks(rules *SmartRules, tracks []*library.Track, tags TagLookup, now time.Time) []uint32 {
	m := rules.compile(now)
	out := make([]uint32, 0, len(tracks))
	for _, t := range tracks {
		if m(t, tags) {
			out = append(out, t.ID)
		}
	}
	return out
}

// smartCache holds a playlist store's smart playlist results.
type smartCache struct {
	mu      sync.Mutex
	results map[uint32]smartResult
}

// smartResult is a smart playlist's tracks and what they were matched with.
type smartResult struct {
	rules  *SmartRules
	lib    *library.Library
	libRev uint64
	tagRev uint64
	day    int // YYYYMMDD of the day relative dates counted back from
	ids    []uint32
}

// tracks returns the IDs of the tracks a smart playlist's rules select, in
// library order: its last result while the library, the tags, its rules and
// the day are the ones it was matched with. The revisions are read before
// matching, so a change made meanwhile moves them past the result's and the
// next request matches again.
func (c *smartCache) tracks(id uint32, rules *SmartRules, lib *library.Library, tags TagLookup, now time.Time) []uint32 {
	if lib == nil || rules == nil {
		return nil
	}
	rev := lib.Rev()
	tagRev, cacheable := tagRevision(tags)
	y, m, d := now.Date()
	day := y*10000 + int(m)*100 + d
	c.mu.Lock()
	if r, ok := c.results[id]; ok && cacheable && r.rules == rules && r.lib == lib && r.libRev == rev && r.tagRev == tagRev && r.day == day {
		c.mu.Unlock()
		return slices.Clone(r.ids)
	}
	c.mu.Unlock()

	ids := matchTracks(rules, lib.Tracks(), tags, now)
	if cacheable {
		c.mu.Lock()
		if c.results == nil {
			c.results = make(map[uint32]smartResult)
		}
		c.results[id] = smartResult{rules: rules, lib: lib, libRev: rev, tagRev: tagRev, day: day, ids: ids}
		c.mu.Unlock()
	}
	return slices.Clone(ids)
}

// forget drops playlists' results, for rules that changed or playlists that
// are gone.
func (c *smartCache) forget(ids ...uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		delete(c.results, id)
	}
}

// tagRevision returns a tag lookup's revision, and whether it has one: a
// result that read tags can be reused only while that hasn't moved. No tags
// count as an unchanging revision.
func tagRevision(tags TagLookup) (uint64, bool) {
	if tags == nil {
		return 0, true
	}
	if r, ok := tags.(interface{ Rev() uint64 }); ok {
		return r.Rev(), true
	}
	return 0, false
}

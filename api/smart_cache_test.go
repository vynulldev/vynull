// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// countingTags is a tag lookup with a revision that counts its lookups, so a
// test can tell whether a playlist was matched again.
type countingTags struct {
	mu      sync.Mutex
	tags    map[uint32][]TagInfo
	rev     uint64
	lookups int
}

func (c *countingTags) GetTagsForTrack(id uint32) []TagInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups++
	return c.tags[id]
}

func (c *countingTags) Rev() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rev
}

// matched reports whether resolving the playlist matched its rules again,
// and returns its tracks.
func (c *countingTags) matched(resolve func() []uint32) ([]uint32, bool) {
	c.mu.Lock()
	before := c.lookups
	c.mu.Unlock()
	ids := resolve()
	c.mu.Lock()
	defer c.mu.Unlock()
	return ids, c.lookups != before
}

// TestSmartResultsReused checks that a smart playlist's result is reused
// until the library, the tags, its rules or the day change, and follows each.
func TestSmartResultsReused(t *testing.T) {
	lib := library.New()
	a := lib.AddTrack(&library.Track{Title: "A", Rating: 3, DateAdded: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)})
	b := lib.AddTrack(&library.Track{Title: "B", Rating: 1, DateAdded: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)})
	tags := &countingTags{tags: map[uint32][]TagInfo{a: {{Name: "Banger"}}}}
	ps := NewPlaylistStore(t.TempDir())
	pl, err := ps.CreateSmart("p", 0, &SmartRules{Combinator: "any", Conditions: []SmartCond{
		{Field: "tag", Operator: "has", Value: "banger"},
		{Field: "rating", Operator: "gte", Value: float64(5)},
		{Field: "date_added", Operator: "in_last_days", Value: float64(2)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	resolve := func() []uint32 {
		ps.mu.RLock()
		rules := ps.find(pl.ID).Rules
		ps.mu.RUnlock()
		return ps.smart.tracks(pl.ID, rules, lib, tags, now)
	}
	check := func(step string, wantIDs []uint32, wantMatched bool) {
		t.Helper()
		ids, matched := tags.matched(resolve)
		if !slices.Equal(ids, wantIDs) || matched != wantMatched {
			t.Errorf("%s: tracks %v (matched again: %v), want %v (%v)", step, ids, matched, wantIDs, wantMatched)
		}
	}

	check("first", []uint32{a, b}, true)
	check("again", []uint32{a, b}, false)
	now = now.Add(time.Hour)
	check("later the same day", []uint32{a, b}, false)
	now = now.Add(24 * time.Hour) // B was added three days ago now
	check("next day", []uint32{a}, true)
	lib.SetRating(b, 5)
	check("rating", []uint32{a, b}, true)
	tags.mu.Lock()
	tags.tags = map[uint32][]TagInfo{}
	tags.rev++
	tags.mu.Unlock()
	check("tags", []uint32{b}, true)
	ps.SetRules(pl.ID, &SmartRules{Conditions: []SmartCond{{Field: "title", Operator: "is", Value: "a"}}})
	ids := resolve()
	if !slices.Equal(ids, []uint32{a}) {
		t.Errorf("new rules: tracks %v, want [%d]", ids, a)
	}

	// A tag lookup without a revision can't tell a result is current.
	untracked := fakeTags{}
	ps.mu.RLock()
	rules := ps.find(pl.ID).Rules
	ps.mu.RUnlock()
	ps.smart.tracks(pl.ID, rules, lib, untracked, now)
	ps.smart.mu.Lock()
	r := ps.smart.results[pl.ID]
	ps.smart.mu.Unlock()
	if r.ids == nil || !slices.Equal(r.ids, []uint32{a}) {
		t.Errorf("cached %v after an untracked lookup, want the earlier result [%d] kept", r.ids, a)
	}

	ps.Delete(pl.ID)
	ps.smart.mu.Lock()
	_, kept := ps.smart.results[pl.ID]
	ps.smart.mu.Unlock()
	if kept {
		t.Error("a deleted playlist's result was kept")
	}
}

// TestSmartResultsCallerOwned checks that callers get their own copy of a
// cached result.
func TestSmartResultsCallerOwned(t *testing.T) {
	lib := library.New()
	a := lib.AddTrack(&library.Track{Title: "A", Genre: "House"})
	ps := NewPlaylistStore(t.TempDir())
	pl, _ := ps.CreateSmart("p", 0, &SmartRules{Conditions: []SmartCond{{Field: "genre", Operator: "is", Value: "house"}}})
	ps.TracksFor(pl.ID, lib, nil)[0] = 99
	if got := ps.TracksFor(pl.ID, lib, nil); !slices.Equal(got, []uint32{a}) {
		t.Errorf("tracks %v after a caller edited its copy, want [%d]", got, a)
	}
}

// TestSmartResultsConcurrent resolves playlists from several goroutines at
// once, sharing the store's results (run with -race).
func TestSmartResultsConcurrent(t *testing.T) {
	lib := library.New()
	for _, g := range []string{"House", "Techno", "Deep House", "Ambient"} {
		lib.AddTrack(&library.Track{Title: g + " track", Genre: g, Key: "Am"})
	}
	ps := NewPlaylistStore(t.TempDir())
	var ids []uint32
	for _, rules := range []*SmartRules{
		{Conditions: []SmartCond{{Field: "genre", Operator: "contains", Value: "house"}}},
		{Conditions: []SmartCond{{Field: "title", Operator: "starts_with", Value: "t"}}},
		{Conditions: []SmartCond{{Field: "key", Operator: "compatible", Value: "8A"}}},
	} {
		pl, _ := ps.CreateSmart("p", 0, rules)
		ids = append(ids, pl.ID)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				ps.TracksFor(ids[(g+i)%len(ids)], lib, nil)
			}
		}()
	}
	wg.Wait()
}

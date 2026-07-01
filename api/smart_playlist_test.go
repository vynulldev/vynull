// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
	"time"

	"vynull/library"
)

// fakeTags is a TagLookup that returns a fixed list for a single
// track ID — keeps tests independent of the JSON TagStore.
type fakeTags map[uint32][]TagInfo

func (f fakeTags) GetTagsForTrack(id uint32) []TagInfo { return f[id] }

func mkTrack(opts func(*library.Track)) *library.Track {
	t := &library.Track{
		ID:        1,
		Title:     "Test Anthem",
		Artist:    "Test Producer",
		Album:     "Test Album",
		Genre:     "Trance",
		BPM:       137.5,
		Key:       "8A",
		Rating:    4,
		Year:      1998,
		ColorID:   5,
		PlayCount: 12,
		Duration:  library.DurationSec(8 * time.Minute),
		DateAdded: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
	}
	if opts != nil {
		opts(t)
	}
	return t
}

func TestSmartRulesNumeric(t *testing.T) {
	tr := mkTrack(nil)
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		cond SmartCond
		want bool
	}{
		{"bpm gte true", SmartCond{Field: "bpm", Operator: "gte", Value: 130.0}, true},
		{"bpm gte false", SmartCond{Field: "bpm", Operator: "gte", Value: 140.0}, false},
		{"bpm between match", SmartCond{Field: "bpm", Operator: "between", Value: []any{135.0, 140.0}}, true},
		{"bpm between miss", SmartCond{Field: "bpm", Operator: "between", Value: []any{120.0, 125.0}}, false},
		{"rating gte 4", SmartCond{Field: "rating", Operator: "gte", Value: 4.0}, true},
		{"rating in list", SmartCond{Field: "rating", Operator: "in", Value: []any{3.0, 4.0, 5.0}}, true},
		{"color eq", SmartCond{Field: "color", Operator: "eq", Value: 5.0}, true},
		{"play_count gt", SmartCond{Field: "play_count", Operator: "gt", Value: 10.0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &SmartRules{Conditions: []SmartCond{tc.cond}}
			if got := r.Match(tr, nil, now); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestSmartRulesStrings(t *testing.T) {
	tr := mkTrack(nil)
	now := time.Now()

	cases := []struct {
		name string
		cond SmartCond
		want bool
	}{
		{"artist is (case insensitive)", SmartCond{Field: "artist", Operator: "is", Value: "test producer"}, true},
		{"artist contains", SmartCond{Field: "artist", Operator: "contains", Value: "produc"}, true},
		{"artist not_contains miss", SmartCond{Field: "artist", Operator: "not_contains", Value: "trance"}, true},
		{"title starts_with", SmartCond{Field: "title", Operator: "starts_with", Value: "test"}, true},
		{"genre is_not", SmartCond{Field: "genre", Operator: "is_not", Value: "House"}, true},
		{"comment is_empty", SmartCond{Field: "comment", Operator: "is_empty"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &SmartRules{Conditions: []SmartCond{tc.cond}}
			if got := r.Match(tr, nil, now); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestSmartRulesKeyCompatible(t *testing.T) {
	// 8A: A minor. Compatible = 8A, 8B (relative major), 7A, 9A.
	tr := mkTrack(func(t *library.Track) { t.Key = "8A" })
	cases := []struct {
		key  string
		want bool
	}{
		{"8A", true},  // same
		{"8B", true},  // relative major
		{"7A", true},  // -1 on the wheel
		{"9A", true},  // +1 on the wheel
		{"6A", false}, // -2 — not compatible
		{"10A", false},
		{"3B", false}, // unrelated
		{"1A", false},
		// wrap-around: 12A neighbours 1A and 11A
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			r := &SmartRules{Conditions: []SmartCond{{
				Field: "key", Operator: "compatible", Value: tc.key,
			}}}
			if got := r.Match(tr, nil, time.Now()); got != tc.want {
				t.Fatalf("track 8A vs target %s: want %v got %v", tc.key, tc.want, got)
			}
		})
	}

	// Wheel wrap: track 12A → 11A and 1A neighbours
	tr2 := mkTrack(func(t *library.Track) { t.Key = "12A" })
	r := &SmartRules{Conditions: []SmartCond{{Field: "key", Operator: "compatible", Value: "1A"}}}
	if !r.Match(tr2, nil, time.Now()) {
		t.Fatal("12A ↔ 1A should be compatible (wheel wrap)")
	}
}

func TestSmartRulesTags(t *testing.T) {
	tr := mkTrack(nil)
	tags := fakeTags{
		1: []TagInfo{{ID: 1, Name: "Peak Hour"}, {ID: 2, Name: "Vocal"}},
	}
	cases := []struct {
		name string
		cond SmartCond
		want bool
	}{
		{"has specific", SmartCond{Field: "tag", Operator: "has", Value: "Peak Hour"}, true},
		{"has missing", SmartCond{Field: "tag", Operator: "has", Value: "Warmup"}, false},
		{"not_has specific", SmartCond{Field: "tag", Operator: "not_has", Value: "Warmup"}, true},
		{"has_any", SmartCond{Field: "tag", Operator: "has_any"}, true},
		{"none", SmartCond{Field: "tag", Operator: "none"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &SmartRules{Conditions: []SmartCond{tc.cond}}
			if got := r.Match(tr, tags, time.Now()); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}

	// Track with zero tags
	tr2 := mkTrack(func(t *library.Track) { t.ID = 99 })
	r := &SmartRules{Conditions: []SmartCond{{Field: "tag", Operator: "none"}}}
	if !r.Match(tr2, tags, time.Now()) {
		t.Fatal("untagged track should match 'none'")
	}
}

func TestSmartRulesDateAdded(t *testing.T) {
	now := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	// Track added 10 days ago
	tr := mkTrack(func(t *library.Track) {
		t.DateAdded = now.AddDate(0, 0, -10)
	})
	cases := []struct {
		name string
		cond SmartCond
		want bool
	}{
		{"last 14 days", SmartCond{Field: "date_added", Operator: "in_last_days", Value: 14.0}, true},
		{"last 7 days (older than)", SmartCond{Field: "date_added", Operator: "in_last_days", Value: 7.0}, false},
		{"after specific", SmartCond{Field: "date_added", Operator: "after", Value: "2026-05-01T00:00:00Z"}, true},
		{"before specific", SmartCond{Field: "date_added", Operator: "before", Value: "2026-05-01T00:00:00Z"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &SmartRules{Conditions: []SmartCond{tc.cond}}
			if got := r.Match(tr, nil, now); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestSmartRulesAndOr(t *testing.T) {
	tr := mkTrack(nil) // bpm=137.5 genre=Trance rating=4

	// (BPM 130-140) AND (Genre=Trance OR Genre=Progressive)
	r := &SmartRules{
		Combinator: "all",
		Conditions: []SmartCond{
			{Field: "bpm", Operator: "between", Value: []any{130.0, 140.0}},
			{Group: &SmartRules{
				Combinator: "any",
				Conditions: []SmartCond{
					{Field: "genre", Operator: "is", Value: "Trance"},
					{Field: "genre", Operator: "is", Value: "Progressive"},
				},
			}},
		},
	}
	if !r.Match(tr, nil, time.Now()) {
		t.Fatal("should match: 137.5 BPM Trance")
	}

	// Flip: same rules but BPM out of range
	tr2 := mkTrack(func(t *library.Track) { t.BPM = 100 })
	if r.Match(tr2, nil, time.Now()) {
		t.Fatal("should miss: 100 BPM fails outer AND")
	}

	// Flip: BPM in range but wrong genre
	tr3 := mkTrack(func(t *library.Track) { t.Genre = "House" })
	if r.Match(tr3, nil, time.Now()) {
		t.Fatal("should miss: House fails OR inside AND")
	}

	// Top-level OR: rating>=4 OR play_count>=20
	r2 := &SmartRules{
		Combinator: "any",
		Conditions: []SmartCond{
			{Field: "rating", Operator: "gte", Value: 4.0},
			{Field: "play_count", Operator: "gte", Value: 20.0},
		},
	}
	if !r2.Match(tr, nil, time.Now()) {
		t.Fatal("OR: rating>=4 should match")
	}
	// Track with rating=2 play_count=5 → both miss
	tr4 := mkTrack(func(t *library.Track) { t.Rating = 2; t.PlayCount = 5 })
	if r2.Match(tr4, nil, time.Now()) {
		t.Fatal("OR: both branches should miss for low rating + plays")
	}
}

func TestSmartRulesEmpty(t *testing.T) {
	// Empty rules match every track (filter that selects everything).
	r := &SmartRules{}
	if !r.Match(mkTrack(nil), nil, time.Now()) {
		t.Fatal("empty rules should match")
	}
	var nilR *SmartRules
	if !nilR.Match(mkTrack(nil), nil, time.Now()) {
		t.Fatal("nil rules should match")
	}
}

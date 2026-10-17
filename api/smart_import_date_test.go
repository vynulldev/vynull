// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// Date-based rekordbox smart conditions must translate to operators the
// evaluator (dateMatch) actually handles — regression for the bug where
// GREATER/LESS on a date produced "gt"/"lt" (which dateMatch ignores → the
// imported playlist matched nothing).
func TestSmartImportDateConditions(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	// Both dates set: dateCreated rules test DateCreated, stockDate rules
	// DateAdded (rekordbox's StockDate).
	track := func(d time.Time) *library.Track { return &library.Track{ID: 1, DateAdded: d, DateCreated: d} }
	before := track(time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC))
	after := track(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))

	cases := []struct {
		name          string
		cond          library.RBSmartCond
		matchAfter    bool // track dated 2024 should match?
		matchBefore   bool // track dated 2022 should match?
		expectDropped bool
	}{
		{"after 2023 (op GREATER)", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: "2023-01-01"}, true, false, false},
		{"before 2023 (op LESS)", library.RBSmartCond{Property: "dateCreated", Operator: 4, Left: "2023-01-01"}, false, true, false},
		{"in range 2023-2025 (op IN_RANGE)", library.RBSmartCond{Property: "stockDate", Operator: 5, Left: "2023-01-01", Right: "2025-01-01"}, true, false, false},
		{"equal, same day (op EQUAL)", library.RBSmartCond{Property: "dateCreated", Operator: 1, Left: "2024-06-01"}, true, false, false},
		{"not equal (op NOT_EQUAL)", library.RBSmartCond{Property: "dateCreated", Operator: 2, Left: "2024-06-01"}, false, true, false},
		{"not equal a blank date: any dated track", library.RBSmartCond{Property: "dateCreated", Operator: 2, Left: ""}, true, true, false},
		{"equal a blank date — dropped (matches nothing)", library.RBSmartCond{Property: "dateCreated", Operator: 1, Left: ""}, false, false, true},
		{"reversed range — dropped (matches nothing)", library.RBSmartCond{Property: "stockDate", Operator: 5, Left: "2025-01-01", Right: "2023-01-01"}, false, false, true},
		{"unparseable date — dropped", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: "not-a-date"}, false, false, true},
		{"other separators read by position", library.RBSmartCond{Property: "dateCreated", Operator: 1, Left: "2024/06.01"}, true, false, false},
		{"date with a time part — dropped", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: "2023-01-01 12:00:00"}, false, false, true},
		{"date with spaces — dropped", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: " 2023-01-01"}, false, false, true},
		{"epoch date — dropped", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: "1970-01-01"}, false, false, true},
		{"year 9999 — dropped", library.RBSmartCond{Property: "dateCreated", Operator: 3, Left: "9999-01-01"}, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := &library.RBSmartList{Logical: 1, Conditions: []library.RBSmartCond{tc.cond}}
			rules, mapped, dropped := smartRulesFromRekordbox(rb)
			if tc.expectDropped {
				if dropped != 1 || mapped != 0 {
					t.Fatalf("expected condition dropped, got mapped=%d dropped=%d", mapped, dropped)
				}
				return
			}
			if mapped != 1 {
				t.Fatalf("expected 1 mapped condition, got mapped=%d dropped=%d", mapped, dropped)
			}
			if got := rules.Match(after, nil, now); got != tc.matchAfter {
				t.Errorf("2024 track: match=%v want %v", got, tc.matchAfter)
			}
			if got := rules.Match(before, nil, now); got != tc.matchBefore {
				t.Errorf("2022 track: match=%v want %v", got, tc.matchBefore)
			}
		})
	}
}

// rekordbox's stockDate (Date Added) and dateCreated are separate dates: a
// rule on one must not test the other.
func TestSmartImportDateFieldsAreDistinct(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := &library.Track{ID: 1, DateAdded: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)} // no DateCreated
	rule := func(prop string) *SmartRules {
		rules, _, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
			Conditions: []library.RBSmartCond{{Property: prop, Operator: 3, Left: "2023-01-01"}}})
		return rules
	}
	if !rule("stockDate").Match(tr, nil, now) {
		t.Errorf("stockDate after 2023 should match the 2024 date added")
	}
	if rule("dateCreated").Match(tr, nil, now) {
		t.Errorf("dateCreated after 2023 matched a track with no created date")
	}
}

// Relative date rules follow rekordbox's pastDayToDay and pastMonthToDay
// (link-spec conformance smart-relative-date-matrix, rekordbox 7.2.19, whose
// clock and dates these are): in the last n days starts n-1 days back, in
// the last n months starts on the last day of the month n months back, only
// the unit month counts months, and a count below one counts as one.
func TestSmartImportRelativeDates(t *testing.T) {
	now := time.Date(2032, 3, 31, 12, 0, 0, 0, time.UTC)
	dates := []string{"2032-03-31", "2032-03-30", "2032-03-24", "2032-02-29", "2032-02-28",
		"2031-03-31", "2031-03-30", "2032-04-01", "", "not-a-date"}
	tracks := make([]*library.Track, len(dates))
	for i, d := range dates {
		tracks[i] = &library.Track{ID: uint32(i + 1), DateAdded: library.ParseRekordboxDate(d)}
	}
	cases := []struct {
		count, unit string
		in          []uint32 // tracks in the last count units; every other dated track is not
	}{
		{"1", "day", []uint32{1, 8}},
		{"1", "week", []uint32{1, 8}},
		{"1", "year", []uint32{1, 8}},
		{"1", "months", []uint32{1, 8}},
		{"1", "", []uint32{1, 8}},
		{"1", "month", []uint32{1, 2, 3, 4, 8}},
		{"1", "MONTH", []uint32{1, 2, 3, 4, 8}},
		{"2", "day", []uint32{1, 2, 8}},
		{"31", "day", []uint32{1, 2, 3, 8}},
		{"2", "month", []uint32{1, 2, 3, 4, 5, 8}},
		{"0", "day", []uint32{1, 8}},
		{"-1", "day", []uint32{1, 8}},
		{"", "day", []uint32{1, 8}},
		{"not-a-number", "day", []uint32{1, 8}},
		{"1.5", "day", []uint32{1, 8}},
	}
	for _, tc := range cases {
		for _, op := range []int{6, 7} {
			rules, _, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
				Conditions: []library.RBSmartCond{{Property: "stockDate", Operator: op, Left: tc.count, Unit: tc.unit}}})
			var got, want []uint32
			for _, tr := range tracks {
				if rules.Match(tr, nil, now) {
					got = append(got, tr.ID)
				}
				if tr.DateAdded.IsZero() {
					continue
				}
				in := false
				for _, id := range tc.in {
					in = in || id == tr.ID
				}
				if in == (op == 6) {
					want = append(want, tr.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("op %d count %q unit %q: got %v, want %v", op, tc.count, tc.unit, got, want)
			}
		}
	}
}

// A rule on dateReleased tests the track's release date.
func TestSmartImportReleaseDate(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	tr := &library.Track{ID: 1, DateAdded: now, ReleaseDate: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)}
	rules, mapped, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
		Conditions: []library.RBSmartCond{{Property: "dateReleased", Operator: 1, Left: "2024/06/01"}}})
	if mapped != 1 || !rules.Match(tr, nil, now) {
		t.Errorf("dateReleased equal 2024/06/01 should match the 2024-06-01 release date (mapped=%d)", mapped)
	}
}

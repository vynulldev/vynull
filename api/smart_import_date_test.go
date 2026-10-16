// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
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

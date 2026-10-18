// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// An import stores keys as Camelot, but rekordbox key rules carry the names
// rekordbox showed: "key is Am" must still find a track stored as 8A, and a
// rule in either notation must find a key stored in the other.
func TestSmartKeyRulesAcrossNotations(t *testing.T) {
	match := func(op int, left, key string) bool {
		tr := &library.Track{ID: 1, Key: key}
		if op == 0 { // Vynull's own "is compatible with", which rekordbox lacks
			return (&SmartRules{Combinator: "all", Conditions: []SmartCond{
				{Field: "key", Operator: "compatible", Value: left}}}).Match(tr, nil, time.Now())
		}
		rules, _, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
			Conditions: []library.RBSmartCond{{Property: "key", Operator: op, Left: left}}})
		return rules.Match(tr, nil, time.Now())
	}
	for _, tc := range []struct {
		op        int
		left, key string
		want      bool
	}{
		{1, "Am", "8A", true},  // is
		{1, "8A", "Am", true},  // is, the other way round
		{1, "Abm", "1A", true}, // an enharmonic spelling
		{1, "Am", "8B", false}, // a different key
		{2, "Am", "8A", false}, // is not
		{2, "Am", "9A", true},
		{8, "m", "8A", true}, // contains, against the classic name Am
		{9, "m", "8B", true}, // does not contain: C has no m in either name
		{9, "m", "8A", false},
		{10, "8", "8A", true},       // starts with, in Camelot
		{1, " Am", "8A", false},     // text with spaces is not a key name
		{1, "Alpha", "Alpha", true}, // non-key text compares as text
		{0, "Am", "9A", true},       // compatible (a neighbour), typed in classic
		{0, "Am", "3B", false},
	} {
		if got := match(tc.op, tc.left, tc.key); got != tc.want {
			t.Errorf("op %d %q vs key %q = %v, want %v", tc.op, tc.left, tc.key, got, tc.want)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// Numeric rule values and stored numbers compare as rekordbox's do (link-spec
// smart-numeric-matrix and smart-numeric-boundaries, rekordbox 7.2.19).
func TestSmartImportNumericLikeRekordbox(t *testing.T) {
	for in, want := range map[string]int64{
		"0.5": 0, "1.5": 1, "-1": -1, "": 0, "not-a-number": 0, " 7": 7,
		"2147483647": 2147483647, "2147483648": 2147483647, "4294967296": 2147483647,
		"-99999999999": -2147483648,
	} {
		if got := rbInt(in); got != want {
			t.Errorf("rbInt(%q) = %d, want %d", in, got, want)
		}
	}
	match := func(prop string, op int, left, right string, tr *library.Track) bool {
		rules, _, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
			Conditions: []library.RBSmartCond{{Property: prop, Operator: op, Left: left, Right: right}}})
		return rules.Match(tr, nil, time.Now())
	}
	// A stored rating of -1 (255 in a byte) is below zero.
	if !match("rating", 4, "0", "", &library.Track{Rating: 255}) {
		t.Errorf("rating 255 (signed -1) should be less than 0")
	}
	// A negative stored BPM matches no rule, not even its complement.
	neg := &library.Track{BPM: -0.01}
	if match("bpm", 2, "100", "", neg) || match("bpm", 5, "-100", "100", neg) {
		t.Errorf("a negative BPM should match no BPM rule")
	}
	// 0.5 truncates to 0.
	if !match("bpm", 1, "0.5", "", &library.Track{BPM: 0}) {
		t.Errorf("BPM equal 0.5 should match a zero BPM")
	}
}

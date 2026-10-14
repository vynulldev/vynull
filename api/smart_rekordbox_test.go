// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// rekordbox smart-playlist semantics from the link-spec conformance smart
// suites (rekordbox 7.2.19).
func TestRekordboxSmartSemantics(t *testing.T) {
	lib := library.New()
	alpha := lib.AddTrack(&library.Track{Title: "Alpha", Artist: "Alpha", BPM: 120, Key: "Alpha Minor"})
	accented := lib.AddTrack(&library.Track{Title: "Álpha", Artist: "Álpha", BPM: 125})
	decomposed := lib.AddTrack(&library.Track{Title: "Álpha", Artist: "Álpha", BPM: 130})
	other := lib.AddTrack(&library.Track{Title: "Beta", Artist: "Beta", BPM: 140})
	now := time.Now()
	ids := func(rb *library.RBSmartList) map[uint32]bool {
		out := map[uint32]bool{}
		for _, id := range RekordboxSmartTracks(rb, lib, nil, now) {
			out[id] = true
		}
		return out
	}
	cond := func(prop string, op int, left, right string) library.RBSmartCond {
		return library.RBSmartCond{Property: prop, Operator: op, Left: left, Right: right}
	}
	all := func(cs ...library.RBSmartCond) *library.RBSmartList {
		return &library.RBSmartList{Logical: 1, Conditions: cs}
	}
	anyOf := func(cs ...library.RBSmartCond) *library.RBSmartList {
		return &library.RBSmartList{Logical: 2, Conditions: cs}
	}

	// Case and accents are ignored; the track's text is normalized first, so
	// a track stored with a separate combining accent equals "alpha" too.
	if got := ids(all(cond("artist", 1, "alpha", ""))); len(got) != 3 || got[other] {
		t.Errorf("text equality: %v, want the three Alpha spellings", got)
	}
	// The rule's value is taken as written: spelled with a separate combining
	// accent it matches nothing, not even identical track text, as in
	// rekordbox.
	if got := ids(all(cond("artist", 1, "A\u0301lpha", ""))); len(got) != 0 {
		t.Errorf("rule with a combining accent should match nothing: %v", got)
	}
	if got := ids(all(cond("artist", 8, "", ""))); len(got) != 0 {
		t.Errorf("contains an empty value should match nothing: %v", got)
	}
	for name, rb := range map[string]*library.RBSmartList{
		"no rules":                 nil,
		"empty rule set":           all(),
		"unknown property":         all(cond("artist", 1, "Alpha", ""), cond("composerAlias", 1, "x", "")),
		"rejected look-alike name": all(cond("playCount", 1, "0", "")),
		"MyTag with equality":      all(cond("myTag", 1, "Warmup", "")),
		"operator not for numbers": all(cond("bpm", 10, "120", "")),
		"reversed range":           all(cond("bpm", 5, "13000", "12000")),
	} {
		if got := ids(rb); len(got) != 0 {
			t.Errorf("%s: matched %v, want nothing", name, got)
		}
	}
	// In an any-of set an unsatisfiable condition just never matches.
	if got := ids(anyOf(cond("bpm", 10, "120", ""), cond("artist", 1, "Beta", ""))); len(got) != 1 || !got[other] {
		t.Errorf("any-of with an unsatisfiable condition: %v", got)
	}
	if got := ids(all(cond("bpm", 5, "11900", "12600"))); !got[alpha] || !got[accented] || got[decomposed] {
		t.Errorf("in-range BPM: %v", got)
	}
	if got := ids(all(cond("key", 8, "alpha", ""))); len(got) != 1 || !got[alpha] {
		t.Errorf("key contains should compare the key name: %v", got)
	}
}

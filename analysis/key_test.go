// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestParseKey(t *testing.T) {
	for s, want := range map[string]Key{
		"8A": {8, true}, "8a": {8, true}, " 11B ": {11, false}, "08A": {8, true},
		"Am": {8, true}, "C": {8, false}, "Abm": {1, true}, "G#m": {1, true}, "Dbm": {12, true},
	} {
		if k, ok := ParseKey(s); !ok || k != want {
			t.Errorf("ParseKey(%q) = %v, %v, want %v", s, k, ok, want)
		}
	}
	for _, s := range []string{"", "8", "8C", "13A", "0A", "H", "Alpha", "100A"} {
		if k, ok := ParseKey(s); ok {
			t.Errorf("ParseKey(%q) = %v, want no key", s, k)
		}
	}
}

// KeyByName knows only key names, not every way of writing a Camelot
// position.
func TestKeyByName(t *testing.T) {
	if k, ok := KeyByName("am"); !ok || k != (Key{8, true}) {
		t.Errorf("KeyByName(am) = %v, %v", k, ok)
	}
	if _, ok := KeyByName("08A"); ok {
		t.Error("KeyByName(08A) found a key")
	}
}

func TestKeyIndex(t *testing.T) {
	for i := 1; i <= 24; i++ {
		k, ok := KeyAtIndex(i)
		if !ok || k.Index() != i {
			t.Errorf("KeyAtIndex(%d) = %v, %v, index %d", i, k, ok, k.Index())
		}
	}
	for s, want := range map[string]int{"1A": 1, "1B": 2, "8A": 15, "C": 16, "12B": 24} {
		if k, _ := ParseKey(s); k.Index() != want {
			t.Errorf("%s: index %d, want %d", s, k.Index(), want)
		}
	}
	if _, ok := KeyAtIndex(25); ok {
		t.Error("KeyAtIndex(25) found a key")
	}
	if (Key{}).Index() != 0 || (Key{}).Camelot() != "" {
		t.Error("no key has an index or a name")
	}
}

func TestKeyNeighbours(t *testing.T) {
	k := Key{12, true}
	if got := k.Relative().Camelot(); got != "12B" {
		t.Errorf("12A relative %s", got)
	}
	if got := k.Step(1).Camelot(); got != "1A" {
		t.Errorf("12A step 1 %s", got)
	}
	if got := (Key{1, false}).Step(-1).Camelot(); got != "12B" {
		t.Errorf("1B step -1 %s", got)
	}
	if got := k.Step(25).Camelot(); got != "1A" {
		t.Errorf("12A step 25 %s", got)
	}
}

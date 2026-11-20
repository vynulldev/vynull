// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"testing"
)

// A library's own colour names replace the stock ones by colour ID, keep
// only real changes (a stock or empty name, or an ID outside 1-8, is
// dropped), move the library's revision, and survive a reload.
func TestColorNames(t *testing.T) {
	dir := t.TempDir()
	lib := New()
	lib.SetDBPath(filepath.Join(dir, "library.json"))
	if got := lib.ColorName(1); got != "Pink" {
		t.Errorf("before: colour 1 %q, want Pink", got)
	}
	rev := lib.Rev()
	lib.SetColorNames(map[uint8]string{1: "Fixture Magenta", 2: "Red", 3: "", 8: "Fixture Violet", 9: "Nine"})
	if lib.Rev() == rev {
		t.Errorf("the revision didn't move")
	}
	for id, want := range map[uint8]string{1: "Fixture Magenta", 2: "Red", 3: "Orange", 8: "Fixture Violet", 9: ""} {
		if got := lib.ColorName(id); got != want {
			t.Errorf("colour %d %q, want %q", id, got, want)
		}
	}

	again := New()
	again.SetDBPath(filepath.Join(dir, "library.json"))
	if got := again.ColorName(8); got != "Fixture Violet" {
		t.Errorf("after a reload: colour 8 %q, want Fixture Violet", got)
	}
}

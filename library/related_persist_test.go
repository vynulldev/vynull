// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"testing"
)

// Imported recommendation pairs persist with the library, so MATCHING still
// has them after a restart.
func TestRelatedPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	lib := New()
	lib.SetDBPath(path)
	a := lib.AddTrack(&Track{Title: "A"})
	b := lib.AddTrack(&Track{Title: "B"})
	c := lib.AddTrack(&Track{Title: "C"})
	lib.AddRelated(a, b)
	lib.AddRelated(c, a)
	lib.AddRelated(a, b) // duplicate
	lib.Save()

	re := New()
	re.SetDBPath(path)
	if got := re.Related(a); len(got) != 2 || got[0] != b || got[1] != c {
		t.Errorf("Related(A) after reload = %v, want [%d %d]", got, b, c)
	}
	if got := re.Related(b); len(got) != 1 || got[0] != a {
		t.Errorf("Related(B) after reload = %v, want [%d]", got, a)
	}
	if got := re.Related(9999); got != nil {
		t.Errorf("Related(unknown) = %v", got)
	}
}

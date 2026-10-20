// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// RefreshMissing flags tracks whose file is absent or that have no path,
// clears the flag on a track whose file has come back, and saves only when a
// flag changed.
func TestRefreshMissing(t *testing.T) {
	dir := t.TempDir()
	here := filepath.Join(dir, "here.mp3")
	os.WriteFile(here, []byte("x"), 0o644)
	lib := newLib(
		&library.Track{ID: 1, FilePath: here},
		&library.Track{ID: 2, FilePath: filepath.Join(dir, "gone.mp3")},
		&library.Track{ID: 3},
		&library.Track{ID: 4, FilePath: here, FileMissing: true},
	)
	rev := lib.Rev()
	if n := lib.RefreshMissing(); n != 2 {
		t.Errorf("%d missing, want 2", n)
	}
	for id, want := range map[uint32]bool{1: false, 2: true, 3: true, 4: false} {
		if got := lib.Track(id).FileMissing; got != want {
			t.Errorf("track %d missing %v, want %v", id, got, want)
		}
	}
	if lib.Rev() == rev {
		t.Error("changed flags weren't saved")
	}
	rev = lib.Rev()
	if n := lib.RefreshMissing(); n != 2 || lib.Rev() != rev {
		t.Errorf("second pass: %d missing, saved %v; want 2 and no save", n, lib.Rev() != rev)
	}
}

// SetFileMissing saves only when the flag changes.
func TestSetFileMissing(t *testing.T) {
	lib := newLib(&library.Track{ID: 1})
	rev := lib.Rev()
	if !lib.SetFileMissing(1, true) || !lib.Track(1).FileMissing || lib.Rev() == rev {
		t.Fatal("flagging a track missing didn't change and save it")
	}
	rev = lib.Rev()
	if lib.SetFileMissing(1, true) || lib.Rev() != rev {
		t.Error("flagging an already-missing track changed or saved it")
	}
	if lib.SetFileMissing(99, true) {
		t.Error("an unknown track reported a change")
	}
}

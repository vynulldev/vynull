// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Flush writes a change and skips a Flush with nothing to write; Close
// writes what's pending. (The background timing is internal/saver's test.)
func TestLibraryWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	reload := func() *Library {
		r := New()
		r.SetDBPath(path)
		return r
	}

	lib := New()
	lib.SetDBPath(path)
	lib.AddTrack(&Track{ID: 1, Title: "One"})
	if err := lib.Flush(); err != nil {
		t.Fatal(err)
	}
	if reload().Track(1) == nil {
		t.Fatal("Flush didn't write the track")
	}
	fi, _ := os.Stat(path)
	time.Sleep(20 * time.Millisecond)
	if err := lib.Flush(); err != nil {
		t.Fatal(err)
	}
	if fj, _ := os.Stat(path); !fj.ModTime().Equal(fi.ModTime()) {
		t.Error("a Flush with nothing to write rewrote the file")
	}

	lib.Update(1, func(tr *Track) error { tr.Title = "Changed"; return nil })
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}
	if got := reload().Track(1); got == nil || got.Title != "Changed" {
		t.Errorf("after Close: %+v, want the change written", got)
	}

	// A library without a file saves nothing and closes cleanly.
	mem := New()
	mem.AddTrack(&Track{ID: 2})
	if err := mem.Close(); err != nil {
		t.Errorf("Close without a file: %v", err)
	}
}

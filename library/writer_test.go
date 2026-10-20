// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The writer saves a change in the background without a Flush, skips a
// Flush with nothing to write, and Close writes what's pending.
func TestBackgroundWriter(t *testing.T) {
	defer func(d time.Duration) { saveInterval = d }(saveInterval)
	saveInterval = 10 * time.Millisecond
	path := filepath.Join(t.TempDir(), "library.json")
	reload := func() *Library {
		r := New()
		r.SetDBPath(path)
		return r
	}

	lib := New()
	lib.SetDBPath(path)
	lib.AddTrack(&Track{ID: 1, Title: "One"})
	deadline := time.Now().Add(5 * time.Second)
	for reload().Track(1) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the background writer never saved the track")
		}
		time.Sleep(5 * time.Millisecond)
	}

	lib.Flush()
	fi, _ := os.Stat(path)
	time.Sleep(20 * time.Millisecond)
	if err := lib.Flush(); err != nil {
		t.Fatal(err)
	}
	if fj, _ := os.Stat(path); !fj.ModTime().Equal(fi.ModTime()) {
		t.Error("a Flush with nothing to write rewrote the file")
	}

	lib.Close()
	saveInterval = time.Hour // a new library's writer won't write before Close
	lib = New()
	lib.SetDBPath(path)
	lib.Update(1, func(tr *Track) error { tr.Title = "Changed"; return nil })
	if err := lib.Close(); err != nil {
		t.Fatal(err)
	}
	if got := reload().Track(1); got == nil || got.Title != "Changed" {
		t.Errorf("after Close: %+v, want the change written", got)
	}
	if err := lib.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

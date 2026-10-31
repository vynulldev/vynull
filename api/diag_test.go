// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDirSizeMeasuredInBackground checks the first request measures the
// directory, later ones answer from that measurement while it is fresh, and
// a stale one is answered at once and measured again in the background.
func TestDirSizeMeasuredInBackground(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.gob", 100)
	var d dirSize
	if got := d.get(dir, time.Hour); got != 100 {
		t.Fatalf("first measurement %d, want 100", got)
	}
	write("b.png", 50)
	if got := d.get(dir, time.Hour); got != 100 {
		t.Errorf("fresh measurement walked again: %d, want the 100 measured", got)
	}
	if got := d.get(dir, 0); got != 100 {
		t.Errorf("stale measurement: %d, want the last one, 100, at once", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.get(dir, time.Hour) != 150 {
		if time.Now().After(deadline) {
			t.Fatalf("background measurement never arrived: %d, want 150", d.get(dir, time.Hour))
		}
		time.Sleep(time.Millisecond)
	}
}

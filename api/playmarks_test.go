// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// Unplayed marks survive a restart and clear when the track is played.
func TestPlayMarks(t *testing.T) {
	dir := t.TempDir()
	pm := NewPlayMarks(dir)
	pm.MarkUnplayed(7)
	pm.MarkUnplayed(9)
	again := NewPlayMarks(dir)
	if !again.IsUnplayed(7) || !again.IsUnplayed(9) || again.IsUnplayed(8) {
		t.Fatalf("marks not restored")
	}
	again.Played(7)
	if third := NewPlayMarks(dir); third.IsUnplayed(7) || !third.IsUnplayed(9) {
		t.Errorf("playing track 7 should clear only its mark")
	}
}

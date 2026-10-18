// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// Played and unplayed marks survive a restart and clear when the track is
// played.
func TestPlayMarks(t *testing.T) {
	dir := t.TempDir()
	pm := NewPlayMarks(dir)
	pm.MarkPlayed(7, false)
	pm.MarkPlayed(9, true)
	again := NewPlayMarks(dir)
	if p, ok := again.PlayMark(7); !ok || p {
		t.Errorf("track 7: %v %v, want marked unplayed", p, ok)
	}
	if p, ok := again.PlayMark(9); !ok || !p {
		t.Errorf("track 9: %v %v, want marked played", p, ok)
	}
	if _, ok := again.PlayMark(8); ok {
		t.Errorf("track 8 has a mark")
	}
	again.Played(7)
	third := NewPlayMarks(dir)
	if _, ok := third.PlayMark(7); ok {
		t.Errorf("playing track 7 should clear its mark")
	}
	if _, ok := third.PlayMark(9); !ok {
		t.Errorf("track 9's mark should remain")
	}
}

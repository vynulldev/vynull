// SPDX-License-Identifier: GPL-3.0-or-later

package library

import "testing"

func TestTrackColorName(t *testing.T) {
	for id, want := range map[uint8]string{0: "", 1: "Pink", 4: "Yellow", 8: "Purple", 9: "", 255: ""} {
		if got := TrackColorName(id); got != want {
			t.Errorf("TrackColorName(%d) = %q, want %q", id, got, want)
		}
	}
}

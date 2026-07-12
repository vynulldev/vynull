// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

func TestSelectNowPlaying(t *testing.T) {
	p := func(dev uint8, title string, playing, onair, master bool) PlayerInfo {
		return PlayerInfo{DeviceNumber: dev, TrackTitle: title, IsPlaying: playing, OnAir: onair, IsMaster: master}
	}
	cases := []struct {
		name    string
		players []PlayerInfo
		wantDev uint8 // 0 = expect nil
	}{
		{"none", nil, 0},
		{"loaded but not playing", []PlayerInfo{p(1, "A", false, true, true)}, 0},
		{"playing with no track", []PlayerInfo{p(1, "", true, true, true)}, 0},
		{"single on-air playing", []PlayerInfo{p(2, "A", true, true, false)}, 2},
		{"on-air beats off-air", []PlayerInfo{p(1, "A", true, false, true), p(2, "B", true, true, false)}, 2},
		{"master beats non-master, both on-air", []PlayerInfo{p(3, "A", true, true, false), p(1, "B", true, true, true)}, 1},
		{"no mixer: master then lowest dev", []PlayerInfo{p(3, "A", true, false, false), p(2, "B", true, false, true)}, 2},
		{"no mixer, no master: lowest dev", []PlayerInfo{p(3, "A", true, false, false), p(1, "B", true, false, false)}, 1},
	}
	for _, c := range cases {
		got := selectNowPlaying(c.players)
		if c.wantDev == 0 {
			if got != nil {
				t.Errorf("%s: got dev %d, want nil", c.name, got.DeviceNumber)
			}
			continue
		}
		if got == nil || got.DeviceNumber != c.wantDev {
			t.Errorf("%s: got %+v, want dev %d", c.name, got, c.wantDev)
		}
	}
}

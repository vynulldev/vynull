// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

func TestNearestCueColorID(t *testing.T) {
	cases := []struct {
		r, g, b byte
		want    uint32
		name    string
	}{
		{0x28, 0xe2, 0x14, 0x16, "exact palette green round-trips"},
		{0x00, 0xff, 0x30, 0x16, "CDJ's brighter green maps to our green"},
		{0xff, 0x6a, 0x00, 0x25, "orange maps to a real orange, not the unset slot"},
	}
	for _, c := range cases {
		if got := NearestCueColorID(c.r, c.g, c.b); got != c.want {
			t.Errorf("%s: nearest(%#x,%#x,%#x) = %#x, want %#x", c.name, c.r, c.g, c.b, got, c.want)
		}
	}
}

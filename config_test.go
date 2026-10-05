// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "testing"

func TestParseBPMRange(t *testing.T) {
	ok := []struct {
		in     string
		lo, hi float64
	}{
		{"160-185", 160, 185},
		{" 160 - 185 ", 160, 185},
		{"88.5-176", 88.5, 176},
	}
	for _, c := range ok {
		lo, hi, err := parseBPMRange(c.in)
		if err != nil {
			t.Errorf("parseBPMRange(%q) unexpected error: %v", c.in, err)
			continue
		}
		if lo != c.lo || hi != c.hi {
			t.Errorf("parseBPMRange(%q) = %g-%g, want %g-%g", c.in, lo, hi, c.lo, c.hi)
		}
	}

	bad := []string{
		"174",      // single value (not yet supported — must be a range)
		"185-160",  // inverted
		"160-160",  // zero width
		"0-185",    // non-positive bound
		"fast-185", // non-numeric
		"160-",     // missing upper
		"",         // empty
	}
	for _, in := range bad {
		if _, _, err := parseBPMRange(in); err == nil {
			t.Errorf("parseBPMRange(%q) = nil error, want an error", in)
		}
	}
}

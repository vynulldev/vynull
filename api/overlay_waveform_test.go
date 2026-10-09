// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "testing"

// TestOverlayWaveformConfig pins the waveform-style and full-band config: the
// style validates (half/full, bad -> default), the band height defaults and
// clamps, and the fields round-trip through sanitize.
func TestOverlayWaveformConfig(t *testing.T) {
	if d := defaultOverlayConfig(); d.WaveformBand || d.WaveformStyle != "full" || d.WaveformHeight != 180 {
		t.Fatalf("defaults = band:%v style:%q height:%d, want band:false style:full height:180",
			d.WaveformBand, d.WaveformStyle, d.WaveformHeight)
	}

	if got := (OverlayConfig{WaveformStyle: "half"}).sanitize(); got.WaveformStyle != "half" {
		t.Errorf("style half dropped: %q", got.WaveformStyle)
	}
	if got := (OverlayConfig{WaveformStyle: "bogus"}).sanitize(); got.WaveformStyle != "full" {
		t.Errorf("bad style not defaulted to full: %q", got.WaveformStyle)
	}

	for _, c := range []struct{ in, want int }{
		{0, 180},    // unset → default
		{200, 200},  // in range → kept
		{10, 60},    // below min → clamped up
		{9999, 600}, // above max → clamped down
	} {
		got := OverlayConfig{WaveformBand: true, WaveformHeight: c.in}.sanitize()
		if !got.WaveformBand {
			t.Errorf("WaveformBand dropped by sanitize")
		}
		if got.WaveformHeight != c.want {
			t.Errorf("height %d sanitized to %d, want %d", c.in, got.WaveformHeight, c.want)
		}
	}
}

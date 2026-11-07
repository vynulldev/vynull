// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"math"
	"testing"
)

// An MP3 with gapless metadata leads by its encoder delay plus the frame
// carrying the metadata (LAME: 576 + 529 samples of delay, one 1152-sample
// frame, about 51.2 ms at 44.1 kHz); one without it, and a FLAC, by nothing.
func TestDecodeLeadMs(t *testing.T) {
	want := (576 + 529 + 1152) / 44100.0 * 1000
	for _, c := range []struct {
		name string
		args []string
		want float64
	}{
		{"gapless.mp3", []string{"-c:a", "libmp3lame", "-q:a", "2"}, want},
		{"untagged.mp3", []string{"-c:a", "libmp3lame", "-q:a", "2", "-write_xing", "0"}, 0},
		{"tone.flac", nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := DecodeLeadMs(encodeTestAudio(t, c.name, c.args...)); math.Abs(got-c.want) > 0.5 {
				t.Errorf("lead %.2f ms, want %.2f", got, c.want)
			}
		})
	}
}

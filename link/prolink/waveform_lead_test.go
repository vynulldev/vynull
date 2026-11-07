// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// A track whose deck timeline leads our decode gets that lead as pre-roll at
// the start of each detail waveform, in that format's silence, and nothing
// else changes.
func TestDetailWaveformLead(t *testing.T) {
	const sr = 44100
	samples := make([]float32, sr*4)
	for i := range samples {
		samples[i] = float32(0.5 * math.Sin(2*math.Pi*60*float64(i)/sr) * math.Exp(-float64(i%(sr/2))/float64(sr/20)))
	}
	encode := func(leadMs float64) *analysis.Result {
		r := &analysis.Result{BPM: 120, WaveLeadMs: leadMs}
		if err := (pioneerEncoder{}).Encode(context.Background(), samples, sr, r, &analysis.BeatResult{BPM: 120}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	base, led := encode(0), encode(51.2) // 51.2 ms is 8 entries at 150 per second
	for _, c := range []struct {
		name      string
		base, led []byte
		silent    []byte
	}{
		{"color detail", base.WaveDetail, led.WaveDetail, []byte{0xff, 0x80}},
		{"mono detail", base.WaveDetailMono, led.WaveDetailMono, []byte{0xe0}},
		{"3-band detail", base.WaveDetail3Band, led.WaveDetail3Band, []byte{0, 0, 0}},
	} {
		pre := 8 * len(c.silent)
		if len(c.led) != len(c.base)+pre {
			t.Errorf("%s: %d bytes with the lead, want %d", c.name, len(c.led), len(c.base)+pre)
			continue
		}
		if !bytes.Equal(c.led[:pre], bytes.Repeat(c.silent, 8)) {
			t.Errorf("%s: lead isn't pre-roll: % x", c.name, c.led[:pre])
		}
		if !bytes.Equal(c.led[pre:], c.base) {
			t.Errorf("%s: the waveform after the lead changed", c.name)
		}
	}
	if !bytes.Equal(base.WavePreview, led.WavePreview) || !bytes.Equal(base.WaveColorPreview, led.WaveColorPreview) {
		t.Error("the previews changed with the lead")
	}
}

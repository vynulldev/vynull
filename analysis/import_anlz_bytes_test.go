// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"bytes"
	"testing"
)

// anlzBytes wraps sections in a PMAI header, returning the whole ANLZ file in
// memory (the byte form ParseANLZBytes / ParseANLZCuesBytes consume).
func anlzBytes(sections ...[]byte) []byte {
	var all []byte
	for _, s := range sections {
		all = append(all, s...)
	}
	hdr := []byte("PMAI")
	hdr = append(hdr, be32(28)...)
	hdr = append(hdr, be32(uint32(28+len(all)))...)
	hdr = append(hdr, make([]byte, 16)...) // pad to a 28-byte header
	return append(hdr, all...)
}

func TestParseANLZBytes(t *testing.T) {
	// .EXT with a colour-detail (PWV5) waveform section.
	wave := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	ext := anlzBytes(makeSection(tagPWV5, make([]byte, 8), wave))

	// .DAT with a 4-beat grid at 120 BPM (500ms/beat), first beat a downbeat.
	beats := []float64{0, 500, 1000, 1500}
	dat := anlzBytes(makeBeatGridSection(120, beats, 0))

	r := ParseANLZBytes(dat, ext, nil, 120, 180)
	if r == nil {
		t.Fatal("ParseANLZBytes returned nil")
	}
	if !bytes.Equal(r.WaveDetail, wave) {
		t.Errorf("WaveDetail = % x, want % x", r.WaveDetail, wave)
	}
	if len(r.Beats) != len(beats) {
		t.Fatalf("got %d beats, want %d", len(r.Beats), len(beats))
	}
	for i, b := range beats {
		if r.Beats[i] != b {
			t.Errorf("beat %d = %v, want %v", i, r.Beats[i], b)
		}
	}

	// A set with no recognizable sections yields nil (so callers fall back).
	if got := ParseANLZBytes(nil, nil, nil, 120, 180); got != nil {
		t.Errorf("empty ANLZ should parse to nil, got %+v", got)
	}
}

func TestParseANLZCuesBytes(t *testing.T) {
	ext := anlzBytes(buildPCO2(1,
		buildPCP2(1, 1, 3347, 0xffffffff, "Intro", 0x05),
		buildPCP2(2, 2, 5747, 12000, "", 0x15),
	))
	cues := ParseANLZCuesBytes(ext, nil)
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}
	if c := cues[0]; c.HotCue != 1 || c.TimeMs != 3347 || c.Comment != "Intro" || c.ColorID != 0x05 {
		t.Errorf("cue0 wrong: %+v", c)
	}
	if c := cues[1]; c.HotCue != 2 || !c.IsLoop || c.LoopMs != 12000 {
		t.Errorf("cue1 wrong: %+v", c)
	}
}

// A cue set on a CDJ carries palette index 0 with the real colour only in its
// RGB bytes; the parser must recover the palette id (its default hot-cue green
// 0x00ff30 -> 0x16) rather than leave it 0 (which paints Pioneer orange).
func TestParseANLZCuesColorFromRGB(t *testing.T) {
	e := buildPCP2(1, 1, 1000, 0xffffffff, "", 0) // color_code 0
	// buildPCP2 appends color_code + R,G,B then 8 padding bytes; set the RGB.
	e[len(e)-11], e[len(e)-10], e[len(e)-9] = 0x00, 0xff, 0x30
	ext := anlzBytes(buildPCO2(1, e))

	cues := ParseANLZCuesBytes(ext, nil)
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if cues[0].ColorID != 0x16 {
		t.Fatalf("ColorID = %#x, want 0x16 (green recovered from RGB)", cues[0].ColorID)
	}
}

// The CDJ-2000NXS2 writes a plain hot cue with no colour section and
// loop_time 0: it must come out green (the unset default, not palette-0 orange)
// and NOT be treated as a loop.
func TestParseANLZCuesUnsetDefaultsGreen(t *testing.T) {
	ext := anlzBytes(buildPCO2(1, buildPCP2(1, 1, 16044, 0, "", 0)))
	cues := ParseANLZCuesBytes(ext, nil)
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if cues[0].ColorID != 0x16 {
		t.Errorf("ColorID = %#x, want 0x16 (unset -> green)", cues[0].ColorID)
	}
	if cues[0].IsLoop {
		t.Errorf("loop_time 0 should not be a loop: %+v", cues[0])
	}
}

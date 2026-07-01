// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

// buildPCP2 builds a PCO2 cue entry matching the verified rekordbox layout.
func buildPCP2(hot int, etype byte, timeMs, loop uint32, comment string, colorCode byte) []byte {
	e := []byte("PCP2")
	e = append(e, be32(16)...) // len_header
	lenPos := len(e)
	e = append(e, be32(0)...)           // len_entry (patched below)
	e = append(e, be32(uint32(hot))...) // @12 hot_cue
	e = append(e, etype, 0, 0x03, 0xe8) // @16 type + 3 unk
	e = append(e, be32(timeMs)...)      // @20 time
	e = append(e, be32(loop)...)        // @24 loop_time
	e = append(e, 0)                    // @28 color_id (unused)
	e = append(e, make([]byte, 7)...)   // @29 unknown2
	e = append(e, be16(0)...)           // @36 loop_num
	e = append(e, be16(0)...)           // @38 loop_den
	// comment (UTF-16BE + null terminator)
	u := utf16.Encode([]rune(comment))
	cb := make([]byte, 0, (len(u)+1)*2)
	for _, c := range u {
		cb = append(cb, byte(c>>8), byte(c))
	}
	cb = append(cb, 0, 0)                   // terminator
	e = append(e, be32(uint32(len(cb)))...) // @40 len_comment
	e = append(e, cb...)                    // @44 comment
	e = append(e, colorCode, 0, 0, 0)       // color_code + RGB
	e = append(e, make([]byte, 8)...)       // trailing padding
	binary.BigEndian.PutUint32(e[lenPos:], uint32(len(e)))
	return e
}

func buildPCO2(listType uint32, entries ...[]byte) []byte {
	body := append([]byte{}, be32(listType)...)
	body = append(body, be16(uint16(len(entries)))...)
	body = append(body, be16(0)...)
	for _, e := range entries {
		body = append(body, e...)
	}
	sec := []byte("PCO2")
	sec = append(sec, be32(20)...)                   // len_header
	sec = append(sec, be32(uint32(12+len(body)))...) // len_tag
	sec = append(sec, body...)
	return sec
}

func writeANLZ(t *testing.T, sections ...[]byte) string {
	hdr := []byte("PMAI")
	hdr = append(hdr, be32(28)...)
	var all []byte
	for _, s := range sections {
		all = append(all, s...)
	}
	hdr = append(hdr, be32(uint32(28+len(all)))...)
	hdr = append(hdr, make([]byte, 16)...) // pad to 28
	p := filepath.Join(t.TempDir(), "ANLZ0000.EXT")
	if err := os.WriteFile(p, append(hdr, all...), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseANLZCues(t *testing.T) {
	hotList := buildPCO2(1,
		buildPCP2(1, 1, 3347, 0xffffffff, "Intro", 0x05),
		buildPCP2(2, 2, 5747, 12000, "", 0x15), // a loop
	)
	memList := buildPCO2(0,
		buildPCP2(0, 1, 1000, 0xffffffff, "Memory A", 0x2a),
	)
	ext := writeANLZ(t, hotList, memList)
	cues := ParseANLZCues(ext, "/nonexistent.DAT")
	if len(cues) != 3 {
		t.Fatalf("expected 3 cues, got %d", len(cues))
	}
	// hot A
	if c := cues[0]; c.HotCue != 1 || c.IsLoop || c.TimeMs != 3347 || c.ColorID != 0x05 || c.Comment != "Intro" {
		t.Errorf("cue0 wrong: %+v", c)
	}
	// hot B loop
	if c := cues[1]; c.HotCue != 2 || !c.IsLoop || c.TimeMs != 5747 || c.LoopMs != 12000 || c.ColorID != 0x15 {
		t.Errorf("cue1 (loop) wrong: %+v", c)
	}
	// memory cue
	if c := cues[2]; c.HotCue != 0 || c.TimeMs != 1000 || c.Comment != "Memory A" || c.ColorID != 0x2a {
		t.Errorf("cue2 (memory) wrong: %+v", c)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"os"
	"strings"
	"unicode/utf16"
)

// ImportedCue is a cue point read from a rekordbox ANLZ cue list. Both hot cues
// and memory cues come through here; HotCue distinguishes them.
type ImportedCue struct {
	HotCue  int    // 0 = memory cue; 1..8 = hot cue A..H
	IsLoop  bool   // true for a saved loop (has a loop end)
	TimeMs  uint32 // cue position in milliseconds
	LoopMs  uint32 // loop end in ms (valid only when IsLoop)
	ColorID uint32 // rekordbox hot-cue colour code (0x00-0x3e); 0 = none
	Comment string // cue comment/label (PCO2 only)
}

// ParseANLZCues reads hot + memory cue points from a rekordbox ANLZ pair.
// It prefers the NXS2 "PCO2" list (carries colour + comment); when no PCO2 is
// present it falls back to the older "PCOB" list. extPath is the .EXT file
// (where PCO2 and PCOB live); datPath is the .DAT (PCOB also appears there).
// Returns nil when the track has no saved cues.
//
// Section/entry layout was verified against rekordbox NXS2 exports:
//
//	PCOB entry (PCPT, 56 bytes): hot_cue@12 u4, type@28 u1 (1=cue,2=loop),
//	    time@32 u4 ms, loop_time@36 u4 (0xffffffff = not a loop).
//	PCO2 entry (PCP2): hot_cue@12 u4, type@16 u1, time@20 u4, loop_time@24 u4,
//	    len_comment@40 u4, comment@44 (UTF-16BE), then color_code/R/G/B.
func ParseANLZCues(extPath, datPath string) []ImportedCue {
	if cues := parsePCO2(extPath); len(cues) > 0 {
		return cues
	}
	if cues := parsePCOB(extPath); len(cues) > 0 {
		return cues
	}
	return parsePCOB(datPath)
}

// walkANLZSections calls fn for every section whose FourCC equals tag.
func walkANLZSections(path, tag string, fn func(section []byte)) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 28 {
		return
	}
	pos := int(binary.BigEndian.Uint32(data[4:8])) // skip PMAI header
	for pos+12 <= len(data) {
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		if string(data[pos:pos+4]) == tag {
			fn(data[pos : pos+secLen])
		}
		pos += secLen
	}
}

func parsePCO2(path string) []ImportedCue {
	var out []ImportedCue
	walkANLZSections(path, "PCO2", func(s []byte) {
		lenHeader := int(binary.BigEndian.Uint32(s[4:8]))
		for p := lenHeader; p+12 <= len(s); {
			if string(s[p:p+4]) != "PCP2" {
				break
			}
			el := int(binary.BigEndian.Uint32(s[p+8 : p+12]))
			if el < 44 || p+el > len(s) {
				break
			}
			e := s[p : p+el]
			c := ImportedCue{
				HotCue: int(binary.BigEndian.Uint32(e[12:16])),
				TimeMs: binary.BigEndian.Uint32(e[20:24]),
			}
			if loop := binary.BigEndian.Uint32(e[24:28]); e[16] == 2 || loop != 0xffffffff {
				c.IsLoop = true
				if loop != 0xffffffff {
					c.LoopMs = loop
				}
			}
			lenComment := int(binary.BigEndian.Uint32(e[40:44]))
			coff := 44
			if lenComment > 0 && coff+lenComment <= len(e) {
				c.Comment = decodeUTF16BE(e[coff : coff+lenComment])
			}
			if colOff := coff + lenComment; colOff < len(e) {
				c.ColorID = uint32(e[colOff]) // color_code byte
			}
			out = append(out, c)
			p += el
		}
	})
	return out
}

func parsePCOB(path string) []ImportedCue {
	var out []ImportedCue
	walkANLZSections(path, "PCOB", func(s []byte) {
		lenHeader := int(binary.BigEndian.Uint32(s[4:8]))
		for p := lenHeader; p+12 <= len(s); {
			if string(s[p:p+4]) != "PCPT" {
				break
			}
			el := int(binary.BigEndian.Uint32(s[p+8 : p+12]))
			if el < 40 || p+el > len(s) {
				break
			}
			e := s[p : p+el]
			c := ImportedCue{
				HotCue: int(binary.BigEndian.Uint32(e[12:16])),
				TimeMs: binary.BigEndian.Uint32(e[32:36]),
			}
			if loop := binary.BigEndian.Uint32(e[36:40]); e[28] == 2 || loop != 0xffffffff {
				c.IsLoop = true
				if loop != 0xffffffff {
					c.LoopMs = loop
				}
			}
			out = append(out, c)
			p += el
		}
	})
	return out
}

func decodeUTF16BE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

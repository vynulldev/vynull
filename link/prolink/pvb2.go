// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import "encoding/binary"

// makeSection builds a tagged ANLZ section: fourcc(4) + len_header(4) +
// len_tag(4) + extra + body. A self-contained copy of the analysis-side
// helper; kept here so GeneratePVB2 can emit its section without depending on
// analysis's unexported ANLZ writers (the PVB2 golden test guards the bytes).
func makeSection(fourcc string, headerExtra, body []byte) []byte {
	lenHeader := uint32(12 + len(headerExtra))
	lenTag := lenHeader + uint32(len(body))
	buf := make([]byte, lenTag)
	copy(buf[0:4], fourcc)
	binary.BigEndian.PutUint32(buf[4:], lenHeader)
	binary.BigEndian.PutUint32(buf[8:], lenTag)
	copy(buf[12:], headerExtra)
	copy(buf[12+len(headerExtra):], body)
	return buf
}

// makeEmptyVB2Section creates an empty PVB2 (extended VBR) section.
// The format uses len_header=32, len_tag=8032 (32 + 8000 byte body).
// Extra header has a u4 file_size embedded — we use 0 since this is a
// placeholder; the deck appears to treat it as advisory.
func makeEmptyVB2Section() []byte {
	extra := make([]byte, 20)
	// bytes 8-11: file size (u4) — leave 0 for placeholder
	binary.BigEndian.PutUint32(extra[12:], 0x00000190) // 400
	binary.BigEndian.PutUint32(extra[16:], 0x00000014) // 20
	body := make([]byte, 8000)
	return makeSection("PVB2", extra, body)
}

// GeneratePVB2 returns a placeholder PVB2 (extended VBR seek index) section
// wrapped with the 4-byte little-endian length prefix used by the dbserver
// ANLZ blob format (same layout ReadANLZSection returns). The dbserver
// serves an 8036-byte blob (LE len + 8032-byte PVB2 section) in reply to a
// 0x2c04 PVB2 request; withholding it makes the deck fall back to a raw
// 0x2805 tagged-section read, which — if unanswered — deadlocks the deck's
// dbserver channel (see the PVB2 case in dbserver/track.go). The seek index
// body is zeroed here (we don't yet compute real VBR offsets), which is
// enough for linear playback; ANLZ-backed tracks serve the real section.
func GeneratePVB2() []byte {
	section := makeEmptyVB2Section()
	blob := make([]byte, 4+len(section))
	binary.LittleEndian.PutUint32(blob, uint32(len(section)))
	copy(blob[4:], section)
	return blob
}

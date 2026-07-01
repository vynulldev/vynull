// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
)

// GeneratePQT2 creates a PQT2 beat grid blob (complete ANLZ section).
//
// PQT2 format (from DjManager docs):
//   56-byte header:
//     +00: "PQT2" fourcc
//     +04: 56 (header length)
//     +08: len_tag = 56 + entry_count * 2
//     +0C: 0x00000000
//     +10: 0x01000002 (constant)
//     +14: 0x00000000
//     +18: first beat: beat_number(2) + tempo(2) + time_ms(4)
//     +20: last beat:  beat_number(2) + tempo(2) + time_ms(4)
//     +28: entry_count (must be > 0 for rekordbox 6)
//     +2C: 0x00000000
//     +30: 0x00000000 (reserved)
//     +34: 0x00000000 (reserved)
//   Body: entry_count × u16BE, each = beat_time_ms % 1000
func GeneratePQT2(bpm float64, beats []float64, downbeatIdx int) []byte {
	if bpm <= 0 || len(beats) < 2 {
		return nil
	}

	numBeats := len(beats)
	tempo := uint16(bpm * 100) // BPM × 100

	// First and last beat info
	firstBeatInBar := ((0 - downbeatIdx) % 4)
	if firstBeatInBar < 0 {
		firstBeatInBar += 4
	}
	firstBeatNum := uint16(firstBeatInBar + 1) // 1-based (1-4)
	firstBeatMs := uint32(beats[0])

	lastIdx := numBeats - 1
	lastBeatInBar := ((lastIdx - downbeatIdx) % 4)
	if lastBeatInBar < 0 {
		lastBeatInBar += 4
	}
	lastBeatNum := uint16(lastBeatInBar + 1) // 1-based
	lastBeatMs := uint32(beats[lastIdx])

	// Build complete ANLZ section
	headerSize := 56
	dataSize := numBeats * 2
	tagLen := headerSize + dataSize

	buf := make([]byte, 4+tagLen) // 4-byte LE prefix + tag

	// LE prefix: rekordbox uses tag_len + 2
	binary.LittleEndian.PutUint32(buf[0:], uint32(tagLen+2))

	off := 4
	// Fourcc
	copy(buf[off:off+4], "PQT2")
	// Header length
	binary.BigEndian.PutUint32(buf[off+4:], uint32(headerSize))
	// Tag length
	binary.BigEndian.PutUint32(buf[off+8:], uint32(tagLen))
	// +0C: zeros
	binary.BigEndian.PutUint32(buf[off+12:], 0)
	// +10: constant 0x01000002
	binary.BigEndian.PutUint32(buf[off+16:], 0x01000002)
	// +14: zeros
	binary.BigEndian.PutUint32(buf[off+20:], 0)
	// +18: first beat
	binary.BigEndian.PutUint16(buf[off+24:], firstBeatNum)
	binary.BigEndian.PutUint16(buf[off+26:], tempo)
	binary.BigEndian.PutUint32(buf[off+28:], firstBeatMs)
	// +20: last beat
	binary.BigEndian.PutUint16(buf[off+32:], lastBeatNum)
	binary.BigEndian.PutUint16(buf[off+34:], tempo)
	binary.BigEndian.PutUint32(buf[off+36:], lastBeatMs)
	// +28: entry count
	binary.BigEndian.PutUint32(buf[off+40:], uint32(numBeats))
	// +2C, +30, +34: zeros
	binary.BigEndian.PutUint32(buf[off+44:], 0)
	binary.BigEndian.PutUint32(buf[off+48:], 0)
	binary.BigEndian.PutUint32(buf[off+52:], 0)

	// Body: beat_time_ms % 1000 as u16BE per beat
	dataOff := off + headerSize
	for i, beatMs := range beats {
		frac := uint16(uint32(beatMs) % 1000)
		binary.BigEndian.PutUint16(buf[dataOff+i*2:], frac)
	}

	return buf
}

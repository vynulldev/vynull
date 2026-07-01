// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "encoding/binary"

// GenerateBeatGrid creates a beat grid blob for the 0x2204 (0x4602) response.
// Format: 20-byte preamble + 16-byte LE entries.
// downbeatMs is the time offset of the first beat (from DetectDownbeat).
func GenerateBeatGrid(bpm, durationMs, downbeatMs float64) []byte {
	if bpm <= 0 || durationMs <= 0 {
		return nil
	}

	msPerBeat := 60000.0 / bpm
	numBeats := int((durationMs - downbeatMs) / msPerBeat)
	if numBeats < 1 {
		return nil
	}
	// Sanity clamp: real tracks rarely exceed a few thousand beats
	// (200 BPM × 60 min ≈ 12,000). A higher value here means callers
	// passed nonsense args (wrong unit, missing downbeat, etc.) and
	// we'd otherwise try to malloc tens-to-hundreds of GB.
	const maxBeats = 100_000
	if numBeats > maxBeats {
		return nil
	}

	tempo := uint16(bpm * 100)
	entrySize := 16
	dataSize := numBeats * entrySize
	buf := make([]byte, 20+dataSize)

	// 20-byte preamble matching real CDJ format (LE u32s).
	binary.LittleEndian.PutUint32(buf[0:], 0x00080000)
	binary.LittleEndian.PutUint32(buf[4:], uint32(numBeats))
	binary.LittleEndian.PutUint32(buf[8:], uint32(dataSize))
	binary.LittleEndian.PutUint32(buf[12:], 1)
	binary.LittleEndian.PutUint32(buf[16:], 1)

	for i := 0; i < numBeats; i++ {
		off := 20 + i*entrySize
		beatNum := uint16((i % 4) + 1)
		timeMs := uint32(downbeatMs + float64(i)*msPerBeat)

		binary.LittleEndian.PutUint16(buf[off+0:], beatNum)
		binary.LittleEndian.PutUint16(buf[off+2:], tempo)
		binary.LittleEndian.PutUint32(buf[off+4:], timeMs)
		for j := 8; j < 16; j++ {
			buf[off+j] = 0xFF
		}
	}

	return buf
}

// GenerateBeatGridFromBeats creates a beat grid from detected beat positions.
// Uses actual beat times instead of computing from BPM + offset.
func GenerateBeatGridFromBeats(result *BeatResult) []byte {
	if result == nil || result.BPM <= 0 || len(result.Beats) < 2 {
		return nil
	}

	// Find which beat index is the downbeat to align 1-2-3-4 correctly.
	downbeatIdx := 0
	for i, b := range result.Beats {
		if b >= result.Downbeat-0.5 {
			downbeatIdx = i
			break
		}
	}

	numBeats := len(result.Beats)
	tempo := uint16(result.BPM * 100)
	entrySize := 16
	dataSize := numBeats * entrySize
	buf := make([]byte, 20+dataSize)

	binary.LittleEndian.PutUint32(buf[0:], 0x00080000)
	binary.LittleEndian.PutUint32(buf[4:], uint32(numBeats))
	binary.LittleEndian.PutUint32(buf[8:], uint32(dataSize))
	binary.LittleEndian.PutUint32(buf[12:], 1)
	binary.LittleEndian.PutUint32(buf[16:], 1)

	for i := 0; i < numBeats; i++ {
		off := 20 + i*entrySize
		// Beat number relative to downbeat: 1-2-3-4 cycle.
		beatInBar := ((i - downbeatIdx) % 4)
		if beatInBar < 0 {
			beatInBar += 4
		}
		beatNum := uint16(beatInBar + 1)
		timeMs := uint32(result.Beats[i])

		binary.LittleEndian.PutUint16(buf[off+0:], beatNum)
		binary.LittleEndian.PutUint16(buf[off+2:], tempo)
		binary.LittleEndian.PutUint32(buf[off+4:], timeMs)
		for j := 8; j < 16; j++ {
			buf[off+j] = 0xFF
		}
	}

	return buf
}

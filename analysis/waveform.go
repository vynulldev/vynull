// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
)

const (
	PreviewPoints      = 900  // mono preview for dbserver network response
	AnlzPreviewPoints  = 400  // mono preview for ANLZ .DAT file (uses 400)
	ColorPreviewPoints = 1200 // color preview (PWV4) — uses 1200
	MaxHeight          = 31   // max height for PWV4/PWV5 color waveforms
	PreviewMaxHeight   = 16   // max height for PWAV mono preview (1-16)
	FftSize            = 2048 // larger window for better bass frequency resolution
)

// Band-split crossover frequencies (Hz) for the colour-detail waveform's
// bass/mid/treble → R/G/B mapping. Exposed as vars so the colour balance can be
// calibrated; the defaults are the calibrated values.
var (
	BandBassMidHz   = 200.0
	BandMidTrebleHz = 750.0
	// PreviewTrebleHz is the PWV4 overview treble HP cutoff (separate from the
	// detail crossover above — PWV4 has its own band structure).
	PreviewTrebleHz = 2000.0
	// PWV4 per-band byte scales (d3/d4/d5 = bass/mid/treble). Calibrated so the
	// overview colour balance holds on actual music
	// (tools/wavecompare): mean d-values land near
	// bass~64 mid~41 treble~31, balance ~47/30/23. These are much higher than
	// a single-tone sweep would imply — real-music band levels run
	// higher than a linear extrapolation from isolated tones, so the broadband
	// balance is the calibration target, not the sweep.
	PreviewBassScale   = 240.0
	PreviewMidScale    = 420.0
	PreviewTrebleScale = 480.0
	// PWV7 (3-band detail) absolute per-band RMS scales — calibrated for
	// PWV7 (tools/wavecompare -pwv7): bass-heavy, treble-light,
	// all channels within ~0.7 of the target means/balance over 120 tracks.
	Detail3BassScale   = 176.0
	Detail3MidScale    = 283.0
	Detail3TrebleScale = 115.0
	// PWV6 (3-band overview) absolute per-band RMS scales — calibrated for
	// PWV6, which is balanced (treble-favouring scales) and low.
	Preview3BassScale   = 83.0
	Preview3MidScale    = 166.0
	Preview3TrebleScale = 198.0
)

// DetailEntriesPerSec is the entry rate for PWV3/PWV5 scrolling waveforms.
// Exports use ~150 entries/sec (e.g. a 484s track has 72,741 PWV5
// entries → 150.3/s; a 165s track has 24,796 entries → 150.3/s).
// The 0x0096 in the format-flags header is literally this rate.
const DetailEntriesPerSec = 150

// The waveform encoders (GeneratePreview, GenerateColorPreview, GenerateDetail,
// the 3-band encoders, and their helpers) moved to link/prolink
// (link/prolink/waveform.go) in the encoder relocation. The calibration
// consts/vars above and DetailEntriesPerSec stay here because bandwaveform.go
// (the neutral band extraction) references them, and WrapANLZ below stays
// because the dbserver serving path and anlz.go's .2EX writer still call it.

// WrapANLZ wraps raw data in an ANLZ tag structure for dbserver responses.
// fourcc is e.g. "PWV4", "PWV5", "PVB2", "PSSI". entrySize is bytes per entry.
// For PVB2, pass the file size as entrySize (used in the extended header).
func WrapANLZ(fourcc string, entrySize int, data []byte) []byte {
	numEntries := len(data) / entrySize

	// Header length is tag-specific (for .EXT/.2EX):
	// PVB2/PVBR=32, PWV6=20 (entry_size+num), PWVC=14 (u2 pad), rest=24.
	var lenHeader uint32
	switch fourcc {
	case "PVB2", "PVBR":
		lenHeader = 32
	case "PWV6":
		lenHeader = 20
	case "PWVC":
		lenHeader = 14
	default:
		lenHeader = 24
	}

	lenTag := lenHeader + uint32(len(data))
	sectionLen := lenTag

	buf := make([]byte, 4+int(lenTag))

	// 4-byte LE prefix: section length
	binary.LittleEndian.PutUint32(buf[0:], sectionLen)

	// ANLZ section header
	copy(buf[4:8], fourcc)
	binary.BigEndian.PutUint32(buf[8:], lenHeader)
	binary.BigEndian.PutUint32(buf[12:], lenTag)

	// Extended header — tag-specific.
	switch fourcc {
	case "PWV5":
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00960305)
	case "PWV4":
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00000000)
	case "PWV7":
		// 3-band detail: entry_size(3), num_entries, u2 rate(150), u2 pad.
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
		binary.BigEndian.PutUint32(buf[24:], 0x00960000)
	case "PWV6":
		// 3-band preview: entry_size(3), num_entries. 20-byte header (no rate).
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
	case "PWVC":
		// 3-band colour metadata: 14-byte header (u2 pad), 6-byte body.
		binary.BigEndian.PutUint16(buf[16:], 0)
	case "PVB2", "PVBR":
		// PVB2: 32-byte header.
		// ext bytes 12-15: 0 (u1)
		// ext bytes 16-19: 0 (u2)
		// ext bytes 20-23: file size (u3)
		// ext bytes 24-27: 0x190 = 400 (u4)
		// ext bytes 28-31: 0x14 = 20 (u5)
		binary.BigEndian.PutUint32(buf[16:], 0)
		binary.BigEndian.PutUint32(buf[20:], 0)
		binary.BigEndian.PutUint32(buf[24:], uint32(entrySize)) // file size passed as entrySize
		binary.BigEndian.PutUint32(buf[28:], 0x00000190)        // 400
		binary.BigEndian.PutUint32(buf[32:], 0x00000014)        // 20
	default:
		binary.BigEndian.PutUint32(buf[16:], uint32(entrySize))
		binary.BigEndian.PutUint32(buf[20:], uint32(numEntries))
	}

	// Data (starts after header)
	copy(buf[4+int(lenHeader):], data)

	return buf
}

// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"encoding/binary"

	"github.com/vynulldev/vynull/analysis"
)

// GeneratePSSI creates the PSSI (song structure) blob — returns the
// "extra header + body" content for an ANLZ PSSI section. The caller
// wraps it with the 12-byte generic section header (PSSI + len_header
// + len_tag).
//
// The layout is:
//
//	extra header (20 bytes total, included in len_header=32):
//	  u4 entry_size = 24
//	  u2 num_entries
//	  u2 mood (1=high, 2=mid, 3=low; masked file shows mood+20-ish)
//	  6 bytes padding
//	  u2 end_beat
//	  2 bytes padding
//	  u1 raw_bank
//	  1 byte padding
//	body (num_entries × 24 bytes): phrase entries
//
// When num_entries > 0, the body is XOR-masked (the
// 14 fixed body bytes + the entries). The mask is 19 bytes:
//
//	mask[j] = base[j] + num_entries  (mod 256)
//
// We follow that mask convention. For unmasked output, return as-is.
func GeneratePSSI(phrases []analysis.Phrase, bpm float64) []byte {
	if len(phrases) == 0 {
		return nil
	}

	numEntries := len(phrases)
	entrySize := 24
	mood := uint16(1) // high
	bank := uint8(0)
	endBeat := uint16(phrases[len(phrases)-1].EndBeat)

	// Combined region that gets XOR-masked: from the mood u2 onward.
	// 14 fixed bytes (mood..bank+padding) + entries.
	maskedRegion := make([]byte, 14+numEntries*entrySize)
	binary.BigEndian.PutUint16(maskedRegion[0:], mood)
	// bytes 2-7: 6 padding bytes (zero)
	binary.BigEndian.PutUint16(maskedRegion[8:], endBeat)
	// bytes 10-11: 2 padding bytes (zero)
	maskedRegion[12] = bank
	// byte 13: 1 padding byte (zero)
	for i, p := range phrases {
		off := 14 + i*entrySize
		binary.BigEndian.PutUint16(maskedRegion[off+0:], uint16(i+1))
		binary.BigEndian.PutUint16(maskedRegion[off+2:], uint16(p.StartBeat))
		binary.BigEndian.PutUint16(maskedRegion[off+4:], p.Kind)
		// rest of the 24-byte entry stays zero
	}

	// XOR mask. The mask byte sequence encodes the fixed obfuscation
	// constants applied to PSSI.
	c := byte(numEntries)
	mask := make([]byte, len(analysis.PSSIMaskBase))
	for j := range analysis.PSSIMaskBase {
		mask[j] = analysis.PSSIMaskBase[j] + c
	}
	for i := range maskedRegion {
		maskedRegion[i] ^= mask[i%len(mask)]
	}

	// Return as: entry_size(4) + num_entries(2) + maskedRegion.
	// Total = 6 + 14 + numEntries*24 = 20 + numEntries*24.
	buf := make([]byte, 6+len(maskedRegion))
	binary.BigEndian.PutUint32(buf[0:], uint32(entrySize))
	binary.BigEndian.PutUint16(buf[4:], uint16(numEntries))
	copy(buf[6:], maskedRegion)
	return buf
}

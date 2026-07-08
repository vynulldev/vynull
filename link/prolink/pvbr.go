// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import "encoding/binary"

// GeneratePVBR creates a PVBR/PVB2 VBR seek index.
// rekordbox uses 2000 entries, each a big-endian uint32 byte offset.
// For CBR files, uses linear mapping.
func GeneratePVBR(fileSize int64) []byte {
	const numEntries = 2000
	buf := make([]byte, numEntries*4)

	for i := 0; i < numEntries; i++ {
		offset := uint32(int64(i) * fileSize / int64(numEntries-1))
		if int64(offset) > fileSize {
			offset = uint32(fileSize)
		}
		binary.BigEndian.PutUint32(buf[i*4:], offset)
	}

	return buf
}

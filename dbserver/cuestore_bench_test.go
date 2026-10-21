// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"io"
	"log"
	"testing"
)

// BenchmarkCueSave measures what a deck's cue save (0x2705) waits for on its
// dbserver session: storing a hot cue on a track that already has six, each
// with the deck's blob.
func BenchmarkCueSave(b *testing.B) {
	defer log.SetOutput(log.Writer()) // keep the store's log out of benchstat's lines
	log.SetOutput(io.Discard)
	cs := NewCueStore(b.TempDir())
	for n := uint16(1); n <= 6; n++ {
		cs.SaveCue(7, &CuePoint{Number: n, Type: 1, TimeMs: uint32(n) * 1000, LoopMs: -1, Status: 1},
			makeNXS2CueBlob(n, 0x2a, 0, 0, 0))
	}
	blob := makeNXS2CueBlob(3, 0x2a, 0, 0, 0)
	b.ResetTimer()
	for i := range b.N {
		binary.LittleEndian.PutUint32(blob[0x0c:], uint32(i))
		cs.SaveCue(7, &CuePoint{Number: 3, Type: 1, TimeMs: uint32(i), LoopMs: -1, Status: 1}, blob)
	}
}

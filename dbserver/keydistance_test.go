// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// The KEY menu's three distance groups for a key index, in Classic names:
// the key, then its relative key, then its neighbours round the wheel.
func TestKeyDistanceGroups(t *testing.T) {
	for index, want := range map[uint32][]string{
		15: {"Am", "Am, C", "Am, C, Dm, Em"},      // 8A
		23: {"Dbm", "Dbm, E", "Dbm, E, F#m, Abm"}, // 12A, round past 1A
		2:  {"B", "B, Abm", "B, Abm, E, F#"},      // 1B, round past 12B
		0:  {"", "", ""},                          // no key
		25: {"", "", ""},                          // past the last key
	} {
		h := &Handler{}
		h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1114,
			Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(0), proto.ArgI32(index)}})
		var got []string
		for i, m := range h.pendingItems {
			got = append(got, m.Label1)
			if m.ID != index || m.ParentID != uint32(i) {
				t.Errorf("index %d group %d: ID %d, parent %d", index, i, m.ID, m.ParentID)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("index %d: groups %q, want %q", index, got, want)
		}
	}
}

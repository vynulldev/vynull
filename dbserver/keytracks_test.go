// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// The KEY track list holds the tracks in the selected key's distance group,
// title-sorted, or the whole collection when no track's key is in it.
func TestKeyTracksByDistance(t *testing.T) {
	lib := library.NewLibrary([]*library.Track{
		{ID: 1, Title: "Exact", Key: "1B"},
		{ID: 2, Title: "Relative", Key: "Abm"}, // 1A
		{ID: 3, Title: "Below", Key: "12B"},
		{ID: 4, Title: "Above", Key: "F#"}, // 2B
		{ID: 5, Title: "Far", Key: "8A"},
		{ID: 6, Title: "Keyless"},
	}, nil)
	h := &Handler{lib: lib}
	titles := func(keyIndex, distance uint32) []string {
		h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1214, Args: []proto.DBArg{
			proto.ArgI32(0x01010401), proto.ArgI32(0), proto.ArgI32(keyIndex), proto.ArgI32(distance)}})
		var out []string
		for _, m := range h.pendingItems {
			out = append(out, m.Label1)
		}
		return out
	}
	for _, tc := range []struct {
		keyIndex, distance uint32
		want               []string
	}{
		{2, 0, []string{"Exact"}},                                                 // 1B
		{2, 1, []string{"Exact", "Relative"}},                                     // + 1A
		{2, 2, []string{"Above", "Below", "Exact", "Relative"}},                   // + 12B, 2B
		{2, 3, []string{"Above", "Below", "Exact", "Relative"}},                   // the widest group
		{9, 0, []string{"Above", "Below", "Exact", "Far", "Keyless", "Relative"}}, // 5A: no track, so all
		{0, 0, []string{"Above", "Below", "Exact", "Far", "Keyless", "Relative"}}, // no key
	} {
		if got := titles(tc.keyIndex, tc.distance); !slices.Equal(got, tc.want) {
			t.Errorf("key %d distance %d: %q, want %q", tc.keyIndex, tc.distance, got, tc.want)
		}
	}
}

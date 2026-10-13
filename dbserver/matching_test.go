// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// TestMatchingRecommendPairs pins MATCHING (0x1017) for a track with
// rekordbox recommendations to rekordbox's behaviour, from the link-spec
// conformance case matching-forward-reverse recorded against rekordbox 7.2.19:
// the list holds exactly the track's djmdRecommendLike pairs, in both
// directions, and nothing else. The fixture mirrors that case: every track is
// a half BPM from its neighbour, alternating Am and C (relative keys), so a
// key/tempo ranking would match them all, yet rekordbox answered with the two
// recommended tracks only.
func TestMatchingRecommendPairs(t *testing.T) {
	lib := library.New()
	var ids []uint32
	for i := 0; i < 6; i++ {
		key := "C"
		if i%2 == 1 {
			key = "Am"
		}
		ids = append(ids, lib.AddTrack(&library.Track{
			Title: fmt.Sprintf("Track %02d", i+1), BPM: 119.5 + float64(i)*0.5, Key: key, Genre: "House",
		}))
	}
	lib.AddRelated(ids[0], ids[1]) // forward: first recommends second
	lib.AddRelated(ids[2], ids[0]) // reverse: third recommends first

	h := &Handler{lib: lib}
	h.handleGetMatching(&proto.DBMessage{Type: 0x1017, Args: []proto.DBArg{
		proto.ArgI32(0x01010301), proto.ArgI32(0), proto.ArgI32(ids[0])}})
	var got []uint32
	for _, it := range h.pendingItems {
		got = append(got, it.ID)
	}
	if len(got) != 2 || got[0] != ids[1] || got[1] != ids[2] {
		t.Fatalf("MATCHING for track %d = %v, want exactly its recommendations [%d %d]", ids[0], got, ids[1], ids[2])
	}

	// A track with no recommendations, where rekordbox shows an empty list,
	// gets Vynull's computed suggestions instead: here every other track, all
	// relative keys within a few BPM, and never the track itself.
	h.handleGetMatching(&proto.DBMessage{Type: 0x1017, Args: []proto.DBArg{
		proto.ArgI32(0x01010301), proto.ArgI32(0), proto.ArgI32(ids[4])}})
	if len(h.pendingItems) != 5 {
		t.Errorf("MATCHING for an unpaired track returned %d rows, want 5 computed suggestions", len(h.pendingItems))
	}
	for _, it := range h.pendingItems {
		if it.ID == ids[4] {
			t.Error("MATCHING suggested the track itself")
		}
	}
}

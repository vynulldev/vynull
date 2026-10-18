// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// historyPlaylists is a PlaylistSource holding only a History folder (ID 1)
// with two daily sessions.
type historyPlaylists struct{}

func (historyPlaylists) Children(id uint32) []PlaylistEntry {
	if id == 1 {
		return []PlaylistEntry{{ID: 2, Name: "History · 2026-10-17"}, {ID: 3, Name: "History · 2026-10-18"}}
	}
	return nil
}

func (historyPlaylists) Tracks(id uint32) []uint32 {
	return map[uint32][]uint32{2: {10, 11}, 3: {12}}[id]
}

func (historyPlaylists) HistoryFolderID() uint32 { return 1 }

// The play-state query answers 2 (played) for a track in any History
// session and 0 otherwise, in rekordbox's 0x4000 [0x3b03, state] reply.
func TestPlayState(t *testing.T) {
	h := &Handler{playlists: historyPlaylists{}}
	for id, want := range map[uint32]uint32{10: 2, 12: 2, 99: 0} {
		r := h.Handle(&proto.DBMessage{TxID: 7, Type: 0x3b03, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(id)}})
		if len(r) != 1 || r[0].Type != proto.DBMsgSuccess || r[0].TxID != 7 || len(r[0].Args) != 2 ||
			r[0].Args[0].Int() != 0x3b03 || r[0].Args[1].Int() != want {
			t.Errorf("track %d: reply %+v, want 4000 [0x3b03, %d]", id, r, want)
		}
	}
	if r := (&Handler{}).Handle(&proto.DBMessage{TxID: 8, Type: 0x3b03, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(10)}}); len(r) != 1 || r[0].Args[1].Int() != 0 {
		t.Errorf("no playlist source: %+v, want state 0", r)
	}
}

// fakeMarks is an in-memory PlayMarkSource.
type fakeMarks map[uint32]bool

func (m fakeMarks) IsUnplayed(id uint32) bool { return m[id] }
func (m fakeMarks) MarkUnplayed(id uint32)    { m[id] = true }

// A deck's 0x3401 marks a played track unplayed: acknowledged with
// 0x4000 [0x3401, 0], and the next play-state query answers 0 while the
// History playlists stay as they are.
func TestMarkUnplayed(t *testing.T) {
	marks := fakeMarks{}
	h := &Handler{playlists: historyPlaylists{}, playMarks: marks}
	state := func(id uint32) uint32 {
		r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x3b03, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(id)}})
		return r[0].Args[1].Int()
	}
	if state(10) != 2 {
		t.Fatalf("track 10 should start played")
	}
	r := h.Handle(&proto.DBMessage{TxID: 2, Type: 0x3401, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(10), proto.ArgI32(0)}})
	if len(r) != 1 || r[0].Type != proto.DBMsgSuccess || r[0].Args[0].Int() != 0x3401 || r[0].Args[1].Int() != 0 {
		t.Errorf("0x3401 reply %+v, want 4000 [0x3401, 0]", r)
	}
	if state(10) != 0 || state(11) != 2 {
		t.Errorf("after marking 10 unplayed: states %d, %d; want 0, 2", state(10), state(11))
	}
	if len(historyPlaylists{}.Tracks(2)) != 2 {
		t.Errorf("History changed")
	}
}

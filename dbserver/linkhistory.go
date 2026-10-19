// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"sync"
	"time"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// linkhistory.go serves rekordbox's Link history: the history the decks keep
// of the current session (link-spec PROTOCOL_REFERENCE.md "History" and the
// history-lifecycle suite, rekordbox 7.2.19).
//
//   - 0x3001 (CMD_INSERT_HISTORY) [ctx, track ID] adds a track. The first one
//     creates the current history, named "LINK HISTORY YYYY-MM-DD" after the
//     day it starts; later ones append. A CDJ-2000NXS2 sends it once a track
//     has been playing for 60 seconds, and from a track's menu to mark it
//     played. No reply.
//   - 0x1012 [ctx, sort] is the HISTORY menu: the current history as one row
//     of type 0x24, or no rows before the first insert.
//   - 0x1112 [ctx, sort, history ID] lists its tracks in the order they were
//     added, numbered from 1 in argument 9.
//   - 0x3401 (CMD_DEL_HISTORY_TRACK) [ctx, track ID] removes a track (from a
//     track's menu: mark unplayed) and closes the gap. Answered 0x4000
//     [0x3401, 0].
//   - 0x3101 (CMD_DEL_HISTORY) [ctx, history ID] deletes the history. No reply.
//
// A track in the Link history is played on the link: the play-state query
// 0x3b03 (CMD_GET_PLAYSTATE) [ctx, track ID] answers 0x4000 [0x3b03, 2] for
// it and 0x4000 [0x3b03, 0] otherwise (a deck keeps the low byte and reads
// bit 0x02), and its track rows carry linkPlayedFlag in argument 7.
//
// rekordbox's HISTORY menu starts empty, even with older histories saved, so
// the Link history lasts as long as the process. Vynull's History playlists,
// which record what the play monitor saw played, are separate from it.

const (
	playStatePlayed = 2
	// linkPlayedFlag is the argument-7 bit of a track row whose track is in
	// the Link history (rekordbox sends 0x101 for a MyTag-carrying track
	// there, 0x1 elsewhere).
	linkPlayedFlag = 0x100
	// historyItemType is the HISTORY menu's row type for a history.
	historyItemType = 0x24
)

// linkHistory is the current Link history, shared by the server's sessions.
type linkHistory struct {
	mu     sync.Mutex
	id     uint32 // 0 while there is no history
	lastID uint32
	name   string
	tracks []uint32 // in the order added; a track can appear more than once
}

// insert appends trackID, creating the history first if there is none.
func (lh *linkHistory) insert(trackID uint32, now time.Time) {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if lh.id == 0 {
		// rekordbox generates the ID per process. Taking it from the clock
		// keeps a deck from selecting a new history with an ID it held from
		// before a restart.
		lh.id = max(lh.lastID+1, uint32(now.Unix()))
		lh.lastID = lh.id
		lh.name = "LINK HISTORY " + now.Format("2006-01-02")
	}
	lh.tracks = append(lh.tracks, trackID)
}

// remove drops every occurrence of trackID, so the track is no longer played.
func (lh *linkHistory) remove(trackID uint32) {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	lh.tracks = slices.DeleteFunc(lh.tracks, func(id uint32) bool { return id == trackID })
}

// delete deletes the history if id names it.
func (lh *linkHistory) delete(id uint32) {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if id != 0 && id == lh.id {
		lh.id, lh.name, lh.tracks = 0, "", nil
	}
}

// current returns the history's ID and name (ID 0 when there is none).
func (lh *linkHistory) current() (uint32, string) {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	return lh.id, lh.name
}

// tracksOf returns the tracks of history id, or nil if id doesn't name it.
func (lh *linkHistory) tracksOf(id uint32) []uint32 {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if id == 0 || id != lh.id {
		return nil
	}
	return slices.Clone(lh.tracks)
}

// played reports whether trackID is in the history.
func (lh *linkHistory) played(trackID uint32) bool {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	return trackID != 0 && slices.Contains(lh.tracks, trackID)
}

// linkHistory returns the session's Link history, creating an unshared one
// for a handler built without a server.
func (h *Handler) linkHistory() *linkHistory {
	if h.history == nil {
		h.history = &linkHistory{}
	}
	return h.history
}

// now is the clock that names a new history (the server's Clock, if set).
func (h *Handler) now() time.Time {
	if h.clock != nil {
		return h.clock()
	}
	return time.Now()
}

// handleGetLinkHistory serves the HISTORY menu, 0x1012 [ctx, sort].
func (h *Handler) handleGetLinkHistory(msg *proto.DBMessage) []*proto.DBMessage {
	h.pendingItems = nil
	if id, name := h.linkHistory().current(); id != 0 {
		h.pendingItems = []*menuItem{{ID: id, Label1: name, ItemType: historyItemType}}
	}
	dlog.Debugf("dbserver: HISTORY (0x1012) returning %d histories", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetLinkHistoryTracks serves a history's tracks, 0x1112 [ctx, sort,
// history ID].
func (h *Handler) handleGetLinkHistoryTracks(msg *proto.DBMessage) []*proto.DBMessage {
	var id uint32
	if len(msg.Args) > 2 {
		id = msg.Args[2].Int()
	}
	items := h.trackIDsToMenuItems(h.linkHistory().tracksOf(id))
	for i, m := range items {
		m.trackPos = uint32(i + 1)
	}
	h.sortTracks(items, getSortOrder(msg))
	h.pendingItems = items
	dlog.Debugf("dbserver: HISTORY %d (0x1112) returning %d tracks", id, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleInsertHistory handles 0x3001 [ctx, track ID]. No reply.
func (h *Handler) handleInsertHistory(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	if trackID != 0 {
		h.linkHistory().insert(trackID, h.now())
	}
	dlog.Debugf("dbserver: Link history insert (0x3001) track=%d", trackID)
	return nil
}

// handleDeleteHistoryTrack handles 0x3401 [ctx, track ID], answered 0x4000
// [0x3401, 0].
func (h *Handler) handleDeleteHistoryTrack(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	h.linkHistory().remove(trackID)
	dlog.Debugf("dbserver: Link history remove (0x3401) track=%d", trackID)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
	}}
}

// handleDeleteHistory handles 0x3101 [ctx, history ID]. No reply.
func (h *Handler) handleDeleteHistory(msg *proto.DBMessage) []*proto.DBMessage {
	var id uint32
	if len(msg.Args) > 1 {
		id = msg.Args[1].Int()
	}
	h.linkHistory().delete(id)
	dlog.Debugf("dbserver: Link history delete (0x3101) history=%d", id)
	return nil
}

// handleGetPlayState handles 0x3b03 [ctx, track ID].
func (h *Handler) handleGetPlayState(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	var state uint32
	if h.linkHistory().played(trackID) {
		state = playStatePlayed
	}
	dlog.Debugf("dbserver: play state (0x3b03) track=%d state=%d", trackID, state)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(state)},
	}}
}

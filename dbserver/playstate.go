// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// playstate.go answers the deck's play-state query, 0x3b03
// (CMD_GET_PLAYSTATE) [ctx, track ID]. rekordbox replies 0x4000 [0x3b03,
// state], with state 2 when the track is in its Link-played record (tracks
// played from it over Pro DJ Link) and 0 otherwise; a deck keeps the low
// byte and uses bit 0x02 to mark the track Played (link-spec
// CONFIGURATION.md and XDJ_RR_CLIENT_NAVIGATION.md, from rekordbox's and the
// XDJ-RR firmware's code). Vynull's record of tracks played on the link is
// its History: a track counts as played when any History session lists it,
// unless a deck has since marked it otherwise.
//
// From a track's menu, a deck marks it played with 0x3001
// (CMD_INSERT_HISTORY) [ctx, track ID], which rekordbox doesn't answer, and
// unplayed with 0x3401 (CMD_DEL_HISTORY_TRACK) [ctx, track ID, 0], answered
// 0x4000 [0x3401, 0] (link-spec link-played-state suite). rekordbox inserts
// into or deletes from its Link history; Vynull records the mark
// (PlayMarkSource) and keeps the History playlists as they are, until the
// track is next played. Decks also send 0x3001 when they play a track
// themselves; Vynull's own play monitor adds that play to History.

const playStatePlayed = 2

// PlayMarkSource records tracks a deck has marked played or unplayed.
type PlayMarkSource interface {
	PlayMark(trackID uint32) (played, marked bool)
	MarkPlayed(trackID uint32, played bool)
}

// handleMarkPlayed handles 0x3001 [ctx, track ID]: mark the track played.
// rekordbox sends no reply.
func (h *Handler) handleMarkPlayed(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	if h.playMarks != nil && trackID != 0 {
		h.playMarks.MarkPlayed(trackID, true)
	}
	dlog.Debugf("dbserver: mark played (0x3001) track=%d", trackID)
	return nil
}

// handleMarkUnplayed handles 0x3401 [ctx, track ID, 0]: mark the track
// unplayed. Acknowledged with 0x4000 [0x3401, 0].
func (h *Handler) handleMarkUnplayed(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	if h.playMarks != nil && trackID != 0 {
		h.playMarks.MarkPlayed(trackID, false)
	}
	dlog.Debugf("dbserver: mark unplayed (0x3401) track=%d", trackID)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
	}}
}

func (h *Handler) handleGetPlayState(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) > 1 {
		trackID = msg.Args[1].Int()
	}
	var state uint32
	if h.playedOnLink(trackID) {
		state = playStatePlayed
	}
	dlog.Debugf("dbserver: play state (0x3b03) track=%d state=%d", trackID, state)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(state)},
	}}
}

// playedOnLink reports whether a deck has marked the track played or, with
// no mark, whether any History session lists it.
func (h *Handler) playedOnLink(trackID uint32) bool {
	if trackID == 0 {
		return false
	}
	if h.playMarks != nil {
		if played, marked := h.playMarks.PlayMark(trackID); marked {
			return played
		}
	}
	if h.playlists == nil {
		return false
	}
	folder := h.playlists.HistoryFolderID()
	if folder == 0 {
		return false
	}
	for _, session := range h.playlists.Children(folder) {
		for _, id := range h.playlists.Tracks(session.ID) {
			if id == trackID {
				return true
			}
		}
	}
	return false
}

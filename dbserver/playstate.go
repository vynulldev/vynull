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
// its History: a track counts as played when any History session lists it.

const playStatePlayed = 2

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

// playedOnLink reports whether any History session lists the track.
func (h *Handler) playedOnLink(trackID uint32) bool {
	if h.playlists == nil || trackID == 0 {
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

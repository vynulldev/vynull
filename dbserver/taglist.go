// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// The decks' TAG LIST is rekordbox's Prepare list (djmdSongTagList): the
// source keeps it, and a linked deck reads and edits it. From real
// rekordbox captures (CDJ-2000NXS2) and link-spec's dispatch reference:
//
//	0x100f [ctx, sort]                  menu location 6: the list, rendered by 0x3000
//	0x3002 [ctx, track ID, 1 or 0]      location 8: add the track at the end, or remove it
//	0x3202 [ctx]                        location 6: remove every track
//	0x3102 [ctx, name length, name, 0]  location 6: a new playlist holding the list
//
// rekordbox answers each change with 0x4000 [kind, 0]. It tells the link
// about none of them except the new playlist; a change made in rekordbox
// itself is announced with a type 0x1b broadcast, after which the decks read
// the list again. Browse rows mark the listed tracks (argument 7 bit 0).

// tagListLocation is the menu location of the TAG LIST's requests; 0x100f
// elsewhere is the FOLDER root listing.
const tagListLocation = 6

// handleGetTagList answers 0x100f at the TAG LIST's location: its tracks as
// playlist track rows, in list order unless a sort is asked for, each with
// its list position in argument 9.
func (h *Handler) handleGetTagList(msg *proto.DBMessage) []*proto.DBMessage {
	var ids []uint32
	if h.lib != nil {
		ids = h.lib.TagList()
	}
	items := h.trackIDsToMenuItems(ids)
	h.applyPlaylistTrackScope(items, getSortOrder(msg), false)
	dlog.Debugf("dbserver: tag list returning %d tracks", len(items))
	return h.countReply(msg, items)
}

// handleSetTagged answers 0x3002: the deck adds a track to the TAG LIST or
// removes it.
func (h *Handler) handleSetTagged(msg *proto.DBMessage) []*proto.DBMessage {
	id, on := msgArg(msg, 1), msgArg(msg, 2) == 1
	if h.lib != nil && id != 0 && h.lib.SetTagged(id, on) {
		if on {
			dlog.Infof("dbserver: tag list: added track %d", id)
		} else {
			dlog.Infof("dbserver: tag list: removed track %d", id)
		}
	}
	return doneReply(msg)
}

// handleClearTagList answers 0x3202: the deck removes every track from the
// TAG LIST.
func (h *Handler) handleClearTagList(msg *proto.DBMessage) []*proto.DBMessage {
	if h.lib != nil && h.lib.ClearTagList() {
		dlog.Infof("dbserver: tag list: removed every track")
	}
	return doneReply(msg)
}

// handleTagListToPlaylist answers 0x3102: the deck makes a playlist of the
// TAG LIST, named as it asks ("TAG LIST"), which keeps its tracks.
func (h *Handler) handleTagListToPlaylist(msg *proto.DBMessage) []*proto.DBMessage {
	pc, ok := h.playlists.(PlaylistCreator)
	if !ok || h.lib == nil {
		return doneReply(msg)
	}
	name := ""
	if len(msg.Args) > 2 {
		name = proto.DecodeUTF16BE([]byte(msg.Args[2].Str))
	}
	if name == "" {
		name = "TAG LIST"
	}
	ids := h.lib.TagList()
	if err := pc.CreatePlaylist(name, msgArg(msg, 3), ids); err != nil {
		dlog.Errorf("dbserver: tag list to playlist %q: %v", name, err)
		return doneReply(msg)
	}
	dlog.Infof("dbserver: tag list: made playlist %q of %d tracks", name, len(ids))
	if h.onPlaylistsChanged != nil {
		h.onPlaylistsChanged()
	}
	return doneReply(msg)
}

// doneReply is rekordbox's answer to a change the deck asks for:
// 0x4000 [request kind, 0].
func doneReply(msg *proto.DBMessage) []*proto.DBMessage {
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
	}}
}

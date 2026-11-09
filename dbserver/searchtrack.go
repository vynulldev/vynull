// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// Search Track, 0x1500 [ctx, sort, declared UTF-16 byte length, query], is
// the track-only search newer decks send from their touchscreen (link-spec
// SEARCH_ORACLE.md, rekordbox 7.2.19). It searches SEARCH's domains (artist
// names, album, title, file name) but returns tracks only: a track whose
// title, file name, artists or album match, as SEARCH renders its content
// rows. Besides:
//
//   - the sort is its second argument, read as a signed low byte: 0 to 17 are
//     sorts (256 is 0), anything else answers total 0xffffffff;
//   - the default sort lists at most 5,000 tracks, in title order; an
//     explicit sort lists every match, and its rows carry the context type;
//   - a declared length of zero, or shorter than the query with its NUL,
//     gets an argumentless 0x0100; a request without a query gets an empty
//     list, and a fifth argument (SEARCH's) is ignored.

const (
	searchTrackMaxSort = 17
	searchTrackCeiling = 5000 // tracks the default sort lists
)

func (h *Handler) handleSearchTrack(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	raw := msg.Args[3].Str // UTF-16BE with the NUL, as the reader hands it over
	if declared := msgArg(msg, 2); declared == 0 || declared < uint32(len(raw)) {
		dlog.Debugf("dbserver: Search Track with declared length %d for a %d-byte query", declared, len(raw))
		return []*proto.DBMessage{{TxID: msg.TxID, Type: 0x0100}}
	}
	sortID := int8(uint8(msgArg(msg, 1)))
	if sortID < 0 || sortID > searchTrackMaxSort {
		return []*proto.DBMessage{{TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0xffffffff)}}}
	}
	order := uint32(sortID)
	tokens := searchTokens(proto.DecodeUTF16BE([]byte(raw)))
	var items []*menuItem
	if len(tokens) > 0 {
		c := h.trackRows()
		items = searchContent(c, c.trackOrder(order), tokens, true)
		if order == 0 {
			items = items[:min(len(items), searchTrackCeiling)]
		} else {
			markContextType(items, h.ctxTrackType)
		}
	}
	dlog.Debugf("dbserver: Search Track %q (sort %d) returning %d tracks", tokens, order, len(items))
	return h.countReply(msg, items)
}

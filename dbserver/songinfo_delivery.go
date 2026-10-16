// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// handleGetDeliveryInfo — opcode 0x2602, Delivery Info: [context, track_id].
// rekordbox answers a header with 13 rows, then serves them through the usual
// render (link-spec docs/SONG_INFO_SIBLINGS_ORACLE.md, "Delivery Info rows"):
//
//	composer, BPM, DeliveryComment, key, file type, genre, label, artist,
//	lyricist, DeliveryControl/ISRC, the track row, album, length
//
// We store no composer, DeliveryComment, lyricist, ISRC or DeliveryControl,
// so those rows carry rekordbox's values for unset fields: empty text, with
// the track ID where rekordbox puts it. The rows come in rekordbox's base
// order; rekordbox itself permutes them according to earlier requests (its
// reused list storage), which decks don't depend on since every row is typed.
// A track we don't have gets a zero-row menu.
func (h *Handler) handleGetDeliveryInfo(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}
	var items []*menuItem
	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			id := func(kind, name string) uint32 {
				if name == "" {
					return 0
				}
				return h.catID(kind, name)
			}
			keyID := id("key", t.Key)

			// The track row: the title with the key as its second column, the
			// single value rekordbox's eight-argument render reads (as in the
			// Hot Cue Bank list), and the request's context track type.
			track := h.trackToStdItem(t)
			track.songInfoTitle = h.newSongInfoTitle(t, t.ID)
			markContextType([]*menuItem{track}, uint32(dmstTrackType(msg)))

			items = []*menuItem{
				{ItemType: 0x36}, // composer (ComposerID lookup)
				{ID: uint32(t.BPM * 100), ItemType: 0x0d},                             // BPM
				{ID: t.ID, ItemType: 0x23},                                            // DeliveryComment
				{ID: newKeyID(t.Key), ParentID: keyID, Label2: t.Key, ItemType: 0x0f}, // key
				{ID: uint32(t.FileTypeID), ItemType: 0x12},                            // file type
				{ID: id("genre", t.Genre), Label1: t.Genre, ItemType: 0x06},
				{ID: id("label", t.Label), Label1: t.Label, ItemType: 0x0e},
				{ID: id("artist", t.Artist), Label1: t.Artist, ItemType: 0x07},
				{ID: t.ID, ItemType: 0x37},       // lyricist
				{ParentID: t.ID, ItemType: 0x4f}, // DeliveryControl / ISRC
				track,
				{ID: id("album", t.Album), Label1: t.Album, ItemType: 0x02, noArtHandle: true},
				{ID: uint32(t.Duration.Seconds()), ItemType: 0x0b}, // length
			}
		}
	}
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: delivery info 0x2602 track=%d returning %d rows", trackID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

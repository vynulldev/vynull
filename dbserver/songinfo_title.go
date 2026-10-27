// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// The title row of Display Song Info (0x2002) and Delivery Info (0x2602)
// takes its second column from the render request, not from the list it was
// built in (link-spec DISPLAY_SONG_INFO_ORACLE and SONG_INFO_SIBLINGS_ORACLE,
// render controls). An eight-argument render [ctx, offset, count, 0, total,
// column, gate, override] with gate 1 shows the override column, or the
// column argument when the override is 0; shorter renders and gate 0 show the
// plain title. The columns follow the ordinary secondary-column map.

// songInfoColumn is one second-column choice for a title row: the value in
// argument 0, the text in argument 5, and the field type in the item type's
// high byte.
type songInfoColumn struct {
	arg0  uint32
	label string
	hi    uint32
}

// songInfoTitle holds a title row's plain form and every column a render can
// select, keyed by selector number (2-17).
type songInfoTitle struct {
	plainArg0 uint32
	columns   map[uint32]songInfoColumn
}

// newSongInfoTitle precomputes the selectable columns for a library track.
// plainArg0 is argument 0 of the plain title: 1 for Display, the track ID
// for Delivery.
func (h *Handler) newSongInfoTitle(t *library.Track, plainArg0 uint32) *songInfoTitle {
	id := func(kind, name string) uint32 {
		if name == "" {
			return 0
		}
		return h.catID(kind, name)
	}
	date := ""
	if !t.DateAdded.IsZero() {
		date = t.DateAdded.Format("2006-01-02")
	}
	color := uint32(t.ColorID)
	return &songInfoTitle{plainArg0: plainArg0, columns: map[uint32]songInfoColumn{
		2:  {id("artist", t.Artist), t.Artist, 0x07},
		3:  {id("album", t.Album), t.Album, 0x02},
		4:  {uint32(t.BPM * 100), "", 0x0d},
		5:  {uint32(t.Rating), "", 0x0a},
		6:  {id("genre", t.Genre), t.Genre, 0x06},
		7:  {t.ID, t.Comment, 0x23},
		8:  {uint32(t.Duration.Seconds()), "", 0x0b},
		9:  {id("remixer", t.Remixer), t.Remixer, 0x29},
		10: {id("label", t.Label), t.Label, 0x0e},
		11: {id("origartist", t.OriginalArtist), t.OriginalArtist, 0x28},
		12: {id("key", t.Key), t.Key, 0x0f},
		13: {uint32(t.Bitrate), "", 0x10},
		14: {0, "", 0x00},
		15: {color, library.TrackColorName(uint8(color)), 0x13 + color&0xff},
		16: {uint32(t.PlayCount), "", 0x2a},
		17: {t.ID, date, 0x2e},
	}}
}

// render returns a copy of the title row composed for this render request.
func (st *songInfoTitle) render(item *menuItem, msg *proto.DBMessage) *menuItem {
	out := *item
	out.ParentID, out.Label2, out.ItemType = st.plainArg0, "", 0x0004
	if len(msg.Args) < 8 || msg.Args[6].Int() == 0 {
		return &out
	}
	sel := msg.Args[7].Int()
	if sel == 0 {
		sel = msg.Args[5].Int()
	}
	if c, ok := st.columns[sel]; ok {
		out.ParentID, out.Label2, out.ItemType = c.arg0, c.label, c.hi<<8|0x04
	}
	return &out
}

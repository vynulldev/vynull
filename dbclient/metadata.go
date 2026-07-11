// SPDX-License-Identifier: GPL-3.0-or-later

package dbclient

import (
	"fmt"

	"github.com/vynulldev/vynull/proto"
)

// Metadata is the subset of a track's metadata the now-playing displays need.
type Metadata struct {
	Title     string
	Artist    string
	Album     string
	Genre     string
	Key       string
	ArtworkID uint32 // for a follow-up 0x2003 artwork request
}

// Slot media types (mirror proto.Slot*).
const (
	SlotCD        uint8 = 0x01
	SlotSD        uint8 = 0x02
	SlotUSB       uint8 = 0x03
	SlotRekordbox uint8 = 0x04
)

// menuDescriptor packs (player, menu, slot, track-type) into the u32 that leads
// every menu request: [player][menu=1][slot][trackType=1 rekordbox].
func (c *Client) menuDescriptor(slot uint8) uint32 {
	return uint32(c.playerNum)<<24 | 0x01<<16 | uint32(slot)<<8 | 0x01
}

// FetchMetadata queries the connected player for trackID on the given slot:
// a 0x2002 request returns an item count, then a 0x3000 render returns the
// detail rows (menu header, items, footer), which are parsed into Metadata.
func (c *Client) FetchMetadata(slot uint8, trackID uint32) (*Metadata, error) {
	desc := c.menuDescriptor(slot)

	resp, err := c.request(c.nextTx(), proto.DBMsgGetMetadata, proto.ArgI32(desc), proto.ArgI32(trackID))
	if err != nil {
		return nil, fmt.Errorf("metadata request: %w", err)
	}
	var count uint32
	if len(resp.Args) >= 2 {
		count = resp.Args[1].Int()
	}
	if count == 0 {
		return nil, fmt.Errorf("no metadata for track %d on slot %d", trackID, slot)
	}

	// Render the menu: [descriptor, offset=0, count, 0, count, 0].
	if err := c.send(&proto.DBMessage{TxID: c.nextTx(), Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(desc), proto.ArgI32(0), proto.ArgI32(count),
		proto.ArgI32(0), proto.ArgI32(count), proto.ArgI32(0),
	}}); err != nil {
		return nil, fmt.Errorf("render request: %w", err)
	}

	m := &Metadata{}
	for i := 0; i < int(count)+2; i++ { // header + count items + footer
		msg, err := c.recv()
		if err != nil {
			return nil, fmt.Errorf("render response: %w", err)
		}
		switch msg.Type {
		case proto.DBMsgMenuItem:
			parseMetaItem(msg, m)
		case proto.DBMsgMenuFooter:
			return m, nil
		}
	}
	return m, nil
}

// parseMetaItem folds one 0x4101 detail row into m. The row layout is
// [parentID, mainID, label1Len, label1, label2Len, label2, itemType, flags…];
// the item type's high byte selects the field (title=0x00, artist=0x07,
// album=0x02, genre=0x06, key=0x0f), the value is label1, and the title row's
// arg[8] carries the artwork id.
func parseMetaItem(msg *proto.DBMessage, m *Metadata) {
	if len(msg.Args) < 7 {
		return
	}
	itemType := msg.Args[6].Int()
	if itemType&0xff != 0x04 { // not a detail row
		return
	}
	label1 := ""
	if len(msg.Args) > 3 {
		label1 = msg.Args[3].Str
	}
	switch (itemType >> 8) & 0xff {
	case 0x00: // title
		m.Title = label1
		if len(msg.Args) > 8 {
			m.ArtworkID = msg.Args[8].Int()
		}
	case 0x07:
		m.Artist = label1
	case 0x02:
		m.Album = label1
	case 0x06:
		m.Genre = label1
	case 0x0f:
		m.Key = label1
	}
}

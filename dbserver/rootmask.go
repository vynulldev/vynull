// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import "github.com/vynulldev/vynull/proto"

// A deck's root menu request (0x1000 [ctx, sort, capability mask]) says
// which categories the deck can open, and rekordbox serves only those
// (link-spec CONFIGURATION.md, rekordbox 7.2.19): a category needs bit
// (menu item - 1) of the mask, except DATE ADDED, which needs bit 24. A
// CDJ-2000NXS2 sends 0x04ffffff, so it gets no DATE ADDED.

// rootMenuItems maps a root category's item type (its rekordbox menu-item
// class) to the menu item, which picks the category's capability bit.
var rootMenuItems = map[uint32]uint32{
	0x80: 1,  // GENRE
	0x81: 2,  // ARTIST
	0x82: 3,  // ALBUM
	0x83: 4,  // TRACK
	0x85: 5,  // BPM
	0x86: 6,  // RATING
	0x87: 7,  // YEAR
	0x88: 8,  // REMIXER
	0x89: 9,  // LABEL
	0x8a: 10, // ORIGINAL ARTIST
	0x8b: 11, // KEY
	0x8d: 12, // CUE
	0x8e: 13, // COLOR
	0x92: 14, // TIME
	0x93: 15, // BITRATE
	0x94: 16, // FILE NAME
	0x84: 17, // PLAYLIST
	0x98: 18, // HOT CUE BANK
	0x95: 19, // HISTORY
	0x91: 20, // SEARCH
	0x96: 21, // COMMENTS
	0x8c: 22, // DATE ADDED
	0x97: 23, // DJ PLAY COUNT
	0x90: 24, // FOLDER
	0xaa: 27, // MATCHING
}

// legacyRootMask is the mask of older decks (dysentery's 0x00ffffff). For
// exactly this mask rekordbox adds HOT CUE BANK when the root lacks it.
const legacyRootMask = 0x00ffffff

// capabilityBit is the mask bit a root category needs, or false for an item
// type rekordbox doesn't have (served regardless).
func capabilityBit(itemType uint32) (uint32, bool) {
	item, ok := rootMenuItems[itemType]
	switch {
	case !ok:
		return 0, false
	case item == 22:
		return 1 << 24, true
	}
	return 1 << (item - 1), true
}

// noteRootMask remembers the capability mask of a root menu request, which
// the session's root menu renders then follow. A request without one (an
// older two-argument form) keeps every category.
func (h *Handler) noteRootMask(msg *proto.DBMessage) {
	h.rootMask, h.rootMaskSet = 0, len(msg.Args) >= 3
	if h.rootMaskSet {
		h.rootMask = msg.Args[2].Int()
	}
}

// maskRootMenu keeps the root categories the session's capability mask
// admits, in order.
func (h *Handler) maskRootMenu(items []*menuItem) []*menuItem {
	if !h.rootMaskSet {
		return items
	}
	out := make([]*menuItem, 0, len(items))
	hotCueBank := false
	for _, m := range items {
		if bit, known := capabilityBit(m.ItemType); known && h.rootMask&bit == 0 {
			continue
		}
		hotCueBank = hotCueBank || m.ItemType == 0x98
		out = append(out, m)
	}
	if h.rootMask == legacyRootMask && !hotCueBank {
		out = append(out, &menuItem{ID: 23, Label1: "HOT CUE BANK", ItemType: 0x98})
	}
	return out
}

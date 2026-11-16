// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// render_override.go: an eight-argument track render can ask for a second
// column of its own (link-spec PROTOCOL_REFERENCE.md "Track render override
// controls" and SECONDARY_COLUMN_ORACLE.md, rekordbox 7.2.19). Argument 7 is
// a gate: zero keeps the list's own column. Otherwise argument 8 selects the
// column: 0 the configured (persisted) detail column, 2-17 a sort ID's
// field, 14 being rekordbox's reserved, empty one. A row already showing
// that column keeps its text (KEY's "Am - 120.0 bpm"); any other row gets
// the field itself: its ID or number in argument 0, its text alone in
// argument 5 (none for the numeric fields) and its type in argument 6.

// renderColumn returns the second column a track render asks for, and
// whether it asks for one.
func (h *Handler) renderColumn(msg *proto.DBMessage) (byte, bool) {
	if len(msg.Args) < 8 || msg.Args[6].Int() == 0 {
		return 0, false
	}
	switch sel := msg.Args[7].Int(); {
	case sel == 0:
		hi, _ := TrackDetailHighByte(h.trackDetailKey())
		return hi, true
	case sel == 14:
		return 0x00, true
	case sel >= 2 && sel <= 17:
		return sortSpecs[sel].column, true
	}
	// rekordbox's extractor admits only 2-17; what it shows for any other
	// selector isn't known, so the list keeps its own column.
	return 0, false
}

// rekordbox's limits on a track row's strings (link-spec
// BOUNDARY_INVALID_LIFECYCLE_ORACLE.md "Secondary string limits"): a lookup
// value (artist, album, genre, label, remixer, original artist, key) keeps 127
// characters and a comment 255. A comment that still takes more than 255
// UTF-16 units (characters beyond the BMP take two) leaves the whole render
// unanswered.
const (
	lookupTextMax  = 127
	commentTextMax = 255
)

// capText cuts s to its first n characters.
func capText(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// capRowText applies rekordbox's 127-character limit to a track row whose
// second column is a lookup or the key, the whole text included (KEY's
// "key - BPM" is cut too); m itself is left alone.
func capRowText(m *menuItem) *menuItem {
	if !m.libTrack || m.ItemType&0xff != 0x04 {
		return m
	}
	switch byte(m.ItemType >> 8) {
	case 0x07, 0x02, 0x06, 0x0e, 0x29, 0x28, 0x0f:
		if capped := capText(m.Label2, lookupTextMax); capped != m.Label2 {
			r := *m
			r.Label2 = capped
			return &r
		}
	}
	return m
}

// isColorColumn reports whether a second-column type is the colour column,
// whose type carries the colour (0x14 Pink to 0x1b Purple).
func isColorColumn(col byte) bool { return col >= 0x13 && col <= 0x1b }

// overrideColumn returns m showing the second column col, as rekordbox
// extracts it for an override; m itself is left alone. Rows other than a
// library track's, and rows already showing col, come back unchanged.
func (h *Handler) overrideColumn(c *trackRows, m *menuItem, col byte) *menuItem {
	v := m.sv
	shown := byte(m.ItemType >> 8)
	if v == nil || !m.libTrack || m.TrackInfo || m.ItemType&0xff != 0x04 ||
		shown == col || isColorColumn(shown) && isColorColumn(col) {
		return m
	}
	var ids catIDs
	if c.holds(v) {
		ids = c.ids[v.pos]
	} else if h.lib != nil {
		if t := h.lib.Track(m.ID); t != nil {
			ids = trackCatIDs(t, h.catID)
		}
	}
	r := *m
	r.ParentID, r.Label2, r.ItemType = 0, "", uint32(col)<<8|0x04
	// A lookup column without a value (no album) shows ID 0 and no text.
	lookup := func(id uint32, text string) {
		if text != "" {
			r.ParentID, r.Label2 = id, capText(text, lookupTextMax)
		}
	}
	switch col {
	case 0x07:
		lookup(ids.artist, v.artist)
	case 0x02:
		lookup(ids.album, v.album)
	case 0x06:
		lookup(ids.genre, v.genre)
	case 0x0e:
		lookup(ids.label, v.label)
	case 0x29:
		lookup(ids.remixer, v.remixer)
	case 0x28:
		lookup(ids.origArtist, v.origArtist)
	case 0x0f: // the key alone, not KEY's "key - BPM"
		r.ParentID, r.Label2 = m.keyID, capText(m.sortKey, lookupTextMax)
	case 0x23: // comments and dates carry the track's ID
		r.ParentID, r.Label2 = m.ID, capText(v.comment, commentTextMax)
	case 0x2e:
		r.ParentID, r.Label2 = m.ID, v.dateAdded
	case 0x0d:
		r.ParentID = uint32(v.tempo)
	case 0x0a:
		r.ParentID = uint32(v.rating)
	case 0x0b:
		r.ParentID = uint32(v.seconds)
	case 0x10:
		r.ParentID = uint32(v.bitrate)
	case 0x2a:
		r.ParentID = uint32(v.plays)
	case 0x13:
		if v.color >= 1 && v.color <= 8 {
			r.ParentID, r.Label2 = uint32(v.color), library.TrackColorName(uint8(v.color))
			r.ItemType = uint32(0x13+v.color)<<8 | 0x04
		}
	}
	return &r
}

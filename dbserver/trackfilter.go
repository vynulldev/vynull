// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"sync"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// trackfilter.go serves the deck's TRACK FILTER: rekordbox's filter commands
// 0x3007 (on/off), 0x3107 (get the conditions), 0x3207 (set a condition),
// 0x3307 (MyTag condition on/off) and 0x3407 (add/remove a MyTag). The deck
// works out each condition's values itself (a BPM range from the tempo and
// percentage chosen, the related-key set of the chosen key) and rekordbox
// stores them, then filters the deck's TRACK list (0x1004 at menu location
// 1) by the enabled conditions while the filter is on. Decoded from real
// rekordbox captures of a CDJ-2000NXS2 setting the filter.

// Filter condition properties, as rekordbox numbers them.
const (
	filterPropBPM    = 6
	filterPropRating = 7
	filterPropKey    = 12
	filterPropColor  = 15
)

// filterPropOrder is the order rekordbox lists the conditions in.
var filterPropOrder = []uint32{filterPropBPM, filterPropKey, filterPropRating, filterPropColor}

// filterCondition is one condition: whether it's on, its kind (6 a range,
// 17 a list, 0 for rating), and its values.
type filterCondition struct {
	on     byte
	kind   byte
	values []uint32
}

// MyTag condition modes, per column: a track must carry every ticked tag
// (AND) or any of them (OR).
const (
	mytagAnd = 0x10
	mytagOr  = 0x11
)

// mytagCondition is the MyTag condition of one column: whether it's on, its
// AND/OR mode, and the ticked tags.
type mytagCondition struct {
	on   bool
	mode uint32
	tags []uint32
}

// trackFilter is one player's filter settings.
type trackFilter struct {
	on     bool
	conds  map[uint32]*filterCondition
	mytags map[uint32]*mytagCondition // by MyTag column
}

func (f *trackFilter) mytag(column uint32) *mytagCondition {
	if f.mytags == nil {
		f.mytags = map[uint32]*mytagCondition{}
	}
	c := f.mytags[column]
	if c == nil {
		c = &mytagCondition{mode: mytagAnd}
		f.mytags[column] = c
	}
	return c
}

// newTrackFilter returns the settings a player starts with: everything off,
// with values like rekordbox's own initial ones.
func newTrackFilter() *trackFilter {
	return &trackFilter{conds: map[uint32]*filterCondition{
		filterPropBPM:    {kind: 6, values: []uint32{12220, 13780}},
		filterPropKey:    {kind: 17, values: []uint32{16, 15, 14, 18}},
		filterPropRating: {kind: 0, values: []uint32{5, 0}},
		filterPropColor:  {kind: 17, values: []uint32{0}},
	}}
}

// trackFilters holds every player's filter settings, shared by all of the
// server's sessions so they survive a deck reconnecting.
type trackFilters struct {
	mu      sync.Mutex
	players map[uint8]*trackFilter
}

// filterFor returns the filter settings of the player that sent msg (its
// context's first byte), creating them on first use.
func (h *Handler) filterFor(msg *proto.DBMessage) *trackFilter {
	if h.filters == nil {
		h.filters = &trackFilters{}
	}
	h.filters.mu.Lock()
	defer h.filters.mu.Unlock()
	if h.filters.players == nil {
		h.filters.players = map[uint8]*trackFilter{}
	}
	player := uint8(0)
	if len(msg.Args) > 0 {
		player = uint8(msg.Args[0].Int() >> 24)
	}
	f := h.filters.players[player]
	if f == nil {
		f = newTrackFilter()
		h.filters.players[player] = f
	}
	return f
}

func filterAck(msg *proto.DBMessage) []*proto.DBMessage {
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
	}}
}

func msgArg(msg *proto.DBMessage, i int) uint32 {
	if len(msg.Args) > i {
		return msg.Args[i].Int()
	}
	return 0
}

// handleSetFilterOnOff handles 0x3007 [ctx, on]: TRACK FILTER on (1) or off
// (0). Decks send it with 0 before opening the root menu, too.
func (h *Handler) handleSetFilterOnOff(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	h.filters.mu.Lock()
	f.on = msgArg(msg, 1) != 0
	h.filters.mu.Unlock()
	dlog.Debugf("dbserver: track filter (0x3007) on=%v", f.on)
	return filterAck(msg)
}

// handleGetFilterConditions handles 0x3107 [ctx]: the player's conditions as
// rekordbox's 0x4004 reply, [kind, 0, blob length, blob, count], the blob
// holding one FCND record per condition.
func (h *Handler) handleGetFilterConditions(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	h.filters.mu.Lock()
	var blob []byte
	for _, prop := range filterPropOrder {
		blob = append(blob, encodeFilterCondition(prop, f.conds[prop])...)
	}
	h.filters.mu.Unlock()
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4004,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0),
			proto.ArgI32(uint32(len(blob))), proto.ArgBlob(blob),
			proto.ArgI32(uint32(len(filterPropOrder))),
		},
	}}
}

// encodeFilterCondition builds one FCND record: a little-endian size and
// version, "FCND", then big-endian fields: 0x14, the size less 8, the
// property, and the condition (on, kind, u16 count, u32 values).
func encodeFilterCondition(prop uint32, c *filterCondition) []byte {
	size := 28 + 4*len(c.values)
	r := make([]byte, size)
	binary.LittleEndian.PutUint32(r[0:], uint32(size))
	binary.LittleEndian.PutUint32(r[4:], 1)
	copy(r[8:], "FCND")
	binary.BigEndian.PutUint32(r[12:], 0x14)
	binary.BigEndian.PutUint32(r[16:], uint32(size-8))
	binary.BigEndian.PutUint32(r[20:], prop)
	r[24], r[25] = c.on, c.kind
	binary.BigEndian.PutUint16(r[26:], uint16(len(c.values)))
	for i, v := range c.values {
		binary.BigEndian.PutUint32(r[28+4*i:], v)
	}
	return r
}

// handleSetFilterCondition handles 0x3207 [ctx, prop, 0, length, blob]: one
// condition, its blob little-endian (on, kind, u16 count, u32 values).
func (h *Handler) handleSetFilterCondition(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	prop := msgArg(msg, 1)
	var b []byte
	if len(msg.Args) > 4 {
		b = msg.Args[4].Bytes
	}
	if len(b) >= 4 {
		n := int(binary.LittleEndian.Uint16(b[2:]))
		c := &filterCondition{on: b[0], kind: b[1]}
		for i := 0; i < n && 8+4*i <= len(b); i++ {
			c.values = append(c.values, binary.LittleEndian.Uint32(b[4+4*i:]))
		}
		h.filters.mu.Lock()
		f.conds[prop] = c
		h.filters.mu.Unlock()
		dlog.Debugf("dbserver: track filter condition (0x3207) prop=%d on=%d values=%v", prop, c.on, c.values)
	}
	return filterAck(msg)
}

// handleSetFilterMyTag handles 0x3307 [ctx, 0, column, on, mode]: a MyTag
// column's condition on or off, with its mode (0x10 AND, 0x11 OR).
func (h *Handler) handleSetFilterMyTag(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	column := msgArg(msg, 2)
	h.filters.mu.Lock()
	c := f.mytag(column)
	c.on = msgArg(msg, 3) != 0
	if m := msgArg(msg, 4); m == mytagAnd || m == mytagOr {
		c.mode = m
	}
	h.filters.mu.Unlock()
	dlog.Debugf("dbserver: track filter MyTag (0x3307) column=%d on=%v mode=%#x", column, c.on, c.mode)
	return filterAck(msg)
}

// handleAddFilterMyTag handles 0x3407 [ctx, 0, column, tag ID, add]: a tag
// ticked (1) or unticked (0) in a MyTag column's condition.
func (h *Handler) handleAddFilterMyTag(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	column, tag, add := msgArg(msg, 2), msgArg(msg, 3), msgArg(msg, 4) != 0
	h.filters.mu.Lock()
	c := f.mytag(column)
	kept := c.tags[:0]
	for _, t := range c.tags {
		if t != tag {
			kept = append(kept, t)
		}
	}
	c.tags = kept
	if add {
		c.tags = append(c.tags, tag)
	}
	h.filters.mu.Unlock()
	dlog.Debugf("dbserver: track filter MyTag item (0x3407) column=%d tag=%d add=%v", column, tag, add)
	return filterAck(msg)
}

// handleGetFilterMyTags handles 0x1015 [ctx, sort, parent, 0], the TRACK
// FILTER's MyTag screen: parent 0 lists the MyTag columns, any other the
// tags in that column (only tags some track carries, as rekordbox lists
// them). Rows are [0, id, len, name, 2, "", 0x4a or 0x4b, 0, mode, index]:
// item type 0x4b marks a column whose condition is on, or a ticked tag, and
// a column row carries its AND/OR mode in argument 8.
func (h *Handler) handleGetFilterMyTags(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	parent := msgArg(msg, 2)
	var items []*menuItem
	if h.mytags != nil {
		h.filters.mu.Lock()
		if parent == 0 {
			for i, col := range h.mytags.MyTagColumns() {
				c := f.mytag(col.ID)
				items = append(items, &menuItem{ID: col.ID, Label1: col.Name, ItemType: tickType(c.on), ArtID: c.mode, trackPos: uint32(i)})
			}
		} else {
			c := f.mytag(parent)
			for i, tag := range h.mytags.MyTagsIn(parent) {
				items = append(items, &menuItem{ID: tag.ID, Label1: tag.Name, ItemType: tickType(containsU32(c.tags, tag.ID)), trackPos: uint32(i)})
			}
		}
		h.filters.mu.Unlock()
	}
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: track filter MyTag list (0x1015) parent=%d returning %d rows", parent, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// tickType is the MyTag screen row type: 0x4b ticked, 0x4a not.
func tickType(ticked bool) uint32 {
	if ticked {
		return 0x4b
	}
	return 0x4a
}

// mytagMatch reports whether a track passes every enabled MyTag column
// condition: a column on with ticked tags keeps tracks carrying all of them
// (AND) or any of them (OR); a column with nothing ticked doesn't narrow.
func (h *Handler) mytagMatch(f *trackFilter, trackID uint32) bool {
	var have []uint32
	loaded := false
	for _, c := range f.mytags {
		if !c.on || len(c.tags) == 0 {
			continue
		}
		if !loaded {
			have, loaded = h.mytags.TrackMyTags(trackID), true
		}
		hits := 0
		for _, t := range c.tags {
			if containsU32(have, t) {
				hits++
			}
		}
		if c.mode == mytagOr && hits == 0 || c.mode != mytagOr && hits < len(c.tags) {
			return false
		}
	}
	return true
}

// applyTrackFilter narrows a TRACK list (0x1004) at menu location 1 by the
// player's enabled conditions while its filter is on, as rekordbox does:
//
//   - BPM [lo, hi] (BPM*100) keeps tracks whose BPM lies within it;
//   - key [indices] keeps tracks whose 1-24 key index is listed;
//   - rating [n, 0] keeps tracks rated exactly n stars;
//   - colour [mask] keeps tracks whose colour's bit (1 << (colour-1); yellow,
//     colour 4, is 8) is set, and a zero mask (no colour chosen) keeps all.
//   - each MyTag column whose condition is on keeps tracks carrying all its
//     ticked tags (AND) or any of them (OR).
func (h *Handler) applyTrackFilter(msg *proto.DBMessage, items []*menuItem) []*menuItem {
	if dmstMenu(msg) != 1 {
		return items
	}
	f := h.filterFor(msg)
	h.filters.mu.Lock()
	defer h.filters.mu.Unlock()
	if !f.on {
		return items
	}
	active := func(prop uint32, minValues int) *filterCondition {
		if c := f.conds[prop]; c != nil && c.on != 0 && len(c.values) >= minValues {
			return c
		}
		return nil
	}
	bpm, key := active(filterPropBPM, 2), active(filterPropKey, 0)
	rating, color := active(filterPropRating, 1), active(filterPropColor, 1)
	if color != nil && color.values[0] == 0 {
		color = nil // no colour chosen
	}
	useMyTag := false
	if h.mytags != nil {
		for _, c := range f.mytags {
			if c.on && len(c.tags) > 0 {
				useMyTag = true
			}
		}
	}
	if bpm == nil && key == nil && rating == nil && color == nil && !useMyTag {
		return items
	}
	var out []*menuItem
	for _, it := range items {
		if bpm != nil && (it.sortBPM < bpm.values[0] || it.sortBPM > bpm.values[1]) {
			continue
		}
		if key != nil && !containsU32(key.values, it.newKeyID) {
			continue
		}
		if rating != nil && it.rating != rating.values[0] {
			continue
		}
		if color != nil && (it.ColorID == 0 || it.ColorID > 32 || color.values[0]&(1<<(it.ColorID-1)) == 0) {
			continue
		}
		if useMyTag && !h.mytagMatch(f, it.ID) {
			continue
		}
		out = append(out, it)
	}
	dlog.Debugf("dbserver: track filter kept %d of %d tracks", len(out), len(items))
	return out
}

func containsU32(s []uint32, v uint32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

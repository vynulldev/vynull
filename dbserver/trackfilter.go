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

// trackFilter is one player's filter settings.
type trackFilter struct {
	on       bool
	conds    map[uint32]*filterCondition
	mytagOn  bool
	mytagArg uint32 // the 0x3307 mode argument, kept as sent
	tags     []uint32
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

// handleSetFilterMyTag handles 0x3307 [ctx, 0, 1, on, mode]: the MyTag
// condition on or off.
func (h *Handler) handleSetFilterMyTag(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	h.filters.mu.Lock()
	f.mytagOn = msgArg(msg, 3) != 0
	f.mytagArg = msgArg(msg, 4)
	h.filters.mu.Unlock()
	dlog.Debugf("dbserver: track filter MyTag (0x3307) on=%v mode=%#x", f.mytagOn, f.mytagArg)
	return filterAck(msg)
}

// handleAddFilterMyTag handles 0x3407 [ctx, 0, 1, tag ID, add]: a MyTag added
// to (1) or removed from (0) the MyTag condition.
func (h *Handler) handleAddFilterMyTag(msg *proto.DBMessage) []*proto.DBMessage {
	f := h.filterFor(msg)
	tag, add := msgArg(msg, 3), msgArg(msg, 4) != 0
	h.filters.mu.Lock()
	kept := f.tags[:0]
	for _, t := range f.tags {
		if t != tag {
			kept = append(kept, t)
		}
	}
	f.tags = kept
	if add {
		f.tags = append(f.tags, tag)
	}
	h.filters.mu.Unlock()
	dlog.Debugf("dbserver: track filter MyTag item (0x3407) tag=%#x add=%v", tag, add)
	return filterAck(msg)
}

// applyTrackFilter narrows a TRACK list (0x1004) at menu location 1 by the
// player's enabled conditions while its filter is on, as rekordbox does:
//
//   - BPM [lo, hi] (BPM*100) keeps tracks whose BPM lies within it;
//   - key [indices] keeps tracks whose 1-24 key index is listed;
//   - rating [n, 0] keeps tracks rated exactly n stars;
//   - colour [mask] keeps tracks whose colour's bit (1 << (colour-1); yellow,
//     colour 4, is 8) is set, and a zero mask (no colour chosen) keeps all.
//
// The MyTag condition isn't decoded yet and doesn't narrow the list.
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
	if bpm == nil && key == nil && rating == nil && color == nil {
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

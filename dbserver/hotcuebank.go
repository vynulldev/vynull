// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"slices"
	"unicode/utf16"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// hotcuebank.go answers the Hot Cue Bank requests: 0x2001 (the bank catalog
// menu), 0x2101 (a bank's cues, legacy records) and 0x2301 (a bank's cues,
// extended records), from the banks a rekordbox import stored in the library
// (library/hotcuebank.go), following link-spec docs/HOT_CUE_BANK_ORACLE.md.
// rekordbox answers an unknown bank exactly like an empty one.
//
// Only context track type 0x01 reaches the bank database in rekordbox. Any
// other type gets status 50: the catalog answers a header carrying 50 and then
// never answers the render, and the getters send their usual reply kind with
// status 50 and no records.

// hotCueBankStatusRejected is rekordbox's status for a Hot Cue Bank request
// whose context track type isn't 0x01.
const hotCueBankStatusRejected = 50

// hotCueBankAllowed reports whether a Hot Cue Bank request carries context
// track type 0x01, the only type rekordbox serves banks for.
func hotCueBankAllowed(msg *proto.DBMessage) bool {
	return len(msg.Args) > 0 && msg.Args[0].Int()&0xff == 0x01
}

// bankRender returns the rows a render answering a Hot Cue Bank catalog
// request reads, and whether it is answered at all. rekordbox keeps a list
// per context (menu location) and renders the one at the render's own
// context: the request's own list, even empty, or another context's list
// only when it holds rows; otherwise no reply (link-spec
// hot-cue-bank-location-state, -location-cross and -discovery).
func (h *Handler) bankRender(msg *proto.DBMessage) (rows []*menuItem, answer bool) {
	rows = h.recentLists().get(dmstContext(msg))
	return rows, dmstContext(msg) == h.bankQuery.context || len(rows) > 0
}

// handleGetHotCueBank — opcode 0x2001, the Hot Cue Bank catalog:
// [context, selector, mode, requested_track_count]. Mode 1 lists the folders
// and banks under a parent (0 = top level); mode 0 lists a bank's tracks;
// any other mode, or an unknown selector, is an empty menu.
func (h *Handler) handleGetHotCueBank(msg *proto.DBMessage) []*proto.DBMessage {
	if !hotCueBankAllowed(msg) {
		dlog.Debugf("dbserver: HOT CUE BANK 0x2001 rejected for context track type 0x%02x", dmstTrackType(msg))
		h.silentRender = true
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(hotCueBankStatusRejected)},
		}}
	}
	arg := func(i int) uint32 {
		if len(msg.Args) > i {
			return msg.Args[i].Int()
		}
		return 0
	}
	selector, mode, count := arg(1), arg(2), arg(3)

	var items []*menuItem
	if h.lib != nil {
		switch mode {
		case 1:
			items = h.hotCueBankTreeItems(selector)
		case 0:
			items = h.hotCueBankTrackItems(selector, count)
		}
	}
	h.setPendingAll(msg, items)
	h.bankQuery.context, h.bankQuery.set = dmstContext(msg), true
	dlog.Debugf("dbserver: HOT CUE BANK selector=%d mode=%d count=%d returning %d items", selector, mode, count, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// hotCueBankTreeItems lists the live folders (item type 0x01) and banks
// (0x2b) under parentID, carrying Seq in argument 9: the folders first, then
// the banks, each in Seq order, as rekordbox lists them (link-spec
// hot-cue-bank-matrix: a folder with Seq 2 before a bank with Seq 1).
func (h *Handler) hotCueBankTreeItems(parentID uint32) []*menuItem {
	children := h.lib.HotCueBankChildren(parentID)
	slices.SortStableFunc(children, func(a, b library.HotCueBank) int {
		switch {
		case a.Folder == b.Folder:
			return 0
		case a.Folder:
			return -1
		}
		return 1
	})
	var items []*menuItem
	for _, b := range children {
		itemType := uint32(0x2b)
		if b.Folder {
			itemType = 0x01
		}
		items = append(items, &menuItem{ID: b.ID, Label1: b.Name, ItemType: itemType, trackPos: uint32(b.Seq)})
	}
	return items
}

// hotCueBankTrackItems lists a bank's tracks as ordinary track rows in
// membership order, argument 9 carrying the membership position as rekordbox
// renders it (16 bits, so -1 is 65535). Memberships whose track isn't in the
// library drop out; duplicates stay. The count caps the rows at
// max(count, 3), and a count with bit 31 set returns none, as rekordbox's
// signed loop does.
func (h *Handler) hotCueBankTrackItems(bankID, count uint32) []*menuItem {
	if int32(count) < 0 {
		return nil
	}
	limit := int(max(count, 3))
	var items []*menuItem
	for _, m := range h.lib.HotCueBankMembers(bankID) {
		if len(items) >= limit {
			break
		}
		t := h.hotCueBankTrack(&m)
		if t == nil {
			continue
		}
		it := h.trackToStdItem(t)
		it.trackPos = uint32(uint16(m.TrackNo))
		// The bank list doesn't build rekordbox's cached "Key - BPM"
		// composite, so its second column comes from the dynamic extractor:
		// the key alone, or for BPM the number with no text
		// (link-spec docs/SECONDARY_COLUMNS.md, "String ownership").
		switch h.trackDetailKey() {
		case "key":
			it.Label2 = t.Key
		case "bpm":
			it.Label2 = ""
		}
		items = append(items, it)
	}
	return items
}

// hotCueBankTrack returns a membership's library track when a deck can play
// it, else nil. Memberships to content not in the library, or to a track
// whose file is missing here (rekordbox's built-in sampler sounds, say,
// imported from a Windows library), are left out of everything a deck is
// served: loading a bank whose pads point at files the deck can't open broke
// the deck's bank load and dropped its connection.
func (h *Handler) hotCueBankTrack(m *library.HotCueBankMember) *library.Track {
	if m.TrackID == 0 {
		return nil
	}
	t := h.lib.Track(m.TrackID)
	if t == nil || t.FileMissing {
		return nil
	}
	return t
}

// msecToFrame150 converts milliseconds to 150-fps frames, truncating.
func msecToFrame150(ms int32) uint32 { return uint32(int64(ms) * 15 / 100) }

// handleGetHotCueBankCues — opcode 0x2101, a bank's cues as legacy 36-byte
// cue records: [context, bank_id]. rekordbox looks up slots 1-3 and replies
// 0x4702 with nine arguments: request kind, status,
// cue blob length, cue records, record width (36), record count, extension
// status, extension blob length, and the 8-byte millisecond extensions.
func (h *Handler) handleGetHotCueBankCues(msg *proto.DBMessage) []*proto.DBMessage {
	var cues, ext []byte
	count := 0
	if hotCueBankAllowed(msg) && h.lib != nil && len(msg.Args) > 1 {
		bankID := msg.Args[1].Int()
		for slot := int32(1); slot <= 3; slot++ {
			m := h.lib.HotCueBankSlot(bankID, slot)
			if m == nil || h.hotCueBankTrack(m) == nil {
				continue
			}
			rec := make([]byte, 36)
			if m.OutMsec != -1 {
				rec[0] = 1 // loop
			}
			rec[1] = 1
			// rekordbox numbers a bank slot's cue slot+3 in this record;
			// decks still show bank slots from pad A.
			rec[2] = byte(slot + 3)
			binary.LittleEndian.PutUint32(rec[4:], m.TrackID)
			binary.LittleEndian.PutUint32(rec[12:], msecToFrame150(m.InMsec))
			if m.OutMsec != -1 {
				if f := msecToFrame150(m.OutMsec); f != 0 {
					binary.LittleEndian.PutUint32(rec[16:], f-1)
				}
			}
			binary.LittleEndian.PutUint32(rec[20:], m.InMpegFrame)
			binary.LittleEndian.PutUint32(rec[24:], m.OutMpegFrame)
			binary.LittleEndian.PutUint32(rec[28:], m.InMpegAbs)
			binary.LittleEndian.PutUint32(rec[32:], m.OutMpegAbs)
			cues = append(cues, rec...)
			ext = binary.LittleEndian.AppendUint32(ext, uint32(m.InMsec))
			ext = binary.LittleEndian.AppendUint32(ext, uint32(m.OutMsec))
			count++
		}
	}
	status := uint32(0)
	if !hotCueBankAllowed(msg) {
		status = hotCueBankStatusRejected
	}
	dlog.Debugf("dbserver: hot cue bank cues 0x2101: %d records, status %d", count, status)
	if count == 0 {
		// Empty blobs are declared but not sent.
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: 0x4702,
			DeclaredArgCount: 9,
			OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06, 0x06, 0x06, 0x06, 0x03},
			Args: []proto.DBArg{
				proto.ArgI32(uint32(msg.Type)), proto.ArgI32(status),
				proto.ArgI32(0), proto.ArgI32(36), proto.ArgI32(0), proto.ArgI32(0), proto.ArgI32(0),
			},
		}}
	}
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4702,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0),
			proto.ArgI32(uint32(len(cues))), proto.ArgBlob(cues),
			proto.ArgI32(36), proto.ArgI32(uint32(count)),
			proto.ArgI32(0), proto.ArgI32(uint32(len(ext))), proto.ArgBlob(ext),
		},
	}}
}

// handleGetHotCueBankCuesExtended — opcode 0x2301, a bank's cues as extended
// records: [context, bank_id, requested_count]. rekordbox looks up slots
// 1..requested_count and replies 0x4e02: request kind, status (0 with
// records, 1 without), blob length, records, record count.
func (h *Handler) handleGetHotCueBankCuesExtended(msg *proto.DBMessage) []*proto.DBMessage {
	var blob []byte
	count := 0
	if hotCueBankAllowed(msg) && h.lib != nil && len(msg.Args) > 2 {
		bankID, requested := msg.Args[1].Int(), msg.Args[2].Int()
		// Slots past the bank's highest position can't match, so stop there
		// rather than walk a huge requested count.
		var top int32
		for _, m := range h.lib.HotCueBankMembers(bankID) {
			top = max(top, m.TrackNo)
		}
		for slot := int32(1); slot <= top && uint32(slot) <= requested; slot++ {
			if m := h.lib.HotCueBankSlot(bankID, slot); m != nil && h.hotCueBankTrack(m) != nil {
				blob = append(blob, hotCueBankExtendedRecord(uint16(slot), m)...)
				count++
			}
		}
	}
	status := uint32(1)
	switch {
	case !hotCueBankAllowed(msg):
		status = hotCueBankStatusRejected
	case count > 0:
		status = 0
	}
	dlog.Debugf("dbserver: hot cue bank cues 0x2301: %d records, status %d", count, status)
	if count == 0 {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: 0x4e02,
			DeclaredArgCount: 5,
			OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06},
			Args: []proto.DBArg{
				proto.ArgI32(uint32(msg.Type)), proto.ArgI32(status),
				proto.ArgI32(0), proto.ArgI32(0),
			},
		}}
	}
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4e02,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)), proto.ArgI32(status),
			proto.ArgI32(uint32(len(blob))), proto.ArgBlob(blob),
			proto.ArgI32(uint32(count)),
		},
	}}
}

// hotCueBankExtendedRecord builds one extended cue record: a 56-byte
// little-endian header, then an option area holding the colour, the
// microsecond and beat-loop fields, the UTF-16LE comment, and a 48-byte seek
// descriptor (length 44, then the empty seek positions), padded to 4 bytes.
func hotCueBankExtendedRecord(slot uint16, m *library.HotCueBankMember) []byte {
	var comment []byte
	if m.Comment != "" {
		for _, u := range utf16.Encode([]rune(m.Comment)) {
			comment = binary.LittleEndian.AppendUint16(comment, u)
		}
		comment = append(comment, 0, 0)
	}
	const seekLen = 48
	optLen := 18 + len(comment) + seekLen
	size := (56 + optLen + 3) &^ 3
	r := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint32(r[0x00:], uint32(size))
	le.PutUint16(r[0x04:], slot)
	r[0x06] = 2
	if m.OutMsec == -1 {
		r[0x06] = 1
	}
	le.PutUint16(r[0x0a:], 1000)
	le.PutUint32(r[0x0c:], uint32(m.InMsec))
	le.PutUint32(r[0x10:], uint32(m.OutMsec))
	le.PutUint32(r[0x18:], 8)
	le.PutUint32(r[0x20:], 0xffffffff)
	le.PutUint32(r[0x24:], m.InMpegFrame)
	le.PutUint32(r[0x28:], m.OutMpegFrame)
	le.PutUint32(r[0x2c:], m.InMpegAbs)
	le.PutUint32(r[0x30:], m.OutMpegAbs)
	le.PutUint32(r[0x34:], uint32(optLen))
	if m.Color >= 0 && m.Color <= 7 {
		r[0x3a] = byte(m.Color + 1)
	}
	le.PutUint32(r[0x3c:], m.CueMicrosec)
	le.PutUint16(r[0x42:], uint16(m.BeatLoopSize>>16)) // high word first
	le.PutUint16(r[0x44:], uint16(m.BeatLoopSize))
	le.PutUint16(r[0x48:], uint16(len(comment)))
	copy(r[0x4a:], comment)
	le.PutUint32(r[0x4a+len(comment):], 44) // seek descriptor length
	return r
}

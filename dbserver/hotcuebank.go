// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

// hotcuebank.go answers the Hot Cue Bank requests: 0x2001 (the bank catalog
// menu), 0x2101 (a bank's cues, legacy records) and 0x2301 (a bank's cues,
// extended records). We store no hot cue banks yet, so every bank is empty;
// these replies are rekordbox's for an empty or unknown bank, which it treats
// alike (link-spec docs/HOT_CUE_BANK_ORACLE.md).
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

// handleGetHotCueBank — opcode 0x2001, the Hot Cue Bank catalog:
// [context, selector, mode, requested_track_count]. With no banks stored,
// every selector and mode is an empty menu, as rekordbox serves an empty or
// unknown bank.
func (h *Handler) handleGetHotCueBank(msg *proto.DBMessage) []*proto.DBMessage {
	if !hotCueBankAllowed(msg) {
		dlog.Debugf("dbserver: HOT CUE BANK 0x2001 rejected for context track type 0x%02x", dmstTrackType(msg))
		h.silentRender = true
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(hotCueBankStatusRejected)},
		}}
	}
	h.setPendingAll(msg, nil)
	dlog.Debugf("dbserver: HOT CUE BANK list (empty, not stored)")
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetHotCueBankCues — opcode 0x2101, a bank's cues as legacy 36-byte
// cue records: [context, bank_id]. The reply is 0x4702 with nine declared
// arguments: request kind, status, cue blob length, cue blob, record width
// (36), record count, two zero fields, and the extended-time blob. Empty blobs
// are declared but not sent.
func (h *Handler) handleGetHotCueBankCues(msg *proto.DBMessage) []*proto.DBMessage {
	status := uint32(0)
	if !hotCueBankAllowed(msg) {
		status = hotCueBankStatusRejected
	}
	dlog.Debugf("dbserver: hot cue bank cues 0x2101 (empty, status %d)", status)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4702,
		DeclaredArgCount: 9,
		OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06, 0x06, 0x06, 0x06, 0x03},
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(status),
			proto.ArgI32(0),  // cue blob length
			proto.ArgI32(36), // record width
			proto.ArgI32(0),  // record count
			proto.ArgI32(0),
			proto.ArgI32(0),
		},
	}}
}

// handleGetHotCueBankCuesExtended — opcode 0x2301, a bank's cues as extended
// records: [context, bank_id, requested_count]. The reply is 0x4e02 with five
// declared arguments: request kind, status, blob length, records blob, record
// count. rekordbox's status for an empty result is 1, not 0.
func (h *Handler) handleGetHotCueBankCuesExtended(msg *proto.DBMessage) []*proto.DBMessage {
	status := uint32(1)
	if !hotCueBankAllowed(msg) {
		status = hotCueBankStatusRejected
	}
	dlog.Debugf("dbserver: hot cue bank cues 0x2301 (empty, status %d)", status)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4e02,
		DeclaredArgCount: 5,
		OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06},
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(status),
			proto.ArgI32(0), // blob length
			proto.ArgI32(0), // record count
		},
	}}
}

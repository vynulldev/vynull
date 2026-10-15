// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/hex"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// The Hot Cue Bank getters must reply byte for byte as rekordbox 7.2.19 does
// (link-spec goldens context-hot-cue-getter-status-*): status 50 for any
// context track type but 0x01, and the empty-bank reply for 0x01, since we
// store no banks and rekordbox treats an unknown bank like an empty one.
func TestHotCueBankGetterReplies(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		name string
		kind uint16
		ctx  uint32
		want string
	}{
		{"legacy type 0x00", 0x2101, 0x01010300, "11872349ae11000000011047020f0914000000090606060306060606031100002101110000003211000000001100000024110000000011000000001100000000"},
		{"legacy type 0x01", 0x2101, 0x01010301, "11872349ae11000000011047020f0914000000090606060306060606031100002101110000000011000000001100000024110000000011000000001100000000"},
		{"legacy type 0x04", 0x2101, 0x01010304, "11872349ae11000000011047020f0914000000090606060306060606031100002101110000003211000000001100000024110000000011000000001100000000"},
		{"extended type 0x00", 0x2301, 0x01010300, "11872349ae1100000001104e020f05140000000506060603061100002301110000003211000000001100000000"},
		{"extended type 0x01", 0x2301, 0x01010301, "11872349ae1100000001104e020f05140000000506060603061100002301110000000111000000001100000000"},
		{"extended type 0x06", 0x2301, 0x01010306, "11872349ae1100000001104e020f05140000000506060603061100002301110000003211000000001100000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.Handle(&proto.DBMessage{TxID: 1, Type: tc.kind, Args: []proto.DBArg{proto.ArgI32(tc.ctx), proto.ArgI32(9001), proto.ArgI32(3)}})
			if len(resp) != 1 {
				t.Fatalf("%d replies, want 1", len(resp))
			}
			if got := hex.EncodeToString(proto.MarshalDBMessage(resp[0])); got != tc.want {
				t.Errorf("reply\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A catalog request with any context track type but 0x01 gets a header
// carrying status 50, and its render is never answered; the next request is
// served normally.
func TestHotCueBankCatalogRejectedType(t *testing.T) {
	h := &Handler{}
	resp := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2001, Args: []proto.DBArg{proto.ArgI32(0x01010300), proto.ArgI32(0), proto.ArgI32(1), proto.ArgI32(0)}})
	if len(resp) != 1 || resp[0].Type != proto.DBMsgSuccess || resp[0].Args[1].Int() != 50 {
		t.Fatalf("header: got %+v, want success with 50", resp)
	}
	render := &proto.DBMessage{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{proto.ArgI32(0x01010300), proto.ArgI32(0), proto.ArgI32(32), proto.ArgI32(0), proto.ArgI32(50), proto.ArgI32(12), proto.ArgI32(1), proto.ArgI32(0)}}
	if resp := h.Handle(render); resp != nil {
		t.Fatalf("render after rejection: got %d replies, want none", len(resp))
	}

	// Type 0x01: an empty menu, rendered normally.
	resp = h.Handle(&proto.DBMessage{TxID: 3, Type: 0x2001, Args: []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(0), proto.ArgI32(1), proto.ArgI32(0)}})
	if len(resp) != 1 || resp[0].Args[1].Int() != 0 {
		t.Fatalf("type 0x01 header: got %+v, want total 0", resp)
	}
	render.TxID, render.Args[0] = 4, proto.ArgI32(0x01010301)
	if resp := h.Handle(render); len(resp) == 0 {
		t.Fatalf("render after an accepted request went unanswered")
	}
}

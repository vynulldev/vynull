// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// TestHandleSetupArity checks the two setup reply shapes (per the link-spec
// rekordbox-7.2.19 oracle): legacy 1-arg -> 0x4000 [0, dev]; extended 2-arg ->
// 0x0000 [dev, echo]. The legacy path is what real CDJs we've tested use and
// must stay byte-identical.
func TestHandleSetupArity(t *testing.T) {
	h := &Handler{deviceNumber: 17}

	// Legacy: 1-arg request.
	legacy := h.handleSetup(&proto.DBMessage{
		TxID: 0xfffffffe, Type: proto.DBMsgSetup,
		Args: []proto.DBArg{proto.ArgI32(1)},
	})
	if len(legacy) != 1 {
		t.Fatalf("legacy: got %d replies, want 1", len(legacy))
	}
	r := legacy[0]
	if r.Type != proto.DBMsgSuccess {
		t.Errorf("legacy reply kind = %#x, want %#x (0x4000)", r.Type, proto.DBMsgSuccess)
	}
	if len(r.Args) != 2 || r.Args[0].Int() != 0 || r.Args[1].Int() != 17 {
		t.Errorf("legacy reply args = %v, want [0, 17]", argInts(r.Args))
	}
	if r.TxID != 0xfffffffe {
		t.Errorf("legacy reply txid = %#x, want echoed 0xfffffffe", r.TxID)
	}

	// Extended: 2-arg request — reply echoes the request kind and 2nd arg.
	ext := h.handleSetup(&proto.DBMessage{
		TxID: 0xfffffffe, Type: proto.DBMsgSetup,
		Args: []proto.DBArg{proto.ArgI32(1), proto.ArgI32(20)},
	})
	re := ext[0]
	if re.Type != proto.DBMsgSetup {
		t.Errorf("extended reply kind = %#x, want %#x (0x0000)", re.Type, proto.DBMsgSetup)
	}
	if len(re.Args) != 2 || re.Args[0].Int() != 17 || re.Args[1].Int() != 20 {
		t.Errorf("extended reply args = %v, want [17, 20]", argInts(re.Args))
	}
}

func argInts(args []proto.DBArg) []uint32 {
	out := make([]uint32, len(args))
	for i, a := range args {
		out[i] = a.Int()
	}
	return out
}

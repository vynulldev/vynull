// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/hex"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// TestPayloadUnavailableReplies pins the replies for artwork and analysis
// requests about a track Vynull has no data for, byte for byte against
// rekordbox 7.2.19 (link-spec goldens xdj-rx3/adjacent-payload-fileless,
// track 10001 of a fixture without files).
func TestPayloadUnavailableReplies(t *testing.T) {
	h := &Handler{}
	nums := func(vs ...uint32) []proto.DBArg {
		args := make([]proto.DBArg, len(vs))
		for i, v := range vs {
			args[i] = proto.ArgI32(v)
		}
		return args
	}
	for _, c := range []struct {
		name string
		kind uint16
		args []proto.DBArg
		want string // "" = no reply
	}{
		{"wave preview", 0x2004, nums(0x01080301, 4, 10001, 0),
			"11872349ae11000000011044020f04140000000406060603110000200411000000321100000000"},
		{"legacy cues", 0x2104, nums(0x01080301, 10001),
			"11872349ae11000000011047020f0914000000090606060306060606031100002104110000000111000000001100000024110000000011000000001100000000"},
		{"beat grid", 0x2204, nums(0x01080301, 10001),
			"11872349ae11000000011046020f05140000000506060603061100002204110000003211000000001100000000"},
		{"log-only 0x2304", 0x2304, nums(0x01080301),
			"11872349ae11000000011040030f011400000001061100002304"},
		{"log-only 0x2404, track type 0", 0x2404, nums(0x01080300),
			"11872349ae11000000011040030f011400000001061100002404"},
		{"log-only 0x2604", 0x2604, nums(0x01080306),
			"11872349ae11000000011040030f011400000001061100002604"},
		{"log-only 0x2704", 0x2704, nums(0x01080301),
			"11872349ae11000000011040030f011400000001061100002704"},
		{"log-only, zero context", 0x2304, nums(0), ""},
		{"log-only, all-ones context", 0x2704, nums(0xffffffff), ""},
		{"VBR", 0x2504, nums(0x01080301, 10001),
			"11872349ae11000000011045020f04140000000406060603110000250411000000001100000000"},
		{"wave detail", 0x2904, nums(0x01010301, 10001, 0),
			"11872349ae1100000001104a020f04140000000406060603110000290411000000321100000000"},
		{"segmented key", 0x2a04, nums(0x01010301, 10001),
			"11872349ae1100000001104c020f041400000004060606031100002a0411000000321100000000"},
	} {
		resp := h.Handle(&proto.DBMessage{TxID: 1, Type: c.kind, Args: c.args})
		got := ""
		for _, r := range resp {
			got += hex.EncodeToString(proto.MarshalDBMessage(r))
		}
		if got != c.want {
			t.Errorf("%s (0x%04x):\n got %q\nwant %q", c.name, c.kind, got, c.want)
		}
	}
}

// TestLogOnlyCommandArguments pins rekordbox's parser policy for the four
// log-only commands (link-spec adjacent-payload-malformed): no arguments get
// no reply, a non-numeric context an empty 0x0100, and trailing arguments
// after a numeric context are ignored.
func TestLogOnlyCommandArguments(t *testing.T) {
	h := &Handler{}
	for _, c := range []struct {
		name string
		args []proto.DBArg
		want uint16 // 0 = no reply
	}{
		{"no arguments", nil, 0},
		{"string context", []proto.DBArg{proto.ArgStr("x")}, 0x0100},
		{"blob context", []proto.DBArg{proto.ArgBlob([]byte{1, 8, 3, 1})}, 0x0100},
		{"extra number", []proto.DBArg{proto.ArgI32(0x01080301), proto.ArgI32(7)}, proto.DBMsgMenuError},
		{"extra string", []proto.DBArg{proto.ArgI32(0x01080301), proto.ArgStr("x")}, proto.DBMsgMenuError},
	} {
		resp := h.Handle(&proto.DBMessage{TxID: 3, Type: 0x2604, Args: c.args})
		var got uint16
		if len(resp) == 1 {
			got = resp[0].Type
			if got == 0x0100 && len(resp[0].Args) != 0 {
				t.Errorf("%s: 0x0100 with arguments %+v, want none", c.name, resp[0].Args)
			}
		} else if len(resp) > 1 {
			t.Fatalf("%s: %d replies", c.name, len(resp))
		}
		if got != c.want {
			t.Errorf("%s: reply 0x%04x, want 0x%04x", c.name, got, c.want)
		}
	}
}

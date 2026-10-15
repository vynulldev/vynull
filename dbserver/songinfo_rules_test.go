// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/proto"
)

func TestSongInfoContextRule(t *testing.T) {
	const (
		served = iota
		menuError
		silent
	)
	for _, c := range []struct {
		kind uint16
		ctx  uint32
		want int
	}{
		{0x2002, 0x01010301, served},
		{0x2002, 0x01010302, menuError},
		{0x2002, 0x01010300, menuError},
		{0x2002, 0, served}, // no context: served
		{0x2102, 0x01010301, served},
		{0x2102, 0x01010306, menuError},
		{0x2602, 0x01010301, served},
		{0x2602, 0x01010305, menuError},
		{0x2202, 0x01010301, menuError},
		{0x2302, 0x01010302, menuError},
		{0x2402, 0x01010301, menuError},
		{0x2502, 0x01010301, menuError},
		{0x2003, 0x01010301, served},
		{0x2003, 0x01010304, silent},
		{0x2103, 0x01010300, silent},
		{0x2004, 0x01010302, served}, // analysis reads answer for every type
		{0x1004, 0x01010302, served}, // browse requests are not song-info
	} {
		msg := &proto.DBMessage{TxID: 7, Type: c.kind, Args: []proto.DBArg{proto.ArgI32(c.ctx), proto.ArgI32(1)}}
		resp, handled := songInfoContextRule(msg)
		got := served
		if handled {
			got = silent
			if len(resp) == 1 {
				got = menuError
				if resp[0].Type != proto.DBMsgMenuError || resp[0].TxID != 7 || resp[0].Args[0].Int() != uint32(c.kind) {
					t.Errorf("0x%04x ctx %#x: error reply %+v, want 0x4003 echoing the kind", c.kind, c.ctx, resp[0])
				}
			}
		}
		if got != c.want {
			t.Errorf("0x%04x ctx %#x: outcome %d, want %d", c.kind, c.ctx, got, c.want)
		}
	}
}

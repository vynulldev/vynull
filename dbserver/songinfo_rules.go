// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import "github.com/vynulldev/vynull/proto"

// songInfoContextRule applies rekordbox's handling of song-information
// requests by the track-type byte of their context argument (link-spec
// conformance suites context-class2-status-*, song-info-siblings, rekordbox
// 7.2.19). It returns handled=false when the request should be served
// normally.
//
//   - 0x2202, 0x2302, 0x2402, 0x2502: always a menu-unavailable error.
//   - 0x2002, 0x2102, 0x2602: unless the context track type is 0x01, a
//     normal header whose total is 0xffffffff, with no rows (every link-spec
//     golden for these kinds; a menu-unavailable error was wrong here).
//   - 0x2003, 0x2103: no reply at all unless the context track type is 0x01.
//
// A context of 0 (no context) is exempt: rekordbox serves those normally.
// Decks browsing Vynull's library send track type 0x01, so they are served
// as before.
func songInfoContextRule(msg *proto.DBMessage) ([]*proto.DBMessage, bool) {
	if len(msg.Args) == 0 || msg.Args[0].Int() == 0 {
		return nil, false
	}
	rekordboxType := msg.Args[0].Int()&0xff == 0x01
	menuError := []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgMenuError,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type))},
	}}
	switch msg.Type {
	case 0x2202, 0x2302, 0x2402, 0x2502:
		return menuError, true
	case 0x2002, 0x2102, 0x2602:
		if !rekordboxType {
			return []*proto.DBMessage{{
				TxID: msg.TxID,
				Type: proto.DBMsgSuccess,
				Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0xffffffff)},
			}}, true
		}
	case 0x2003, 0x2103:
		if !rekordboxType {
			return nil, true
		}
	}
	return nil, false
}

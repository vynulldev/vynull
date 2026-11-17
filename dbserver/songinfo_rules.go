// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/proto"
)

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

// malformedSongInfo applies rekordbox's answers to a Play Song Info (0x2102)
// or Delivery Info (0x2602) request whose arguments aren't a numeric context
// and content ID (link-spec SONG_INFO_SIBLINGS_ORACLE.md, "Malformed
// arguments and parser state"): a request with no arguments gets no reply,
// and a string or blob context, or a blob content ID, a bare 0x0100. A string
// content ID names no track, so the normal path's zero-row menu already
// matches rekordbox. (A blob content ID sent as the last argument never gets
// here: readMessage doesn't wait for a trailing blob, which decks advertise
// without sending.) It returns handled=false for every other request.
func malformedSongInfo(msg *proto.DBMessage) ([]*proto.DBMessage, bool) {
	if msg.Type != 0x2102 && msg.Type != 0x2602 {
		return nil, false
	}
	if len(msg.Args) == 0 {
		dlog.Debugf("dbserver: 0x%04x with no arguments goes unanswered, as rekordbox does", msg.Type)
		return nil, true
	}
	if !numberArg(msg.Args[0]) || len(msg.Args) > 1 && msg.Args[1].Tag == proto.ArgBinary {
		dlog.Debugf("dbserver: 0x%04x with a non-numeric argument gets a bare 0x0100, as rekordbox does", msg.Type)
		return []*proto.DBMessage{{TxID: msg.TxID, Type: 0x0100}}, true
	}
	return nil, false
}

// numberArg reports whether a request argument is a number.
func numberArg(a proto.DBArg) bool {
	switch a.Tag {
	case proto.ArgInt8, proto.ArgInt16, proto.ArgInt32:
		return true
	}
	return false
}

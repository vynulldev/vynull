// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import "github.com/vynulldev/vynull/proto"

// rekordbox 7.2.19's replies when an artwork or analysis request (0x2003,
// 0x2004..0x2d04) has nothing to send: no track, no file, an empty or null
// path, or no analysis (link-spec docs/ADJACENT_PAYLOAD_SERVICES.md, suites
// adjacent-payload-fileless and adjacent-payload-missing-files).

// payloadStatusUnavailable is the status rekordbox puts in a payload reply
// whose data it doesn't have.
const payloadStatusUnavailable = 0x32

// payloadUnavailable is rekordbox's empty payload reply: the request's kind,
// a status, a byte count of zero, and a declared blob that is left out, as
// rekordbox does whenever a reply's count is zero.
func payloadUnavailable(msg *proto.DBMessage, replyType uint16, status uint32) []*proto.DBMessage {
	return []*proto.DBMessage{{
		TxID:             msg.TxID,
		Type:             replyType,
		DeclaredArgCount: 4,
		ExtraTags:        []byte{0x03},
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(status),
			proto.ArgI32(0),
		},
	}}
}

// beatGridUnavailable is 0x2204's empty reply: like payloadUnavailable, plus
// the trailing number its replies carry after the blob.
func beatGridUnavailable(msg *proto.DBMessage) []*proto.DBMessage {
	return []*proto.DBMessage{{
		TxID:             msg.TxID,
		Type:             0x4602,
		DeclaredArgCount: 5,
		OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06},
		Args: []proto.DBArg{
			proto.ArgI32(0x2204),
			proto.ArgI32(payloadStatusUnavailable),
			proto.ArgI32(0),
			proto.ArgI32(0),
		},
	}}
}

// emptyLegacyCues is 0x2104's reply for a track without cues: status 1, no
// records, the 36-byte record width, and both counts zero with their blobs
// left out.
func emptyLegacyCues(msg *proto.DBMessage) []*proto.DBMessage {
	return []*proto.DBMessage{{
		TxID:             msg.TxID,
		Type:             0x4702,
		DeclaredArgCount: 9,
		OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06, 0x06, 0x06, 0x06, 0x03},
		Args: []proto.DBArg{
			proto.ArgI32(0x2104),
			proto.ArgI32(1),
			proto.ArgI32(0),
			proto.ArgI32(36),
			proto.ArgI32(0),
			proto.ArgI32(0),
			proto.ArgI32(0),
		},
	}}
}

// logOnlyCommand answers 0x2304, 0x2404, 0x2604 and 0x2704, which rekordbox
// only logs. A numeric context gets a menu-unavailable error echoing the
// kind (trailing arguments are ignored); a context of all zeros or all ones,
// or no arguments, gets no reply; any other first argument gets an empty
// 0x0100.
func logOnlyCommand(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) == 0 {
		return nil
	}
	switch msg.Args[0].Tag {
	case proto.ArgInt8, proto.ArgInt16, proto.ArgInt32:
	default:
		return []*proto.DBMessage{{TxID: msg.TxID, Type: 0x0100}}
	}
	if ctx := msg.Args[0].Int(); ctx == 0 || ctx == 0xffffffff {
		return nil
	}
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgMenuError,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type))},
	}}
}

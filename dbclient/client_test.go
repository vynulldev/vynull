// SPDX-License-Identifier: GPL-3.0-or-later

package dbclient

import (
	"net"
	"testing"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// metaItem builds a 0x4101 detail row in the same 12-arg layout package
// dbserver emits (parent, main, label1Len, label1, label2Len, label2,
// itemType, flags…), so parsing it exercises the real wire format.
func metaItem(itemType uint32, label string, artID uint32) *proto.DBMessage {
	return &proto.DBMessage{Type: proto.DBMsgMenuItem, Args: []proto.DBArg{
		proto.ArgI32(0),
		proto.ArgI32(1),
		proto.ArgI32(uint32((len([]rune(label)) + 1) * 2)),
		proto.ArgStr(label),
		proto.ArgI32(2),
		proto.ArgStr(""),
		proto.ArgI32(itemType),
		proto.ArgI32(0), proto.ArgI32(artID), proto.ArgI32(0), proto.ArgI32(0), proto.ArgI32(0),
	}}
}

func TestParseMetaItem(t *testing.T) {
	m := &Metadata{}
	parseMetaItem(metaItem(0x0004, "Strobe", 77), m) // title
	parseMetaItem(metaItem(0x0704, "deadmau5", 0), m)
	parseMetaItem(metaItem(0x0f04, "9A", 0), m)
	parseMetaItem(metaItem(0x0204, "For Lack of a Better Name", 0), m)
	if m.Title != "Strobe" || m.Artist != "deadmau5" || m.Key != "9A" || m.Album != "For Lack of a Better Name" {
		t.Errorf("parsed = %+v", m)
	}
	if m.ArtworkID != 77 {
		t.Errorf("artwork id = %d, want 77", m.ArtworkID)
	}
}

// TestFetchMetadataRoundTrip drives the full request/response flow over an
// in-memory pipe, with a fake dbserver that marshals responses with the real
// proto codec — validating decodeMessage against MarshalDBMessage end to end.
func TestFetchMetadataRoundTrip(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	client := &Client{conn: cli, playerNum: 5, txID: 1, timeout: 2 * time.Second}

	go func() {
		w := func(m *proto.DBMessage) { srv.Write(proto.MarshalDBMessage(m)) }
		req, err := decodeMessage(srv) // 0x2002 metadata request
		if err != nil {
			return
		}
		w(&proto.DBMessage{TxID: req.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(0x2002), proto.ArgI32(3)}}) // count = 3
		decodeMessage(srv) // 0x3000 render request
		w(&proto.DBMessage{Type: proto.DBMsgMenuHeader, Args: []proto.DBArg{proto.ArgI32(1), proto.ArgI32(0)}})
		w(metaItem(0x0004, "Strobe", 0))
		w(metaItem(0x0704, "deadmau5", 0))
		w(metaItem(0x0f04, "9A", 0))
		w(&proto.DBMessage{Type: proto.DBMsgMenuFooter})
	}()

	m, err := client.FetchMetadata(SlotUSB, 42)
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if m.Title != "Strobe" || m.Artist != "deadmau5" || m.Key != "9A" {
		t.Errorf("fetched = %+v", m)
	}
}

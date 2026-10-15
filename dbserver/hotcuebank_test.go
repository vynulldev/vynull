// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/hex"
	"fmt"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/library"
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

// hotCueBankFixture mirrors link-spec's hot-cue-banks fixture: Alpha Bank
// (9012) holds tracks 10008, 10001, 10006 at slots 1-3 with NULL cue fields
// other than InMsec, which rekordbox reads as 0.
func hotCueBankFixture() *Handler {
	lib := library.New()
	for _, id := range []uint32{10001, 10002, 10003, 10004, 10006, 10008} {
		lib.AddTrack(&library.Track{ID: id, Title: fmt.Sprintf("Track %d", id)})
	}
	lib.SetHotCueBanks([]library.HotCueBank{
		{ID: 9002, Seq: 1, Name: "Beta Folder", Folder: true},
		{ID: 9001, Seq: 2, Name: "Alpha Folder", Folder: true},
		{ID: 9003, Seq: 3, Name: "Root Bank"},
		{ID: 9031, ParentID: 9002, Seq: 1, Name: "Deleted Bank", Deleted: true},
		{ID: 9032, ParentID: 9002, Seq: 2, Name: "Beta Bank"},
		{ID: 9012, ParentID: 9001, Seq: 1, Name: "Alpha Bank"},
		{ID: 9021, ParentID: 9011, Seq: 1, Name: "Deep Bank"},
	}, []library.HotCueBankMember{
		{BankID: 9012, TrackID: 10008, ContentID: 10008, TrackNo: 1, InMsec: 91002},
		{BankID: 9012, TrackID: 10001, ContentID: 10001, TrackNo: 2, InMsec: 91003},
		{BankID: 9012, TrackID: 10006, ContentID: 10006, TrackNo: 3, InMsec: 91004},
		{BankID: 9012, TrackID: 10003, ContentID: 10003, TrackNo: 4, InMsec: 91005},
		{BankID: 9012, ContentID: 999999, TrackNo: 5},                               // dangling content
		{BankID: 9012, TrackID: 10001, ContentID: 10001, TrackNo: 6},                // duplicate
		{BankID: 9012, TrackID: 10002, ContentID: 10002, TrackNo: 7, Deleted: true}, // deleted membership
		{BankID: 9021, TrackID: 10003, ContentID: 10003, TrackNo: 0},
		{BankID: 9021, TrackID: 10004, ContentID: 10004, TrackNo: -1},
	})
	return &Handler{lib: lib}
}

// A populated bank's cue records must match rekordbox's byte for byte
// (golden context-hot-cue-getter-status-extended, type-01 populated).
func TestHotCueBankPopulatedCues(t *testing.T) {
	h := hotCueBankFixture()
	for _, tc := range []struct {
		name string
		kind uint16
		args []proto.DBArg
		want string
	}{
		{"legacy", 0x2101, []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(9012)},
			"11872349ae11000000011047020f09140000000906060603060606060311000021011100000000110000006c140000006c010104001827000000000000523500000000000000000000000000000000000000000000010105001127000000000000523500000000000000000000000000000000000000000000010106001627000000000000523500000000000000000000000000000000000000000000110000002411000000031100000000110000001814000000187a630100000000007b630100000000007c63010000000000"},
		{"extended", 0x2301, []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(9012), proto.ArgI32(3)},
			"11872349ae1100000001104e020f051400000005060606030611000023011100000000110000017414000001747c000000010002000000e8037a63010000000000000000000800000000000000ffffffff00000000000000000000000000000000420000000000010000000000000000000000000000002c000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000007c000000020002000000e8037b63010000000000000000000800000000000000ffffffff00000000000000000000000000000000420000000000010000000000000000000000000000002c000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000007c000000030002000000e8037c63010000000000000000000800000000000000ffffffff00000000000000000000000000000000420000000000010000000000000000000000000000002c000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001100000003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.Handle(&proto.DBMessage{TxID: 1, Type: tc.kind, Args: tc.args})
			if len(resp) != 1 {
				t.Fatalf("%d replies, want 1", len(resp))
			}
			if got := hex.EncodeToString(proto.MarshalDBMessage(resp[0])); got != tc.want {
				t.Errorf("reply\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The catalog lists the live tree in Seq order and a bank's tracks in
// membership order, following the oracle's tree and track-mode rules.
func TestHotCueBankCatalog(t *testing.T) {
	h := hotCueBankFixture()
	ids := func(items []*menuItem) (out []uint32) {
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out
	}
	positions := func(items []*menuItem) (out []uint32) {
		for _, it := range items {
			out = append(out, it.trackPos)
		}
		return out
	}

	root := h.hotCueBankTreeItems(0)
	if got := ids(root); !slices.Equal(got, []uint32{9002, 9001, 9003}) {
		t.Errorf("root = %v, want Beta Folder, Alpha Folder, Root Bank", got)
	}
	if root[0].ItemType != 0x01 || root[2].ItemType != 0x2b || root[2].trackPos != 3 {
		t.Errorf("root rows: folder type %#x, bank type %#x, bank seq %d", root[0].ItemType, root[2].ItemType, root[2].trackPos)
	}
	if got := ids(h.hotCueBankTreeItems(9002)); !slices.Equal(got, []uint32{9032}) {
		t.Errorf("Beta Folder = %v, want only the live Beta Bank", got)
	}

	// Alpha Bank: dangling content drops out, the duplicate and the deleted
	// membership stay; the count is floored to 3.
	if got := ids(h.hotCueBankTrackItems(9012, 16)); !slices.Equal(got, []uint32{10008, 10001, 10006, 10003, 10001, 10002}) {
		t.Errorf("Alpha Bank tracks = %v", got)
	}
	if got := len(h.hotCueBankTrackItems(9012, 0)); got != 3 {
		t.Errorf("count 0 returned %d rows, want 3", got)
	}
	if got := len(h.hotCueBankTrackItems(9012, 0x80000000)); got != 0 {
		t.Errorf("count 0x80000000 returned %d rows, want 0", got)
	}
	// Deep Bank: TrackNo -1 sorts first and renders as 65535.
	if got := positions(h.hotCueBankTrackItems(9021, 8)); !slices.Equal(got, []uint32{65535, 0}) {
		t.Errorf("Deep Bank positions = %v, want [65535 0]", got)
	}

	// The getters serve a bank's live slots: Alpha Bank has slots 1-3.
	if r := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2101, Args: []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(9012)}}); r[0].Args[5].Int() != 3 {
		t.Errorf("Alpha Bank legacy cue count = %d, want 3", r[0].Args[5].Int())
	}
}

// A bank pad whose track file is missing here (rekordbox's sampler sounds
// from a Windows library, say) is left out of the track list and the cue
// records: decks broke trying to load it.
func TestHotCueBankSkipsMissingFiles(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Here"})
	lib.AddTrack(&library.Track{ID: 2, Title: "HORN", FileMissing: true})
	lib.AddTrack(&library.Track{ID: 3, Title: "Also here"})
	lib.SetHotCueBanks([]library.HotCueBank{{ID: 50, Name: "Bank"}}, []library.HotCueBankMember{
		{BankID: 50, TrackID: 1, TrackNo: 1, InMsec: 100, OutMsec: -1},
		{BankID: 50, TrackID: 2, TrackNo: 2, InMsec: 200, OutMsec: -1},
		{BankID: 50, TrackID: 3, TrackNo: 3, InMsec: 300, OutMsec: -1},
	})
	h := &Handler{lib: lib}

	var ids []uint32
	for _, it := range h.hotCueBankTrackItems(50, 8) {
		ids = append(ids, it.ID)
	}
	if !slices.Equal(ids, []uint32{1, 3}) {
		t.Errorf("track list = %v, want [1 3]", ids)
	}

	legacy := h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2101, Args: []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(50)}})[0]
	if n := legacy.Args[5].Int(); n != 2 {
		t.Fatalf("legacy record count = %d, want 2", n)
	}
	if rec := legacy.Args[3].Bytes; rec[2] != 4 || rec[36+2] != 6 {
		t.Errorf("legacy cue numbers = %d, %d, want slots 1 and 3 (4, 6)", rec[2], rec[36+2])
	}
	ext := h.Handle(&proto.DBMessage{TxID: 2, Type: 0x2301, Args: []proto.DBArg{proto.ArgI32(0x01010301), proto.ArgI32(50), proto.ArgI32(8)}})[0]
	if n := ext.Args[4].Int(); n != 2 {
		t.Errorf("extended record count = %d, want 2", n)
	}
}

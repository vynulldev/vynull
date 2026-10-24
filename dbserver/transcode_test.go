// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// With --transcode the NFS server serves a FLAC as an MP3, so the deck is
// told it is one: decoder 1 in the track info, an MP3 row without the
// new-CDJ incompatible bit, and no FLAC seek index. On a CDJ-2000NXS2, told
// FLAC, the deck read the MP3 with FLAC-sized reads and gave up (play-state
// 0x0e).
func TestTranscodedTrackIsAnMP3(t *testing.T) {
	lib := library.NewLibrary([]*library.Track{
		{ID: 1, FilePath: "/music/a.flac", FileType: "flac", FileTypeID: 5},
		{ID: 2, FilePath: "/music/b.mp3", FileType: "mp3", FileTypeID: 1},
	}, nil)
	store := analysis.NewStore()
	store.Set(1, &analysis.Result{BPM: 120})
	pvb2 := uint32('2')<<24 | uint32('B')<<16 | uint32('V')<<8 | uint32('P') // "PVB2" reversed

	for _, tc := range []struct {
		transcode bool
		decoder   uint32
		fileType  string
		pvb2      bool
	}{{false, 5, "flac", true}, {true, 1, "mp3", false}} {
		h := &Handler{lib: lib, analysis: store, transcode: tc.transcode}
		h.Handle(&proto.DBMessage{TxID: 1, Type: 0x2102, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(1)}})
		if title := h.pendingItems[0]; title.ID != tc.decoder || title.FileType != tc.fileType {
			t.Errorf("transcode %v: track info decoder %d (%s), want %d (%s)", tc.transcode, title.ID, title.FileType, tc.decoder, tc.fileType)
		}
		row := h.trackToStdItem(lib.Track(1))
		if row.FileType != tc.fileType || newCDJIncompatible(row.fileTypeID, row.FileType, row.sampleRate) != tc.pvb2 {
			t.Errorf("transcode %v: row %s, id %d", tc.transcode, row.FileType, row.fileTypeID)
		}
		reply := h.handleGetExtAnalysis(&proto.DBMessage{TxID: 2, Type: 0x2c04,
			Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(1), proto.ArgI32(pvb2), proto.ArgI32(0x00545845)}})
		if got := reply[0].Args[1].Int() == 0; got != tc.pvb2 {
			t.Errorf("transcode %v: PVB2 served %v, want %v", tc.transcode, got, tc.pvb2)
		}
		// An MP3 is served as itself either way.
		if row := h.trackToStdItem(lib.Track(2)); row.FileType != "mp3" || row.fileTypeID != 1 {
			t.Errorf("transcode %v: MP3 row %s, id %d", tc.transcode, row.FileType, row.fileTypeID)
		}
	}
}

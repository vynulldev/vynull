// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

// A served USB export is served from the library FromPDB builds: the deck
// gets the export's own ANLZ sections and category IDs, and the label's name.
func TestServedExportTrack(t *testing.T) {
	root := t.TempDir()
	onDisk := &analysis.Result{WaveColorPreview: bytes.Repeat([]byte{1, 2, 3, 4, 5, 6}, 1200)}
	anlz, err := analysis.WriteANLZFiles(root, 7, "/Contents/t.mp3", onDisk)
	if err != nil {
		t.Fatal(err)
	}
	lib := library.FromPDB([]*pdb.Track{{
		ID: 7, Title: "T", Artist: "A", Album: "B", Genre: "G", Label: "L", Key: "8A", Tempo: 12800, Duration: 300,
		ArtistID: 11, AlbumID: 12, GenreID: 13, LabelID: 14, KeyID: 15,
		FilePath: "/Contents/t.mp3", AnalyzePath: anlz,
	}}, root)
	store := analysis.NewStore()
	store.Set(7, &analysis.Result{BPM: 128, WaveColorPreview: make([]byte, 7200)})
	h := &Handler{lib: lib, exportRoot: root, analysis: store}

	pwv4 := uint32('4')<<24 | uint32('V')<<16 | uint32('W')<<8 | uint32('P') // "PWV4" reversed
	reply := h.handleGetExtAnalysis(&proto.DBMessage{TxID: 1, Type: 0x2c04,
		Args: []proto.DBArg{proto.ArgI32(0x01080401), proto.ArgI32(7), proto.ArgI32(pwv4), proto.ArgI32(0x00545845)}})
	ext := filepath.Join(root, strings.Replace(anlz, ".DAT", ".EXT", 1))
	if want := analysis.ReadANLZSection(ext, "PWV4"); len(want) == 0 || !bytes.Equal(reply[0].Args[3].Bytes, want) {
		t.Errorf("PWV4 is not the export's (%d bytes, want %d)", len(reply[0].Args[3].Bytes), len(want))
	}

	h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(7)}})
	rows := h.pendingItems
	if len(rows) != 16 {
		t.Fatalf("%d metadata rows, want 16", len(rows))
	}
	for i, want := range map[int]struct {
		id    uint32
		label string
	}{1: {11, "A"}, 2: {12, "B"}, 5: {15, "8A"}, 8: {13, "G"}, 13: {14, "L"}} {
		if rows[i].ID != want.id || rows[i].Label1 != want.label {
			t.Errorf("row %d = %d %q, want %d %q", i, rows[i].ID, rows[i].Label1, want.id, want.label)
		}
	}
}

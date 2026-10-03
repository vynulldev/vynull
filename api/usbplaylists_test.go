// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"

	"github.com/vynulldev/vynull/pdb"
)

// TestExportPlaylistSourceUSB pins export resolution for a served
// rekordbox USB's playlists: the namespaced ID resolves to the stick's
// playlist name and track IDs (read-only applies to mutation, not
// export), and unknown IDs fail cleanly.
func TestExportPlaylistSourceUSB(t *testing.T) {
	db := &pdb.Database{}
	db.AddTrack(&pdb.Track{ID: 11, Title: "Alpha"})
	db.AddTrack(&pdb.Track{ID: 12, Title: "Beta"})
	db.PlaylistTree = []*pdb.FolderNode{
		{ID: 101, ParentID: 0, Name: "Warmup", TrackIDs: []uint32{11, 12}},
	}
	s := &Server{PDB: db}

	name, ids, ok := s.exportPlaylistSource(usbPlaylistIDBit | 101)
	if !ok || name != "Warmup" || len(ids) != 2 || ids[0] != 11 {
		t.Fatalf("usb playlist resolution = %q %v %v", name, ids, ok)
	}
	if _, _, ok := s.exportPlaylistSource(usbPlaylistIDBit | 999); ok {
		t.Fatal("unknown usb playlist id resolved")
	}
	if _, _, ok := s.exportPlaylistSource(5); ok {
		t.Fatal("store id resolved with nil store")
	}
}

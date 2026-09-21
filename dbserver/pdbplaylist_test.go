// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

// pdbWithPlaylists builds a minimal served-USB database: two tracks, one
// playlist folder at root containing one playlist with both tracks.
func pdbWithPlaylists() *pdb.Database {
	db := &pdb.Database{}
	db.AddTrack(&pdb.Track{ID: 11, Title: "Alpha"})
	db.AddTrack(&pdb.Track{ID: 12, Title: "Beta"})
	db.PlaylistTree = []*pdb.FolderNode{
		{ID: 100, ParentID: 0, Name: "Crates", IsFolder: true},
		{ID: 101, ParentID: 100, Name: "Warmup", IsFolder: false, TrackIDs: []uint32{11, 12}},
	}
	return db
}

// TestPDBPlaylistsServed pins the served-rekordbox-USB playlist path: a PDB
// with a playlist tree and no user-defined store playlists must serve the
// PDB's tree on the PLAYLIST menu (the tree used to be parsed and then
// ignored — decks browsing a served USB saw an empty PLAYLIST menu).
func TestPDBPlaylistsServed(t *testing.T) {
	h := &Handler{pdb: pdbWithPlaylists()}

	// Root listing (0x1005, 2 args): the folder must appear.
	h.handleGetPlaylist(&proto.DBMessage{Type: 0x1005, Args: []proto.DBArg{proto.ArgI32(0), proto.ArgI32(0)}})
	if len(h.pendingItems) != 1 || h.pendingItems[0].Label1 != "Crates" || h.pendingItems[0].ItemType != 0x0001 {
		t.Fatalf("root listing = %+v, want the Crates folder", h.pendingItems)
	}

	// Drill into the folder (0x1105, type=1): the playlist must appear.
	h.handleGetPlaylist(&proto.DBMessage{Type: 0x1105, Args: []proto.DBArg{
		proto.ArgI32(0), proto.ArgI32(0), proto.ArgI32(100), proto.ArgI32(1)}})
	if len(h.pendingItems) != 1 || h.pendingItems[0].Label1 != "Warmup" || h.pendingItems[0].ItemType != 0x0008 {
		t.Fatalf("folder listing = %+v, want the Warmup playlist", h.pendingItems)
	}

	// Drill into the playlist (type=0): its tracks must appear.
	h.handleGetPlaylist(&proto.DBMessage{Type: 0x1105, Args: []proto.DBArg{
		proto.ArgI32(0), proto.ArgI32(0), proto.ArgI32(101), proto.ArgI32(0)}})
	if len(h.pendingItems) != 2 {
		t.Fatalf("playlist tracks = %+v, want 2 tracks", h.pendingItems)
	}
}

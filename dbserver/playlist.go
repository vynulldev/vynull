// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"log"
	"path/filepath"

	"vynull/pdb"
	"vynull/proto"
)

// playlist.go contains the playlist / folder / history menu handlers:
// 0x1105 PLAYLIST drill, 0x2006 FOLDER (filesystem mirror), and 0x1016
// HISTORY (auto-generated daily session folders), plus the
// trackIDsToMenuItems shared converter.

func (h *Handler) handleGetHistory(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver: HISTORY list (0x1016)")
	var items []*menuItem
	if h.playlists != nil {
		folderID := h.playlists.HistoryFolderID()
		if folderID != 0 {
			for _, c := range h.playlists.Children(folderID) {
				items = append(items, &menuItem{
					ID:       c.ID,
					Label1:   c.Name,
					ItemType: 0x0008, // playlist row
				})
			}
		}
	}
	h.setPendingAll(msg, items)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(len(items)))},
	}}
}

// handleGetHistoryTracks handles opcode 0x1116 — the deck drilling
// into a specific history session (daily playlist). arg[2] is the
// playlist ID we returned in handleGetHistory.
func (h *Handler) handleGetHistoryTracks(msg *proto.DBMessage) []*proto.DBMessage {
	playlistID := uint32(0)
	if len(msg.Args) >= 3 {
		playlistID = msg.Args[2].Int()
	}
	log.Printf("dbserver: HISTORY drill (0x1116) playlist=%d", playlistID)

	var items []*menuItem
	if h.playlists != nil && playlistID != 0 {
		trackIDs := h.playlists.Tracks(playlistID)
		items = h.trackIDsToMenuItems(trackIDs)
	}
	h.setPendingAll(msg, items)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(len(items)))},
	}}
}

// handleSearchSelect handles opcode 0x1200, the drill-in the deck
// sends when the user picks a row from the SEARCH-result list. The
// selected item's arg1 (a library track ID, in our case) arrives as
// arg[2] of the request. We resolve it back to the actual track and
// stash a 1-item pending list — the deck's follow-up 0x3000 render
// then displays the track and lets the user press LOAD normally.
//
// Without this handler the request hits the unhandled-opcode fallback
// (returns Success with 0 items), the deck has nothing to render, and
// the LOAD eventually falls back to whatever track was last loaded —
// which looked like "always loads track 1" from the user's POV.
func (h *Handler) handleGetPlaylist(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1005: PLAYLIST root request — [DMST, sort]. CDJ sends this when
	// the user first opens LINK → PLAYLIST. Treated as "list playlists
	// + folders at root (folderID=0, type=folder)".
	// 0x1105: PLAYLIST drill-down — [DMST, sort, folderID, type(0=playlist,1=folder)].
	var folderID uint32
	isFolder := uint32(1) // default: list folder/playlist tree
	if len(msg.Args) >= 3 {
		folderID = msg.Args[2].Int()
	}
	if len(msg.Args) >= 4 {
		isFolder = msg.Args[3].Int()
	}

	// User-defined playlists take precedence — when configured, the
	// PLAYLIST menu on the CDJ shows the user's tree (matching real
	// rekordbox) instead of mirroring the on-disk folder structure.
	// Filesystem folders stay available via the separate FOLDER menu
	// (handleGetFolder / 0x2006).
	if h.playlists != nil {
		if isFolder == 1 {
			children := h.playlists.Children(folderID)
			items := make([]*menuItem, 0, len(children))
			for _, c := range children {
				itemType := uint32(0x0008) // playlist
				if c.IsFolder {
					itemType = 0x0001 // folder
				}
				items = append(items, &menuItem{
					ID:       c.ID,
					Label1:   c.Name,
					ItemType: itemType,
				})
			}
			h.pendingItems = items
			log.Printf("dbserver: user-playlist folder %d returning %d items", folderID, len(items))
		} else {
			trackIDs := h.playlists.Tracks(folderID)
			items := h.trackIDsToMenuItems(trackIDs)
			sortItems(items, getSortOrder(msg))
			h.pendingItems = items
			log.Printf("dbserver: user-playlist %d returning %d tracks", folderID, len(items))
		}
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	// No playlist source configured — fall back to the legacy filesystem
	// mirror so existing setups (no PlaylistStore wired) still get a
	// usable PLAYLIST menu.
	if h.folders == nil && h.lib != nil && h.lib.TrackCount() > 0 {
		h.rebuildFolders()
	}
	if h.folders == nil {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	if isFolder == 1 {
		children := h.folders.ListDir(folderID)
		var items []*menuItem
		for _, c := range children {
			itemType := uint32(0x0008) // playlist
			if c.IsFolder {
				itemType = 0x0001 // folder
			}
			items = append(items, &menuItem{
				ID:       c.ID,
				Label1:   c.Name,
				ItemType: itemType,
			})
		}
		h.pendingItems = items
		log.Printf("dbserver: playlist folder %d returning %d items (filesystem fallback)", folderID, len(items))
	} else {
		trackIDs := h.folders.TrackIDs(folderID)
		items := h.trackIDsToMenuItems(trackIDs)
		sortItems(items, getSortOrder(msg))
		h.pendingItems = items
		log.Printf("dbserver: playlist %d returning %d tracks (filesystem fallback)", folderID, len(items))
	}

	return []*proto.DBMessage{h.successWithCount(msg)}
}

// trackIDsToMenuItems turns an ordered list of track IDs into menu
// items, looking up metadata from whichever source is available — PDB
// preferred (gives the full pdb.Track fields), library as the fallback
// (library mode has no PDB). Without the library fallback, every
// user-playlist on a library-mode server rendered as EMPTY on the CDJ
// even when the store had the right track IDs.
func (h *Handler) trackIDsToMenuItems(trackIDs []uint32) []*menuItem {
	items := make([]*menuItem, 0, len(trackIDs))
	for _, tid := range trackIDs {
		if h.pdb != nil {
			if t := h.pdb.TrackByID(tid); t != nil {
				m := h.pdbTrackToStdItem(t)
				m.sortArtist = t.Artist
				m.sortAlbum = t.Album
				m.sortBPM = t.Tempo
				m.sortKey = t.Key
				items = append(items, m)
				continue
			}
		}
		if h.lib != nil {
			if t := h.lib.Track(tid); t != nil {
				items = append(items, h.trackToStdItem(t))
			}
		}
	}
	return items
}

func (h *Handler) handleGetFolder(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x100f: FOLDER root listing — the CDJ sends this when the user
	// first opens the FOLDER menu, with only [DMST, sort]. Folder ID is
	// implicit (root).
	// 0x2006: FOLDER drill-down by ID — args are [DMST, sort, folderID].
	// Both funnel through ListDir; folderID 0 means root.
	var folderID uint32
	if len(msg.Args) >= 3 {
		folderID = msg.Args[2].Int()
	}

	// Rebuild folder lookup from library tracks if needed.
	if h.folders == nil && h.lib != nil && h.lib.TrackCount() > 0 {
		h.rebuildFolders()
	}

	if h.folders == nil {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	children := h.folders.ListDir(folderID)
	var items []*menuItem
	for _, c := range children {
		itemType := uint32(0x0008) // playlist
		if c.IsFolder {
			itemType = 0x0001 // folder
		}
		items = append(items, &menuItem{
			ID:       c.ID,
			Label1:   c.Name,
			ItemType: itemType,
		})
	}
	h.pendingItems = items
	log.Printf("dbserver: folder %d returning %d items", folderID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// rebuildFolders creates a folder lookup from library tracks.
func (h *Handler) rebuildFolders() {
	if h.lib == nil {
		return
	}
	tracks := h.lib.Tracks()
	pdbTracks := make([]*pdb.Track, len(tracks))
	for i, t := range tracks {
		pdbTracks[i] = &pdb.Track{
			ID:       t.ID,
			Title:    t.Title,
			Artist:   t.Artist,
			FilePath: t.FilePath,
			FileName: filepath.Base(t.FilePath),
		}
	}
	h.folders = pdb.BuildFolderLookup(pdbTracks)
}


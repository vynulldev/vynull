// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"path/filepath"

	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

// playlist.go contains the playlist and folder menu handlers: 0x1105
// PLAYLIST drill and 0x2006 FOLDER (filesystem mirror), plus the
// trackIDsToMenuItems shared converter.

// storeHasUserPlaylists reports whether the local playlist store holds any
// user-defined playlist or folder at root, ignoring the auto-managed
// History folder (which exists on any server that has ever played a track).
func (h *Handler) storeHasUserPlaylists() bool {
	if h.playlists == nil {
		return false
	}
	hist := h.playlists.HistoryFolderID()
	for _, c := range h.playlists.Children(0) {
		if c.ID != hist {
			return true
		}
	}
	return false
}

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

	// A rekordbox USB served directly (a PDB with a playlist tree) carries
	// the user's real rekordbox playlists — serve those, unless the local
	// store has user-defined playlists of its own (which then keep
	// precedence, matching the store-first rule below). The auto-managed
	// History folder doesn't count as a user playlist: it has its own
	// top-level HISTORY menu, and its presence must not hide a served
	// USB's entire playlist tree.
	if h.pdb != nil && len(h.pdb.PlaylistTree) > 0 && !h.storeHasUserPlaylists() {
		var items []*menuItem
		if isFolder == 1 {
			for _, node := range h.pdb.PlaylistTree {
				if node.ParentID != folderID {
					continue
				}
				itemType := uint32(0x0008) // playlist
				if node.IsFolder {
					itemType = 0x0001 // folder
				}
				items = append(items, &menuItem{
					ID:       node.ID,
					Label1:   node.Name,
					ItemType: itemType,
				})
			}
			dlog.Debugf("dbserver: pdb playlist folder %d returning %d items", folderID, len(items))
		} else {
			for _, node := range h.pdb.PlaylistTree {
				if node.ID != folderID {
					continue
				}
				items = h.trackIDsToMenuItems(node.TrackIDs)
				break
			}
			h.applyPlaylistTrackScope(items, getSortOrder(msg), false)
			dlog.Debugf("dbserver: pdb playlist %d returning %d tracks", folderID, len(items))
		}
		h.pendingItems = items
		return []*proto.DBMessage{h.successWithCount(msg)}
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
					trackPos: c.Seq, // rekordbox serves the display sequence in arg 9
				})
			}
			h.pendingItems = items
			dlog.Debugf("dbserver: user-playlist folder %d returning %d items", folderID, len(items))
		} else {
			trackIDs := h.playlists.Tracks(folderID)
			items := h.trackIDsToMenuItems(trackIDs)
			h.applyPlaylistTrackScope(items, getSortOrder(msg), h.isSmartPlaylist(folderID))
			h.pendingItems = items
			dlog.Debugf("dbserver: user-playlist %d returning %d tracks", folderID, len(items))
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
		dlog.Debugf("dbserver: playlist folder %d returning %d items (filesystem fallback)", folderID, len(items))
	} else {
		trackIDs := h.folders.TrackIDs(folderID)
		items := h.trackIDsToMenuItems(trackIDs)
		h.sortTracks(items, getSortOrder(msg))
		h.pendingItems = items
		dlog.Debugf("dbserver: playlist %d returning %d tracks (filesystem fallback)", folderID, len(items))
	}

	return []*proto.DBMessage{h.successWithCount(msg)}
}

// trackIDsToMenuItems turns an ordered list of track IDs into menu
// items, looking up metadata from whichever source is available — PDB
// preferred (gives the full pdb.Track fields), library as the fallback
// (library mode has no PDB). Without the library fallback, every
// user-playlist on a library-mode server rendered as EMPTY on the CDJ
// even when the store had the right track IDs.
// applyPlaylistTrackScope stamps the playlist-track-leaf wire conventions on
// a playlist's track rows: a 1-based position in argument 9 (in playlist
// order) and, for a regular playlist, the packed context track type in
// argument 7, matching rekordbox. A smart playlist's rows carry no context
// type (link-spec conformance smart suites, rekordbox 7.2.19). Tracks stay
// in playlist order for the default sort (sort=0); a requested sort reorders
// them while each row keeps its original position.
func (h *Handler) applyPlaylistTrackScope(items []*menuItem, order uint32, smart bool) {
	for i := range items {
		items[i].trackPos = uint32(i + 1)
	}
	h.sortTracks(items, order)
	if smart {
		markContextType(items, 0) // sorted or not, a smart playlist's rows carry none
	} else {
		markContextType(items, h.ctxTrackType)
	}
}

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
	dlog.Debugf("dbserver: folder %d returning %d items", folderID, len(items))
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

// isSmartPlaylist reports whether the playlist source marks the playlist as
// smart (rule-based); sources that don't implement SmartPlaylistSource have
// none.
func (h *Handler) isSmartPlaylist(id uint32) bool {
	sp, ok := h.playlists.(SmartPlaylistSource)
	return ok && sp.IsSmart(id)
}

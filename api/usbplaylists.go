// SPDX-License-Identifier: GPL-3.0-or-later

package api

// usbplaylists.go surfaces a served rekordbox USB's playlist tree (parsed
// from its export.pdb) on the /api/playlists endpoints as read-only
// entries, so the web UI shows the same PLAYLIST tree the decks browse.
// Track IDs need no translation: in PDB mode the library is built from the
// PDB (library.FromPDB), so a USB playlist's track IDs are library IDs.

// usbPlaylistIDBit namespaces served-USB playlist IDs in the API surface so
// they can never collide with PlaylistStore IDs.
const usbPlaylistIDBit uint32 = 1 << 30

// usbPlaylists returns the served USB's playlist tree as read-only
// PlaylistInfo entries, or nil when no PDB (or no playlists) is served.
func (s *Server) usbPlaylists() []*PlaylistInfo {
	if s.PDB == nil || len(s.PDB.PlaylistTree) == 0 {
		return nil
	}
	out := make([]*PlaylistInfo, 0, len(s.PDB.PlaylistTree))
	for i, n := range s.PDB.PlaylistTree {
		p := &PlaylistInfo{
			ID:        n.ID | usbPlaylistIDBit,
			Name:      n.Name,
			IsFolder:  n.IsFolder,
			SortOrder: 1<<30 + i, // after user playlists, in the stick's order
			ReadOnly:  true,
		}
		if n.ParentID != 0 {
			p.ParentID = n.ParentID | usbPlaylistIDBit
		}
		if !n.IsFolder {
			p.TrackIDs = append([]uint32(nil), n.TrackIDs...)
		}
		out = append(out, p)
	}
	return out
}

// exportPlaylistSource resolves a "playlist:<id>"/"smart:<id>" export
// source to its name and ordered track IDs — from the user playlist
// store, or, for namespaced IDs, from a served rekordbox USB's playlist
// tree. USB playlists are read-only for MUTATION, but export only reads
// tracks, so re-exporting a stick's playlist is allowed.
func (s *Server) exportPlaylistSource(id uint32) (string, []uint32, bool) {
	if id&usbPlaylistIDBit != 0 {
		ids := s.usbPlaylistTrackIDs(id)
		if ids == nil {
			return "", nil, false
		}
		name := "USB playlist"
		raw := id &^ usbPlaylistIDBit
		for _, n := range s.PDB.PlaylistTree {
			if n.ID == raw {
				name = n.Name
				break
			}
		}
		return name, ids, true
	}
	if s.Playlists == nil {
		return "", nil, false
	}
	pl := s.Playlists.Get(id)
	if pl == nil {
		return "", nil, false
	}
	return pl.Name, s.Playlists.TracksFor(pl.ID, s.Library, s.Tags), true
}

// usbPlaylistTrackIDs resolves a namespaced USB playlist ID to its ordered
// track IDs, or nil when it doesn't exist.
func (s *Server) usbPlaylistTrackIDs(id uint32) []uint32 {
	if s.PDB == nil {
		return nil
	}
	raw := id &^ usbPlaylistIDBit
	for _, n := range s.PDB.PlaylistTree {
		if n.ID == raw {
			return n.TrackIDs
		}
	}
	return nil
}

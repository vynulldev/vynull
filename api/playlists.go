// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// User-defined playlists + folders. Mirrors the rekordbox PLAYLIST menu
// on the CDJ (phase 2 wires PlaylistStore into dbserver 0x1105).
//
// Tree shape is flat-with-parent-pointers: each entry has ParentID; the
// web UI walks the list and builds a tree.

// GET  /api/playlists           — flat list of every playlist/folder
// POST /api/playlists           — create {name, parent_id?, is_folder?}
func (s *Server) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	if s.Playlists == nil {
		http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		// User playlists plus, when serving a rekordbox USB, its playlist
		// tree as read-only entries — the same tree the decks browse.
		writeJSON(w, append(s.Playlists.All(), s.usbPlaylists()...))
	case "POST":
		// is_smart=true creates a smart playlist with the supplied rules
		// (folders + smart are mutually exclusive — server rejects both
		// flags being set together via PlaylistStore.CreateSmart's
		// isFolder=false hardcode).
		var req struct {
			Name     string      `json:"name"`
			ParentID uint32      `json:"parent_id"`
			IsFolder bool        `json:"is_folder"`
			IsSmart  bool        `json:"is_smart"`
			Rules    *SmartRules `json:"rules,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.IsSmart && req.IsFolder {
			http.Error(w, "is_smart and is_folder are mutually exclusive", http.StatusBadRequest)
			return
		}
		var (
			p   *PlaylistInfo
			err error
		)
		if req.IsSmart {
			p, err = s.Playlists.CreateSmart(req.Name, req.ParentID, req.Rules)
		} else {
			p, err = s.Playlists.Create(req.Name, req.ParentID, req.IsFolder)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT     /api/playlists/{id}        — {name?, parent_id?, sort_order?}
// DELETE  /api/playlists/{id}        — delete (recursive for folders)
// GET     /api/playlists/{id}/tracks — track list as []TrackInfo
// POST    /api/playlists/{id}/tracks — replace tracks {track_ids: [...]}
func (s *Server) handlePlaylistByID(w http.ResponseWriter, r *http.Request) {
	if s.Playlists == nil {
		http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
		return
	}

	// Parse /api/playlists/{id}[/tracks]
	path := strings.TrimPrefix(r.URL.Path, "/api/playlists/")
	parts := strings.Split(path, "/")
	var id uint32
	fmt.Sscanf(parts[0], "%d", &id)
	if id == 0 {
		http.Error(w, "invalid playlist ID", http.StatusBadRequest)
		return
	}

	// A served rekordbox USB's playlists (namespaced IDs): track listing
	// only, everything else is read-only — they mirror the stick.
	if id&usbPlaylistIDBit != 0 {
		if r.Method == "GET" && len(parts) >= 2 && parts[1] == "tracks" {
			ids := s.usbPlaylistTrackIDs(id)
			out := make([]TrackInfo, 0, len(ids))
			if s.Library != nil {
				for _, tid := range ids {
					if t := s.Library.Track(tid); t != nil {
						out = append(out, s.libTrackToInfo(t))
					}
				}
			}
			writeJSON(w, out)
			return
		}
		http.Error(w, "playlists on a served rekordbox USB are read-only", http.StatusForbidden)
		return
	}

	// /tracks sub-resource: list or replace ordered tracks.
	if len(parts) >= 2 && parts[1] == "tracks" {
		s.handlePlaylistTracks(w, r, id)
		return
	}
	// /rules sub-resource: get or replace smart-playlist rules.
	if len(parts) >= 2 && parts[1] == "rules" {
		s.handlePlaylistRules(w, r, id)
		return
	}

	switch r.Method {
	case "PUT":
		// Optional updates — pointers distinguish "not sent" from zero.
		var req struct {
			Name      *string `json:"name,omitempty"`
			ParentID  *uint32 `json:"parent_id,omitempty"`
			SortOrder *int    `json:"sort_order,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name != nil {
			if err := s.Playlists.Rename(id, *req.Name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if req.ParentID != nil || req.SortOrder != nil {
			// Default order=-1 means "append" inside Move.
			order := -1
			if req.SortOrder != nil {
				order = *req.SortOrder
			}
			parent := uint32(0)
			if req.ParentID != nil {
				parent = *req.ParentID
			} else {
				// Keep current parent when only sort changed.
				if cur := s.Playlists.Get(id); cur != nil {
					parent = cur.ParentID
				}
			}
			if err := s.Playlists.Move(id, parent, order); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, struct{ OK bool }{true})

	case "DELETE":
		if err := s.Playlists.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePlaylistTracks serves GET/POST /api/playlists/{id}/tracks.
// GET evaluates smart-playlist rules when the playlist is smart; for
// regular playlists it returns the stored ordered list. Either way
// the response is []TrackInfo so the web UI can render rows directly.
// POST replaces the entire track list with the supplied ordered IDs
// (rejected for smart playlists by PlaylistStore.SetTracks).
func (s *Server) handlePlaylistTracks(w http.ResponseWriter, r *http.Request, id uint32) {
	switch r.Method {
	case "GET":
		ids := s.Playlists.TracksFor(id, s.Library, s.Tags)
		out := make([]TrackInfo, 0, len(ids))
		if s.Library != nil {
			for _, tid := range ids {
				t := s.Library.Track(tid)
				if t == nil {
					continue
				}
				out = append(out, s.libTrackToInfo(t))
			}
		}
		writeJSON(w, out)
	case "POST":
		var req struct {
			TrackIDs []uint32 `json:"track_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Playlists.SetTracks(id, req.TrackIDs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePlaylistRules serves GET/PUT /api/playlists/{id}/rules.
// GET returns the SmartRules tree (or null if regular playlist).
// PUT replaces the rules. The body is the SmartRules JSON directly
// (no wrapper) so the editor can post the same shape it reads.
func (s *Server) handlePlaylistRules(w http.ResponseWriter, r *http.Request, id uint32) {
	switch r.Method {
	case "GET":
		p := s.Playlists.Get(id)
		if p == nil {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		writeJSON(w, p.Rules)
	case "PUT":
		var rules SmartRules
		if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Playlists.SetRules(id, &rules); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

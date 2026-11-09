// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/internal/dlog"
)

// The TAG LIST is the decks' list of tracks to play next, which linked
// decks read and edit too (library/taglist.go, dbserver/taglist.go). A
// change made here is announced on the link, as rekordbox announces its
// own, so the decks read the list again.

type tagListReply struct {
	TrackIDs []uint32 `json:"track_ids"`
}

// GET /api/taglist: the tag list's track IDs, in list order.
// POST /api/taglist {track_id}: add a track at the end.
// DELETE /api/taglist: remove every track.
func (s *Server) handleTagList(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		jsonError(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.tagListReply())
	case http.MethodPost:
		var req struct {
			TrackID uint32 `json:"track_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if s.Library.Track(req.TrackID) == nil {
			jsonError(w, "track not found", http.StatusNotFound)
			return
		}
		s.tagListChanged(s.Library.SetTagged(req.TrackID, true), "added track %d", req.TrackID)
		writeJSON(w, s.tagListReply())
	case http.MethodDelete:
		s.tagListChanged(s.Library.ClearTagList(), "removed every track")
		writeJSON(w, s.tagListReply())
	default:
		notAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

// DELETE /api/taglist/{track id}: remove a track.
func (s *Server) handleTagListTrack(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		jsonError(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodDelete {
		notAllowed(w, http.MethodDelete)
		return
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/taglist/"), 10, 32)
	if err != nil || id == 0 {
		jsonError(w, "track ID required", http.StatusBadRequest)
		return
	}
	s.tagListChanged(s.Library.SetTagged(uint32(id), false), "removed track %d", id)
	writeJSON(w, s.tagListReply())
}

func (s *Server) tagListReply() tagListReply {
	ids := s.Library.TagList()
	if ids == nil {
		ids = []uint32{}
	}
	return tagListReply{TrackIDs: ids}
}

// tagListChanged logs a change and tells the decks to read the list again.
func (s *Server) tagListChanged(changed bool, format string, args ...any) {
	if !changed {
		return
	}
	dlog.Infof("api: tag list: "+format, args...)
	if s.Device != nil {
		s.Device.BroadcastTagListRefresh()
	}
}

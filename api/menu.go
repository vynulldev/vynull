// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"

	"github.com/vynulldev/vynull/dbserver"
)

// User-configurable visibility + order of the LINK menu on the deck.

// GET /api/menu-items  → flat ordered list, visible + hidden
// PUT /api/menu-items  → replace ordered list {items: [{key, label?, visible}, ...]}
//
// Both also carry the deck's SORT menu as "sorts" ([{id, visible}, ...] in
// menu order).
func (s *Server) handleMenuItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if s.Menu == nil {
		http.Error(w, "menu store not available", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			Items             []MenuItem                  `json:"items"`
			TrackDetail       string                      `json:"track_detail"`
			TrackDetailFields []dbserver.TrackDetailField `json:"track_detail_fields"`
			Sorts             []SortItem                  `json:"sorts"`
		}{
			Items:             s.Menu.All(),
			TrackDetail:       s.Menu.TrackDetail(),
			TrackDetailFields: dbserver.TrackDetailFields,
			Sorts:             s.Menu.Sorts(),
		})
	case "PUT":
		// Items + track_detail are optional/independent — the UI
		// usually sends both, but a thin client could PUT just one
		// (e.g. {"track_detail":"artist"}) to update only that.
		var req struct {
			Items       *[]MenuItem `json:"items,omitempty"`
			TrackDetail *string     `json:"track_detail,omitempty"`
			Sorts       *[]SortItem `json:"sorts,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Items != nil {
			if err := s.Menu.Replace(*req.Items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if req.TrackDetail != nil {
			if err := s.Menu.SetTrackDetail(*req.TrackDetail); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if req.Sorts != nil {
			if err := s.Menu.ReplaceSorts(*req.Sorts); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/menu-items/reset → restore default order + visibility
// (?sorts=1: the SORT menu's instead)
func (s *Server) handleMenuItemsReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.Menu == nil {
		http.Error(w, "menu store not available", http.StatusServiceUnavailable)
		return
	}
	// ?sorts=1 resets the SORT menu instead of the root menu.
	if r.URL.Query().Get("sorts") != "" {
		s.Menu.ResetSorts()
	} else {
		s.Menu.ResetToDefaults()
	}
	writeJSON(w, struct{ OK bool }{true})
}

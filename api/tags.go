// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GET /api/tags — list all tags
// POST /api/tags — create a tag {name: "..."}
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		tags := s.Tags.GetAllTags()
		if tags == nil {
			tags = []TagInfo{}
		}
		writeJSON(w, tags)

	case "POST":
		var req struct {
			Name       string `json:"name"`
			CategoryID uint32 `json:"category_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		id, err := s.Tags.CreateTag(req.Name, req.CategoryID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, TagInfo{ID: id, Name: req.Name, CategoryID: req.CategoryID})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/tags/{id} — rename tag
// DELETE /api/tags/{id} — delete tag
func (s *Server) handleTagByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	// Parse ID from /api/tags/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/tags/")
	var id uint32
	fmt.Sscanf(path, "%d", &id)
	if id == 0 {
		http.Error(w, "invalid tag ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "PUT":
		// Accept any subset — name to rename, category_id to move, color
		// to recolour. Use pointers so we can tell "not sent" apart from
		// "sent zero" (0 is a meaningful colour value meaning "clear").
		var req struct {
			Name       *string `json:"name,omitempty"`
			CategoryID *uint32 `json:"category_id,omitempty"`
			Color      *uint8  `json:"color,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name != nil {
			if err := s.Tags.RenameTag(id, *req.Name); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		}
		if req.CategoryID != nil {
			s.Tags.SetTagCategory(id, *req.CategoryID)
		}
		if req.Color != nil {
			if err := s.Tags.SetTagColor(id, *req.Color); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		}
		writeJSON(w, struct{ OK bool }{true})

	case "DELETE":
		s.Tags.DeleteTag(id)
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/tracks/{id}/tags — get tags for a track
// POST /api/tracks/{id}/tags — set tags for a track {tag_ids: [...]}
func (s *Server) handleTrackTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	// Parse /api/tracks/{trackID}/tags
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		tags := s.Tags.GetTagsForTrack(trackID)
		if tags == nil {
			tags = []TagInfo{}
		}
		writeJSON(w, tags)

	case "POST":
		var req struct {
			TagIDs []uint32 `json:"tag_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Tags.SetTagsForTrack(trackID, req.TagIDs)
		s.Library.Touch() // tags live in the tag store, not a library Save()
		if s.Device != nil {
			// Tag-set changes fire the same 0x1d "track data invalidated"
			// trigger as cue and colour edits — track ID only, no per-tag
			// granularity needed since the CDJ re-fetches the whole tag
			// list on receipt.
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/tag-categories — list all categories
// POST /api/tag-categories — create category {name: "..."}
func (s *Server) handleTagCategories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		cats := s.Tags.GetAllCategories()
		if cats == nil {
			cats = []TagCategoryInfo{}
		}
		writeJSON(w, cats)

	case "POST":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		id, err := s.Tags.CreateCategory(req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, TagCategoryInfo{ID: id, Name: req.Name})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/tag-categories/{id} — rename category
// DELETE /api/tag-categories/{id} — delete category
func (s *Server) handleTagCategoryByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/tag-categories/")
	var id uint32
	fmt.Sscanf(path, "%d", &id)
	if id == 0 {
		http.Error(w, "invalid category ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "PUT":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Tags.RenameCategory(id, req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, struct{ OK bool }{true})

	case "DELETE":
		s.Tags.DeleteCategory(id)
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

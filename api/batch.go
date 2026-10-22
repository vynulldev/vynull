// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"

	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/library"
)

// Batch track edits, for the web UI's bulk bar and any client editing a
// selection: one request for many tracks, applied as one change to the store,
// instead of one request (and one deck refresh) per track.
//
//	PUT  /api/tracks/color   {"track_ids": [...], "color_id": 0-8}
//	PUT  /api/tracks/rating  {"track_ids": [...], "rating": 0-5}
//	POST /api/tracks/tags    {"track_ids": [...], "add": [...], "remove": [...]}
//
// Each answers {"updated": n}, the number of tracks it changed. Track IDs
// the library doesn't hold are skipped.

type batchResult struct {
	Updated int `json:"updated"`
}

func (s *Server) handleTracksColor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackIDs []uint32 `json:"track_ids"`
		ColorID  uint8    `json:"color_id"`
	}
	if !s.batchRequest(w, r, http.MethodPut, &req, &req.TrackIDs) {
		return
	}
	if req.ColorID > 8 {
		http.Error(w, "color_id must be 0-8", http.StatusBadRequest)
		return
	}
	colors := make([]library.ColorImport, len(req.TrackIDs))
	for i, id := range req.TrackIDs {
		colors[i] = library.ColorImport{TrackID: id, ColorID: req.ColorID}
	}
	n := s.Library.SetColors(colors)
	log.Printf("api: colour %s on %d tracks", colorLabel(req.ColorID), n)
	s.refreshLoaded(req.TrackIDs, (*device.VirtualDevice).BroadcastTrackRefresh)
	writeJSON(w, batchResult{n})
}

func (s *Server) handleTracksRating(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackIDs []uint32 `json:"track_ids"`
		Rating   uint8    `json:"rating"`
	}
	if !s.batchRequest(w, r, http.MethodPut, &req, &req.TrackIDs) {
		return
	}
	ratings := make(map[uint32]uint8, len(req.TrackIDs))
	for _, id := range req.TrackIDs {
		ratings[id] = req.Rating // clamped to 5
	}
	n := s.Library.SetRatings(ratings)
	log.Printf("api: rating %d on %d tracks", min(req.Rating, 5), n)
	s.refreshLoaded(req.TrackIDs, (*device.VirtualDevice).BroadcastRatingRefresh)
	writeJSON(w, batchResult{n})
}

func (s *Server) handleTracksTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackIDs []uint32 `json:"track_ids"`
		Add      []uint32 `json:"add"`
		Remove   []uint32 `json:"remove"`
	}
	if !s.batchRequest(w, r, http.MethodPost, &req, &req.TrackIDs) {
		return
	}
	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}
	if len(req.Add)+len(req.Remove) == 0 {
		http.Error(w, "add or remove required", http.StatusBadRequest)
		return
	}
	var known []uint32
	for _, id := range req.TrackIDs {
		if s.Library.Track(id) != nil {
			known = append(known, id)
		}
	}
	changed, err := s.Tags.EditTags(known, req.Add, req.Remove)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(changed) > 0 {
		s.Library.Touch() // tags live in the tag store, not a library Save()
	}
	s.refreshLoaded(changed, (*device.VirtualDevice).BroadcastTrackRefresh)
	writeJSON(w, batchResult{len(changed)})
}

// batchRequest answers the CORS preflight and decodes a batch edit's body
// into req, refusing another method, bad JSON or an empty selection. It
// reports whether the handler should go on.
func (s *Server) batchRequest(w http.ResponseWriter, r *http.Request, method string, req any, trackIDs *[]uint32) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", method+", OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	switch r.Method {
	case http.MethodOptions:
		return false
	case method:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if len(*trackIDs) == 0 {
		http.Error(w, "track_ids required", http.StatusBadRequest)
		return false
	}
	slices.Sort(*trackIDs)
	*trackIDs = slices.Compact(*trackIDs)
	return true
}

// refreshLoaded sends the refresh trigger for each of ids that a deck has
// loaded from us. A deck acts on a refresh only for its loaded track, so a
// batch edit or an import sends a few triggers instead of a pair of packets
// per track.
func (s *Server) refreshLoaded(ids []uint32, refresh func(*device.VirtualDevice, uint32)) {
	if s.Device == nil || s.Device.Monitor == nil || len(ids) == 0 {
		return
	}
	loaded := s.Device.Monitor.LoadedTracks()
	for _, id := range ids {
		if loaded[id] {
			refresh(s.Device, id)
		}
	}
}

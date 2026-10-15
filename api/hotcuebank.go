// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"strings"
)

// HotCueBankNode is one folder or bank in GET /api/hotcuebanks.
type HotCueBankNode struct {
	ID         uint32 `json:"id"`
	ParentID   uint32 `json:"parent_id"` // 0 at the top level
	Seq        int    `json:"seq"`
	Name       string `json:"name"`
	IsFolder   bool   `json:"is_folder"`
	TrackCount int    `json:"track_count"` // banks only: the tracks a deck is sent
}

// HotCueBankEntry is one track of a bank in GET /api/hotcuebanks/{id}/tracks,
// with the cue the bank holds for it.
type HotCueBankEntry struct {
	// Position is the membership position; positions 1-8 are pads A-H.
	Position int32     `json:"position"`
	Track    TrackInfo `json:"track"`
	InMs     int32     `json:"in_ms"`
	OutMs    *int32    `json:"out_ms,omitempty"` // loop end; absent when not a loop
	Color    int       `json:"color"`            // rekordbox's Color field as stored (-1 = none)
	ColorID  int       `json:"color_id"`         // rekordbox hot-cue palette colour; 0 = unset (default colour)
	Comment  string    `json:"comment,omitempty"`
}

// hotCueBankEntries lists a bank's tracks in membership order, duplicates
// kept, entries whose track isn't in the library left out. Tracks whose file
// is missing here are listed (track.file_missing) though decks aren't sent
// them.
func (s *Server) hotCueBankEntries(bankID uint32) []HotCueBankEntry {
	out := []HotCueBankEntry{}
	for _, m := range s.Library.HotCueBankMembers(bankID) {
		t := s.Library.Track(m.TrackID)
		if m.TrackID == 0 || t == nil {
			continue
		}
		e := HotCueBankEntry{Position: m.TrackNo, Track: s.libTrackListInfo(t), InMs: m.InMsec, Color: m.Color, ColorID: m.ColorTableIndex, Comment: m.Comment}
		if m.OutMsec != -1 {
			end := m.OutMsec
			e.OutMs = &end
		}
		out = append(out, e)
	}
	return out
}

// handleHotCueBanks serves GET /api/hotcuebanks: the Hot Cue Bank folders and
// banks imported from rekordbox, as a flat list (parent_id links the tree;
// siblings order by seq). Read-only.
func (s *Server) handleHotCueBanks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	out := []HotCueBankNode{}
	for _, b := range s.Library.HotCueBankList() {
		n := HotCueBankNode{ID: b.ID, ParentID: b.ParentID, Seq: b.Seq, Name: b.Name, IsFolder: b.Folder}
		if !b.Folder {
			for _, e := range s.hotCueBankEntries(b.ID) {
				if !e.Track.FileMissing {
					n.TrackCount++
				}
			}
		}
		out = append(out, n)
	}
	writeJSON(w, out)
}

// handleHotCueBankByID serves GET /api/hotcuebanks/{id}/tracks: a bank's
// tracks and the cue it holds for each. Read-only.
func (s *Server) handleHotCueBankByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	if !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/tracks") {
		http.NotFound(w, r)
		return
	}
	id := parseTrackIDFromPath(r.URL.Path, "/api/hotcuebanks/")
	found := false
	for _, b := range s.Library.HotCueBankList() {
		if b.ID == id && !b.Folder {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "hot cue bank not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.hotCueBankEntries(id))
}

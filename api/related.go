// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"math"
	"net/http"
	"strconv"

	"github.com/vynulldev/vynull/related"
)

// RelatedTrack is one suggestion in GET /api/tracks/{id}/related.
type RelatedTrack struct {
	Track   TrackInfo `json:"track"`
	Score   float64   `json:"score"`
	Reasons []string  `json:"reasons"`
}

// handleTrackRelated serves GET /api/tracks/{id}/related: tracks that mix well
// with this one, best first (see package related). Query parameters: limit
// (1-200, default 25) and bpm_tolerance in percent (0.5-50, default 6).
func (s *Server) handleTrackRelated(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 || s.Library.Track(trackID) == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}
	var o related.Options
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			http.Error(w, "limit must be 1-200", http.StatusBadRequest)
			return
		}
		o.Limit = n
	}
	if v := r.URL.Query().Get("bpm_tolerance"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || f < 0.5 || f > 50 {
			http.Error(w, "bpm_tolerance must be 0.5-50 (percent)", http.StatusBadRequest)
			return
		}
		o.BPMTolerance = f
	}
	out := []RelatedTrack{}
	for _, m := range related.Find(s.Library, trackID, o) {
		out = append(out, RelatedTrack{
			Track:   s.libTrackListInfo(m.Track),
			Score:   math.Round(m.Score*100) / 100,
			Reasons: m.Reasons,
		})
	}
	writeJSON(w, out)
}

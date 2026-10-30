// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
)

// artExtractSem bounds concurrent lazy ffmpeg artwork probes so a fast scroll
// over a fresh import doesn't spawn one ffmpeg process per visible row at once.
var artExtractSem = make(chan struct{}, 4)

// GET /api/artwork/{trackID} — returns the cached album art (JPEG)
// for trackID. 404 when the track has no art. Used by the library
// table's optional ART column.
func (s *Server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/artwork/")
	if trackID == 0 || s.Library == nil {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil {
		http.NotFound(w, r)
		return
	}
	// Lazy extraction: if this track's file has never been probed, do it now
	// (one ffmpeg probe, then cached for good). This replaces the old startup
	// artwork sweep — we only pay for tracks whose art is actually requested,
	// and the result (ArtID + ArtChecked) is persisted via a debounced save.
	// Dedup concurrent requests for the same track, bound total concurrency,
	// and write the track fields under the library lock (vs the debounced Save).
	if t.ArtID == 0 && !t.ArtChecked && t.FilePath != "" {
		if _, busy := s.artInFlight.LoadOrStore(trackID, struct{}{}); busy {
			// Another request is already probing this track; don't double-probe.
			// The thumbnail appears once that finishes (next poll re-fetches).
			http.NotFound(w, r)
			return
		}
		artExtractSem <- struct{}{} // cap concurrent ffmpeg probes
		data := analysis.ExtractArtwork(t.FilePath)
		<-artExtractSem
		var artID uint32
		if data != nil {
			artID = s.Library.Artwork.Add("image/jpeg", data)
		}
		s.Library.SetArtwork(trackID, artID) // saves the library
		s.artInFlight.Delete(trackID)
	}
	if t.ArtID == 0 {
		http.NotFound(w, r)
		return
	}
	art := s.Library.Artwork.Get(t.ArtID)
	if art == nil || len(art.Data) == 0 {
		http.NotFound(w, r)
		return
	}
	contentType := art.MIMEType
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(art.Data)
}

// GET /api/artwork/ext/{player}/{slot}/{trackID} — cover art for a track a deck
// plays from another player's media (downloaded over NFS by package mediadb).
// 404 until the image is cached; the PLAYERS view retries on its next poll.
func (s *Server) handleExtArtwork(w http.ResponseWriter, r *http.Request) {
	if s.ExtArtwork == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/artwork/ext/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.Error(w, "expected /api/artwork/ext/{player}/{slot}/{trackID}", http.StatusBadRequest)
		return
	}
	player, err1 := strconv.ParseUint(parts[0], 10, 8)
	slot, err2 := strconv.ParseUint(parts[1], 10, 8)
	trackID, err3 := strconv.ParseUint(parts[2], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil || trackID == 0 {
		http.Error(w, "invalid player/slot/trackID", http.StatusBadRequest)
		return
	}
	data := s.ExtArtwork(uint8(player), uint8(slot), uint32(trackID))
	if len(data) == 0 {
		http.NotFound(w, r) // not fetched yet, or the track has no art
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

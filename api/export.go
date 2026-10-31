// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/export"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
)

// parseSelectionIDs parses a comma-separated track-ID list, the "selection:"
// export source used by the library's bulk-select bar (e.g. "selection:3,7,9").
func parseSelectionIDs(csv string) ([]uint32, error) {
	var ids []uint32
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad selection track ID %q: %w", p, err)
		}
		ids = append(ids, uint32(id))
	}
	return ids, nil
}

// handleExportPreview returns a summary of what the export will contain
// for a given source (?source=all|playlist:N|smart:N). The web UI uses
// this to warn the user before exporting — e.g. "3 tracks will be
// re-encoded due to decode errors that would freeze the deck".
func (s *Server) handleExportPreview(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("source")
	if src == "" {
		src = "all"
	}
	var tracks []*library.Track
	if src == "all" {
		tracks = s.Library.Tracks()
	} else if strings.HasPrefix(src, "playlist:") || strings.HasPrefix(src, "smart:") {
		idStr := src[strings.IndexByte(src, ':')+1:]
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			jsonError(w, "bad source ID: "+err.Error(), http.StatusBadRequest)
			return
		}
		_, trackIDs, ok := s.exportPlaylistSource(uint32(id))
		if !ok {
			jsonError(w, "playlist not found", http.StatusNotFound)
			return
		}
		for _, tid := range trackIDs {
			if t := s.Library.Track(tid); t != nil {
				tracks = append(tracks, t)
			}
		}
	} else if strings.HasPrefix(src, "selection:") {
		trackIDs, err := parseSelectionIDs(src[len("selection:"):])
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, tid := range trackIDs {
			if t := s.Library.Track(tid); t != nil {
				tracks = append(tracks, t)
			}
		}
	} else {
		jsonError(w, "source must be 'all', 'playlist:N', 'smart:N', or 'selection:<ids>'", http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]interface{}{
		"total": len(tracks),
	})
}

// artworkLookup wraps the library's artwork cache so the export
// package can resolve JPEG bytes without depending on the library
// type directly. nil when the library has no cache.
func artworkLookup(lib *library.Library) func(uint32) []byte {
	if lib == nil || lib.Artwork == nil {
		return nil
	}
	return func(id uint32) []byte {
		art := lib.Artwork.Get(id)
		if art == nil {
			return nil
		}
		return art.Data
	}
}

// POST /api/export — write a Rekordbox USB export.
//
// Request body (JSON):
//
//	destination   string  required; absolute path on the server
//	source        string  "all" (default) | "playlist:<id>" | "smart:<id>"
//	copy_files    bool    true = copy audio (slow, portable);
//	                      false = symlink (fast, in-place)
//
// Runs the export synchronously and returns when it's complete.
// Returns 400 for bad input, 404 when the playlist isn't found, 500
// for any pipeline error.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if s.Library == nil {
		jsonError(w, "library not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Destination string `json:"destination"`
		Source      string `json:"source"`
		CopyFiles   bool   `json:"copy_files"`
		Merge       bool   `json:"merge"` // append to existing export.pdb instead of replacing
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Destination == "" {
		jsonError(w, "destination is required", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Destination) {
		jsonError(w, "destination must be an absolute path", http.StatusBadRequest)
		return
	}

	src := req.Source
	if src == "" {
		src = "all"
	}

	opts := export.Options{
		Library:       s.Library,
		SrcDir:        s.MusicDir,
		DestDir:       req.Destination,
		CopyFiles:     req.CopyFiles,
		Merge:         req.Merge,
		Analysis:      s.Analysis,
		ArtworkLookup: artworkLookup(s.Library),
	}
	if s.Menu != nil {
		opts.Menu = s.Menu.PDBMenuConfig()
	}
	if s.Settings != nil {
		opts.Settings = pdb.SettingsBodies{
			MySetting:    s.Settings.GetMySetting(),
			MySetting2:   s.Settings.GetMySetting2(),
			DjmMySetting: s.Settings.GetDjmMySetting(),
			DevSetting:   s.Settings.GetDevSettingDat(),
		}
	}

	var sourceLabel string
	switch {
	case src == "all":
		sourceLabel = "COLLECTION"
		// Resolve the full collection here rather than letting export.Run
		// default it from opts.Library, so the response's track_count (and
		// the web UI toast) reflect what was actually written instead of 0.
		opts.Tracks = export.LibraryToTracks(s.Library)
	case strings.HasPrefix(src, "playlist:") || strings.HasPrefix(src, "smart:"):
		idStr := src[strings.IndexByte(src, ':')+1:]
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			jsonError(w, "source ID must be a uint32: "+err.Error(), http.StatusBadRequest)
			return
		}
		plName, trackIDs, ok := s.exportPlaylistSource(uint32(id))
		if !ok {
			jsonError(w, "playlist not found", http.StatusNotFound)
			return
		}
		if len(trackIDs) == 0 {
			jsonError(w, "playlist has no tracks", http.StatusBadRequest)
			return
		}
		opts.Tracks = export.FilterTracks(export.LibraryToTracks(s.Library), trackIDs)
		opts.Playlists = export.SinglePlaylist(plName, trackIDs)
		sourceLabel = plName
	case strings.HasPrefix(src, "selection:"):
		trackIDs, err := parseSelectionIDs(src[len("selection:"):])
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(trackIDs) == 0 {
			jsonError(w, "no tracks selected", http.StatusBadRequest)
			return
		}
		opts.Tracks = export.FilterTracks(export.LibraryToTracks(s.Library), trackIDs)
		opts.Playlists = export.SinglePlaylist("Selected Tracks", trackIDs)
		sourceLabel = fmt.Sprintf("%d selected", len(trackIDs))
	default:
		jsonError(w, "source must be 'all', 'playlist:<id>', 'smart:<id>', or 'selection:<ids>'", http.StatusBadRequest)
		return
	}

	dlog.Infof("export: starting (source=%s dest=%s copy=%v)", sourceLabel, req.Destination, req.CopyFiles)
	if err := export.Run(opts); err != nil {
		dlog.Errorf("export: failed: %v", err)
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, struct {
		OK          bool   `json:"ok"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
		TrackCount  int    `json:"track_count"`
	}{true, sourceLabel, req.Destination, len(opts.Tracks)})
}

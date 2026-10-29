// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/internal/filetype"
	"github.com/vynulldev/vynull/library"
)

// TrackInfo is the API representation of a library track. Used by
// /api/tracks, /api/playlists/{id}/tracks, and any other endpoint that
// returns track rows. Tags are flattened to []string (names) since the
// callers (library list, drawer) don't need the full tag struct.
type TrackInfo struct {
	ID             uint32   `json:"id"`
	Title          string   `json:"title"`
	Artist         string   `json:"artist"`
	Album          string   `json:"album"`
	Genre          string   `json:"genre"`
	BPM            float64  `json:"bpm"`
	Key            string   `json:"key"`
	Duration       int      `json:"duration"`
	FilePath       string   `json:"file_path"`
	FileType       string   `json:"file_type,omitempty"`
	FileSize       int64    `json:"file_size,omitempty"`
	Comment        string   `json:"comment,omitempty"`
	Label          string   `json:"label,omitempty"`
	OriginalArtist string   `json:"original_artist,omitempty"`
	Remixer        string   `json:"remixer,omitempty"`
	MixName        string   `json:"mix_name,omitempty"`
	Bitrate        int      `json:"bitrate,omitempty"`
	Year           int      `json:"year,omitempty"`
	TrackNum       int      `json:"track_num,omitempty"`
	Rating         int      `json:"rating,omitempty"`
	ColorID        uint8    `json:"color_id,omitempty"`
	ColorName      string   `json:"color_name,omitempty"`
	DateAdded      string   `json:"date_added,omitempty"`
	DateCreated    string   `json:"date_created,omitempty"` // rekordbox's DateCreated, from an import
	ReleaseDate    string   `json:"release_date,omitempty"` // rekordbox's ReleaseDate, from an import
	PlayCount      int      `json:"play_count,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	ArtID          uint32   `json:"art_id,omitempty"`
	ArtChecked     bool     `json:"art_checked,omitempty"` // false with art_id 0 = never probed; the web UI requests art optimistically to trigger lazy extraction
	FileMissing    bool     `json:"file_missing,omitempty"`
	AnalysisLocked bool     `json:"analysis_locked,omitempty"` // grid is protected from re-analysis / edits
}

// colorLabel names a track colour for the log: "Yellow (4)", "none (0)".
func colorLabel(id uint8) string {
	name := library.TrackColorName(id)
	if name == "" {
		name = "none"
	}
	return fmt.Sprintf("%s (%d)", name, id)
}

// libTrackListInfo is the payload for the full-collection list. It drops the
// fields only ever shown in the track-detail drawer (which refetches the full
// single track via /api/tracks/{id}) — chiefly file_size, which is set on
// every imported track and adds up across a large library. Everything the
// table's columns, filters, sort and search read is kept.
func (s *Server) libTrackListInfo(t *library.Track) TrackInfo {
	info := s.libTrackToInfo(t)
	info.FileSize = 0        // omitempty → omitted
	info.OriginalArtist = "" // detail-only
	info.MixName = ""        // detail-only
	info.TrackNum = 0        // detail-only
	return info
}

// libTrackToInfo converts a library.Track to the API TrackInfo shape,
// including tag names when a tag store is configured. Centralised so
// every endpoint that returns tracks (list, single, playlist tracks)
// uses the same field set and date format.
func (s *Server) libTrackToInfo(t *library.Track) TrackInfo {
	info := TrackInfo{
		ID:             t.ID,
		Title:          t.Title,
		Artist:         t.Artist,
		Album:          t.Album,
		Genre:          t.Genre,
		BPM:            t.BPM,
		Key:            t.Key,
		Duration:       int(t.Duration.Seconds()),
		FilePath:       t.FilePath,
		FileType:       t.FileType,
		FileSize:       t.FileSize,
		Comment:        t.Comment,
		Label:          t.Label,
		OriginalArtist: t.OriginalArtist,
		Remixer:        t.Remixer,
		MixName:        t.MixName,
		Bitrate:        t.Bitrate,
		Year:           t.Year,
		TrackNum:       t.TrackNum,
		Rating:         int(t.Rating),
		ColorID:        t.ColorID,
		ColorName:      library.TrackColorName(t.ColorID),
		PlayCount:      t.PlayCount,
		ArtID:          t.ArtID,
		ArtChecked:     t.ArtChecked,
		FileMissing:    t.FileMissing,
		AnalysisLocked: t.AnalysisLocked,
	}
	if !t.DateAdded.IsZero() {
		info.DateAdded = t.DateAdded.Format("2006-01-02")
	}
	if !t.DateCreated.IsZero() {
		info.DateCreated = t.DateCreated.Format("2006-01-02")
	}
	if !t.ReleaseDate.IsZero() {
		info.ReleaseDate = t.ReleaseDate.Format("2006-01-02")
	}
	if s.Tags != nil {
		for _, tag := range s.Tags.GetTagsForTrack(t.ID) {
			info.Tags = append(info.Tags, tag.Name)
		}
	}
	return info
}

func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	trackToInfo := s.libTrackToInfo

	// Single track lookup: /api/tracks/{id}
	path := r.URL.Path
	if path == "/api/tracks" || path == "/api/tracks/" {
		path = ""
	} else {
		path = strings.TrimPrefix(path, "/api/tracks/")
		path = strings.TrimSuffix(path, "/")
	}
	if path != "" && path != "add" && path != "reimport" {
		trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
		if trackID == 0 {
			http.Error(w, "invalid track ID", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodDelete {
			if s.Library == nil || s.Library.Track(trackID) == nil {
				http.Error(w, "track not found", http.StatusNotFound)
				return
			}
			// Cascade cleanup: drop the track from playlists, tag/colour
			// /rating maps, and cue store so we don't leave dangling
			// references. The library entry goes last so callers reading
			// during cleanup still see a consistent track record.
			if s.Playlists != nil {
				s.Playlists.RemoveTrackFromAll(trackID)
			}
			if s.Tags != nil {
				s.Tags.RemoveAllTrackData(trackID)
			}
			if s.Cues != nil {
				s.Cues.DeleteAllForTrack(trackID)
			}
			// Free the in-memory analysis entry (it holds the waveform
			// blobs) and any stale queue marker. The on-disk analysis
			// cache is kept on purpose: it is keyed by file path, so
			// re-adding the same file reuses it instead of re-analyzing.
			if s.Analysis != nil {
				s.Analysis.Remove(trackID)
			}
			s.Library.RemoveTrack(trackID)
			log.Printf("api: deleted track %d (library + playlists + tags + cues + analysis)", trackID)
			writeJSON(w, struct{ OK bool }{true})
			return
		}
		if s.Library != nil {
			if t := s.Library.Track(trackID); t != nil {
				writeJSON(w, trackToInfo(t))
				return
			}
		}
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}

	// All tracks. Tag the response with an ETag keyed to the library
	// revision; an unchanged If-None-Match lets us skip serializing the
	// whole list (and lets HTTP caches revalidate cheaply).
	if s.Library != nil {
		etag := fmt.Sprintf(`"tracks-%d"`, s.Library.Rev())
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	var tracks []TrackInfo
	if s.Library != nil {
		for _, t := range s.Library.Tracks() {
			tracks = append(tracks, s.libTrackListInfo(t))
		}
	}
	if tracks == nil {
		tracks = []TrackInfo{}
	}
	writeJSONGzip(w, r, tracks)
}

// handleTracksRev serves just the library revision — a tiny payload the web
// UI polls every tick to decide whether the full track list is worth
// refetching. Cheap on both ends compared to diffing the whole library.
func (s *Server) handleTracksRev(w http.ResponseWriter, r *http.Request) {
	var rev uint64
	if s.Library != nil {
		rev = s.Library.Rev()
	}
	writeJSON(w, map[string]uint64{"rev": rev})
}

func (s *Server) handleLoadTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TrackID      uint32 `json:"track_id"`
		FilePath     string `json:"file_path"`     // alternative: resolve track by path
		DeviceNumber uint8  `json:"device_number"` // target CDJ (1-4)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.DeviceNumber == 0 {
		http.Error(w, "device_number required", http.StatusBadRequest)
		return
	}

	// Resolve file_path to track_id if provided
	if req.TrackID == 0 && req.FilePath != "" {
		req.TrackID = s.resolveTrackID(req.FilePath)

		// Auto-add to library if not found
		if req.TrackID == 0 {
			id, err := s.addTrackByPath(req.FilePath, false)
			if err != nil {
				http.Error(w, "failed to add track: "+err.Error(), http.StatusBadRequest)
				return
			}
			req.TrackID = id
		}
	}

	if req.TrackID == 0 {
		http.Error(w, "track_id or file_path required", http.StatusBadRequest)
		return
	}

	// Find the target CDJ's IP from peers
	if s.Device.Peers == nil {
		http.Error(w, "no peers connected", http.StatusServiceUnavailable)
		return
	}
	var targetIP net.IP
	for _, p := range s.Device.Peers.Peers() {
		if p.DeviceNumber == req.DeviceNumber {
			targetIP = p.IP
			break
		}
	}
	if targetIP == nil {
		http.Error(w, fmt.Sprintf("CDJ device %d not found", req.DeviceNumber), http.StatusNotFound)
		return
	}

	// Send the load track command. The name rides along for the logs.
	loadName := ""
	if s.Library != nil {
		if t := s.Library.Track(req.TrackID); t != nil {
			loadName = t.Title
		}
	}
	err := s.Device.LoadTrackOnCDJ(req.TrackID, loadName, req.DeviceNumber, targetIP)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"status":        "ok",
		"track_id":      req.TrackID,
		"device_number": req.DeviceNumber,
	})
}

// resolveTrackID finds a track ID by matching file path.
// Matches by exact path, by basename, or by suffix (for relative PDB paths).
func (s *Server) resolveTrackID(filePath string) uint32 {
	base := filepath.Base(filePath)

	// Try PDB tracks first
	if s.PDB != nil {
		for _, t := range s.PDB.Tracks {
			// Exact match (PDB paths may be relative to export root)
			if t.FilePath == filePath {
				return t.ID
			}
			// Match with export root prefix
			if s.Device != nil {
				// PDB stores paths like "/Contents/..." relative to USB root
				// The actual file might be at exportRoot + PDB path
				if filepath.Base(t.FilePath) == base {
					return t.ID
				}
			}
		}
		// Suffix match — the player's path might end with the PDB's relative path
		for _, t := range s.PDB.Tracks {
			if strings.HasSuffix(filePath, t.FilePath) || strings.HasSuffix(t.FilePath, "/"+base) {
				return t.ID
			}
		}
	}

	// Try library tracks
	if s.Library != nil {
		for _, t := range s.Library.Tracks() {
			if t.FilePath == filePath || filepath.Base(t.FilePath) == base {
				return t.ID
			}
		}
	}

	return 0
}

// editError is a refused track edit and the HTTP status that answers it.
type editError struct {
	status int
	msg    string
}

func (e *editError) Error() string { return e.msg }

// errUnchanged ends a Library.Update whose edit turns out not to be needed:
// the track is left as it is.
var errUnchanged = errors.New("unchanged")

// updateTrack applies edit to the track through Library.Update, so the edit
// is all or nothing and the library's indexes follow it. If the edit fails it
// answers the request: 404 for a track that has gone, an editError's own
// status for a refused edit. It returns the track as edited and whether the
// edit was applied.
func (s *Server) updateTrack(w http.ResponseWriter, trackID uint32, edit func(*library.Track) error) (library.Track, bool) {
	var updated library.Track
	err := s.Library.Update(trackID, func(t *library.Track) error {
		if err := edit(t); err != nil {
			return err
		}
		updated = *t
		return nil
	})
	var refused *editError
	switch {
	case err == nil:
		return updated, true
	case errors.Is(err, library.ErrTrackNotFound):
		http.Error(w, "track not found", http.StatusNotFound)
	case errors.As(err, &refused):
		http.Error(w, refused.msg, refused.status)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	return library.Track{}, false
}

// PUT /api/tracks/{id}/metadata — update editable text metadata in the
// library DB ONLY. Never touches the audio file. Body is a partial object;
// omitted fields are left unchanged. A field that fails validation refuses
// the whole edit, and artist, album and genre edits keep the library's
// indexes in step.
func (s *Server) handleTrackMetadata(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != "PUT" && r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	if s.Library.Track(trackID) == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}
	var req struct {
		Title          *string `json:"title"`
		Artist         *string `json:"artist"`
		Album          *string `json:"album"`
		Genre          *string `json:"genre"`
		Year           *string `json:"year"` // sent as text from the form; parsed below
		Key            *string `json:"key"`  // any notation; normalized to Camelot below
		Label          *string `json:"label"`
		Remixer        *string `json:"remixer"`
		OriginalArtist *string `json:"original_artist"`
		MixName        *string `json:"mix_name"`
		Comment        *string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	updated, ok := s.updateTrack(w, trackID, func(t *library.Track) error {
		text := func(field *string, v *string) {
			if v != nil {
				*field = strings.TrimSpace(*v)
			}
		}
		text(&t.Title, req.Title)
		text(&t.Artist, req.Artist)
		text(&t.Album, req.Album)
		text(&t.Genre, req.Genre)
		if req.Year != nil {
			y := strings.TrimSpace(*req.Year)
			if y == "" {
				t.Year = 0
			} else if n, err := strconv.Atoi(y); err == nil && n >= 0 {
				t.Year = n
			} else {
				return &editError{http.StatusBadRequest, "year must be a non-negative whole number (or blank)"}
			}
		}
		if req.Key != nil {
			k := strings.TrimSpace(*req.Key)
			if k == "" {
				t.Key = ""
			} else if cam, _ := analysis.KeyNamesFrom(k); cam != "" {
				t.Key = cam // store canonical Camelot, matching how imports normalize
			} else {
				return &editError{http.StatusBadRequest, "unrecognized musical key (use Camelot like 8A or classic like Am)"}
			}
		}
		text(&t.Label, req.Label)
		text(&t.Remixer, req.Remixer)
		text(&t.OriginalArtist, req.OriginalArtist)
		text(&t.MixName, req.MixName)
		text(&t.Comment, req.Comment)
		return nil
	})
	if !ok {
		return
	}
	if s.Device != nil {
		s.Device.BroadcastTrackRefresh(trackID)
	}
	writeJSON(w, updated)
}

// PUT /api/tracks/{id}/path — point a track at a new file path {path: "..."}.
// Updates the library DB only (never moves or renames the file on disk),
// rekeys the analysis cache so existing waveforms/beat grid follow the track,
// rechecks presence (clearing/setting FileMissing), and refreshes size/type
// from the new file when it exists.
func (s *Server) handleTrackPath(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != "PUT" && r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	if s.Library.Track(trackID) == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	newPath := strings.TrimSpace(req.Path)
	if newPath == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	newType, ok := filetype.ForPath(newPath)
	if !ok {
		http.Error(w, "unsupported file type: "+filepath.Ext(newPath), http.StatusBadRequest)
		return
	}
	// Recheck presence, and size from the new file if it exists, before taking
	// the library's lock.
	var size int64
	miss := true
	if fi, err := os.Stat(newPath); err == nil && !fi.IsDir() {
		miss, size = false, fi.Size()
	}
	var oldPath string
	updated, ok := s.updateTrack(w, trackID, func(t *library.Track) error {
		oldPath = t.FilePath
		t.FilePath = newPath
		if !miss {
			t.FileSize = size
			t.FileType = newType.Name
		}
		t.FileMissing = miss
		return nil
	})
	if !ok {
		return
	}
	if newPath != oldPath && s.Analysis != nil {
		// Move the cached analysis to the new path's slot so the existing
		// waveforms/beat grid follow the track instead of orphaning. Until
		// SetPath, lookups still use the old path, whose cache is still there.
		s.Analysis.RenameCachedPath(oldPath, newPath)
		s.Analysis.SetPath(trackID, newPath)
	}
	if s.Device != nil {
		s.Device.BroadcastTrackRefresh(trackID)
	}
	log.Printf("track %d: path updated %q -> %q (missing=%v)", trackID, oldPath, newPath, miss)
	writeJSON(w, s.libTrackToInfo(&updated))
}

// PUT /api/tracks/{id}/color — set track color {color_id: N}
func (s *Server) handleTrackColor(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}

	// The library owns track colours: what the decks, smart rules and the
	// track list read.
	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			ColorID uint8 `json:"color_id"`
		}{t.ColorID})

	case "PUT", "POST":
		var req struct {
			ColorID uint8 `json:"color_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.ColorID > 8 {
			http.Error(w, "color_id must be 0-8", http.StatusBadRequest)
			return
		}
		s.Library.SetColor(trackID, req.ColorID)
		log.Printf("api: colour %s on track %d", colorLabel(req.ColorID), trackID)
		if s.Device != nil {
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/tracks/{id}/rating — set track rating {rating: N} (0-5; 0 clears)
func (s *Server) handleTrackRating(w http.ResponseWriter, r *http.Request) {
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	t := s.Library.Track(trackID)
	if t == nil {
		http.Error(w, "track not found", http.StatusNotFound)
		return
	}

	// The library owns ratings, whether set here or on a deck (0x2107).
	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			Rating uint8 `json:"rating"`
		}{t.Rating})

	case "PUT", "POST":
		var req struct {
			Rating uint8 `json:"rating"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Library.SetRating(trackID, req.Rating) // clamps to 5
		if s.Device != nil {
			s.Device.BroadcastRatingRefresh(trackID)
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

// Package api provides an HTTP API for external applications to query
// the state of the Pro DJ Link virtual device and connected CDJs.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/mediadb"
	"github.com/vynulldev/vynull/pdb"
)

// Server exposes device state via a JSON HTTP API.
type Server struct {
	Device       *device.VirtualDevice
	Library      *library.Library
	PDB          *pdb.Database
	Analysis     *analysis.Store
	Cues         CueStoreInterface
	Tags         TagStoreInterface
	Playlists    *PlaylistStore
	Menu         *MenuStore
	Settings     *device.CDJSettings // CDJ settings, written into USB exports
	DBServer     DBServerControl     // optional; if set, /api/unlink uses it
	LazyAnalysis bool                // if true, defer analysis to CDJ request time
	MusicDir     string              // source music root for USB export pathing
	BrowseRoots  []string            // extra roots the "add files/folders" browser may access
	Port         int                 // legacy: when Listen is empty, used as 127.0.0.1:<Port>
	Listen       string              // full listen address (e.g. "0.0.0.0:9443"). Takes precedence over Port.
	Web          bool                // if true, serve the HTML UI at /
	CacheDir     string              // disk cache root for rendered waveform PNGs etc.
	Overlay      *OverlayStore       // now-playing streaming-overlay config (optional)

	// ExtArtwork, if set, returns cover-art JPEG bytes for a track a deck plays
	// from another player's media (downloaded over NFS by package mediadb).
	// Returns nil when not yet cached; the /api/artwork/ext endpoint 404s and
	// the UI retries on its next poll.
	ExtArtwork func(player, slot uint8, trackID uint32) []byte

	// ExtAnalysis, if set, returns the ANLZ-derived waveform + cues for a track a
	// deck plays from another player's media (from package mediadb). nil until
	// cached; the /api/analysis/ext endpoint 404s and the UI retries.
	ExtAnalysis func(player, slot uint8, trackID uint32) *mediadb.Analysis

	// Set on first Start(); used by the diagnostic endpoints.
	started time.Time
	logs    *logRing

	// artInFlight dedups concurrent lazy artwork extractions per track so two
	// requests for the same un-probed track don't both spawn ffmpeg / race on
	// the track fields. Keyed by trackID.
	artInFlight sync.Map

	// addJobs tracks in-flight/recently-finished background bulk adds
	// (large folder adds). Keyed by job ID; value is *addJob.
	addJobs sync.Map

	trackList trackListCache // GET /api/tracks's body, per revision

	// importProg is the pollable phase/progress of an in-flight rekordbox import.
	// The import POST is synchronous; the web UI polls /api/import/status while
	// it runs to drive a progress bar. Guarded by importProgMu.
	importProgMu sync.Mutex
	importProg   importProgress
}

// DBServerControl exposes the slice of dbserver functionality the API
// layer needs without forcing a direct dependency on the dbserver package.
type DBServerControl interface {
	// Unlink tears down all live CDJ sessions and returns how many were closed.
	Unlink() int
}

// CueStoreInterface abstracts the cue store for the API layer.
type CueStoreInterface interface {
	GetCues(trackID uint32) []CueInfo
	AllCues() map[uint32][]CueInfo
	SaveCue(trackID uint32, cue CueInfo)
	SaveCues(trackID uint32, cues []CueInfo)
	DeleteCue(trackID uint32, cueNumber uint16)
	DeleteAllForTrack(trackID uint32)
	Rev() uint64 // moves on every change to any track's cues
}

// Handler builds the mux with every API endpoint registered. Extracted
// from Start so tests can wrap it in httptest.NewServer without binding
// real ports or starting any of the device / NFS / dbserver listeners.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/players", s.handlePlayers)
	mux.HandleFunc("/api/nowplaying", s.handleNowPlaying)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/overlay/config", s.handleOverlayConfig)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/tracks", s.handleTracks)
	mux.HandleFunc("/api/tracks/rev", s.handleTracksRev)
	mux.HandleFunc("/api/tracks/color", s.handleTracksColor)
	mux.HandleFunc("/api/tracks/rating", s.handleTracksRating)
	mux.HandleFunc("/api/tracks/tags", s.handleTracksTags)
	mux.HandleFunc("/api/tracks/add", s.handleAddTracks)
	mux.HandleFunc("/api/tracks/add/status", s.handleAddStatus)
	mux.HandleFunc("/api/fs/list", s.handleFSList)
	mux.HandleFunc("/api/tracks/reimport", s.handleReimportTracks)
	mux.HandleFunc("/api/export/preview", s.handleExportPreview)
	mux.HandleFunc("/api/import/rekordbox", s.handleImportRekordbox)
	mux.HandleFunc("/api/import/status", s.handleImportStatus)
	mux.HandleFunc("/api/library/remap-paths", s.handleRemapPaths)
	mux.HandleFunc("/api/load", s.handleLoadTrack)

	mux.HandleFunc("/api/diag", s.handleDiag)
	mux.HandleFunc("/api/diag/logs", s.handleDiagLogs)
	mux.HandleFunc("/api/diag/status", s.handleDiagStatus)

	mux.HandleFunc("/api/link", s.handleLink)

	// Analysis endpoints
	mux.HandleFunc("/api/audio/", s.handleAudio)
	mux.HandleFunc("/api/analysis/", s.handleAnalysis)
	mux.HandleFunc("/api/analysis/ext/", s.handleExtAnalysis)
	mux.HandleFunc("/api/analysis/reanalyze/", s.handleReanalyze)
	mux.HandleFunc("/api/analysis/beatgrid/adjust", s.handleBeatGridAdjust)
	mux.HandleFunc("/api/analysis/waveform/", s.handleWaveform)
	mux.HandleFunc("/api/analysis/waveform-png/", s.handleWaveformPNG)
	mux.HandleFunc("/api/artwork/ext/", s.handleExtArtwork)
	mux.HandleFunc("/api/artwork/", s.handleArtwork)
	mux.HandleFunc("/api/export", s.handleExport)

	// CDJ settings endpoints
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/settings/enums", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, device.SettingsEnums())
	})

	// Tags endpoints
	mux.HandleFunc("/api/cues", s.handleAllCues)
	mux.HandleFunc("/api/cues/rev", s.handleCuesRev)
	mux.HandleFunc("/api/unlink", s.handleUnlink)
	mux.HandleFunc("/api/tags", s.handleTags)
	mux.HandleFunc("/api/tags/", s.handleTagByID)
	mux.HandleFunc("/api/tag-categories", s.handleTagCategories)
	mux.HandleFunc("/api/tag-categories/", s.handleTagCategoryByID)
	mux.HandleFunc("/api/playlists", s.handlePlaylists)
	mux.HandleFunc("/api/playlists/", s.handlePlaylistByID)
	mux.HandleFunc("/api/hotcuebanks", s.handleHotCueBanks)
	mux.HandleFunc("/api/hotcuebanks/", s.handleHotCueBankByID)
	mux.HandleFunc("/api/menu-items", s.handleMenuItems)
	mux.HandleFunc("/api/menu-items/reset", s.handleMenuItemsReset)

	// Track sub-resource endpoints. Dispatch on the exact action SEGMENT
	// (/api/tracks/{id}/{action}[/...]) rather than a substring match, so
	// action names can't collide (e.g. a future "/colors" vs "/color") and
	// position is unambiguous. No action segment → the single-track handler.
	mux.HandleFunc("/api/tracks/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tracks/"), "/")
		action := ""
		if len(parts) >= 2 {
			action = parts[1]
		}
		switch action {
		case "cues":
			s.handleCues(w, r)
		case "tags":
			s.handleTrackTags(w, r)
		case "color":
			s.handleTrackColor(w, r)
		case "rating":
			s.handleTrackRating(w, r)
		case "metadata":
			s.handleTrackMetadata(w, r)
		case "path":
			s.handleTrackPath(w, r)
		case "phrases":
			s.handleTrackPhrases(w, r)
		case "beats":
			s.handleTrackBeats(w, r)
		case "lock":
			s.handleTrackLock(w, r)
		case "related":
			s.handleTrackRelated(w, r)
		default:
			s.handleTracks(w, r)
		}
	})

	// Any other path under /api/ is an unknown endpoint, answered in JSON
	// like every API error rather than with the web UI's or the mux's text.
	mux.HandleFunc("/api/", handleUnknownAPI)

	// API documentation (spec + self-hosted Swagger UI). Always on, so the API
	// is self-describing even for a headless daemon with --web off.
	registerAPIDocs(mux)

	if s.Web {
		RegisterWebUI(mux)
	}
	return corsMiddleware(mux)
}

// corsMiddleware adds permissive CORS to every response and answers preflight
// OPTIONS requests itself, so a frontend served from another origin or device
// can call the API, and no handler sets CORS headers or sees an OPTIONS
// request. Vynull is a trusted-LAN tool with no auth; this keeps that open
// posture the same on every endpoint.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, HEAD, OPTIONS")
		// Allow the conditional-request header our ETag endpoints read, and
		// expose the response headers a cross-origin client needs to see:
		// ETag (so /api/tracks revalidation works off the header, not just
		// /api/tracks/rev) and X-Audio-Source (the DIRECT/TRANSCODED label).
		// Without these a browser on another origin can't send If-None-Match
		// or read either header, though same-origin at /app works.
		h.Set("Access-Control-Allow-Headers", "Content-Type, If-None-Match")
		h.Set("Access-Control-Expose-Headers", "ETag, X-Audio-Source")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start begins serving the HTTP API. Blocks until context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	s.started = time.Now()
	s.installLogTail()
	mux := s.Handler()
	addr := s.Listen
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", s.Port)
	}
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	dlog.Infof("api: listening on %s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func parseTrackIDFromPath(path, prefix string) uint32 {
	s := strings.TrimPrefix(path, prefix)
	s = strings.Split(s, "/")[0]
	s = strings.Split(s, "?")[0]
	var id uint32
	fmt.Sscanf(s, "%d", &id)
	return id
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

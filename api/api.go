// SPDX-License-Identifier: GPL-3.0-or-later

// Package api provides an HTTP API for external applications to query
// the state of the Pro DJ Link virtual device and connected CDJs.
package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dhowden/tag"
	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/export"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/link/prolink"
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

	// Set on first Start(); used by the diagnostic endpoints.
	started time.Time
	logs    *logRing

	analyzeOnce sync.Once
	analyzeCh   chan analyzeJob
	// queuedAnalyses guards against duplicate enqueues when the same
	// trackID is requested by many in-flight HTTP handlers — typically
	// the library page firing N <img loading="lazy"> waveform fetches in
	// parallel after a cacheVersion bump invalidates the on-disk cache.
	// Keyed by trackID; presence means "queued or in flight".
	queuedAnalyses sync.Map

	// Debounced library save for lazy artwork extraction: handleArtwork sets
	// ArtID/ArtChecked on first view and coalesces the resulting library.json
	// writes so scrolling the ART column doesn't trigger a save per row.
	artSaveMu    sync.Mutex
	artSaveTimer *time.Timer
	// artInFlight dedups concurrent lazy artwork extractions per track so two
	// requests for the same un-probed track don't both spawn ffmpeg / race on
	// the track fields. Keyed by trackID.
	artInFlight sync.Map

	// addJobs tracks in-flight/recently-finished background bulk adds
	// (large folder adds). Keyed by job ID; value is *addJob.
	addJobs sync.Map

	// importProg is the pollable phase/progress of an in-flight rekordbox import.
	// The import POST is synchronous; the web UI polls /api/import/status while
	// it runs to drive a progress bar. Guarded by importProgMu.
	importProgMu sync.Mutex
	importProg   importProgress
}

// importProgress is the shared, pollable state of a rekordbox import.
type importProgress struct {
	Active bool   `json:"active"`
	Phase  string `json:"phase"`
	Done   int    `json:"done"`  // items processed in the current counted phase
	Total  int    `json:"total"` // items in the current counted phase (0 = indeterminate)
}

func (s *Server) importBegin(phase string) {
	s.importProgMu.Lock()
	s.importProg = importProgress{Active: true, Phase: phase}
	s.importProgMu.Unlock()
}
func (s *Server) importPhase(phase string) {
	s.importProgMu.Lock()
	s.importProg.Phase, s.importProg.Done, s.importProg.Total = phase, 0, 0
	s.importProgMu.Unlock()
}
func (s *Server) importCount(phase string, done, total int) {
	s.importProgMu.Lock()
	s.importProg.Phase, s.importProg.Done, s.importProg.Total = phase, done, total
	s.importProgMu.Unlock()
}
func (s *Server) importEnd() {
	s.importProgMu.Lock()
	s.importProg = importProgress{}
	s.importProgMu.Unlock()
}
func (s *Server) importBusy() bool {
	s.importProgMu.Lock()
	defer s.importProgMu.Unlock()
	return s.importProg.Active
}

// handleImportStatus reports the progress of an in-flight import (polled by the
// web UI). GET /api/import/status.
func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	s.importProgMu.Lock()
	p := s.importProg
	s.importProgMu.Unlock()
	writeJSON(w, p)
}

// artExtractSem bounds concurrent lazy ffmpeg artwork probes so a fast scroll
// over a fresh import doesn't spawn one ffmpeg process per visible row at once.
var artExtractSem = make(chan struct{}, 4)

// scheduleArtworkSave persists the library ~2s after the last lazy-artwork
// extraction, coalescing a burst of per-track writes into one.
func (s *Server) scheduleArtworkSave() {
	if s.Library == nil {
		return
	}
	s.artSaveMu.Lock()
	defer s.artSaveMu.Unlock()
	if s.artSaveTimer != nil {
		return // a save is already pending
	}
	s.artSaveTimer = time.AfterFunc(2*time.Second, func() {
		s.artSaveMu.Lock()
		s.artSaveTimer = nil
		s.artSaveMu.Unlock()
		s.Library.Save()
	})
}

// flushArtworkSave persists immediately if a debounced artwork save is pending,
// so ArtID/ArtChecked extracted just before shutdown aren't lost (which would
// force ffmpeg to re-probe those tracks on the next view).
func (s *Server) flushArtworkSave() {
	s.artSaveMu.Lock()
	pending := s.artSaveTimer != nil
	if pending {
		s.artSaveTimer.Stop()
		s.artSaveTimer = nil
	}
	s.artSaveMu.Unlock()
	if pending && s.Library != nil {
		s.Library.Save()
	}
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
	DeleteCue(trackID uint32, cueNumber uint16)
	DeleteAllForTrack(trackID uint32)
}

// CueInfo is the API representation of a cue point.
type CueInfo struct {
	Number  uint16 `json:"number"`   // 1=A, 2=B, 3=C, ...
	Type    string `json:"type"`     // "cue" or "loop"
	TimeMs  uint32 `json:"time_ms"`  // position in milliseconds
	LoopMs  int32  `json:"loop_ms"`  // loop end in ms (-1 if not loop)
	ColorID uint32 `json:"color_id"` // 0=default, 1=pink, 2=red, etc.
	Label   string `json:"label"`    // optional label
}

type analyzeJob struct {
	trackID  uint32
	filePath string
}

// PeerInfo is the JSON representation of a connected device.
type PeerInfo struct {
	DeviceNumber uint8  `json:"device_number"`
	Name         string `json:"name"`
	DeviceType   string `json:"device_type"`
	IP           string `json:"ip"`
	MAC          string `json:"mac"`
	// Mixer is populated only for DeviceType=="mixer" and only when
	// at least one 0x29 status broadcast has been received from this
	// device since startup. Fields are best-effort and reflect the
	// most recent broadcast.
	Mixer *MixerInfo `json:"mixer,omitempty"`
}

// MixerInfo is the API-side projection of proto.MixerStatus.
type MixerInfo struct {
	MasterDevice      uint8   `json:"master_device"` // 0 = no master
	MasterBPM         float64 `json:"master_bpm"`
	BeatInBar         uint8   `json:"beat_in_bar"`         // 1..4 (0 if not parsed)
	ChannelOnAir      []bool  `json:"channel_on_air"`      // index 0 = ch1
	ChannelStateKnown bool    `json:"channel_state_known"` // true after we've seen at least one channel-state packet
}

// PlayerInfo is the JSON representation of a CDJ's current state.
type PlayerInfo struct {
	DeviceNumber  uint8   `json:"device_number"`
	Name          string  `json:"name,omitempty"` // CDJ display name
	TrackID       uint32  `json:"track_id,omitempty"`
	TrackTitle    string  `json:"track_title"`
	Artist        string  `json:"artist"`
	BPM           float64 `json:"bpm"`
	PitchPct      float64 `json:"pitch_pct"` // pitch as percent (-100..+100); 0 = nominal
	Key           string  `json:"key"`
	IsPlaying     bool    `json:"is_playing"`
	IsMaster      bool    `json:"is_master"`
	IsSync        bool    `json:"is_sync,omitempty"`
	OnAir         bool    `json:"on_air"`
	BeatInBar     uint8   `json:"beat_in_bar"`               // 1-4 (0 = unknown), from CDJ status packet
	BeatInTrack   uint32  `json:"beat_in_track,omitempty"`   // beats elapsed since track start (0 = unknown)
	DurationMs    uint32  `json:"duration_ms,omitempty"`     // loaded track duration in ms (for playhead positioning)
	PlayState     uint8   `json:"play_state,omitempty"`      // raw play-state byte from CDJ status
	PlayStateName string  `json:"play_state_name,omitempty"` // human-readable form (PLAYING, PAUSED, CUED, ...)
}

// StatusResponse is the top-level API status response.
type StatusResponse struct {
	DeviceName   string          `json:"device_name"`
	DeviceNumber uint8           `json:"device_number"`
	TrackCount   int             `json:"track_count"`
	Peers        []PeerInfo      `json:"peers"`
	Players      []PlayerInfo    `json:"players"`
	Analysis     *AnalysisStatus `json:"analysis,omitempty"`
}

// AnalysisStatus reports on the analysis engine's current state.
type AnalysisStatus struct {
	Status   string `json:"status"`   // human-readable status line
	Pending  int    `json:"pending"`  // tracks queued for analysis
	Analyzed int    `json:"analyzed"` // total analyzed tracks
	Cached   int    `json:"cached"`   // loaded from disk cache
}

// Start begins serving the HTTP API. Blocks until context is cancelled.
// Handler builds the mux with every API endpoint registered. Extracted
// from Start so tests can wrap it in httptest.NewServer without binding
// real ports or starting any of the device / NFS / dbserver listeners.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/players", s.handlePlayers)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/tracks", s.handleTracks)
	mux.HandleFunc("/api/tracks/rev", s.handleTracksRev)
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
	mux.HandleFunc("/api/analysis/", s.handleAnalysis)
	mux.HandleFunc("/api/analysis/reanalyze/", s.handleReanalyze)
	mux.HandleFunc("/api/analysis/beatgrid/adjust", s.handleBeatGridAdjust)
	mux.HandleFunc("/api/analysis/waveform/", s.handleWaveform)
	mux.HandleFunc("/api/analysis/waveform-png/", s.handleWaveformPNG)
	mux.HandleFunc("/api/artwork/", s.handleArtwork)
	mux.HandleFunc("/api/export", s.handleExport)

	// CDJ settings endpoints
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/settings/enums", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, device.SettingsEnums())
	})

	// Tags endpoints
	mux.HandleFunc("/api/cues", s.handleAllCues)
	mux.HandleFunc("/api/unlink", s.handleUnlink)
	mux.HandleFunc("/api/tags", s.handleTags)
	mux.HandleFunc("/api/tags/", s.handleTagByID)
	mux.HandleFunc("/api/tag-categories", s.handleTagCategories)
	mux.HandleFunc("/api/tag-categories/", s.handleTagCategoryByID)
	mux.HandleFunc("/api/playlists", s.handlePlaylists)
	mux.HandleFunc("/api/playlists/", s.handlePlaylistByID)
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
		default:
			s.handleTracks(w, r)
		}
	})

	if s.Web {
		RegisterWebUI(mux)
	}
	return mux
}

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
		s.flushArtworkSave() // persist any pending lazy-artwork writes
	}()

	log.Printf("api: listening on %s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		DeviceName:   s.Device.Name,
		DeviceNumber: s.Device.DeviceNumber,
		TrackCount:   int(s.Device.TrackCount),
		Peers:        s.getPeers(),
		Players:      s.getPlayers(),
	}
	if s.Analysis != nil {
		resp.Analysis = &AnalysisStatus{
			Status:   s.Analysis.Status(),
			Pending:  int(s.Analysis.Pending()),
			Analyzed: s.Analysis.Count(),
			Cached:   s.Analysis.CachedCount(),
		}
	}
	writeJSON(w, resp)
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.getPeers())
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.getPlayers())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.Device.Monitor == nil {
		writeJSON(w, []struct{}{})
		return
	}
	type HistoryItem struct {
		StartedAt    string  `json:"started_at"`
		EndedAt      string  `json:"ended_at"`
		DeviceNumber uint8   `json:"device_number"`
		DeviceName   string  `json:"device_name"`
		TrackID      uint32  `json:"track_id"`
		Title        string  `json:"title"`
		Artist       string  `json:"artist"`
		BPM          float64 `json:"bpm"`
		Key          string  `json:"key"`
	}
	history := s.Device.Monitor.History()
	items := make([]HistoryItem, len(history))
	for i, h := range history {
		items[i] = HistoryItem{
			StartedAt:    h.StartedAt.Format(time.RFC3339),
			EndedAt:      h.EndedAt.Format(time.RFC3339),
			DeviceNumber: h.DeviceNumber,
			DeviceName:   h.DeviceName,
			TrackID:      h.TrackID,
			Title:        h.Title,
			Artist:       h.Artist,
			BPM:          h.BPM,
			Key:          h.Key,
		}
	}
	writeJSON(w, items)
}

func (s *Server) getPeers() []PeerInfo {
	if s.Device.Peers == nil {
		return []PeerInfo{}
	}
	peers := s.Device.Peers.Peers()
	mixers := s.Device.MixerSnapshot()
	out := make([]PeerInfo, len(peers))
	for i, p := range peers {
		dt := "unknown"
		switch p.DeviceType {
		case 0x01:
			dt = "mixer"
		case 0x02:
			dt = "cdj"
		case 0x03:
			dt = "rekordbox"
		}
		pi := PeerInfo{
			DeviceNumber: p.DeviceNumber,
			Name:         p.Name,
			DeviceType:   dt,
			IP:           p.IP.String(),
			MAC:          p.MAC.String(),
		}
		if dt == "mixer" {
			if mx, ok := mixers[p.DeviceNumber]; ok {
				onAir := make([]bool, 4)
				for k := 0; k < 4; k++ {
					onAir[k] = (mx.ChannelOnAir>>uint(k))&1 == 1
				}
				pi.Mixer = &MixerInfo{
					MasterDevice:      mx.MasterDevice,
					MasterBPM:         mx.MasterBPM,
					BeatInBar:         mx.BeatInBar,
					ChannelOnAir:      onAir,
					ChannelStateKnown: mx.ChannelStateKnown,
				}
			}
		}
		out[i] = pi
	}
	return out
}

func (s *Server) getPlayers() []PlayerInfo {
	if s.Device.Monitor == nil {
		return []PlayerInfo{}
	}
	states := s.Device.Monitor.States()
	out := make([]PlayerInfo, 0, len(states))
	for _, ps := range states {
		if ps.Status == nil {
			continue
		}
		pitchPct := float64(ps.Status.Pitch-0x100000) / float64(0x100000) * 100.0
		// Track duration from library — needed by the web UI's playhead to
		// position itself proportionally on the waveform. If the track isn't
		// in the library (yet/anymore), leave it at 0 and the UI hides the
		// playhead.
		var durationMs uint32
		if ps.Status.TrackID != 0 && s.Library != nil {
			if t := s.Library.Track(ps.Status.TrackID); t != nil && t.Duration > 0 {
				durationMs = uint32(t.Duration.Duration().Milliseconds())
			}
		}
		out = append(out, PlayerInfo{
			DeviceNumber:  ps.Status.DeviceNumber,
			Name:          ps.Status.Name,
			TrackID:       ps.Status.TrackID,
			TrackTitle:    ps.TrackName,
			Artist:        ps.Artist,
			BPM:           float64(ps.Status.BPM) / 100.0,
			PitchPct:      pitchPct,
			Key:           ps.Key,
			IsPlaying:     ps.Status.IsPlaying,
			IsMaster:      ps.Status.IsMaster,
			IsSync:        ps.Status.IsSync,
			OnAir:         ps.Status.IsOnAir,
			BeatInBar:     ps.Status.BeatInBar,
			BeatInTrack:   ps.Status.BeatInTrack,
			DurationMs:    durationMs,
			PlayState:     ps.Status.PlayState,
			PlayStateName: ps.Status.PlayStateString(),
		})
	}
	// Monitor.States() returns a map; iteration order is non-deterministic,
	// which made the web UI's Players tab swap player positions on each
	// 1s poll. Sort by device number for stable rendering.
	sort.Slice(out, func(i, j int) bool {
		return out[i].DeviceNumber < out[j].DeviceNumber
	})
	return out
}

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
	PlayCount      int      `json:"play_count,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	ArtID          uint32   `json:"art_id,omitempty"`
	FileMissing    bool     `json:"file_missing,omitempty"`
}

// trackColorNames maps the 8-entry rekordbox track-colour palette to
// human names. Index 0 is "no colour" → empty string.
var trackColorNames = map[uint8]string{
	1: "Pink", 2: "Red", 3: "Orange", 4: "Yellow",
	5: "Green", 6: "Aqua", 7: "Blue", 8: "Purple",
}

// libTrackToInfo converts a library.Track to the API TrackInfo shape,
// including tag names when a tag store is configured. Centralised so
// every endpoint that returns tracks (list, single, playlist tracks)
// uses the same field set and date format.
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
		ColorName:      trackColorNames[t.ColorID],
		PlayCount:      t.PlayCount,
		ArtID:          t.ArtID,
		FileMissing:    t.FileMissing,
	}
	if !t.DateAdded.IsZero() {
		info.DateAdded = t.DateAdded.Format("2006-01-02")
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
			s.Library.RemoveTrack(trackID)
			log.Printf("api: deleted track %d (library + playlists + tags + cues)", trackID)
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

	// Send the load track command
	err := s.Device.LoadTrackOnCDJ(req.TrackID, req.DeviceNumber, targetIP)
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

// handleExportPreview returns a summary of what the export will contain
// for a given source (?source=all|playlist:N|smart:N). The web UI uses
// this to warn the user before exporting — e.g. "3 tracks will be
// re-encoded due to decode errors that would freeze the deck".
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// handleImportRekordbox imports tracks (and optionally playlists/tags)
// from a rekordbox XML export OR a rekordbox 6 master.db. The body
// chooses which path:
//
//	{"path": "/.../master.db", "key": "<sqlcipher key>"}      // master.db
//	{"path": "/.../rekordbox.xml"}                              // XML
//
// Auto-detected from extension if not obvious.
func (s *Server) handleImportRekordbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path      string `json:"path"`
		Key       string `json:"key"`
		RemapFrom string `json:"remap_from"`
		RemapTo   string `json:"remap_to"`
		// Include lets the caller choose what to import. Each field is a
		// pointer so an omitted field (or an absent "include" object) defaults
		// to true — keeping the legacy "import everything" behaviour for older
		// clients that don't send it.
		Include *struct {
			Tracks    *bool `json:"tracks"`
			Playlists *bool `json:"playlists"`
			Tags      *bool `json:"tags"`
			Analysis  *bool `json:"analysis"`
			Artwork   *bool `json:"artwork"`
			Cues      *bool `json:"cues"`
			Settings  *bool `json:"settings"`
		} `json:"include"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	// Resolve the include flags (nil → true). incTracks gates the core track
	// import; the rest gate the per-category materialization below. Playlists,
	// tags, analysis, artwork, and cues all derive from the track import, so
	// they only take effect when tracks are imported too.
	want := func(p *bool) bool { return p == nil || *p }
	incTracks, incPlaylists, incTags := true, true, true
	incAnalysis, incArtwork, incCues, incSettings := true, true, true, true
	if req.Include != nil {
		incTracks = want(req.Include.Tracks)
		incPlaylists = want(req.Include.Playlists)
		incTags = want(req.Include.Tags)
		incAnalysis = want(req.Include.Analysis)
		incArtwork = want(req.Include.Artwork)
		incCues = want(req.Include.Cues)
		incSettings = want(req.Include.Settings)
	}
	if _, err := os.Stat(req.Path); err != nil {
		http.Error(w, "path not found: "+err.Error(), http.StatusBadRequest)
		return
	}

	// One import at a time — the progress state (and the library writes) assume
	// a single in-flight import. The UI disables its button too, but guard here
	// against a second concurrent request.
	if s.importBusy() {
		http.Error(w, "an import is already running", http.StatusConflict)
		return
	}
	s.importBegin("Reading rekordbox database…")
	defer s.importEnd()

	ext := strings.ToLower(filepath.Ext(req.Path))
	var result *library.ImportResult
	var playlists []library.PlaylistImport
	var tags []library.TagImport
	var colors []library.ColorImport
	var assets []library.ImportedAsset   // ANLZ + artwork paths (.zip, or .db in its lib folder)
	var masterCues []library.ImportedCue // cue points from djmdCue (.db/.zip)
	var shareRoot string                 // share/ root (.zip extract, or a .db's own folder)
	var settingsDir string               // *SETTING.DAT dir (.zip extract, or a .db's own folder)
	var bundle *library.ImportBundle     // importer side-data; destructured into the locals below
	var err error

	switch ext {
	case ".xml":
		if incTracks {
			bundle, err = library.ImportRekordboxXML(s.Library, req.Path)
		}
	case ".nml":
		if incTracks {
			bundle, err = library.ImportTraktorNML(s.Library, req.Path)
		}
	case ".db":
		// The master.db is SQLCipher-encrypted; the user supplies the 64-hex
		// key (the import dialog collects it). We ship no key and don't extract
		// one, so it is required here.
		key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.Key), "0x"))
		if !isHex64(key) {
			http.Error(w, "a 64-hex SQLCipher key is required", http.StatusBadRequest)
			return
		}
		if incTracks {
			bundle, err = library.ImportRekordboxMasterDB(s.Library, req.Path, key)
		}
		// A master.db that still lives in its rekordbox library folder has its
		// analysis (share/PIONEER/USBANLZ), artwork (share/PIONEER/Artwork), and
		// *SETTING.DAT blobs right beside it — the same layout a backup zip
		// mirrors. Point shareRoot/settingsDir at the DB's own directory so the
		// asset + settings import below picks them up, making a bare-.db import
		// nearly as complete as a .zip. (A DB copied out on its own has no
		// neighbours, so these stay empty and nothing extra is imported.)
		dbDir := filepath.Dir(req.Path)
		if fi, e := os.Stat(filepath.Join(dbDir, "share")); e == nil && fi.IsDir() {
			shareRoot = filepath.Join(dbDir, "share")
		}
		if incSettings && s.Settings != nil {
			hasSettings := func(dir string) bool {
				for _, sf := range rekordboxSettingsFiles {
					if _, e := os.Stat(filepath.Join(dir, sf)); e == nil {
						return true
					}
				}
				return false
			}
			// rekordbox 6 keeps the *SETTING.DAT blobs in a sibling "rekordbox6"
			// folder (master.db lives in "rekordbox"); a backup zip instead puts
			// them beside the db. Check the sibling first, then the db's dir.
			for _, cand := range []string{filepath.Join(filepath.Dir(dbDir), "rekordbox6"), dbDir} {
				if hasSettings(cand) {
					settingsDir = cand
					break
				}
			}
		}
	case ".zip":
		// rekordbox library backup: master.db (+ share/PIONEER analysis &
		// artwork, + *SETTING.DAT blobs) inside a zip. Extract to a temp dir,
		// import the DB, then pull in the ANLZ waveforms/beat grids, cover art,
		// and settings below. The master.db is SQLCipher-encrypted; the user
		// supplies the 64-hex key (we ship none and don't extract one).
		key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.Key), "0x"))
		if !isHex64(key) {
			http.Error(w, "a 64-hex SQLCipher key is required", http.StatusBadRequest)
			return
		}
		tmp, terr := os.MkdirTemp("", "rb-backup-*")
		if terr != nil {
			http.Error(w, "could not create temp dir: "+terr.Error(), http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(tmp)
		var dbPath string
		s.importPhase("Extracting backup…")
		dbPath, shareRoot, settingsDir, err = extractRekordboxBackup(req.Path, tmp, incSettings && s.Settings != nil)
		if err != nil {
			http.Error(w, "extract backup: "+err.Error(), http.StatusBadRequest)
			return
		}
		if incTracks {
			s.importPhase("Reading rekordbox database…")
			bundle, err = library.ImportRekordboxMasterDB(s.Library, dbPath, key)
		}
	default:
		http.Error(w, "path must be a .xml, .nml (Traktor), .db, or .zip (rekordbox backup) file", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "import failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Spread the importer's side-data into the locals the apply pipeline below
	// reads. shareRoot/settingsDir stay handler-managed (set per case above).
	if bundle != nil {
		result = bundle.Result
		playlists = bundle.Playlists
		tags = bundle.Tags
		colors = bundle.Colors
		assets = bundle.Assets
		masterCues = bundle.Cues
	}
	// !incTracks paths skip the importer entirely (e.g. a settings-only zip
	// import), so synthesize an empty result for the materialization + summary.
	if result == nil {
		result = &library.ImportResult{}
	}

	// Optional path-prefix remap (e.g. "Z:/Music/" → "/media/usb/Music/").
	// Applied after import so it covers tracks added in *this* import as
	// well as any already present with the matching prefix.
	if req.RemapFrom != "" && req.RemapTo != "" {
		n, _ := s.Library.RemapPaths(req.RemapFrom, req.RemapTo)
		log.Printf("import: remapped %d track paths %q -> %q", n, req.RemapFrom, req.RemapTo)
	}

	s.Library.Save()

	// Materialize the playlist tree (folders and leaf playlists) into
	// the playlist store. Re-importing creates new entries — we don't
	// try to dedupe by name since the user may legitimately want both.
	if s.Playlists != nil && incPlaylists {
		if len(playlists) > 0 {
			s.importPhase("Importing playlists…")
		}
		var created, skipped, smart int
		var walk func(parent uint32, nodes []library.PlaylistImport)
		walk = func(parent uint32, nodes []library.PlaylistImport) {
			for _, n := range nodes {
				var p *PlaylistInfo
				var perr error
				if n.IsSmart && n.Smart != nil {
					// Translate rekordbox rules → SmartRules and create a
					// rule-based playlist (membership computed on read).
					rules, mapped, dropped := smartRulesFromRekordbox(n.Smart)
					if mapped == 0 {
						// Nothing we could map (an empty rule set matches the
						// whole library) — make a plain empty playlist instead.
						log.Printf("import: smart playlist %q: no supported conditions, creating empty", n.Name)
						p, perr = s.Playlists.Create(n.Name, parent, false)
					} else {
						if dropped > 0 {
							log.Printf("import: smart playlist %q: %d condition(s) dropped (unsupported)", n.Name, dropped)
						}
						p, perr = s.Playlists.CreateSmart(n.Name, parent, rules)
						smart++
					}
				} else {
					p, perr = s.Playlists.Create(n.Name, parent, n.IsFolder)
				}
				if perr != nil {
					skipped++
					log.Printf("import: create playlist %q failed: %v", n.Name, perr)
					continue
				}
				created++
				if !n.IsFolder && !n.IsSmart && len(n.TrackIDs) > 0 {
					if terr := s.Playlists.SetTracks(p.ID, n.TrackIDs); terr != nil {
						log.Printf("import: SetTracks for playlist %q failed: %v", n.Name, terr)
					}
				}
				if len(n.Children) > 0 {
					walk(p.ID, n.Children)
				}
			}
		}
		walk(0, playlists)
		log.Printf("import: materialized %d playlist nodes (%d smart, %d skipped)", created, smart, skipped)
	}

	// Batch all tag/category/color writes into a single persist at the end of
	// the import — otherwise each CreateTag/SetTagsForTrack/SetTrackColor
	// rewrites the whole JSON file, which is O(n²) over hundreds of assignments.
	if s.Tags != nil {
		s.Tags.BeginBatch()
		defer s.Tags.EndBatch()
	}

	// Materialize MyTags (from the XML's comment encoding). Each tag is
	// created uncategorized if it doesn't already exist by name, then
	// assigned to the imported tracks. Existing per-track tag sets are
	// unioned so a re-import doesn't drop tags applied elsewhere.
	if s.Tags != nil && incTags && len(tags) > 0 {
		s.importPhase("Importing tags…")
		byName := make(map[string]TagInfo)
		for _, t := range s.Tags.GetAllTags() {
			byName[t.Name] = t
		}
		// Resolve MyTag categories by name, creating any that don't exist yet
		// so imported tags land under their rekordbox category (Genre, Mood…).
		catByName := make(map[string]uint32)
		for _, c := range s.Tags.GetAllCategories() {
			catByName[c.Name] = c.ID
		}
		ensureCategory := func(name string) uint32 {
			if name == "" {
				return 0
			}
			if id, ok := catByName[name]; ok {
				return id
			}
			id, err := s.Tags.CreateCategory(name)
			if err != nil {
				log.Printf("import: create tag category %q failed: %v", name, err)
				return 0
			}
			catByName[name] = id
			return id
		}
		var tagged int
		for _, ti := range tags {
			catID := ensureCategory(ti.Category)
			cur, ok := byName[ti.Name]
			var id uint32
			if !ok {
				newID, err := s.Tags.CreateTag(ti.Name, catID)
				if err != nil {
					log.Printf("import: create tag %q failed: %v", ti.Name, err)
					continue
				}
				id = newID
				byName[ti.Name] = TagInfo{ID: id, Name: ti.Name, CategoryID: catID}
			} else {
				id = cur.ID
				// Adopt the imported category only if the tag is currently
				// uncategorized — don't clobber a category the user set.
				if catID != 0 && cur.CategoryID == 0 {
					s.Tags.SetTagCategory(id, catID)
					cur.CategoryID = catID
					byName[ti.Name] = cur
				}
			}
			for _, trackID := range ti.TrackIDs {
				existing := s.Tags.GetTagsForTrack(trackID)
				ids := []uint32{id}
				has := false
				for _, e := range existing {
					if e.ID == id {
						has = true
					}
					ids = append(ids, e.ID)
				}
				if has {
					continue
				}
				s.Tags.SetTagsForTrack(trackID, ids)
				tagged++
			}
		}
		log.Printf("import: materialized %d MyTag(s), %d track assignments", len(tags), tagged)
	}

	// Persist track colour labels (from each TRACK's Colour attribute) to
	// the tag store, which is the app's authoritative colour source — the
	// in-memory library Track.ColorID was already set during import.
	if s.Tags != nil && len(colors) > 0 {
		for _, c := range colors {
			s.Tags.SetTrackColor(c.TrackID, c.ColorID)
		}
		log.Printf("import: materialized %d track colour label(s)", len(colors))
	}

	// Import ANLZ analysis (waveforms, beat grids, phrases) and cover art from
	// the share/ tree, so we reuse rekordbox's exact data instead of
	// re-analysing. Populated by a .zip (extracted share/) or by a .db that
	// still sits in its rekordbox library folder (share/ beside it); a .xml or
	// a copied-out .db has no accompanying file tree, so this is skipped.
	if shareRoot != "" && len(assets) > 0 && (incArtwork || incAnalysis || incCues) {
		var artN, anlzN, cueN int
		for i, a := range assets {
			if i%8 == 0 {
				s.importCount("Importing waveforms & artwork…", i, len(assets))
			}
			t := s.Library.Track(a.TrackID)
			if t == nil {
				continue
			}
			if incArtwork && a.ImagePath != "" && s.Library.Artwork != nil {
				p := filepath.Join(shareRoot, filepath.FromSlash(a.ImagePath))
				if data, e := os.ReadFile(p); e == nil && len(data) > 0 {
					t.ArtID = s.Library.Artwork.Add("image/jpeg", data)
					// Mark checked so the startup ffmpeg pass doesn't re-probe
					// (and clobber) this curated rekordbox artwork.
					t.ArtChecked = true
					artN++
				}
			}
			if a.AnalyzePath == "" {
				continue
			}
			dat := filepath.Join(shareRoot, filepath.FromSlash(a.AnalyzePath))
			base := strings.TrimSuffix(dat, filepath.Ext(dat))
			extPath := base + ".EXT"
			if incAnalysis && s.Analysis != nil {
				twoEXPath := base + ".2EX" // CDJ-3000 3-band waveforms (absent in older libraries)
				if res := analysis.ParseANLZ(dat, extPath, twoEXPath, t.BPM, int(t.Duration.Seconds())); res != nil {
					// The ANLZ files don't carry the musical key, so ParseANLZ
					// leaves it blank. Backfill from the DB key (rekordbox
					// stores Camelot or standard notation) so imported tracks
					// report a key like our own analysis does.
					if res.KeyCamelot == "" && t.Key != "" {
						res.KeyCamelot, res.KeyStandard = analysis.KeyNamesFrom(t.Key)
					}
					s.Analysis.SetPath(a.TrackID, t.FilePath)
					s.Analysis.Set(a.TrackID, res)
					anlzN++
				}
			}
			// Cue points (hot + memory) from the ANLZ PCO2/PCOB lists. Hot cues
			// keep their pad number (A=1..H=8); memory cues are numbered 9.. in
			// order, matching the app's cue-number convention.
			if incCues && s.Cues != nil {
				mem := 8
				for _, c := range analysis.ParseANLZCues(extPath, dat) {
					num := uint16(c.HotCue)
					if c.HotCue == 0 {
						mem++
						num = uint16(mem)
					}
					ci := CueInfo{Number: num, Type: "cue", TimeMs: c.TimeMs, LoopMs: -1, ColorID: c.ColorID, Label: c.Comment}
					if c.IsLoop {
						ci.Type = "loop"
						if c.LoopMs > 0 {
							ci.LoopMs = int32(c.LoopMs)
						}
					}
					s.Cues.SaveCue(a.TrackID, ci)
					cueN++
				}
			}
		}
		if artN > 0 {
			s.Library.Save() // persist the new Track.ArtID references
		}
		result.ArtworkImported = artN
		result.AnalysisImported = anlzN
		result.CuesImported = cueN
		log.Printf("import: imported %d artwork image(s), %d ANLZ analysis set(s), %d cue point(s)", artN, anlzN, cueN)
	}

	// Cue points from the master.db djmdCue table — for imports without an
	// ANLZ tree (bare .db) or whose ANLZ files carried no cues. Skipped when
	// the ANLZ pass above already imported cues, so a zip doesn't double up.
	if incCues && s.Cues != nil && result.CuesImported == 0 && len(masterCues) > 0 {
		mems := map[uint32]int{} // per-track memory-cue counter (numbered 9..)
		n := 0
		for _, c := range masterCues {
			num := uint16(c.HotCue)
			if c.HotCue < 1 || c.HotCue > 8 {
				mems[c.TrackID]++
				num = uint16(8 + mems[c.TrackID])
			}
			ci := CueInfo{Number: num, Type: "cue", TimeMs: c.TimeMs, LoopMs: -1, ColorID: c.ColorID, Label: c.Comment}
			if c.LoopMs > 0 {
				ci.Type = "loop"
				ci.LoopMs = c.LoopMs
			}
			s.Cues.SaveCue(c.TrackID, ci)
			n++
		}
		result.CuesImported = n
		log.Printf("import: imported %d cue point(s) from master.db", n)
	}

	// Flag tracks whose audio file isn't present on this machine. A
	// rekordbox DB faithfully carries every track's path, including files on
	// drives we don't have here or paths the remap didn't cover — those are
	// unplayable (the CDJ rejects a load it can't fetch). Mark them so the UI
	// can show it, and report a count. Existence is cheap (os.Stat); scan the
	// whole library so re-imports re-evaluate previously-flagged tracks too.
	if incTracks {
		s.importPhase("Checking files…")
		keyFixed := 0
		var toStat []*library.Track
		for _, t := range s.Library.Tracks() {
			// Standardize key notation to Camelot. rekordbox's ScaleName is a
			// mix of Camelot ("8A") and classic ("Am") depending on the track;
			// our analyzed tracks store Camelot, so normalize imports to match
			// — otherwise the KEY column shows both notations.
			if t.Key != "" {
				if cam, _ := analysis.KeyNamesFrom(t.Key); cam != "" && cam != t.Key {
					t.Key = cam
					keyFixed++
				}
			}
			if t.FilePath == "" {
				t.FileMissing = true
				continue
			}
			toStat = append(toStat, t)
		}
		// Check file presence in parallel — a serial os.Stat over a large or
		// network-backed library is a latency cliff (one slow/absent mount
		// stalls the whole import). Each worker writes a distinct track's flag.
		statCh := make(chan *library.Track)
		var statWG sync.WaitGroup
		statWorkers := 16
		if len(toStat) < statWorkers {
			statWorkers = len(toStat)
		}
		for i := 0; i < statWorkers; i++ {
			statWG.Add(1)
			go func() {
				defer statWG.Done()
				for t := range statCh {
					_, err := os.Stat(t.FilePath)
					t.FileMissing = err != nil
				}
			}()
		}
		for _, t := range toStat {
			statCh <- t
		}
		close(statCh)
		statWG.Wait()
		// Tally results (no syscalls).
		var missing int
		var sample []string
		for _, t := range s.Library.Tracks() {
			if t.FileMissing {
				missing++
				if len(sample) < 5 {
					sample = append(sample, t.FilePath)
				}
			}
		}
		if missing > 0 || keyFixed > 0 {
			s.Library.Save()
		}
		if keyFixed > 0 {
			log.Printf("import: standardized %d track key(s) to Camelot notation", keyFixed)
		}
		if missing > 0 {
			result.FilesMissing = missing
			log.Printf("import: WARNING — %d track(s) reference files not present on this machine (unplayable). Examples: %v", missing, sample)
		}
	}

	// Player/mixer settings (DEVSETTING/MYSETTING/MYSETTING2/DJMMYSETTING)
	// from the backup zip's root *SETTING.DAT blobs. settingsDir is only
	// populated for a .zip when the caller opted in (incSettings); a bare
	// .db/.xml carries no settings files. Merged into the live settings store
	// and persisted, exactly like the --import-settings CLI flag.
	if incSettings && settingsDir != "" && s.Settings != nil {
		cur := s.Settings.Config()
		imported, serr := device.ImportSettingsDir(settingsDir, &cur)
		if serr != nil {
			log.Printf("import: settings import failed: %v", serr)
			result.Errors = append(result.Errors, "settings: "+serr.Error())
		} else if len(imported) > 0 {
			s.Settings.SetConfig(cur)
			result.SettingsImported = len(imported)
			log.Printf("import: imported %d settings file(s): %v", len(imported), imported)
		}
	}

	writeJSON(w, result)
}

// rekordboxSettingsFiles are the player/mixer setting blobs rekordbox writes
// to the root of a library-backup zip (and to /PIONEER on a USB export).
var rekordboxSettingsFiles = []string{
	"MYSETTING.DAT", "MYSETTING2.DAT", "DJMMYSETTING.DAT", "DEVSETTING.DAT",
}

// extractRekordboxBackup unpacks the parts of a rekordbox library-backup zip
// we care about — master.db, the share/PIONEER/{USBANLZ,Artwork} trees, and
// (when wantSettings) the *SETTING.DAT blobs at the zip root — into dest. It
// returns the extracted master.db path, the share/ root, and the directory
// holding the settings files (empty if none were extracted). Other entries
// (lighting DBs, XML prefs, etc.) are skipped. Guards against zip-slip.
func extractRekordboxBackup(zipPath, dest string, wantSettings bool) (dbPath, shareRoot, settingsDir string, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", "", err
	}
	defer zr.Close()

	isSettingsFile := func(name string) bool {
		for _, s := range rekordboxSettingsFiles {
			if name == s {
				return true
			}
		}
		return false
	}

	cleanDest := filepath.Clean(dest)
	gotSettings := false
	for _, f := range zr.File {
		name := f.Name // zip paths use forward slashes
		settings := wantSettings && isSettingsFile(name)
		keep := name == "master.db" ||
			strings.HasPrefix(name, "share/PIONEER/USBANLZ/") ||
			strings.HasPrefix(name, "share/PIONEER/Artwork/") ||
			settings
		if !keep || f.FileInfo().IsDir() {
			continue
		}
		target := filepath.Join(cleanDest, filepath.FromSlash(name))
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			continue // zip-slip: entry escapes dest
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", "", err
		}
		if err := extractZipEntry(f, target); err != nil {
			return "", "", "", err
		}
		if settings {
			gotSettings = true
		}
	}

	dbPath = filepath.Join(cleanDest, "master.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "", "", "", fmt.Errorf("master.db not found in backup zip")
	}
	if gotSettings {
		settingsDir = cleanDest
	}
	return dbPath, filepath.Join(cleanDest, "share"), settingsDir, nil
}

// extractZipEntry copies one zip file entry to target on disk.
func extractZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// handleRemapPaths rewrites every track whose FilePath starts with
// req.From so the prefix becomes req.To. Renames the on-disk analysis
// cache (keyed by hash(file_path)) so the user doesn't have to
// re-analyze after the move. Other per-track caches (artwork, cues,
// waveform-png) are keyed by track ID or art ID and survive untouched.
// refreshMissingFlags re-checks every track's FilePath on disk and updates its
// FileMissing flag. Stats run in parallel — a serial pass over a large or
// network-backed library is a latency cliff. Returns the count still missing.
func (s *Server) refreshMissingFlags() int {
	tracks := s.Library.Tracks()
	var toStat []*library.Track
	for _, t := range tracks {
		if t.FilePath == "" {
			t.FileMissing = true
			continue
		}
		toStat = append(toStat, t)
	}
	if len(toStat) > 0 {
		statCh := make(chan *library.Track)
		var wg sync.WaitGroup
		workers := 16
		if len(toStat) < workers {
			workers = len(toStat)
		}
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range statCh {
					_, err := os.Stat(t.FilePath)
					t.FileMissing = err != nil
				}
			}()
		}
		for _, t := range toStat {
			statCh <- t
		}
		close(statCh)
		wg.Wait()
	}
	missing := 0
	for _, t := range tracks {
		if t.FileMissing {
			missing++
		}
	}
	return missing
}

func (s *Server) handleRemapPaths(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.From == "" || req.To == "" {
		http.Error(w, "both 'from' and 'to' are required", http.StatusBadRequest)
		return
	}
	n, changes := s.Library.RemapPaths(req.From, req.To)
	for _, c := range changes {
		s.Analysis.RenameCachedPath(c[0], c[1])
	}
	// Rewritten paths may now resolve (or stop resolving) on disk — re-check the
	// FileMissing flags so remapped tracks stop showing as missing.
	missing := s.refreshMissingFlags()
	s.Library.Save()
	log.Printf("remap-paths: rewrote %d tracks %q -> %q (%d still missing)", n, req.From, req.To, missing)
	writeJSON(w, map[string]any{"changed": n, "missing": missing})
}

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
			http.Error(w, "bad source ID: "+err.Error(), http.StatusBadRequest)
			return
		}
		if s.Playlists == nil {
			http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
			return
		}
		pl := s.Playlists.Get(uint32(id))
		if pl == nil {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		trackIDs := s.Playlists.TracksFor(pl.ID, s.Library, s.Tags)
		for _, tid := range trackIDs {
			if t := s.Library.Track(tid); t != nil {
				tracks = append(tracks, t)
			}
		}
	} else if strings.HasPrefix(src, "selection:") {
		trackIDs, err := parseSelectionIDs(src[len("selection:"):])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, tid := range trackIDs {
			if t := s.Library.Track(tid); t != nil {
				tracks = append(tracks, t)
			}
		}
	} else {
		http.Error(w, "source must be 'all', 'playlist:N', 'smart:N', or 'selection:<ids>'", http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]interface{}{
		"total": len(tracks),
	})
}

func (s *Server) handleAddTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Paths []string `json:"paths"` // file paths to add
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Paths) == 0 {
		http.Error(w, "paths array is required", http.StatusBadRequest)
		return
	}

	// Expand any directories into their supported audio files up front so we
	// know the batch size and can choose sync vs. background adding.
	var files []string
	for _, path := range req.Paths {
		files = append(files, expandAudioPaths(path)...)
	}

	// Small batch: add inline and return the count immediately.
	if len(files) <= addSyncThreshold {
		added := s.addFiles(files, nil)
		s.Library.FinalizeBulk()
		writeJSON(w, map[string]interface{}{
			"status": "ok",
			"added":  added,
			"total":  s.Library.TrackCount(),
		})
		return
	}

	// Large batch: add in the background so the request/UI doesn't block on the
	// per-file decode check (ffmpeg per file). The client polls the status
	// endpoint below.
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	job := &addJob{total: len(files)}
	s.addJobs.Store(id, job)
	go func() {
		s.addFiles(files, job)
		s.Library.FinalizeBulk()
		job.finish()
		log.Printf("api: add job %s done: +%d (%d failed, %d already present) of %d",
			id, job.added, job.failed, job.skipped, job.total)
		time.AfterFunc(2*time.Minute, func() { s.addJobs.Delete(id) })
	}()
	writeJSON(w, map[string]interface{}{
		"status": "started",
		"async":  true,
		"job_id": id,
		"total":  len(files),
	})
}

const addSyncThreshold = 25 // files at or under this are added synchronously

// addJob tracks the progress of a background bulk add.
type addJob struct {
	mu      sync.Mutex
	total   int
	added   int
	failed  int
	skipped int
	done    bool
}

func (j *addJob) bumpAdded()   { j.mu.Lock(); j.added++; j.mu.Unlock() }
func (j *addJob) bumpFailed()  { j.mu.Lock(); j.failed++; j.mu.Unlock() }
func (j *addJob) bumpSkipped() { j.mu.Lock(); j.skipped++; j.mu.Unlock() }
func (j *addJob) finish()      { j.mu.Lock(); j.done = true; j.mu.Unlock() }
func (j *addJob) snapshot() map[string]interface{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]interface{}{
		"total": j.total, "added": j.added, "failed": j.failed,
		"skipped": j.skipped, "done": j.done,
	}
}

// addFiles bulk-adds each supported file, skipping ones already in the library.
// Updates job progress when job is non-nil. The caller must call
// Library.FinalizeBulk() once after this returns.
func (s *Server) addFiles(files []string, job *addJob) int {
	added := 0
	for _, f := range files {
		if s.Library.TrackByPath(f) != nil {
			if job != nil {
				job.bumpSkipped()
			}
			continue
		}
		if _, err := s.addTrackByPath(f, true); err != nil {
			log.Printf("api: skipping %s: %v", f, err)
			if job != nil {
				job.bumpFailed()
			}
			continue
		}
		added++
		if job != nil {
			job.bumpAdded()
		}
	}
	return added
}

// handleAddStatus reports progress of a background add job started by
// handleAddTracks. GET /api/tracks/add/status?id=<job_id>.
func (s *Server) handleAddStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	v, ok := s.addJobs.Load(id)
	if !ok {
		http.Error(w, "unknown or expired job", http.StatusNotFound)
		return
	}
	writeJSON(w, v.(*addJob).snapshot())
}

// audioExts are the file extensions addTrackByPath can add.
var audioExts = map[string]bool{
	".mp3": true, ".m4a": true, ".flac": true, ".wav": true, ".aiff": true, ".aif": true,
}

// expandAudioPaths returns the path itself when it's a file, or — when it's a
// directory — every supported audio file beneath it (recursively). Lets the
// "add files/folders" UI accept a folder and pull in its whole tree.
func expandAudioPaths(path string) []string {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return []string{path} // a file (or missing — addTrackByPath reports it)
	}
	var files []string
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if audioExts[strings.ToLower(filepath.Ext(p))] {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// addTrackByPath adds a single track to the library by file path. When bulk is
// true it uses AddTrackBulk (no per-track list rebuild/save) — the caller must
// call Library.FinalizeBulk() once after the batch.
// Returns the new track ID. If the file doesn't exist or is unsupported, returns an error.
func (s *Server) addTrackByPath(path string, bulk bool) (uint32, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("file not found: %s", path)
	}

	track := &library.Track{
		FilePath: path,
	}

	// Detect file type
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp3":
		track.FileType = "mp3"
	case ".m4a":
		track.FileType = "m4a"
	case ".flac":
		track.FileType = "flac"
	case ".wav":
		track.FileType = "wav"
	case ".aiff", ".aif":
		track.FileType = "aiff"
	default:
		return 0, fmt.Errorf("unsupported file type: %s", ext)
	}

	s.readTrackTags(track)

	// Fallback title
	if track.Title == "" {
		track.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	track.DateAdded = time.Now()

	// File size
	if info, err := os.Stat(path); err == nil {
		track.FileSize = info.Size()
	}

	var id uint32
	if bulk {
		id = s.Library.AddTrackBulk(track)
	} else {
		id = s.Library.AddTrack(track)
	}

	// Also add to PDB if available
	if s.PDB != nil {
		pdbTrack := &pdb.Track{
			ID:          id,
			Title:       track.Title,
			Artist:      track.Artist,
			Album:       track.Album,
			Genre:       track.Genre,
			Label:       track.Label,
			FilePath:    path,
			FileName:    filepath.Base(path),
			Duration:    uint16(track.Duration.Seconds()),
			FileSize:    uint32(track.FileSize),
			Year:        uint16(track.Year),
			TrackNum:    uint32(track.TrackNum),
			DiscNumber:  uint16(track.DiscNum),
			Comment:     track.Comment,
			SampleRate:  uint32(track.SampleRate),
			SampleDepth: uint16(track.SampleDepth),
			PlayCount:   uint16(track.PlayCount),
		}
		s.PDB.AddTrack(pdbTrack)
	}

	log.Printf("api: added track %d: %s — %s", id, track.Artist, track.Title)

	// Register path for cache lookups. In lazy mode, analysis happens
	// when the CDJ requests the track. Otherwise, queue for analysis.
	if s.Analysis != nil {
		s.Analysis.SetPath(id, path)
	}
	if s.Analysis != nil && !s.LazyAnalysis {
		s.tryQueueAnalysis(id, path)
	}

	// Update device track count
	if s.Device != nil {
		s.Device.TrackCount = uint16(s.Library.TrackCount())
	}

	return id, nil
}

// queueAnalysis sends a track to the analysis worker pool.
// readTrackTags reads metadata tags from a track's audio file into the Track struct.
func (s *Server) readTrackTags(track *library.Track) {
	f, err := os.Open(track.FilePath)
	if err != nil {
		return
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return
	}

	track.Title = m.Title()
	track.Artist = m.Artist()
	track.Album = m.Album()
	track.Genre = m.Genre()
	track.Year = m.Year()
	track.Comment = m.Comment()
	trackNum, _ := m.Track()
	track.TrackNum = trackNum
	discNum, _ := m.Disc()
	track.DiscNum = discNum

	raw := m.Raw()
	if v, ok := raw["label"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	} else if v, ok := raw["LABEL"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	} else if v, ok := raw["publisher"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	} else if v, ok := raw["TPUB"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	} else if v, ok := raw["ORGANIZATION"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	} else if v, ok := raw["organization"]; ok {
		track.Label = fmt.Sprintf("%v", v)
	}
	if v, ok := raw["remixer"]; ok {
		track.Remixer = fmt.Sprintf("%v", v)
	} else if v, ok := raw["TPE4"]; ok {
		track.Remixer = fmt.Sprintf("%v", v)
	} else if v, ok := raw["MIXARTIST"]; ok {
		track.Remixer = fmt.Sprintf("%v", v)
	}
	if v, ok := raw["original artist"]; ok {
		track.OriginalArtist = fmt.Sprintf("%v", v)
	} else if v, ok := raw["TOPE"]; ok {
		track.OriginalArtist = fmt.Sprintf("%v", v)
	}
	if v, ok := raw["mix"]; ok {
		track.MixName = fmt.Sprintf("%v", v)
	} else if v, ok := raw["TSST"]; ok {
		track.MixName = fmt.Sprintf("%v", v)
	}

	if pic := m.Picture(); pic != nil && len(pic.Data) > 0 {
		track.ArtID = s.Library.Artwork.Add(pic.MIMEType, pic.Data)
	}

	if track.Title == "" {
		track.Title = strings.TrimSuffix(filepath.Base(track.FilePath), filepath.Ext(track.FilePath))
	}
}

// handleReimportTracks re-reads metadata tags from disk for tracks in the library.
// POST /api/tracks/reimport — reimport all tracks
// POST /api/tracks/reimport {"track_ids": [1, 2, 3]} — reimport specific tracks
func (s *Server) handleReimportTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TrackIDs []uint32 `json:"track_ids"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	tracks := s.Library.Tracks()
	updated := 0

	for _, t := range tracks {
		if len(req.TrackIDs) > 0 {
			found := false
			for _, id := range req.TrackIDs {
				if t.ID == id {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		s.readTrackTags(t)
		updated++
	}

	s.Library.Save()
	log.Printf("api: reimported tags for %d tracks", updated)

	writeJSON(w, map[string]interface{}{
		"status":  "ok",
		"updated": updated,
	})
}

func (s *Server) queueAnalysis(trackID uint32, filePath string) {
	s.analyzeOnce.Do(func() {
		s.analyzeCh = make(chan analyzeJob, 1000)
		workers := 2 // limit concurrent analyses to control memory usage
		for i := 0; i < workers; i++ {
			go s.analysisWorker()
		}
	})
	s.analyzeCh <- analyzeJob{trackID: trackID, filePath: filePath}
}

// tryQueueAnalysis enqueues trackID for background analysis through the
// worker pool, deduped against concurrent submissions for the same ID.
// Returns true if newly queued, false if another goroutine already
// enqueued this track or analysis is unavailable. If filePath is empty,
// looks it up from the library/PDB.
func (s *Server) tryQueueAnalysis(trackID uint32, filePath string) bool {
	if s.Analysis == nil {
		return false
	}
	if _, loaded := s.queuedAnalyses.LoadOrStore(trackID, struct{}{}); loaded {
		return false
	}
	if filePath == "" {
		if s.Library != nil {
			if t := s.Library.Track(trackID); t != nil {
				filePath = t.FilePath
			}
		}
		if filePath == "" && s.PDB != nil {
			if t := s.PDB.TrackByID(trackID); t != nil {
				filePath = t.FilePath
			}
		}
	}
	if filePath == "" {
		s.queuedAnalyses.Delete(trackID)
		return false
	}
	s.Analysis.SetPath(trackID, filePath)
	// Try the on-disk cache before queuing a fresh AnalyzeTrack. Without
	// this, every page reload that fans out waveform PNG requests will
	// re-analyze every track from scratch (handleWaveformPNG calls Get,
	// which silently misses the cache when pathMap was empty, then queues
	// here). SetPath above lets Get find the .gob now.
	if r := s.Analysis.Get(trackID); r != nil {
		s.queuedAnalyses.Delete(trackID)
		return false
	}
	s.Analysis.IncPending()
	s.queueAnalysis(trackID, filePath)
	return true
}

func (s *Server) analysisWorker() {
	for job := range s.analyzeCh {
		s.Analysis.SetStatus(fmt.Sprintf("Analyzing: %s", filepath.Base(job.filePath)))
		r, err := analysis.AnalyzeTrack(job.filePath)
		s.Analysis.DecPending()
		s.queuedAnalyses.Delete(job.trackID)
		if err != nil {
			s.Analysis.ClearStatus()
			log.Printf("api: analysis failed for track %d: %v", job.trackID, err)
			continue
		}
		s.Analysis.Set(job.trackID, r)

		if t := s.Library.Track(job.trackID); t != nil {
			if t.BPM == 0 && r.BPM > 0 {
				t.BPM = r.BPM
			}
			if t.Duration == 0 && r.Duration > 0 {
				t.Duration = library.DurationSec(time.Duration(r.Duration) * time.Second)
			}
			if t.Key == "" && r.KeyCamelot != "" {
				t.Key = r.KeyCamelot
			}
			// Estimate bitrate from file size and duration.
			if t.Bitrate == 0 && t.FileSize > 0 && t.Duration > 0 {
				t.Bitrate = int(t.FileSize * 8 / int64(t.Duration.Seconds()) / 1000)
			}
			s.Library.Save()
		}
		if r.Artwork != nil {
			s.Library.Artwork.AddWithID(job.trackID, "image/jpeg", r.Artwork)
		}

		// Update device track count.
		if s.Device != nil {
			s.Device.TrackCount = uint16(s.Library.TrackCount())
		}

		s.Analysis.ClearStatus()
		log.Printf("api: analyzed track %d: BPM=%.1f key=%s path=%s", job.trackID, r.BPM, r.KeyCamelot, job.filePath)
	}
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

// getOrAnalyze returns the analysis result for a track, triggering on-demand
// analysis if the result is missing or was invalidated (stale cache version).
func (s *Server) getOrAnalyze(trackID uint32) *analysis.Result {
	if s.Analysis == nil {
		return nil
	}
	if r := s.Analysis.Get(trackID); r != nil {
		return r
	}

	// Find file path from library or PDB.
	var filePath string
	if s.Library != nil {
		if t := s.Library.Track(trackID); t != nil {
			filePath = t.FilePath
		}
	}
	if filePath == "" && s.PDB != nil {
		if t := s.PDB.TrackByID(trackID); t != nil {
			filePath = t.FilePath
		}
	}
	if filePath == "" {
		return nil
	}

	// Analyze synchronously.
	s.Analysis.SetPath(trackID, filePath)
	if r := s.Analysis.Get(trackID); r != nil {
		return r // disk cache was valid after SetPath
	}

	log.Printf("api: on-demand analysis for track %d: %s", trackID, filePath)
	r, err := analysis.AnalyzeTrack(filePath)
	if err != nil {
		log.Printf("api: on-demand analysis failed for track %d: %v", trackID, err)
		return nil
	}
	s.Analysis.Set(trackID, r)
	log.Printf("api: analyzed track %d: BPM=%.1f key=%s path=%s", trackID, r.BPM, r.KeyCamelot, filePath)
	return r
}

// handleReanalyze invalidates the cached analysis for a track and
// kicks off a fresh analysis run. Synchronous — returns once the new
// result is ready (a few seconds for typical tracks). The fresh
// result replaces both the in-memory entry and the on-disk .gob cache.
// POST /api/analysis/reanalyze/{trackID}
func (s *Server) handleReanalyze(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/reanalyze/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	if s.Analysis == nil {
		http.Error(w, "analysis store not available", http.StatusServiceUnavailable)
		return
	}
	s.Analysis.Invalidate(trackID)
	log.Printf("api: reanalyzing track %d (user-requested)", trackID)
	result := s.getOrAnalyze(trackID)
	if result == nil {
		http.Error(w, "reanalysis failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":         true,
		"track_id":   trackID,
		"bpm":        result.BPM,
		"key":        result.KeyCamelot,
		"beat_count": len(result.Beats),
	})
}

// handleAnalysis returns the analysis data for a track.
// GET /api/analysis/{trackID}
func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		return
	}

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	result := s.getOrAnalyze(trackID)
	if result == nil {
		http.Error(w, "analysis not found", http.StatusNotFound)
		return
	}

	type BeatInfo struct {
		TimeMs    float64 `json:"time_ms"`
		BeatInBar int     `json:"beat_in_bar"` // 1-4
	}

	// Effective beats: if the library track has a BPM/phase override
	// (PUT /api/tracks/{id}/beats), rebuild the beat array at the new
	// tempo so the zoom waveform's beat-grid overlay reflects what the
	// user actually set. Without this, /api/analysis/{id} keeps
	// returning the analyzer's original beats forever.
	effectiveBPM := result.BPM
	effectiveBeats := result.Beats
	downbeatIdx := result.DownbeatIndex
	if t := s.Library.Track(trackID); t != nil {
		if t.BPM > 0 && t.DetectedBPM > 0 && t.BPM != result.BPM {
			effectiveBPM = t.BPM
			interval := 60000.0 / effectiveBPM
			phase := 0.0
			if len(result.Beats) > 0 {
				phase = result.Beats[0]
			}
			durationMs := float64(result.Duration) * 1000
			effectiveBeats = effectiveBeats[:0]
			for tt := phase; tt < durationMs; tt += interval {
				effectiveBeats = append(effectiveBeats, tt)
			}
		}
		if t.BeatPhaseShift != 0 {
			downbeatIdx = ((downbeatIdx+t.BeatPhaseShift)%4 + 4) % 4
		}
	}

	// Build beat list with bar positions.
	var beats []BeatInfo
	for i, b := range effectiveBeats {
		beatInBar := ((i - downbeatIdx) % 4)
		if beatInBar < 0 {
			beatInBar += 4
		}
		beats = append(beats, BeatInfo{
			TimeMs:    b,
			BeatInBar: beatInBar + 1,
		})
	}

	// Get cues if available.
	var cues []CueInfo
	if s.Cues != nil {
		cues = s.Cues.GetCues(trackID)
	}
	if cues == nil {
		cues = []CueInfo{}
	}

	resp := struct {
		TrackID       uint32     `json:"track_id"`
		BPM           float64    `json:"bpm"`
		Key           string     `json:"key"`
		KeyStandard   string     `json:"key_standard"`
		Duration      uint16     `json:"duration_sec"`
		BeatCount     int        `json:"beat_count"`
		DownbeatIndex int        `json:"downbeat_index"`
		Beats         []BeatInfo `json:"beats"`
		Cues          []CueInfo  `json:"cues"`
	}{
		TrackID:       trackID,
		BPM:           effectiveBPM,
		Key:           result.KeyCamelot,
		KeyStandard:   result.KeyStandard,
		Duration:      result.Duration,
		BeatCount:     len(effectiveBeats),
		DownbeatIndex: downbeatIdx,
		Beats:         beats,
		Cues:          cues,
	}
	writeJSON(w, resp)
}

// handleWaveform returns waveform data for a track.
// GET /api/analysis/waveform/{trackID}?type=detail|preview|color_preview&style=spectral|3band
//
// For type=detail, the optional style parameter selects the encoding:
//   - "spectral" (default): per-entry r/g/b/h from PWV5 — rekordbox's stored colors
//     where r/g/b indicate which band dominates and h is overall amplitude
//   - "3band": per-band bass/mid/high amplitudes, each normalized to that band's
//     global peak across the track. For CDJ/Rekordbox-style additive 3-band rendering
//     where each band drives its own bar height
func (s *Server) handleWaveform(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/waveform/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	result := s.getOrAnalyze(trackID)
	if result == nil {
		http.Error(w, "analysis not found", http.StatusNotFound)
		return
	}

	waveType := r.URL.Query().Get("type")
	if waveType == "" {
		waveType = "detail"
	}
	style := r.URL.Query().Get("style")
	if style == "" {
		style = "spectral"
	}

	// HTTP cache: ETag derived from the underlying analysis bytes + cacheVersion
	// + waveType lets the browser revalidate cheaply via If-None-Match. A
	// cacheVersion bump → different ETag → fresh fetch. Otherwise the browser
	// reuses its cached response (no JSON parsing on our side either).
	var sourceBytes []byte
	switch waveType {
	case "detail":
		if style == "3band" {
			sourceBytes = result.WaveDetail3Band
		} else {
			sourceBytes = result.WaveDetail
		}
	case "preview":
		sourceBytes = result.WavePreview
	case "color_preview":
		sourceBytes = result.WaveColorPreview
	}
	if len(sourceBytes) > 0 {
		sum := sha256.Sum256(sourceBytes)
		etag := fmt.Sprintf(`"v%d-%s-%s-%s"`, result.CacheVersion, waveType, style, hex.EncodeToString(sum[:8]))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400, must-revalidate")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	switch waveType {
	case "detail":
		if style == "3band" {
			// 3-band format: 3 bytes per entry (bass, mid, treble; 0-255 each),
			// per-band global-normalized for additive 3-band rendering.
			data := result.WaveDetail3Band
			n := len(data) / 3
			type Entry struct {
				Bass int `json:"bass"` // 0-255
				Mid  int `json:"mid"`  // 0-255
				High int `json:"high"` // 0-255
			}
			entries := make([]Entry, n)
			for i := 0; i < n; i++ {
				entries[i] = Entry{
					Bass: int(data[i*3]),
					Mid:  int(data[i*3+1]),
					High: int(data[i*3+2]),
				}
			}
			writeJSON(w, struct {
				TrackID    uint32  `json:"track_id"`
				Type       string  `json:"type"`
				Style      string  `json:"style"`
				SampleRate int     `json:"sample_rate"`
				Entries    []Entry `json:"entries"`
			}{trackID, "detail", "3band", 150, entries})
			return
		}

		// Default "spectral" style — PWV5: 2 bytes per entry → JSON {r,g,b,h}.
		data := result.WaveDetail
		n := len(data) / 2
		type Entry struct {
			R int `json:"r"` // 0-7
			G int `json:"g"` // 0-7
			B int `json:"b"` // 0-7
			H int `json:"h"` // 0-31
		}
		entries := make([]Entry, n)
		for i := 0; i < n; i++ {
			word := uint16(data[i*2])<<8 | uint16(data[i*2+1])
			entries[i] = Entry{
				R: int((word >> 13) & 7),
				G: int((word >> 10) & 7),
				B: int((word >> 7) & 7),
				H: int((word >> 2) & 0x1f),
			}
		}
		writeJSON(w, struct {
			TrackID    uint32  `json:"track_id"`
			Type       string  `json:"type"`
			Style      string  `json:"style"`
			SampleRate int     `json:"sample_rate"` // entries per second
			Entries    []Entry `json:"entries"`
		}{trackID, "detail", "spectral", 150, entries})

	case "preview":
		// Raw PWAV blob (900 bytes for network, 400 for ANLZ).
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(result.WavePreview)

	case "color_preview":
		// PWV4: 6 bytes per entry, decode to JSON.
		data := result.WaveColorPreview
		n := len(data) / 6
		type Entry struct {
			Bass   int `json:"bass"`   // 0-255
			Mid    int `json:"mid"`    // 0-255
			Treble int `json:"treble"` // 0-255
		}
		entries := make([]Entry, n)
		for i := 0; i < n; i++ {
			entries[i] = Entry{
				Bass:   int(data[i*6+3]),
				Mid:    int(data[i*6+4]),
				Treble: int(data[i*6+5]),
			}
		}
		writeJSON(w, struct {
			TrackID uint32  `json:"track_id"`
			Type    string  `json:"type"`
			Entries []Entry `json:"entries"`
		}{trackID, "color_preview", entries})

	default:
		http.Error(w, "type must be 'detail', 'preview', or 'color_preview'", http.StatusBadRequest)
	}
}

// handleWaveformPNG returns a pre-rendered waveform image, disk-cached
// across server restarts. The browser also gets a long Cache-Control +
// ETag so client reloads come from local browser cache too.
//
// GET /api/artwork/{trackID} — returns the cached album art (JPEG)
// for trackID. 404 when the track has no art. Used by the library
// table's optional ART column.
func (s *Server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
		s.Library.SetArtwork(trackID, artID)
		s.artInFlight.Delete(trackID)
		s.scheduleArtworkSave()
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

// GET /api/analysis/waveform-png/{trackID}?type=detail|color_preview&w=280&h=56
//
// Default type=detail (PWV5) since that's what the web UI uses for thumbnails.
// Width/height are clamped to sane bounds; the cache key includes them so
// requests at different sizes coexist.
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Library == nil {
		http.Error(w, "library not available", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Destination string `json:"destination"`
		Source      string `json:"source"`
		CopyFiles   bool   `json:"copy_files"`
		Merge       bool   `json:"merge"` // append to existing export.pdb instead of replacing
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Destination == "" {
		http.Error(w, "destination is required", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Destination) {
		http.Error(w, "destination must be an absolute path", http.StatusBadRequest)
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
	case strings.HasPrefix(src, "playlist:") || strings.HasPrefix(src, "smart:"):
		idStr := src[strings.IndexByte(src, ':')+1:]
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			http.Error(w, "source ID must be a uint32: "+err.Error(), http.StatusBadRequest)
			return
		}
		if s.Playlists == nil {
			http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
			return
		}
		pl := s.Playlists.Get(uint32(id))
		if pl == nil {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		trackIDs := s.Playlists.TracksFor(pl.ID, s.Library, s.Tags)
		if len(trackIDs) == 0 {
			http.Error(w, "playlist has no tracks", http.StatusBadRequest)
			return
		}
		opts.Tracks = export.FilterTracks(export.LibraryToTracks(s.Library), trackIDs)
		opts.Playlists = export.SinglePlaylist(pl.Name, trackIDs)
		sourceLabel = pl.Name
	case strings.HasPrefix(src, "selection:"):
		trackIDs, err := parseSelectionIDs(src[len("selection:"):])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(trackIDs) == 0 {
			http.Error(w, "no tracks selected", http.StatusBadRequest)
			return
		}
		opts.Tracks = export.FilterTracks(export.LibraryToTracks(s.Library), trackIDs)
		opts.Playlists = export.SinglePlaylist("Selected Tracks", trackIDs)
		sourceLabel = fmt.Sprintf("%d selected", len(trackIDs))
	default:
		http.Error(w, "source must be 'all', 'playlist:<id>', 'smart:<id>', or 'selection:<ids>'", http.StatusBadRequest)
		return
	}

	log.Printf("export: starting (source=%s dest=%s copy=%v)", sourceLabel, req.Destination, req.CopyFiles)
	if err := export.Run(opts); err != nil {
		log.Printf("export: failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, struct {
		OK          bool   `json:"ok"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
		TrackCount  int    `json:"track_count"`
	}{true, sourceLabel, req.Destination, len(opts.Tracks)})
}

func (s *Server) handleWaveformPNG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/waveform-png/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	waveType := r.URL.Query().Get("type")
	if waveType == "" {
		waveType = "detail"
	}
	if waveType != "detail" && waveType != "color_preview" {
		http.Error(w, "type must be detail or color_preview", http.StatusBadRequest)
		return
	}
	// Colour mode mirrors the CDJ "waveform color" DEVSETTING: "rgb" (the
	// spectral PWV5 colours, default), "blue" (monochrome blue, treble-tinted
	// caps), or "3band" (CDJ-3000 additive bass/mid/treble). Only meaningful
	// for the "detail" type.
	colorMode := r.URL.Query().Get("color")
	if colorMode != "blue" && colorMode != "3band" {
		colorMode = "rgb"
	}
	// Overview height mirrors the CDJ "overview" DEVSETTING: "full" (default)
	// or "half" (waveform drawn at half height).
	overview := r.URL.Query().Get("overview")
	if overview != "half" {
		overview = "full"
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	height, _ := strconv.Atoi(r.URL.Query().Get("h"))
	if width <= 0 {
		width = 280
	}
	if height <= 0 {
		height = 56
	}
	if width > 2400 {
		width = 2400
	}
	if height > 240 {
		height = 240
	}

	// Non-blocking lookup. We must NOT call getOrAnalyze here: a fresh
	// cacheVersion bump invalidates every track's disk cache, and the
	// library page's <img loading="lazy"> tags can fan out to dozens of
	// concurrent requests on layout — each AnalyzeTrack decodes the full
	// PCM into memory and pegs CPU on FFT/beat detection, so running them
	// inline on per-request goroutines starves the host (with Swap: 0
	// it's enough to lock up the desktop). Instead, queue the work onto
	// the bounded worker pool and return a placeholder PNG. Once the
	// library row's BPM/Duration/Key change from the worker's writeback,
	// /api/tracks differs and the front-end re-renders the row, which
	// re-fetches this URL and gets the real PNG.
	//
	// Set the file path first so Get can find the disk cache — without
	// this, every page reload sees pathMap empty, Get returns nil for
	// every track, and tryQueueAnalysis runs a full fresh AnalyzeTrack
	// even when the .gob is sitting on disk.
	var filePath string
	if s.Library != nil {
		if t := s.Library.Track(trackID); t != nil && t.FilePath != "" {
			filePath = t.FilePath
			s.Analysis.SetPath(trackID, filePath)
		}
	}
	result := s.Analysis.Get(trackID)
	if result == nil {
		s.tryQueueAnalysis(trackID, "")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(placeholderWaveformPNG)
		return
	}
	cacheKey := waveformCacheKey(trackID, filePath)
	variant := fmt.Sprintf("r%d_%s_%s_%s", waveformPNGRenderVersion, waveType, colorMode, overview)
	cachePath := s.waveformPNGPath(cacheKey, variant, width, height, result.CacheVersion)
	etag := fmt.Sprintf(`"png-r%d-v%d-%s-%s-%s-%s-%dx%d"`, waveformPNGRenderVersion, result.CacheVersion, waveType, colorMode, overview, cacheKey, width, height)

	// Browser ETag revalidation.
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=604800, must-revalidate") // 7 days
	w.Header().Set("Content-Type", "image/png")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Serve from disk if present (created on a prior request).
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			w.Write(data)
			return
		}
	}

	// Cache miss → render now.
	imgBytes, err := renderWaveformPNG(result, waveType, colorMode, overview, width, height)
	if err != nil {
		http.Error(w, "render failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Best-effort write to disk for next time.
	if cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, imgBytes, 0o644)
	}
	w.Write(imgBytes)
}

// waveformCacheKey identifies a track's waveform by a hash of its file path
// (matching the analysis cache), NOT its track ID. Track IDs are positional —
// deleting a track renumbers the rest on the next load, so an ID-keyed cache
// would serve the deleted track's waveform to whoever inherits its ID. The
// waveform belongs to the audio file, so key it by path. Empty path (a
// missing-file track) falls back to the ID — those have no waveform anyway.
func waveformCacheKey(trackID uint32, filePath string) string {
	if filePath != "" {
		h := sha256.Sum256([]byte(filePath))
		return hex.EncodeToString(h[:8])
	}
	return fmt.Sprintf("id%d", trackID)
}

func (s *Server) waveformPNGPath(key, waveType string, width, height, cacheVersion int) string {
	if s.CacheDir == "" {
		return ""
	}
	shard := key[len(key)-2:]
	name := fmt.Sprintf("v%d_%s_%s_%dx%d.png", cacheVersion, key, waveType, width, height)
	return filepath.Join(s.CacheDir, "waveform-png", shard, name)
}

// placeholderWaveformPNG is a tiny 1×1 dark PNG served while a track's
// analysis is still in flight. The browser stretches it to the <img>'s
// width/height attributes so layout doesn't shift; Cache-Control:no-store
// on the response means the next <img> element created for the same URL
// re-fetches and gets the real waveform once analysis completes.
var placeholderWaveformPNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 3, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}()

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// blueColor maps PWV5 per-bucket band weights to the CDJ "blue" waveform:
// a medium blue body that whitens toward the caps where treble (b) dominates.
// mr/mg/mb are summed 0-7 weights over the bucket; only their ratio matters.
func blueColor(mr, mg, mb int) color.RGBA {
	sum := float64(mr + mg + mb)
	tf := 0.0
	if sum > 0 {
		tf = float64(mb) / sum // treble fraction → whitening amount
	}
	k := tf * 0.85
	return color.RGBA{
		R: clamp8(40 + (255-40)*k),
		G: clamp8(120 + (255-120)*k),
		B: clamp8(235 + (255-235)*k),
		A: 255,
	}
}

// waveformPNGRenderVersion invalidates disk-cached thumbnails + browser ETags
// when the PNG rendering changes (independently of the analysis CacheVersion).
// Bump on any renderWaveformPNG change. 2: 3-band drawn as a stacked half wave.
const waveformPNGRenderVersion = 2

// renderWaveformPNG draws a waveform to an RGBA image and PNG-encodes it.
// PWV5 (detail): per-entry r/g/b weights ∈ [0,7] + h ∈ [0,31]. Subsamples
// to canvas width, max-h per bucket, averaged colour weights — matches the
// drawWaveformPWV5 routine in the web UI's index.html.
// PWV4 (color_preview): per-entry bass/mid/treble bytes 0-255, normalised
// to track peak.
func renderWaveformPNG(result *analysis.Result, waveType, colorMode, overview string, width, height int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Solid near-black background so the PNG looks the same as the canvas.
	bg := color.RGBA{R: 3, G: 2, B: 3, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	clampBar := func(barH int) int {
		if barH < 1 {
			barH = 1
		}
		return barH
	}
	// "half" overview shows only the lower half of the waveform, anchored to
	// the bottom of the box (same amplitude, not squished); "full" centres it.
	// Matches barTop in index.html.
	barTop := func(barH int) int {
		if overview == "half" {
			return height - barH
		}
		return (height - barH) / 2
	}
	// entryRange maps pixel column x to the [start,end) detail-entry range on
	// the waveform's true time base (entry i = i/150 s), scaled to the track
	// duration so features align with the time-positioned cue overlay instead
	// of drifting toward the end. Falls back to a proportional index split when
	// the duration is unknown. Only meaningful for the fixed-rate detail series.
	const detailRate = 150.0
	durMs := float64(result.Duration) * 1000
	entryRange := func(x, n int) (int, int) {
		var s, e int
		if durMs <= 0 {
			s = int(float64(x) * float64(n) / float64(width))
			e = int(float64(x+1) * float64(n) / float64(width))
		} else {
			s = int(float64(x) / float64(width) * durMs * detailRate / 1000)
			e = int(float64(x+1) / float64(width) * durMs * detailRate / 1000)
		}
		if e <= s {
			e = s + 1
		}
		if s > n {
			s = n
		}
		if e > n {
			e = n
		}
		return s, e
	}

	// 3-band (CDJ-3000): 3 bytes/entry (bass,mid,treble; 0-255), absolute
	// amplitudes. Prefer PWV6 (the 1200-entry whole-track overview rekordbox uses
	// for its top minimap), falling back to PWV7 detail then PWV5. PWV6 is a
	// FIXED-count overview, not 150/sec, so it must be index-mapped across the
	// width — using entryRange's detail-rate mapping would cram it into the first
	// few percent of the thumbnail.
	band3, overview3 := result.WavePreview3Band, true
	if len(band3) < 3 {
		band3, overview3 = result.WaveDetail3Band, false
	}
	if waveType == "detail" && colorMode == "3band" && len(band3) >= 3 {
		data := band3
		n := len(data) / 3
		// The 3-band PREVIEW is a bottom-anchored VERTICAL STACK (rekordbox
		// convention, matching drawStacked3Band in index.html): blue bass on the
		// bottom, orange mid stacked above, white treble on top — each segment's
		// height is that band's amplitude, not overlaid bars. Pass 1: per-column
		// per-band peak.
		bassC := make([]int, width)
		midC := make([]int, width)
		trebC := make([]int, width)
		for x := 0; x < width; x++ {
			var start, end int
			if overview3 {
				start = x * n / width
				end = (x + 1) * n / width
				if end <= start {
					end = start + 1
				}
				if end > n {
					end = n
				}
			} else {
				start, end = entryRange(x, n)
			}
			var mb, mm, mt int
			for i := start; i < end; i++ {
				if b := int(data[i*3]); b > mb {
					mb = b
				}
				if m := int(data[i*3+1]); m > mm {
					mm = m
				}
				if t := int(data[i*3+2]); t > mt {
					mt = t
				}
			}
			bassC[x], midC[x], trebC[x] = mb, mm, mt
		}
		// Light column-space smoothing (rekordbox's preview is very smooth).
		smoothCol := func(a []int) {
			src := append([]int(nil), a...)
			for x := range a {
				sum, cnt := 0, 0
				for j := x - 1; j <= x+1; j++ {
					if j >= 0 && j < len(a) {
						sum += src[j]
						cnt++
					}
				}
				a[x] = sum / cnt
			}
		}
		smoothCol(bassC)
		smoothCol(midC)
		smoothCol(trebC)
		// Pass 2: stack bottom-up, normalised so the loudest column fills the box.
		// Full vivid colours (segments don't overlap). Always bottom-anchored —
		// the 3-band preview is a half waveform regardless of the overview setting.
		maxTot := 1
		for x := 0; x < width; x++ {
			if t := bassC[x] + midC[x] + trebC[x]; t > maxTot {
				maxTot = t
			}
		}
		stackCols := [3]color.RGBA{{R: 30, G: 120, B: 255, A: 255}, {R: 255, G: 170, B: 30, A: 255}, {R: 235, G: 245, B: 255, A: 255}}
		bands := [3][]int{bassC, midC, trebC}
		for x := 0; x < width; x++ {
			y := height
			for bi := 0; bi < 3; bi++ {
				seg := (bands[bi][x] * height) / maxTot
				for k := 0; k < seg; k++ {
					y--
					if y >= 0 {
						img.SetRGBA(x, y, stackCols[bi])
					}
				}
			}
		}
	} else if waveType == "detail" {
		data := result.WaveDetail
		n := len(data) / 2
		if n > 0 {
			for x := 0; x < width; x++ {
				start, end := entryRange(x, n)
				var mh, mr, mg, mb int
				count := 0
				for i := start; i < end; i++ {
					word := uint16(data[i*2])<<8 | uint16(data[i*2+1])
					r := int((word >> 13) & 7)
					g := int((word >> 10) & 7)
					b := int((word >> 7) & 7)
					h := int((word >> 2) & 0x1f)
					if h > mh {
						mh = h
					}
					mr += r
					mg += g
					mb += b
					count++
				}
				if mh == 0 || count == 0 {
					continue
				}
				var col color.RGBA
				if colorMode == "blue" {
					col = blueColor(mr, mg, mb)
				} else {
					col = color.RGBA{
						R: uint8(min(255, (mr*38)/count)),
						G: uint8(min(255, (mg*38)/count)),
						B: uint8(min(255, (mb*38)/count)),
						A: 255,
					}
				}
				barH := clampBar((mh * height) / 31)
				top := barTop(barH)
				for y := top; y < top+barH; y++ {
					img.SetRGBA(x, y, col)
				}
			}
		}
	} else {
		// color_preview (PWV4)
		data := result.WaveColorPreview
		n := len(data) / 6
		if n > 0 {
			var maxV int = 1
			for i := 0; i < n; i++ {
				peak := int(data[i*6+3])
				if int(data[i*6+4]) > peak {
					peak = int(data[i*6+4])
				}
				if int(data[i*6+5]) > peak {
					peak = int(data[i*6+5])
				}
				if peak > maxV {
					maxV = peak
				}
			}
			step := float64(n) / float64(width)
			for x := 0; x < width; x++ {
				idx := int(float64(x) * step)
				if idx >= n {
					idx = n - 1
				}
				b := int(data[idx*6+3])
				m := int(data[idx*6+4])
				t := int(data[idx*6+5])
				peak := b
				if m > peak {
					peak = m
				}
				if t > peak {
					peak = t
				}
				if peak == 0 {
					continue
				}
				ar := uint8(min(255, b*3/2))
				ag := uint8(min(255, m*3/2))
				ab := uint8(min(255, t*3/2))
				barH := clampBar((peak * height) / maxV)
				top := barTop(barH)
				col := color.RGBA{R: ar, G: ag, B: ab, A: 255}
				for y := top; y < top+barH; y++ {
					img.SetRGBA(x, y, col)
				}
			}
		}
	}

	var buf strings.Builder
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	bw := &writerToBuilder{b: &buf}
	if err := enc.Encode(bw, img); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// writerToBuilder is a tiny io.Writer over a strings.Builder so we can
// PNG-encode without allocating a separate bytes.Buffer.
type writerToBuilder struct{ b *strings.Builder }

func (w *writerToBuilder) Write(p []byte) (int, error) {
	w.b.Write(p)
	return len(p), nil
}

// handleBeatGridAdjust shifts the beat grid by a given offset.
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "offset_ms": -25.5}
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "bpm": 130.00}
// POST /api/analysis/beatgrid/adjust {"track_id": 123, "set_downbeat_ms": 4975.0}
func (s *Server) handleBeatGridAdjust(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TrackID       uint32   `json:"track_id"`
		OffsetMs      *float64 `json:"offset_ms"`       // shift all beats by this amount
		BPM           *float64 `json:"bpm"`             // set new BPM (rebuilds grid)
		SetDownbeatMs *float64 `json:"set_downbeat_ms"` // set beat 1 at this position
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TrackID == 0 {
		http.Error(w, "track_id required", http.StatusBadRequest)
		return
	}

	result := s.getOrAnalyze(req.TrackID)
	if result == nil {
		http.Error(w, "analysis not found", http.StatusNotFound)
		return
	}

	// Anchor phrases to absolute time from the CURRENT (pre-edit) grid for older
	// phrases that lack it, so the grid edits below don't drag the phrase strip
	// off the audio. New analyses already carry StartMs/EndMs; this only fills in
	// pre-existing cached results, and Set() below persists it.
	if len(result.Phrases) > 0 && result.Phrases[0].EndMs <= 0 {
		analysis.SetPhraseTimes(result.Phrases, result.Beats, result.BPM)
	}

	modified := false

	// Shift all beats by offset.
	if req.OffsetMs != nil {
		offset := *req.OffsetMs
		for i := range result.Beats {
			result.Beats[i] += offset
		}
		result.BeatGrid = nil // invalidate cached beat grid blobs
		result.BeatGridPQT2 = nil
		modified = true
		log.Printf("api: beat grid for track %d shifted by %.1fms", req.TrackID, offset)
	}

	// Set new BPM — rebuild grid keeping the same phase.
	if req.BPM != nil && *req.BPM > 0 {
		newBPM := *req.BPM
		newInterval := 60000.0 / newBPM
		phase := 0.0
		if len(result.Beats) > 0 {
			phase = result.Beats[0]
		}
		durationMs := float64(result.Duration) * 1000
		result.Beats = result.Beats[:0]
		for t := phase; t < durationMs; t += newInterval {
			result.Beats = append(result.Beats, t)
		}
		result.BPM = newBPM
		result.BeatGrid = nil
		result.BeatGridPQT2 = nil
		modified = true
		log.Printf("api: beat grid for track %d BPM set to %.2f", req.TrackID, newBPM)
	}

	// Set downbeat position — shift grid so that a beat lands at the given time.
	if req.SetDownbeatMs != nil {
		target := *req.SetDownbeatMs
		interval := 60000.0 / result.BPM
		// Find the current beat nearest to the target.
		if len(result.Beats) > 0 {
			nearestIdx := 0
			nearestDist := math.Abs(result.Beats[0] - target)
			for i, b := range result.Beats {
				d := math.Abs(b - target)
				if d < nearestDist {
					nearestDist = d
					nearestIdx = i
				}
			}
			offset := target - result.Beats[nearestIdx]
			for i := range result.Beats {
				result.Beats[i] += offset
			}
			// Set the downbeat index so this beat becomes beat 1.
			result.DownbeatIndex = nearestIdx
			result.BeatGrid = nil
			result.BeatGridPQT2 = nil
			modified = true
			log.Printf("api: beat grid for track %d downbeat set to %.1fms (shifted %.1fms, beat %d)",
				req.TrackID, target, offset, nearestIdx)

			// Rebuild beats forward/backward from new phase to cover full track.
			newPhase := math.Mod(result.Beats[0], interval)
			if newPhase < 0 {
				newPhase += interval
			}
			durationMs := float64(result.Duration) * 1000
			result.Beats = result.Beats[:0]
			startPhase := newPhase
			for startPhase-interval >= 0 {
				startPhase -= interval
			}
			downbeatSet := false
			for t := startPhase; t < durationMs; t += interval {
				result.Beats = append(result.Beats, t)
				if !downbeatSet && t >= target-interval/2 {
					result.DownbeatIndex = len(result.Beats) - 1
					downbeatSet = true
				}
			}
		}
	}

	if modified {
		// Mark the grid as user-edited so the dbserver serves our regenerated
		// blobs instead of the (now-stale) on-disk ANLZ .EXT/.DAT files.
		result.GridEdited = true
		// Regenerate beat grid blobs from updated beats.
		if len(result.Beats) > 0 {
			beatResult := &analysis.BeatResult{
				BPM:      result.BPM,
				Beats:    result.Beats,
				Downbeat: result.Beats[0],
			}
			if result.DownbeatIndex >= 0 && result.DownbeatIndex < len(result.Beats) {
				beatResult.Downbeat = result.Beats[result.DownbeatIndex]
			}
			result.BeatGrid = analysis.GenerateBeatGridFromBeats(beatResult)
			result.BeatGridPQT2 = prolink.GeneratePQT2(result.BPM, result.Beats, result.DownbeatIndex)
		}
		s.Analysis.Set(req.TrackID, result)
	}

	writeJSON(w, struct {
		OK    bool    `json:"ok"`
		BPM   float64 `json:"bpm"`
		Beats int     `json:"beats"`
		Phase float64 `json:"phase_ms"`
	}{
		OK:    true,
		BPM:   result.BPM,
		Beats: len(result.Beats),
		Phase: func() float64 {
			if len(result.Beats) > 0 {
				return result.Beats[0]
			}
			return 0
		}(),
	})
}

// handleLink reports or sets the device link-state — when off, the
// virtual device suppresses every outbound UDP send and CDJs time us
// out within ~6s (vanishes from their LINK menu). GET returns
// {linked: bool}; POST takes the same shape and applies it. Toggling
// off also tears down any active dbserver sessions so a CDJ that was
// browsing our library gets kicked immediately rather than after the
// next 0x0100 round-trip.
func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.Device == nil {
		http.Error(w, "device not available", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]bool{"linked": s.Device.Linked()})
	case http.MethodPost:
		var req struct {
			Linked bool `json:"linked"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Device.SetLinked(req.Linked)
		writeJSON(w, map[string]bool{"linked": s.Device.Linked()})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleUnlink tears down every active CDJ dbserver session, mirroring the
// "Unlink" button in rekordbox. CDJs reconnect on their own a few
// seconds later via discovery, so this is a soft disconnect — useful when
// state has gone weird and the simplest fix is to start over.
func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.DBServer == nil {
		http.Error(w, "dbserver not available", http.StatusServiceUnavailable)
		return
	}
	closed := s.DBServer.Unlink()
	// Drop the disconnected peers from the tracker so the UI updates
	// immediately instead of waiting for the 5s keep-alive timeout.
	if s.Device != nil && s.Device.Peers != nil {
		for _, p := range s.Device.Peers.Peers() {
			s.Device.Peers.RemoveByIP(p.IP)
		}
	}
	writeJSON(w, map[string]int{"sessions_closed": closed})
}

// handleAllCues returns a {trackID: [cue, ...]} map of every track that has
// cues. Used by the web UI's library page to overlay cue markers on every
// waveform thumbnail in a single round-trip instead of per-row fetches.
func (s *Server) handleAllCues(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Cues == nil {
		writeJSON(w, map[string][]CueInfo{})
		return
	}
	all := s.Cues.AllCues()
	// JSON object keys must be strings, so convert track IDs.
	out := make(map[string][]CueInfo, len(all))
	for id, cues := range all {
		out[strconv.FormatUint(uint64(id), 10)] = cues
	}
	writeJSON(w, out)
}

// handleCues manages cue points for a track.
// GET  /api/tracks/{trackID}/cues — list cues
// POST /api/tracks/{trackID}/cues — add/update cue
// DELETE /api/tracks/{trackID}/cues/{number} — delete cue
func (s *Server) handleCues(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	// Parse /api/tracks/{trackID}/cues[/{number}]
	path := strings.TrimPrefix(r.URL.Path, "/api/tracks/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	var trackID uint32
	fmt.Sscanf(parts[0], "%d", &trackID)
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	if s.Cues == nil {
		http.Error(w, "cue store not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		cues := s.Cues.GetCues(trackID)
		if cues == nil {
			cues = []CueInfo{}
		}
		writeJSON(w, cues)

	case "POST":
		var cue CueInfo
		if err := json.NewDecoder(r.Body).Decode(&cue); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if cue.Number == 0 {
			http.Error(w, "cue number required (1=A, 2=B, ...)", http.StatusBadRequest)
			return
		}
		if cue.Type == "" {
			cue.Type = "cue"
		}
		s.Cues.SaveCue(trackID, cue)
		log.Printf("api: saved cue #%d for track %d at %dms (color=%d)",
			cue.Number, trackID, cue.TimeMs, cue.ColorID)
		writeJSON(w, struct {
			OK bool `json:"ok"`
		}{true})

	case "DELETE":
		if len(parts) < 3 {
			http.Error(w, "cue number required in path", http.StatusBadRequest)
			return
		}
		var cueNum uint16
		fmt.Sscanf(parts[2], "%d", &cueNum)
		if cueNum == 0 {
			http.Error(w, "invalid cue number", http.StatusBadRequest)
			return
		}
		s.Cues.DeleteCue(trackID, cueNum)
		log.Printf("api: deleted cue #%d for track %d", cueNum, trackID)
		writeJSON(w, struct {
			OK bool `json:"ok"`
		}{true})
	}
}

func parseTrackIDFromPath(path, prefix string) uint32 {
	s := strings.TrimPrefix(path, prefix)
	s = strings.Split(s, "/")[0]
	s = strings.Split(s, "?")[0]
	var id uint32
	fmt.Sscanf(s, "%d", &id)
	return id
}

// handleSettings handles GET/POST /api/settings for CDJ display settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		return
	}

	settings := s.Device.Settings
	if settings == nil {
		http.Error(w, "settings not available", http.StatusServiceUnavailable)
		return
	}

	type SettingsJSON struct {
		WaveformSize     string `json:"waveform_size"`     // "full" or "half"
		WaveformColor    string `json:"waveform_color"`    // "blue", "rgb", or "3band"
		WaveformPosition string `json:"waveform_position"` // "left" or "center"
		KeyDisplay       string `json:"key_display"`       // "classic" or "alphanumeric"
		TrackDetail      string `json:"track_detail"`      // field shown next to track titles
	}

	devSettingToJSON := func() SettingsJSON {
		dev := settings.GetDevSetting()
		var js SettingsJSON
		js.TrackDetail = settings.GetTrackDetail()
		if len(dev) >= 6 {
			switch dev[1] {
			case 0x01:
				js.WaveformSize = "half"
			default:
				js.WaveformSize = "full"
			}
			switch dev[2] {
			case 0x01:
				js.WaveformColor = "blue"
			case 0x03:
				js.WaveformColor = "rgb"
			default:
				js.WaveformColor = "3band"
			}
			switch dev[4] {
			case 0x01:
				js.KeyDisplay = "classic"
			default:
				js.KeyDisplay = "alphanumeric"
			}
			switch dev[5] {
			case 0x01:
				js.WaveformPosition = "center"
			default:
				js.WaveformPosition = "left"
			}
		}
		return js
	}

	if r.Method == http.MethodGet {
		// Backward-compatible: existing 5-field DEVSETTING-derived shape,
		// plus the full YAML config under "full" so the web UI can render
		// every MYSETTING/MYSETTING2/DJMMYSETTING field too.
		resp := struct {
			SettingsJSON
			Full device.SettingsConfig `json:"full"`
		}{
			SettingsJSON: devSettingToJSON(),
			Full:         settings.Config(),
		}
		writeJSON(w, resp)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}

	// Two POST shapes accepted:
	//   1. {full: {<entire SettingsConfig>}} — web UI uses this when the
	//      user changes any field; we replace the whole config and persist.
	//   2. {waveform_size: ..., waveform_color: ..., ...} — legacy shape
	//      that only touches the 4 DEVSETTING bytes + track_detail.
	var combined struct {
		SettingsJSON
		Full *device.SettingsConfig `json:"full,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&combined); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if combined.Full != nil {
		settings.SetConfig(*combined.Full)
		log.Printf("api: settings: full config replaced")
		s.Device.NotifySettingsChanged()
		writeJSON(w, struct{ OK bool }{true})
		return
	}
	req := combined.SettingsJSON

	// Start from current settings and apply changes.
	dev := make([]byte, 6)
	copy(dev, settings.GetDevSetting())
	if len(dev) < 6 {
		dev = []byte{0x01, 0x02, 0x03, 0x01, 0x02, 0x01}
	}
	// Ensure required constant bytes are always 0x01.
	dev[0] = 0x01
	dev[3] = 0x01

	if req.WaveformSize != "" {
		switch req.WaveformSize {
		case "half":
			dev[1] = 0x01
		case "full":
			dev[1] = 0x02
		}
	}
	if req.WaveformColor != "" {
		switch req.WaveformColor {
		case "blue":
			dev[2] = 0x01
		case "rgb":
			dev[2] = 0x03
		case "3band":
			dev[2] = 0x04
		}
	}
	if req.KeyDisplay != "" {
		switch req.KeyDisplay {
		case "classic":
			dev[4] = 0x01
		case "alphanumeric":
			dev[4] = 0x02
		}
	}
	if req.WaveformPosition != "" {
		switch req.WaveformPosition {
		case "center":
			dev[5] = 0x01
		case "left":
			dev[5] = 0x02
		}
	}

	settings.SaveDevSetting(dev)
	if req.TrackDetail != "" {
		settings.SetTrackDetail(req.TrackDetail)
	}
	log.Printf("api: settings updated: %+v", devSettingToJSON())

	// Notify connected CDJs that settings changed.
	s.Device.NotifySettingsChanged()

	writeJSON(w, devSettingToJSON())
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}

// writeJSONGzip is writeJSON that gzip-compresses the body when the client
// accepts it. Worth it for the large, highly repetitive track-list payload
// (keys and long file paths compress well); small responses stay on
// writeJSON. Content negotiation is advertised via Vary so caches key on it.
func writeJSONGzip(w http.ResponseWriter, r *http.Request, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		json.NewEncoder(gz).Encode(v)
		return
	}
	json.NewEncoder(w).Encode(v)
}

// ── Tag handlers ───────────────────────────────────────────────────���────

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

// PUT /api/tracks/{id}/metadata — update editable text metadata in the
// library DB ONLY. Never touches the audio file. Body is a partial object;
// omitted fields are left unchanged. Limited to non-indexed text fields
// (label/remixer/original_artist/mix_name/comment) so the library's
// artist/album/genre indexes don't need rebuilding.
func (s *Server) handleTrackMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
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
	t := s.Library.Track(trackID)
	if t == nil {
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
	if req.Title != nil {
		t.Title = strings.TrimSpace(*req.Title)
	}
	if req.Artist != nil {
		t.Artist = strings.TrimSpace(*req.Artist)
	}
	if req.Album != nil {
		t.Album = strings.TrimSpace(*req.Album)
	}
	if req.Genre != nil {
		t.Genre = strings.TrimSpace(*req.Genre)
	}
	if req.Year != nil {
		y := strings.TrimSpace(*req.Year)
		if y == "" {
			t.Year = 0
		} else if n, err := strconv.Atoi(y); err == nil && n >= 0 {
			t.Year = n
		} else {
			http.Error(w, "year must be a non-negative whole number (or blank)", http.StatusBadRequest)
			return
		}
	}
	if req.Key != nil {
		k := strings.TrimSpace(*req.Key)
		if k == "" {
			t.Key = ""
		} else if cam, _ := analysis.KeyNamesFrom(k); cam != "" {
			t.Key = cam // store canonical Camelot, matching how imports normalize
		} else {
			http.Error(w, "unrecognized musical key (use Camelot like 8A or classic like Am)", http.StatusBadRequest)
			return
		}
	}
	if req.Label != nil {
		t.Label = strings.TrimSpace(*req.Label)
	}
	if req.Remixer != nil {
		t.Remixer = strings.TrimSpace(*req.Remixer)
	}
	if req.OriginalArtist != nil {
		t.OriginalArtist = strings.TrimSpace(*req.OriginalArtist)
	}
	if req.MixName != nil {
		t.MixName = strings.TrimSpace(*req.MixName)
	}
	if req.Comment != nil {
		t.Comment = strings.TrimSpace(*req.Comment)
	}
	s.Library.Save()
	if s.Device != nil {
		s.Device.BroadcastTrackRefresh(trackID)
	}
	writeJSON(w, t)
}

// PUT /api/tracks/{id}/path — point a track at a new file path {path: "..."}.
// Updates the library DB only (never moves or renames the file on disk),
// rekeys the analysis cache so existing waveforms/beat grid follow the track,
// rechecks presence (clearing/setting FileMissing), and refreshes size/type
// from the new file when it exists.
func (s *Server) handleTrackPath(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
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
	t := s.Library.Track(trackID)
	if t == nil {
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
	oldPath := t.FilePath
	if newPath != oldPath {
		// Move the cached analysis to the new path's slot so the existing
		// waveforms/beat grid follow the track instead of orphaning.
		if s.Analysis != nil {
			s.Analysis.RenameCachedPath(oldPath, newPath)
			s.Analysis.SetPath(trackID, newPath)
		}
		t.FilePath = newPath
	}
	// Recheck presence and refresh size/type from the new file if it exists.
	miss := true
	if fi, err := os.Stat(newPath); err == nil && !fi.IsDir() {
		miss = false
		t.FileSize = fi.Size()
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(newPath), ".")); ext != "" {
			t.FileType = ext
		}
	}
	t.FileMissing = miss
	s.Library.Save()
	if s.Device != nil {
		s.Device.BroadcastTrackRefresh(trackID)
	}
	log.Printf("track %d: path updated %q -> %q (missing=%v)", trackID, oldPath, newPath, miss)
	writeJSON(w, s.libTrackToInfo(t))
}

// PUT /api/tracks/{id}/color — set track color {color_id: N}
func (s *Server) handleTrackColor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		color := s.Tags.GetTrackColor(trackID)
		writeJSON(w, struct {
			ColorID uint8 `json:"color_id"`
		}{color})

	case "PUT", "POST":
		var req struct {
			ColorID uint8 `json:"color_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Tags.SetTrackColor(trackID, req.ColorID)
		// Also update the library track so it's visible in browse menus.
		if t := s.Library.Track(trackID); t != nil {
			t.ColorID = req.ColorID
		}
		s.Library.Touch() // colour lives in the tag store, not a library Save()
		if s.Device != nil {
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET/PUT /api/tracks/{id}/beats — beat-grid overrides
//
//	GET:  returns { bpm, detected_bpm, beat_phase_shift }
//	PUT:  body { bpm?: number, beat_phase_shift?: int, clear?: bool }
//	      - bpm omitted/0 keeps current effective BPM
//	      - beat_phase_shift in 0..3
//	      - clear: true reverts BPM to detected_bpm and phase to 0
func (s *Server) handleTrackBeats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
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

	switch r.Method {
	case "GET":
		writeJSON(w, struct {
			BPM            float64 `json:"bpm"`
			DetectedBPM    float64 `json:"detected_bpm"`
			BeatPhaseShift int     `json:"beat_phase_shift"`
		}{t.BPM, t.DetectedBPM, t.BeatPhaseShift})

	case "PUT", "POST":
		var req struct {
			BPM            *float64 `json:"bpm"`
			BeatPhaseShift *int     `json:"beat_phase_shift"`
			Clear          bool     `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Clear {
			if t.DetectedBPM > 0 {
				t.BPM = t.DetectedBPM
			}
			// Also clear DetectedBPM so the UI no longer reports the
			// track as overridden (next BPM edit re-snapshots).
			t.DetectedBPM = 0
			t.BeatPhaseShift = 0
		} else {
			if req.BPM != nil && *req.BPM > 0 {
				// Snapshot the detected BPM the first time we override,
				// so a later "clear" can restore it.
				if t.DetectedBPM == 0 {
					t.DetectedBPM = t.BPM
				}
				t.BPM = *req.BPM
			}
			if req.BeatPhaseShift != nil {
				v := *req.BeatPhaseShift % 4
				if v < 0 {
					v += 4
				}
				t.BeatPhaseShift = v
			}
		}
		s.Library.Save()
		if s.Device != nil {
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct {
			OK             bool    `json:"ok"`
			BPM            float64 `json:"bpm"`
			DetectedBPM    float64 `json:"detected_bpm"`
			BeatPhaseShift int     `json:"beat_phase_shift"`
		}{true, t.BPM, t.DetectedBPM, t.BeatPhaseShift})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /api/tracks/{id}/rating — set track rating {rating: N} (0-5; 0 clears)
func (s *Server) handleTrackRating(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	if s.Tags == nil {
		http.Error(w, "tag store not available", http.StatusServiceUnavailable)
		return
	}

	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		rating := s.Tags.GetTrackRating(trackID)
		writeJSON(w, struct {
			Rating uint8 `json:"rating"`
		}{rating})

	case "PUT", "POST":
		var req struct {
			Rating uint8 `json:"rating"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Rating > 5 {
			req.Rating = 5
		}
		s.Tags.SetTrackRating(trackID, req.Rating)
		// Mirror onto the in-memory library so browse menus / CDJ requests
		// see the new value without needing a restart.
		if t := s.Library.Track(trackID); t != nil {
			t.Rating = req.Rating
		}
		s.Library.Touch() // rating lives in the tag store, not a library Save()
		if s.Device != nil {
			s.Device.BroadcastRatingRefresh(trackID)
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/tracks/{id}/phrases — list detected phrases (intro/up/down/chorus/outro)
// with start/end times in milliseconds for visual rendering.
func (s *Server) handleTrackPhrases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/tracks/")
	if trackID == 0 {
		http.Error(w, "track ID required", http.StatusBadRequest)
		return
	}
	type PhraseInfo struct {
		Kind       uint16  `json:"kind"`     // 1=intro, 2=up, 3=down, 5=chorus, 6=outro
		Name       string  `json:"name"`     // human label
		StartMs    uint32  `json:"start_ms"` // start time in milliseconds
		EndMs      uint32  `json:"end_ms"`   // end time in milliseconds
		Energy     float64 `json:"energy,omitempty"`
		HasVocal   bool    `json:"has_vocal"`   // contains singing at the default threshold
		VocalScore float64 `json:"vocal_score"` // raw score, for the UI's live sensitivity toggle
	}
	names := map[uint16]string{
		1: "INTRO", 2: "UP", 3: "DOWN", 5: "CHORUS", 6: "OUTRO",
	}
	out := []PhraseInfo{}
	if s.Analysis != nil {
		// Set the path first so Get can fall back to the on-disk analysis cache
		// on a cold first open. Without this, pathMap is empty for a track that
		// isn't already in memory, Get returns nil, and phrases stay blank until
		// some other request (e.g. /api/analysis, fetched in parallel) loads the
		// result — which is why they only appeared after re-opening the track.
		if s.Library != nil {
			if t := s.Library.Track(trackID); t != nil && t.FilePath != "" {
				s.Analysis.SetPath(trackID, t.FilePath)
			}
		}
		if res := s.Analysis.Get(trackID); res != nil && len(res.Phrases) > 0 && res.BPM > 0 {
			msPerBeat := 60000.0 / res.BPM
			// Map a 1-based grid beat to its actual time from the beat grid, so
			// phrase boundaries line up with the waveform/beats instead of being
			// anchored at t=0 (which dropped the lead-in before the first beat
			// and left phrases shifted left of the first downbeat). Falls back to
			// a linear estimate (extrapolated past the grid ends, or from 0 when
			// no grid is present).
			beatMs := func(beat int) float64 {
				n := len(res.Beats)
				if n == 0 {
					return float64(beat-1) * msPerBeat
				}
				if beat < 1 {
					return res.Beats[0] + float64(beat-1)*msPerBeat
				}
				if beat <= n {
					return res.Beats[beat-1]
				}
				return res.Beats[n-1] + float64(beat-n)*msPerBeat
			}
			// Prefer each phrase's absolute time (anchored to the audio at analysis
			// time), so it stays on the music when the beat grid is edited. Fall
			// back to mapping beat numbers through the current grid only for older
			// cached phrases that predate StartMs/EndMs.
			np := len(res.Phrases)
			starts := make([]float64, np)
			ends := make([]float64, np)
			for i, p := range res.Phrases {
				if p.EndMs > 0 {
					starts[i], ends[i] = p.StartMs, p.EndMs
				} else {
					starts[i], ends[i] = beatMs(p.StartBeat), beatMs(p.EndBeat)
				}
			}
			for i := range res.Phrases {
				name := names[res.Phrases[i].Kind]
				if name == "" {
					name = fmt.Sprintf("KIND %d", res.Phrases[i].Kind)
				}
				start, end := starts[i], ends[i]
				// Phrases are contiguous sections: each ends exactly where the next
				// begins. Snap the end to the next phrase's start so there's no gap
				// (covers older cached times computed before the EndBeat+1 fix too).
				if i+1 < np && starts[i+1] > start {
					end = starts[i+1]
				}
				if start < 0 {
					start = 0
				}
				if end < start {
					end = start
				}
				out = append(out, PhraseInfo{
					Kind:       res.Phrases[i].Kind,
					Name:       name,
					StartMs:    uint32(start),
					EndMs:      uint32(end),
					Energy:     res.Phrases[i].Energy,
					HasVocal:   res.Phrases[i].HasVocal,
					VocalScore: res.Phrases[i].VocalScore,
				})
			}
		}
	}
	writeJSON(w, out)
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

// ── Playlists ──────────────────────────────────────────────────────────
// User-defined playlists + folders. Mirrors the rekordbox PLAYLIST menu
// on the CDJ (phase 2 wires PlaylistStore into dbserver 0x1105).
//
// Tree shape is flat-with-parent-pointers: each entry has ParentID; the
// web UI walks the list and builds a tree.

// GET  /api/playlists           — flat list of every playlist/folder
// POST /api/playlists           — create {name, parent_id?, is_folder?}
func (s *Server) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if s.Playlists == nil {
		http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		writeJSON(w, s.Playlists.All())
	case "POST":
		// is_smart=true creates a smart playlist with the supplied rules
		// (folders + smart are mutually exclusive — server rejects both
		// flags being set together via PlaylistStore.CreateSmart's
		// isFolder=false hardcode).
		var req struct {
			Name     string      `json:"name"`
			ParentID uint32      `json:"parent_id"`
			IsFolder bool        `json:"is_folder"`
			IsSmart  bool        `json:"is_smart"`
			Rules    *SmartRules `json:"rules,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.IsSmart && req.IsFolder {
			http.Error(w, "is_smart and is_folder are mutually exclusive", http.StatusBadRequest)
			return
		}
		var (
			p   *PlaylistInfo
			err error
		)
		if req.IsSmart {
			p, err = s.Playlists.CreateSmart(req.Name, req.ParentID, req.Rules)
		} else {
			p, err = s.Playlists.Create(req.Name, req.ParentID, req.IsFolder)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT     /api/playlists/{id}        — {name?, parent_id?, sort_order?}
// DELETE  /api/playlists/{id}        — delete (recursive for folders)
// GET     /api/playlists/{id}/tracks — track list as []TrackInfo
// POST    /api/playlists/{id}/tracks — replace tracks {track_ids: [...]}
func (s *Server) handlePlaylistByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if s.Playlists == nil {
		http.Error(w, "playlist store not available", http.StatusServiceUnavailable)
		return
	}

	// Parse /api/playlists/{id}[/tracks]
	path := strings.TrimPrefix(r.URL.Path, "/api/playlists/")
	parts := strings.Split(path, "/")
	var id uint32
	fmt.Sscanf(parts[0], "%d", &id)
	if id == 0 {
		http.Error(w, "invalid playlist ID", http.StatusBadRequest)
		return
	}

	// /tracks sub-resource: list or replace ordered tracks.
	if len(parts) >= 2 && parts[1] == "tracks" {
		s.handlePlaylistTracks(w, r, id)
		return
	}
	// /rules sub-resource: get or replace smart-playlist rules.
	if len(parts) >= 2 && parts[1] == "rules" {
		s.handlePlaylistRules(w, r, id)
		return
	}

	switch r.Method {
	case "PUT":
		// Optional updates — pointers distinguish "not sent" from zero.
		var req struct {
			Name      *string `json:"name,omitempty"`
			ParentID  *uint32 `json:"parent_id,omitempty"`
			SortOrder *int    `json:"sort_order,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name != nil {
			if err := s.Playlists.Rename(id, *req.Name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if req.ParentID != nil || req.SortOrder != nil {
			// Default order=-1 means "append" inside Move.
			order := -1
			if req.SortOrder != nil {
				order = *req.SortOrder
			}
			parent := uint32(0)
			if req.ParentID != nil {
				parent = *req.ParentID
			} else {
				// Keep current parent when only sort changed.
				if cur := s.Playlists.Get(id); cur != nil {
					parent = cur.ParentID
				}
			}
			if err := s.Playlists.Move(id, parent, order); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		writeJSON(w, struct{ OK bool }{true})

	case "DELETE":
		if err := s.Playlists.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, struct{ OK bool }{true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePlaylistTracks serves GET/POST /api/playlists/{id}/tracks.
// GET evaluates smart-playlist rules when the playlist is smart; for
// regular playlists it returns the stored ordered list. Either way
// the response is []TrackInfo so the web UI can render rows directly.
// POST replaces the entire track list with the supplied ordered IDs
// (rejected for smart playlists by PlaylistStore.SetTracks).
func (s *Server) handlePlaylistTracks(w http.ResponseWriter, r *http.Request, id uint32) {
	switch r.Method {
	case "GET":
		ids := s.Playlists.TracksFor(id, s.Library, s.Tags)
		out := make([]TrackInfo, 0, len(ids))
		if s.Library != nil {
			for _, tid := range ids {
				t := s.Library.Track(tid)
				if t == nil {
					continue
				}
				out = append(out, s.libTrackToInfo(t))
			}
		}
		writeJSON(w, out)
	case "POST":
		var req struct {
			TrackIDs []uint32 `json:"track_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Playlists.SetTracks(id, req.TrackIDs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePlaylistRules serves GET/PUT /api/playlists/{id}/rules.
// GET returns the SmartRules tree (or null if regular playlist).
// PUT replaces the rules. The body is the SmartRules JSON directly
// (no wrapper) so the editor can post the same shape it reads.
func (s *Server) handlePlaylistRules(w http.ResponseWriter, r *http.Request, id uint32) {
	switch r.Method {
	case "GET":
		p := s.Playlists.Get(id)
		if p == nil {
			http.Error(w, "playlist not found", http.StatusNotFound)
			return
		}
		writeJSON(w, p.Rules)
	case "PUT":
		var rules SmartRules
		if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Playlists.SetRules(id, &rules); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── CDJ root menu ──────────────────────────────────────────────────────
// User-configurable visibility + order of the LINK menu on the deck.

// GET /api/menu-items  → flat ordered list, visible + hidden
// PUT /api/menu-items  → replace ordered list {items: [{key, label?, visible}, ...]}
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
			Items             []MenuItem         `json:"items"`
			TrackDetail       string             `json:"track_detail"`
			TrackDetailFields []TrackDetailField `json:"track_detail_fields"`
		}{
			Items:             s.Menu.All(),
			TrackDetail:       s.Menu.TrackDetail(),
			TrackDetailFields: TrackDetailFields,
		})
	case "PUT":
		// Items + track_detail are optional/independent — the UI
		// usually sends both, but a thin client could PUT just one
		// (e.g. {"track_detail":"artist"}) to update only that.
		var req struct {
			Items       *[]MenuItem `json:"items,omitempty"`
			TrackDetail *string     `json:"track_detail,omitempty"`
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
		writeJSON(w, struct{ OK bool }{true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// POST /api/menu-items/reset → restore default order + visibility
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
	s.Menu.ResetToDefaults()
	writeJSON(w, struct{ OK bool }{true})
}

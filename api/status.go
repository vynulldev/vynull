// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/internal/buildinfo"
)

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
	External      bool    `json:"external,omitempty"`     // track loaded from a source other than us
	Source        string  `json:"source,omitempty"`       // that source, e.g. "USB · player 2"
	ArtworkURL    string  `json:"artwork_url,omitempty"`  // cover-art endpoint for the loaded track
	AnalysisURL   string  `json:"analysis_url,omitempty"` // waveform+cues endpoint (external tracks only)
	WaveformURL   string  `json:"waveform_url,omitempty"` // colour-detail waveform endpoint (local or external)
	IsPlaying     bool    `json:"is_playing"`
	IsMaster      bool    `json:"is_master"`
	IsSync        bool    `json:"is_sync,omitempty"`
	OnAir         bool    `json:"on_air"`
	BeatInBar     uint8   `json:"beat_in_bar"`               // 1-4 (0 = unknown), from CDJ status packet
	BeatInTrack   uint32  `json:"beat_in_track,omitempty"`   // beats elapsed since track start (0 = unknown)
	BeatAgeMs     uint32  `json:"beat_age_ms,omitempty"`     // ms since that beat fired — the sub-beat phase, so a client can place the playhead between beats
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

// handleVersion reports which build of Vynull is running.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildinfo.Get())
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
		// Cover-art endpoint: external tracks resolve via the source player's
		// media (over NFS), local tracks via the library by track ID.
		artURL := ""
		analysisURL := ""
		waveURL := ""
		if ps.Status.TrackID > 0 {
			if ps.External {
				artURL = fmt.Sprintf("/api/artwork/ext/%d/%d/%d", ps.Status.TrackDevice, ps.Status.TrackSlot, ps.Status.TrackID)
				// Waveform + cues come from the source player's ANLZ over NFS;
				// local tracks use the existing track-ID waveform/cue endpoints.
				analysisURL = fmt.Sprintf("/api/analysis/ext/%d/%d/%d", ps.Status.TrackDevice, ps.Status.TrackSlot, ps.Status.TrackID)
				waveURL = analysisURL // ext endpoint returns {waveform, beats, duration_ms}
			} else {
				artURL = fmt.Sprintf("/api/artwork/%d", ps.Status.TrackID)
				waveURL = fmt.Sprintf("/api/analysis/waveform/%d?type=detail", ps.Status.TrackID)
			}
		}
		out = append(out, PlayerInfo{
			DeviceNumber:  ps.Status.DeviceNumber,
			Name:          ps.Status.Name,
			TrackID:       ps.Status.TrackID,
			TrackTitle:    ps.TrackName,
			Artist:        ps.Artist,
			ArtworkURL:    artURL,
			AnalysisURL:   analysisURL,
			WaveformURL:   waveURL,
			BPM:           float64(ps.Status.BPM) / 100.0,
			PitchPct:      pitchPct,
			Key:           ps.Key,
			External:      ps.External,
			Source:        ps.Source,
			IsPlaying:     ps.Status.IsPlaying,
			IsMaster:      ps.Status.IsMaster,
			IsSync:        ps.Status.IsSync,
			OnAir:         ps.Status.IsOnAir,
			BeatInBar:     ps.Status.BeatInBar,
			BeatInTrack:   ps.Status.BeatInTrack,
			BeatAgeMs:     beatAgeMs(ps),
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

// beatAgeMs returns how long ago the playing deck's current beat fired, in
// ms — the sub-beat playhead phase that BeatInTrack alone quantizes away.
// Anchored on the deck's 0x28 beat packets when they're heard (exact), else
// on the status packet that advanced the counter (~200ms granularity). 0 for
// paused/unknown states, where age would just measure time since pause.
func beatAgeMs(ps *device.PlayerState) uint32 {
	if !ps.Status.IsPlaying || ps.Status.BeatInTrack == 0 || ps.BeatChangedAt.IsZero() {
		return 0
	}
	a := time.Since(ps.BeatChangedAt)
	if a <= 0 {
		return 0
	}
	return uint32(a.Milliseconds())
}

// NowPlaying is the currently-audible track, for the streaming overlay: the
// on-air playing deck (preferring the tempo master during a transition), or the
// playing deck when no mixer reports on-air. Playing is false when nothing is.
type NowPlaying struct {
	Playing      bool    `json:"playing"`
	DeviceNumber uint8   `json:"device_number,omitempty"`
	TrackID      uint32  `json:"track_id,omitempty"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	BPM          float64 `json:"bpm,omitempty"`
	Key          string  `json:"key,omitempty"`
	DurationMs   uint32  `json:"duration_ms,omitempty"`
	BeatInTrack  uint32  `json:"beat_in_track,omitempty"`
	BeatAgeMs    uint32  `json:"beat_age_ms,omitempty"`
	PitchPct     float64 `json:"pitch_pct,omitempty"`
	// ArtworkURL is the cover-art endpoint for the audible track: the library
	// endpoint for our own tracks, or the external endpoint (the source player's
	// media over NFS) when a deck plays from its own USB/SD.
	ArtworkURL string `json:"artwork_url,omitempty"`
	// WaveformURL is the colour-detail waveform endpoint: the library waveform
	// endpoint (returns {entries}) for our own tracks, or the external analysis
	// endpoint (returns {waveform, beats, duration_ms}) for external ones.
	WaveformURL string `json:"waveform_url,omitempty"`
}

func (s *Server) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, nowPlayingFrom(s.getPlayers()))
}

// NowPlayingSnapshot returns the audible deck's now-playing state — the
// same projection /api/nowplaying serves — for in-process consumers (the
// MPRIS publisher).
func (s *Server) NowPlayingSnapshot() NowPlaying {
	return nowPlayingFrom(s.getPlayers())
}

// nowPlayingFrom projects the selected player into a NowPlaying.
func nowPlayingFrom(players []PlayerInfo) NowPlaying {
	p := selectNowPlaying(players)
	if p == nil {
		return NowPlaying{}
	}
	return NowPlaying{
		Playing:      true,
		DeviceNumber: p.DeviceNumber,
		TrackID:      p.TrackID,
		Title:        p.TrackTitle,
		Artist:       p.Artist,
		BPM:          p.BPM,
		Key:          p.Key,
		DurationMs:   p.DurationMs,
		BeatInTrack:  p.BeatInTrack,
		BeatAgeMs:    p.BeatAgeMs,
		PitchPct:     p.PitchPct,
		ArtworkURL:   p.ArtworkURL,
		WaveformURL:  p.WaveformURL,
	}
}

// selectNowPlaying picks the audible deck from the live states: among decks that
// are playing with a loaded track, on-air beats off-air, then the tempo master
// beats the rest, then the lowest device number breaks ties. Returns nil when no
// deck is playing.
func selectNowPlaying(players []PlayerInfo) *PlayerInfo {
	var best *PlayerInfo
	for i := range players {
		p := &players[i]
		if !p.IsPlaying || p.TrackTitle == "" {
			continue
		}
		if best == nil || betterNowPlaying(p, best) {
			best = p
		}
	}
	return best
}

func betterNowPlaying(a, b *PlayerInfo) bool {
	if a.OnAir != b.OnAir {
		return a.OnAir
	}
	if a.IsMaster != b.IsMaster {
		return a.IsMaster
	}
	return a.DeviceNumber < b.DeviceNumber
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

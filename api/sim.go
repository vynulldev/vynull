// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vynulldev/vynull/proto"
)

// handleSim drives the virtual playing deck (the CDJ emulator, --simulate).
// Routes under /api/sim/: status (GET), and load/play/pause/cue/seek/pitch/
// eject (POST). Every call returns the deck's current status as JSON. See
// docs/design/cdj-emulator.md.
func (s *Server) handleSim(w http.ResponseWriter, r *http.Request) {
	if s.Device == nil || s.Device.Sim == nil {
		http.Error(w, "simulate not enabled (start with --simulate)", http.StatusServiceUnavailable)
		return
	}
	sim := s.Device.Sim
	action := strings.TrimPrefix(r.URL.Path, "/api/sim/")

	// status is the only GET; everything else mutates and wants POST.
	if action == "status" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, sim.Status())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "play":
		sim.Play()
	case "pause":
		sim.Pause()
	case "cue":
		sim.Cue()
	case "eject":
		sim.Eject()
	case "seek":
		var req struct {
			PositionMs float64 `json:"position_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		sim.Seek(req.PositionMs)
	case "pitch":
		var req struct {
			PitchPct float64 `json:"pitch_pct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		sim.SetPitch(req.PitchPct)
	case "load":
		if !s.simLoad(w, r) {
			return
		}
	default:
		http.Error(w, "unknown sim action: "+action, http.StatusNotFound)
		return
	}

	writeJSON(w, sim.Status())
}

// simLoad resolves a track to its analysis and loads it onto the deck. It
// writes an error response and returns false on failure; on success it loads
// the deck and returns true (the caller writes the status).
func (s *Server) simLoad(w http.ResponseWriter, r *http.Request) bool {
	var req struct {
		TrackID  uint32 `json:"track_id"`
		FilePath string `json:"file_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if req.TrackID == 0 && req.FilePath != "" {
		req.TrackID = s.resolveTrackID(req.FilePath)
	}
	if req.TrackID == 0 {
		http.Error(w, "track_id or file_path required", http.StatusBadRequest)
		return false
	}
	if s.Analysis == nil {
		http.Error(w, "analysis store not available", http.StatusServiceUnavailable)
		return false
	}
	res := s.Analysis.Get(req.TrackID)
	if res == nil {
		http.Error(w, "no analysis for track (analyze it first)", http.StatusNotFound)
		return false
	}

	// The deck is loaded "from us": our device number, USB slot, rekordbox type.
	s.Device.Sim.Load(
		req.TrackID,
		s.Device.DeviceNumber,
		proto.SlotUSB,
		1,
		res.Beats,
		res.DownbeatIndex,
		float64(res.Duration)*1000,
		res.BPM,
	)
	return true
}

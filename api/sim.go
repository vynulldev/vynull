// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/proto"
)

// simListResponse is the manager-level view returned by GET /api/sim/status and
// after every add/remove/renumber: all decks plus the deck cap.
type simListResponse struct {
	Decks []device.SimDeckStatus `json:"decks"`
	Max   int                    `json:"max"`
}

// handleSim drives the virtual playing decks (the CDJ emulator, --simulate).
//
// Manager-level routes under /api/sim/:
//
//	status            GET   list every deck (+ the deck cap)
//	add               POST  {number?}       add a deck (0/omitted = lowest free)
//	remove            POST  {number}        remove a deck
//	renumber          POST  {number, to}    change a deck's player number
//
// Per-deck routes under /api/sim/{number}/:
//
//	status                  GET   that deck's state
//	load                    POST  {track_id|file_path}
//	play|pause|cue|eject    POST
//	seek                    POST  {position_ms}
//	pitch                   POST  {pitch_pct}
//	onair                   POST  {on}            mixer channel up (now-playing)
//	master                  POST  {on}            tempo master
//
// See docs/design/cdj-emulator.md.
func (s *Server) handleSim(w http.ResponseWriter, r *http.Request) {
	if s.Device == nil || s.Device.Sim == nil {
		http.Error(w, "simulate not enabled (start with --simulate)", http.StatusServiceUnavailable)
		return
	}
	mgr := s.Device.Sim
	rest := strings.TrimPrefix(r.URL.Path, "/api/sim/")

	// Per-deck: "<number>/<action>".
	if i := strings.IndexByte(rest, '/'); i > 0 {
		num, err := strconv.Atoi(rest[:i])
		if err != nil || num < 1 || num > device.MaxSimDecks {
			http.Error(w, "bad player number", http.StatusNotFound)
			return
		}
		deck := mgr.Get(uint8(num))
		if deck == nil {
			http.Error(w, "no such virtual player", http.StatusNotFound)
			return
		}
		s.handleSimDeck(w, r, uint8(num), deck, rest[i+1:])
		return
	}

	// Manager-level.
	switch rest {
	case "status":
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
	case "add":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Number uint8 `json:"number"`
		}
		json.NewDecoder(r.Body).Decode(&req) // body optional
		if _, err := mgr.Add(req.Number); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	case "remove":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Number uint8 `json:"number"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := mgr.Remove(req.Number); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	case "renumber":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Number uint8 `json:"number"`
			To     uint8 `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := mgr.Renumber(req.Number, req.To); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	default:
		http.Error(w, "unknown sim action: "+rest, http.StatusNotFound)
		return
	}

	writeJSON(w, simListResponse{Decks: mgr.Statuses(), Max: device.MaxSimDecks})
}

// handleSimDeck handles the per-deck actions. On success it responds with the
// deck's status (with its number filled in).
func (s *Server) handleSimDeck(w http.ResponseWriter, r *http.Request, num uint8, deck *device.SimDeck, action string) {
	respond := func() {
		st := deck.Status()
		st.Number = num
		writeJSON(w, st)
	}

	if action == "status" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		respond()
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "play":
		deck.Play()
	case "pause":
		deck.Pause()
	case "cue":
		deck.Cue()
	case "eject":
		deck.Eject()
	case "seek":
		var req struct {
			PositionMs float64 `json:"position_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		deck.Seek(req.PositionMs)
	case "pitch":
		var req struct {
			PitchPct float64 `json:"pitch_pct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		deck.SetPitch(req.PitchPct)
	case "onair":
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		deck.SetOnAir(req.On)
	case "master":
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		deck.SetMaster(req.On)
	case "load":
		if !s.simLoad(w, r, deck) {
			return
		}
	default:
		http.Error(w, "unknown sim action: "+action, http.StatusNotFound)
		return
	}
	respond()
}

// simLoad resolves a track to its analysis and loads it onto deck. It writes an
// error response and returns false on failure.
func (s *Server) simLoad(w http.ResponseWriter, r *http.Request, deck *device.SimDeck) bool {
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
	// getOrAnalyze (not Analysis.Get) so a track analyzed in an earlier session
	// loads: it resolves the file path from the library/PDB and pulls the
	// on-disk cache, analyzing synchronously only as a last resort.
	res := s.getOrAnalyze(req.TrackID)
	if res == nil {
		http.Error(w, "track not found, or analysis unavailable", http.StatusNotFound)
		return false
	}

	// The deck plays a track linked from our rekordbox library: the source is
	// our own device number (rekordbox slot, rekordbox type). So our monitor
	// resolves it locally (externalSource is false, since TrackDevice == our
	// SelfDevice) and metadata clients (prolink-tools) query our dbserver as
	// the rekordbox source.
	deck.Load(
		req.TrackID,
		s.Device.DeviceNumber,
		proto.SlotRekordbox,
		1,
		res.Beats,
		res.DownbeatIndex,
		float64(res.Duration)*1000,
		res.BPM,
	)
	return true
}

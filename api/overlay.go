// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
)

// OverlayConfig is the now-playing overlay's appearance, edited from the web UI
// settings tab and read by the /overlay page (which runs in a separate browser,
// e.g. OBS — hence a server-side store rather than browser-local prefs).
type OverlayConfig struct {
	Position       string `json:"position"`        // bottom-left | bottom-right | top-left | top-right | bottom-center | top-center
	Style          string `json:"style"`           // vinyl | cover
	Accent         string `json:"accent"`          // #rrggbb
	ShowMeta       bool   `json:"show_meta"`       // show the BPM / key line
	ShowNowplaying bool   `json:"show_nowplaying"` // show the now-playing card (the audible deck)
	ShowWaveform   bool   `json:"show_waveform"`   // show the now-playing card's scrolling waveform
	ShowDecks      bool   `json:"show_decks"`      // show the per-deck section (which deck plays what)
	ShowHistory    bool   `json:"show_history"`    // show the recently-played list
	HistoryCount   int    `json:"history_count"`   // how many recent tracks to list
	Label          string `json:"label"`           // header text (empty = no label)
}

func defaultOverlayConfig() OverlayConfig {
	return OverlayConfig{
		Position:       "bottom-left",
		Style:          "vinyl",
		Accent:         "#ff7714",
		ShowMeta:       true,
		ShowNowplaying: true,
		ShowWaveform:   true,
		ShowDecks:      false,
		ShowHistory:    false,
		HistoryCount:   5,
		Label:          "Now Playing",
	}
}

var (
	overlayPositions = map[string]bool{
		"bottom-left": true, "bottom-right": true, "top-left": true,
		"top-right": true, "bottom-center": true, "top-center": true,
	}
	overlayStyles = map[string]bool{"vinyl": true, "cover": true}
	hexColorRe    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// sanitize clamps a config to valid values, replacing anything invalid with the
// default so a bad edit or hand-tweaked file can't break the overlay.
func (c OverlayConfig) sanitize() OverlayConfig {
	d := defaultOverlayConfig()
	if !overlayPositions[c.Position] {
		c.Position = d.Position
	}
	if !overlayStyles[c.Style] {
		c.Style = d.Style
	}
	if !hexColorRe.MatchString(c.Accent) {
		c.Accent = d.Accent
	}
	if r := []rune(strings.TrimSpace(c.Label)); len(r) > 40 {
		c.Label = string(r[:40])
	} else {
		c.Label = string(r)
	}
	if c.HistoryCount < 0 {
		c.HistoryCount = 0
	} else if c.HistoryCount > 20 {
		c.HistoryCount = 20
	}
	return c
}

// OverlayStore holds the overlay config in memory and persists it as JSON.
type OverlayStore struct {
	mu   sync.RWMutex
	path string
	cfg  OverlayConfig
}

// NewOverlayStore loads the config from path (defaults if absent/invalid).
func NewOverlayStore(path string) *OverlayStore {
	s := &OverlayStore{path: path, cfg: defaultOverlayConfig()}
	if data, err := os.ReadFile(path); err == nil {
		var c OverlayConfig
		if json.Unmarshal(data, &c) == nil {
			s.cfg = c.sanitize()
		}
	}
	return s
}

func (s *OverlayStore) Get() OverlayConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *OverlayStore) Set(c OverlayConfig) error {
	c = c.sanitize()
	s.mu.Lock()
	s.cfg = c
	path := s.path
	s.mu.Unlock()
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

// handleOverlayConfig serves GET (read) and PUT/POST (update) of the config.
func (s *Server) handleOverlayConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	switch r.Method {
	case http.MethodGet:
		cfg := defaultOverlayConfig()
		if s.Overlay != nil {
			cfg = s.Overlay.Get()
		}
		writeJSON(w, cfg)
	case http.MethodPut, http.MethodPost:
		if s.Overlay == nil {
			http.Error(w, "overlay config not available", http.StatusServiceUnavailable)
			return
		}
		var c OverlayConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Overlay.Set(c); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.Overlay.Get())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

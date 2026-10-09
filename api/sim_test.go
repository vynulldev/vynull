// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
)

func simTestServer() *Server {
	store := analysis.NewStore()
	beats := make([]float64, 600)
	for i := range beats {
		beats[i] = float64(i) * (60000.0 / 128.0)
	}
	store.Set(42, &analysis.Result{
		BPM:           128,
		Duration:      300,
		Beats:         beats,
		DownbeatIndex: 0,
	})
	mgr := device.NewSimManager()
	mgr.Add(1) // start with Player 1, as --simulate does
	return &Server{
		Device:   &device.VirtualDevice{DeviceNumber: 3, Sim: mgr},
		Analysis: store,
	}
}

// simReq performs one /api/sim request and returns the recorder.
func simReq(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/sim/"+path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/sim/"+path, nil)
	}
	w := httptest.NewRecorder()
	s.handleSim(w, r)
	return w
}

// deckStatus drives a per-deck route and decodes the deck status.
func deckStatus(t *testing.T, s *Server, method, path, body string) device.SimDeckStatus {
	t.Helper()
	w := simReq(t, s, method, path, body)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s = %d (%s)", method, path, w.Code, strings.TrimSpace(w.Body.String()))
	}
	var st device.SimDeckStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("%s %s: bad JSON: %v", method, path, err)
	}
	return st
}

func simList(t *testing.T, s *Server) simListResponse {
	t.Helper()
	w := simReq(t, s, http.MethodGet, "status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list simListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("status: bad JSON: %v", err)
	}
	return list
}

func TestSimDeckAPIFlow(t *testing.T) {
	s := simTestServer()

	// One deck (Player 1), not loaded.
	list := simList(t, s)
	if len(list.Decks) != 1 || list.Decks[0].Number != 1 || list.Decks[0].Loaded {
		t.Fatalf("initial list = %+v, want one empty Player 1", list.Decks)
	}
	if list.Max != device.MaxSimDecks {
		t.Errorf("max = %d, want %d", list.Max, device.MaxSimDecks)
	}

	// Load, play, pitch on deck 1.
	st := deckStatus(t, s, http.MethodPost, "1/load", `{"track_id":42}`)
	if !st.Loaded || st.TrackID != 42 || st.State != "CUED" || st.Number != 1 {
		t.Errorf("after load: %+v", st)
	}
	st = deckStatus(t, s, http.MethodPost, "1/play", "")
	if st.State != "PLAYING" || !st.Playing {
		t.Errorf("after play: %+v", st)
	}
	st = deckStatus(t, s, http.MethodPost, "1/pitch", `{"pitch_pct":6}`)
	if st.PitchPct != 6 || st.EffectiveBPM < 135 || st.EffectiveBPM > 137 {
		t.Errorf("after pitch: %+v", st)
	}
	deckStatus(t, s, http.MethodPost, "1/pause", "")
	st = deckStatus(t, s, http.MethodPost, "1/seek", `{"position_ms":60000}`)
	if st.PositionMs < 59900 || st.PositionMs > 60100 {
		t.Errorf("after seek: %.1f", st.PositionMs)
	}
	st = deckStatus(t, s, http.MethodGet, "1/status", "")
	if st.Number != 1 {
		t.Errorf("per-deck status number = %d", st.Number)
	}
}

func TestSimManagerAPI(t *testing.T) {
	s := simTestServer()

	// Add auto-assigns the lowest free number (2).
	w := simReq(t, s, http.MethodPost, "add", "")
	if w.Code != http.StatusOK {
		t.Fatalf("add = %d (%s)", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if list := simList(t, s); len(list.Decks) != 2 || list.Decks[1].Number != 2 {
		t.Fatalf("after add, decks = %+v", list.Decks)
	}

	// Add an explicit number.
	if w := simReq(t, s, http.MethodPost, "add", `{"number":4}`); w.Code != http.StatusOK {
		t.Fatalf("add 4 = %d", w.Code)
	}
	// Adding a taken number conflicts.
	if w := simReq(t, s, http.MethodPost, "add", `{"number":1}`); w.Code != http.StatusConflict {
		t.Errorf("add taken number = %d, want 409", w.Code)
	}

	// Renumber 2 -> 3.
	if w := simReq(t, s, http.MethodPost, "renumber", `{"number":2,"to":3}`); w.Code != http.StatusOK {
		t.Fatalf("renumber = %d (%s)", w.Code, strings.TrimSpace(w.Body.String()))
	}
	nums := map[uint8]bool{}
	for _, d := range simList(t, s).Decks {
		nums[d.Number] = true
	}
	if !nums[1] || !nums[3] || !nums[4] || nums[2] {
		t.Errorf("after renumber, numbers = %v, want {1,3,4}", nums)
	}

	// Fill to the cap, then adding fails.
	simReq(t, s, http.MethodPost, "add", `{"number":2}`) // now 1,2,3,4
	if w := simReq(t, s, http.MethodPost, "add", ""); w.Code != http.StatusConflict {
		t.Errorf("add when full = %d, want 409", w.Code)
	}

	// Remove 4.
	if w := simReq(t, s, http.MethodPost, "remove", `{"number":4}`); w.Code != http.StatusOK {
		t.Fatalf("remove = %d", w.Code)
	}
	if n := len(simList(t, s).Decks); n != 3 {
		t.Errorf("after remove, %d decks, want 3", n)
	}
	// Removing a gone deck 404s.
	if w := simReq(t, s, http.MethodPost, "remove", `{"number":4}`); w.Code != http.StatusNotFound {
		t.Errorf("remove missing = %d, want 404", w.Code)
	}
}

func TestSimAPIErrors(t *testing.T) {
	s := simTestServer()

	// Unknown per-deck action.
	if w := simReq(t, s, http.MethodPost, "1/frobnicate", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", w.Code)
	}
	// Action on a nonexistent deck.
	if w := simReq(t, s, http.MethodPost, "2/play", ""); w.Code != http.StatusNotFound {
		t.Errorf("play nonexistent deck = %d, want 404", w.Code)
	}
	// Load an unanalyzed track.
	if w := simReq(t, s, http.MethodPost, "1/load", `{"track_id":999}`); w.Code != http.StatusNotFound {
		t.Errorf("load unanalyzed = %d, want 404", w.Code)
	}
	// Wrong method on a mutating route.
	if w := simReq(t, s, http.MethodGet, "1/play", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET play = %d, want 405", w.Code)
	}

	// Simulate disabled.
	disabled := &Server{Device: &device.VirtualDevice{DeviceNumber: 3}}
	r := httptest.NewRequest(http.MethodGet, "/api/sim/status", nil)
	w := httptest.NewRecorder()
	disabled.handleSim(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("sim disabled = %d, want 503", w.Code)
	}
}

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
	return &Server{
		Device:   &device.VirtualDevice{DeviceNumber: 3, Sim: device.NewSimDeck()},
		Analysis: store,
	}
}

// simCall exercises one /api/sim route and decodes the status (when 200).
func simCall(t *testing.T, s *Server, method, action, body string) (*httptest.ResponseRecorder, device.SimDeckStatus) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/sim/"+action, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/sim/"+action, nil)
	}
	w := httptest.NewRecorder()
	s.handleSim(w, r)
	var st device.SimDeckStatus
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("%s %s: bad JSON: %v", method, action, err)
		}
	}
	return w, st
}

func TestSimAPIFlow(t *testing.T) {
	s := simTestServer()

	// Empty deck.
	w, st := simCall(t, s, http.MethodGet, "status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if st.Loaded {
		t.Error("deck reports loaded before any load")
	}

	// Load a track.
	w, st = simCall(t, s, http.MethodPost, "load", `{"track_id":42}`)
	if w.Code != http.StatusOK {
		t.Fatalf("load = %d (%s), want 200", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if !st.Loaded || st.TrackID != 42 || st.State != "CUED" {
		t.Errorf("after load: loaded=%v id=%d state=%q, want true/42/CUED", st.Loaded, st.TrackID, st.State)
	}
	if st.BPM != 128 {
		t.Errorf("BPM = %.1f, want 128", st.BPM)
	}

	// Play.
	_, st = simCall(t, s, http.MethodPost, "play", "")
	if st.State != "PLAYING" || !st.Playing {
		t.Errorf("after play: state=%q playing=%v, want PLAYING/true", st.State, st.Playing)
	}

	// Pitch.
	_, st = simCall(t, s, http.MethodPost, "pitch", `{"pitch_pct":6}`)
	if st.PitchPct != 6 {
		t.Errorf("pitch = %.2f, want 6", st.PitchPct)
	}
	if st.EffectiveBPM < 135 || st.EffectiveBPM > 137 {
		t.Errorf("effective BPM at +6%% = %.2f, want ~135.7", st.EffectiveBPM)
	}

	// Pause, then seek deterministically.
	simCall(t, s, http.MethodPost, "pause", "")
	_, st = simCall(t, s, http.MethodPost, "seek", `{"position_ms":60000}`)
	if st.PositionMs < 59900 || st.PositionMs > 60100 {
		t.Errorf("after seek to 60s, position = %.1fms", st.PositionMs)
	}
}

func TestSimAPIErrors(t *testing.T) {
	s := simTestServer()

	// Unknown action.
	w, _ := simCall(t, s, http.MethodPost, "frobnicate", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", w.Code)
	}

	// Load an unanalyzed track.
	w, _ = simCall(t, s, http.MethodPost, "load", `{"track_id":999}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("load unanalyzed = %d, want 404", w.Code)
	}

	// Wrong method on a mutating route.
	w, _ = simCall(t, s, http.MethodGet, "play", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET play = %d, want 405", w.Code)
	}

	// Simulate disabled.
	disabled := &Server{Device: &device.VirtualDevice{DeviceNumber: 3}}
	r := httptest.NewRequest(http.MethodGet, "/api/sim/status", nil)
	w = httptest.NewRecorder()
	disabled.handleSim(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("sim disabled = %d, want 503", w.Code)
	}
}

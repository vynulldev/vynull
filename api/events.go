// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents streams live deck + now-playing state as Server-Sent Events, so
// a frontend gets push updates instead of polling /api/players. Each event is:
//
//	event: state
//	data: {"players":[...],"nowplaying":{...}}
//
// A snapshot is sent on change — effectively ~4 Hz while a deck is active (the
// playhead's beat_age_ms keeps changing) and nothing while idle, with a
// heartbeat comment to hold the connection open. beat_age_ms lets the client
// interpolate the playhead between events, so a few per second render smoothly.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// CORS is applied by the mux middleware.

	ctx := r.Context()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	var last []byte
	send := func() {
		players := s.getPlayers()
		snap := struct {
			Players    []PlayerInfo `json:"players"`
			NowPlaying NowPlaying   `json:"nowplaying"`
		}{Players: players, NowPlaying: nowPlayingFrom(players)}
		data, err := json.Marshal(snap)
		if err != nil || bytes.Equal(data, last) {
			return // marshal failed, or nothing changed since the last event
		}
		last = data
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		flusher.Flush()
	}

	send() // prime the client immediately on connect
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			send()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
)

// CueInfo is the API representation of a cue point.
type CueInfo struct {
	Number  uint16 `json:"number"`   // 1=A, 2=B, 3=C, ...
	Type    string `json:"type"`     // "cue" or "loop"
	TimeMs  uint32 `json:"time_ms"`  // position in milliseconds
	LoopMs  int32  `json:"loop_ms"`  // loop end in ms (-1 if not loop)
	ColorID uint32 `json:"color_id"` // 0=default, 1=pink, 2=red, etc.
	Label   string `json:"label"`    // optional label
}

// importedCuesToInfo maps ANLZ-parsed cues to CueInfo. Hot cues keep their pad
// number (A=1..H=8); memory cues are numbered 9.. in order.
func importedCuesToInfo(cues []analysis.ImportedCue) []CueInfo {
	out := make([]CueInfo, 0, len(cues))
	mem := 8
	for _, c := range cues {
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
		out = append(out, ci)
	}
	return out
}

// handleAllCues returns a {trackID: [cue, ...]} map of every track that has
// cues. Used by the web UI's library page to overlay cue markers on every
// waveform thumbnail in a single round-trip instead of per-row fetches.
func (s *Server) handleAllCues(w http.ResponseWriter, r *http.Request) {
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
		if s.Device != nil {
			// The deck re-fetches the cues of its loaded track, so a
			// colour edit shows without reloading it.
			s.Device.BroadcastTrackRefresh(trackID)
		}
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
		if s.Device != nil {
			s.Device.BroadcastTrackRefresh(trackID)
		}
		writeJSON(w, struct {
			OK bool `json:"ok"`
		}{true})
	}
}

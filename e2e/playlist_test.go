// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestPlaylistMembership covers the API side of playlist editing:
// playlist create, wholesale set-tracks (the operation behind both the
// per-row × and bulk "− PLAYLIST" UI affordances), removal leaving library
// rows intact, and smart-playlist rule resolution. The UI wiring itself
// (which buttons render where) stays a manual/browser check.
func TestPlaylistMembership(t *testing.T) {
	s := startServer(t, "")
	media := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ {
		paths = append(paths, kickWAV(t, media, fmt.Sprintf("pl%d.wav", i), float64(122+i*2)))
	}
	s.addTracks(paths...)
	s.waitFor("tracks analyzed", 2*time.Minute, func() bool {
		n := 0
		for _, tr := range s.tracks() {
			if tr.BPM > 0 {
				n++
			}
		}
		return n == 3
	})
	all := s.tracks()
	ids := make([]uint32, len(all))
	for i, tr := range all {
		ids[i] = tr.ID
	}

	// Create a regular playlist and set its membership wholesale.
	var pl struct {
		ID uint32 `json:"id"`
	}
	s.postOK("/api/playlists", map[string]any{"name": "E2E Set", "parent_id": 0, "is_folder": false}, &pl)
	s.postOK(fmt.Sprintf("/api/playlists/%d/tracks", pl.ID), map[string]any{"track_ids": ids}, nil)

	var got []track
	s.getJSON(fmt.Sprintf("/api/playlists/%d/tracks", pl.ID), &got)
	if len(got) != 3 {
		t.Fatalf("playlist has %d tracks, want 3", len(got))
	}

	// Remove the middle track the way the UI does: post the list minus it.
	s.postOK(fmt.Sprintf("/api/playlists/%d/tracks", pl.ID),
		map[string]any{"track_ids": []uint32{ids[0], ids[2]}}, nil)
	got = nil
	s.getJSON(fmt.Sprintf("/api/playlists/%d/tracks", pl.ID), &got)
	if len(got) != 2 || got[0].ID != ids[0] || got[1].ID != ids[2] {
		t.Fatalf("after removal: %+v, want [%d %d]", got, ids[0], ids[2])
	}
	// The removed track must still be in the library, analysis intact.
	if lib := s.tracks(); len(lib) != 3 {
		t.Fatalf("library shrank to %d rows after playlist removal", len(lib))
	}

	// Smart playlist: BPM >= 124 resolves to the two faster tracks.
	var sp struct {
		ID uint32 `json:"id"`
	}
	s.postOK("/api/playlists", map[string]any{
		"name": "E2E Smart", "is_smart": true,
		"rules": map[string]any{
			"combinator": "all",
			"conditions": []map[string]any{{"field": "bpm", "operator": "gte", "value": 124}},
		},
	}, &sp)
	got = nil
	s.getJSON(fmt.Sprintf("/api/playlists/%d/tracks", sp.ID), &got)
	if len(got) != 2 {
		t.Fatalf("smart playlist resolved %d tracks, want 2 (BPM >= 124)", len(got))
	}

	// Deleting the playlist leaves the library untouched.
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/playlists/%d", s.baseURL, pl.ID), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("delete playlist: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	if lib := s.tracks(); len(lib) != 3 {
		t.Fatalf("library has %d rows after playlist delete, want 3", len(lib))
	}
}

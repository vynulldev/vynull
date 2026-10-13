// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// Covers the per-track analysis lock (issue #70): toggling it, that it is
// reported on the track, and that it refuses the three grid-mutating endpoints
// with 409 while set and permits them once cleared.
func TestAnalysisLock(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Locked", Artist: "A", FilePath: "/music/one.flac", BPM: 128})
	srv := &Server{Library: lib, Analysis: analysis.NewStore()}
	h := srv.Handler()

	do := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Mutators are allowed while unlocked (beats PUT uses only the library).
	if w := do("PUT", "/api/tracks/1/beats", `{"bpm":130}`); w.Code != 200 {
		t.Fatalf("beats PUT while unlocked: want 200, got %d (%s)", w.Code, w.Body)
	}

	// Lock the track.
	if w := do("PUT", "/api/tracks/1/lock", `{"locked":true}`); w.Code != 200 {
		t.Fatalf("lock PUT: want 200, got %d (%s)", w.Code, w.Body)
	}
	if w := do("GET", "/api/tracks/1/lock", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"locked":true`) {
		t.Fatalf("lock GET: want locked:true, got %d (%s)", w.Code, w.Body)
	}

	// The lock is reported on the track list.
	listW := do("GET", "/api/tracks", "")
	var rows []map[string]any
	json.Unmarshal(listW.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["analysis_locked"] != true {
		t.Fatalf("track list should carry analysis_locked:true, got %v", rows)
	}

	// Each grid-mutating endpoint must now refuse with 409 Conflict.
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"reanalyze", "POST", "/api/analysis/reanalyze/1", ""},
		{"adjust", "POST", "/api/analysis/beatgrid/adjust", `{"track_id":1,"offset_ms":10}`},
		{"beats", "PUT", "/api/tracks/1/beats", `{"bpm":124}`},
	} {
		if w := do(tc.method, tc.path, tc.body); w.Code != http.StatusConflict {
			t.Errorf("%s while locked: want 409, got %d (%s)", tc.name, w.Code, w.Body)
		}
	}

	// Unlock restores the previous behavior for a library-only edit.
	if w := do("PUT", "/api/tracks/1/lock", `{"locked":false}`); w.Code != 200 {
		t.Fatalf("unlock PUT: want 200, got %d", w.Code)
	}
	if w := do("PUT", "/api/tracks/1/beats", `{"bpm":124}`); w.Code == http.StatusConflict {
		t.Errorf("beats PUT after unlock should not be 409")
	}
}

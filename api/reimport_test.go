// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Reimporting tags covers every track for an empty body, only the listed
// tracks for track_ids, and nothing for a body that isn't valid JSON (which
// used to be taken as "everything", overwriting tag edits).
func TestReimportTracksBody(t *testing.T) {
	lib := library.New()
	for id := uint32(1); id <= 3; id++ {
		lib.AddTrack(&library.Track{ID: id, Title: "T", FilePath: "/nonexistent/t.mp3"})
	}
	s := &Server{Library: lib}
	post := func(body string) (int, int) {
		rec := httptest.NewRecorder()
		s.handleReimportTracks(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/reimport", strings.NewReader(body)))
		var resp struct{ Updated int }
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Updated
	}
	for _, tc := range []struct {
		body          string
		code, updated int
	}{
		{"", http.StatusOK, 3},
		{`{"track_ids":[2,3]}`, http.StatusOK, 2},
		{`{"track_ids":[2,`, http.StatusBadRequest, 0},
		{`not json`, http.StatusBadRequest, 0},
	} {
		if code, updated := post(tc.body); code != tc.code || updated != tc.updated {
			t.Errorf("body %q: status %d, updated %d; want %d, %d", tc.body, code, updated, tc.code, tc.updated)
		}
	}
}

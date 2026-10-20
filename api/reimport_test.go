// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/internal/tagtest"
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

// Reimporting a track whose file now carries a different artist files it
// under that artist, so the deck's ARTIST browse finds it there.
func TestReimportReindexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp3")
	tagtest.WriteMP3(t, path, map[string]string{"TIT2": "Song", "TPE1": "New"})
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Song", Artist: "Old", FilePath: path})
	s := &Server{Library: lib}
	rec := httptest.NewRecorder()
	s.handleReimportTracks(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/reimport", strings.NewReader("")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if lib.Track(1).Artist != "New" || len(lib.TracksByArtist("Old")) != 0 || len(lib.TracksByArtist("New")) != 1 ||
		!slices.Equal(lib.Artists(), []string{"New"}) {
		t.Errorf("artist %q; Old %d, New %d, artists %v; want New filed under New",
			lib.Track(1).Artist, len(lib.TracksByArtist("Old")), len(lib.TracksByArtist("New")), lib.Artists())
	}
}

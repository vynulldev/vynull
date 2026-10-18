// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Ratings and colours have one owner, the library: one set on a deck shows
// through the API, and one cleared in the web UI stays cleared after a
// restart instead of the imported value coming back.
func TestRatingAndColorHaveOneOwner(t *testing.T) {
	db := filepath.Join(t.TempDir(), "library.json")
	lib := library.New()
	lib.SetDBPath(db)
	lib.AddTrack(&library.Track{ID: 1, Title: "T", Rating: 4, ColorID: 2}) // as imported
	lib.Save()
	s := &Server{Library: lib}

	do := func(h http.HandlerFunc, method, path, body string) map[string]int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d %s", method, path, rec.Code, rec.Body.String())
		}
		var m map[string]int
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}

	// A deck's rating (0x2107) goes through the library; the API sees it.
	lib.SetRating(1, 3)
	if got := do(s.handleTrackRating, http.MethodGet, "/api/tracks/1/rating", "")["rating"]; got != 3 {
		t.Errorf("GET rating after a deck set it to 3: %d", got)
	}

	// Clearing both in the web UI survives a restart.
	do(s.handleTrackRating, http.MethodPut, "/api/tracks/1/rating", `{"rating":0}`)
	do(s.handleTrackColor, http.MethodPut, "/api/tracks/1/color", `{"color_id":0}`)
	reloaded := library.New()
	reloaded.SetDBPath(db)
	if tr := reloaded.Track(1); tr == nil || tr.Rating != 0 || tr.ColorID != 0 {
		t.Fatalf("after reload: %+v, want rating and colour cleared", tr)
	}

	// Out-of-range values: a rating clamps to 5, a colour is refused.
	do(s.handleTrackRating, http.MethodPut, "/api/tracks/1/rating", `{"rating":9}`)
	if lib.Track(1).Rating != 5 {
		t.Errorf("rating 9 stored as %d, want 5", lib.Track(1).Rating)
	}
	rec := httptest.NewRecorder()
	s.handleTrackColor(rec, httptest.NewRequest(http.MethodPut, "/api/tracks/1/color", strings.NewReader(`{"color_id":9}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("colour 9: status %d, want 400", rec.Code)
	}
}

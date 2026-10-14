// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func TestTrackRelatedEndpoint(t *testing.T) {
	lib := library.New()
	seed := lib.AddTrack(&library.Track{Title: "Seed", BPM: 126, Key: "8A"})
	lib.AddTrack(&library.Track{Title: "Match", BPM: 126.5, Key: "8A"})
	clash := lib.AddTrack(&library.Track{Title: "Clash", BPM: 126, Key: "3B"})
	srv := &Server{Library: lib}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	w := get("/api/tracks/" + itoa(seed) + "/related")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got []RelatedTrack
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Track.Title != "Match" || got[0].Score <= 0 || len(got[0].Reasons) == 0 {
		t.Fatalf("got %+v", got)
	}

	for path, want := range map[string]int{
		"/api/tracks/9999/related":                                 http.StatusNotFound,
		"/api/tracks/" + itoa(seed) + "/related?limit=0":           http.StatusBadRequest,
		"/api/tracks/" + itoa(seed) + "/related?bpm_tolerance=0.1": http.StatusBadRequest,
	} {
		if c := get(path).Code; c != want {
			t.Errorf("%s: status %d, want %d", path, c, want)
		}
	}
	// No matches is an empty list, not null: nothing goes with Clash's key.
	if b := get("/api/tracks/" + itoa(clash) + "/related").Body.String(); b != "[]\n" && b != "[]" {
		t.Errorf("no matches returned %q", b)
	}
}

func itoa(id uint32) string { return strconv.FormatUint(uint64(id), 10) }

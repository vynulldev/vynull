// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Covers the large-collection polling optimisation: the /api/tracks/rev
// counter, the ETag/304 on /api/tracks, that edits bump the revision, and
// that the list payload omits the detail-only fields.
func TestTracksRevAndETag(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{
		ID: 1, Title: "Track One", Artist: "Artist", FilePath: "/music/one.mp3",
		FileSize: 9_999_999, OriginalArtist: "Orig", MixName: "Club Mix", TrackNum: 7,
	})
	srv := &Server{Library: lib}
	h := srv.Handler()

	get := func(path, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// /api/tracks/rev returns a positive revision (AddTrack → Save bumped it).
	var rr struct {
		Rev uint64 `json:"rev"`
	}
	revW := get("/api/tracks/rev", "")
	if revW.Code != 200 {
		t.Fatalf("/api/tracks/rev status = %d", revW.Code)
	}
	if err := json.Unmarshal(revW.Body.Bytes(), &rr); err != nil {
		t.Fatalf("rev decode: %v", err)
	}
	if rr.Rev == 0 {
		t.Fatalf("rev should be > 0 after AddTrack")
	}

	// /api/tracks returns an ETag and a slim payload.
	listW := get("/api/tracks", "")
	if listW.Code != 200 {
		t.Fatalf("/api/tracks status = %d", listW.Code)
	}
	etag := listW.Header().Get("ETag")
	if etag == "" {
		t.Fatal("/api/tracks missing ETag")
	}
	var rows []map[string]any
	if err := json.Unmarshal(listW.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	for _, drop := range []string{"file_size", "original_artist", "mix_name", "track_num"} {
		if _, ok := rows[0][drop]; ok {
			t.Errorf("list payload should omit detail-only field %q", drop)
		}
	}
	for _, keep := range []string{"id", "title", "artist", "file_path"} {
		if _, ok := rows[0][keep]; !ok {
			t.Errorf("list payload should keep %q", keep)
		}
	}

	// Matching If-None-Match → 304 (no re-serialize).
	if w := get("/api/tracks", etag); w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match match: want 304, got %d", w.Code)
	}

	// An edit bumps the revision, so the old ETag no longer matches → 200.
	lib.Touch()
	if w := get("/api/tracks", etag); w.Code != 200 {
		t.Fatalf("after Touch, stale ETag should 200, got %d", w.Code)
	}
	var rr2 struct {
		Rev uint64 `json:"rev"`
	}
	json.Unmarshal(get("/api/tracks/rev", "").Body.Bytes(), &rr2)
	if rr2.Rev <= rr.Rev {
		t.Fatalf("rev should increase after Touch: %d -> %d", rr.Rev, rr2.Rev)
	}
}

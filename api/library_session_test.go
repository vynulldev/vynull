// SPDX-License-Identifier: GPL-3.0-or-later
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func TestLibrarySessionScopesRevisionAndRequests(t *testing.T) {
	first, second := library.New(), library.New()
	first.AddTrack(&library.Track{ID: 1, Title: "First"})
	second.AddTrack(&library.Track{ID: 1, Title: "Second"})
	get := func(lib *library.Library, path, etag, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("If-None-Match", etag)
		r.Header.Set("X-Vynull-Library-Session", session)
		w := httptest.NewRecorder()
		(&Server{Library: lib}).Handler().ServeHTTP(w, r)
		return w
	}
	var revision struct {
		Rev     uint64 `json:"rev"`
		Session string `json:"library_session"`
	}
	w := get(first, "/api/tracks/rev", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	if revision.Session == "" || revision.Session != w.Header().Get("X-Vynull-Library-Session") {
		t.Fatal("missing session")
	}
	original := get(first, "/api/tracks", "", revision.Session)
	etag := original.Header().Get("ETag")
	if get(first, "/api/tracks", etag, revision.Session).Code != 304 {
		t.Fatal("same session should revalidate")
	}
	if first.Rev() != second.Rev() {
		t.Fatal("fixture revisions must collide")
	}
	if get(second, "/api/tracks", etag, "").Code != 200 {
		t.Fatal("ETag crossed libraries")
	}
	for _, path := range []string{"/api/tracks/1", "/api/audio/1", "/api/playlists"} {
		if get(second, path, "", revision.Session).Code != 409 {
			t.Fatalf("stale session allowed on %s", path)
		}
	}
	first.Touch()
	if first.SessionID() != revision.Session {
		t.Fatal("edit changed identity")
	}
	if get(first, "/api/tracks", etag, revision.Session).Code != 200 {
		t.Fatal("edit failed to invalidate ETag")
	}
	if get(nil, "/api/tracks", "", revision.Session).Code != 409 {
		t.Fatal("missing library accepted stale session")
	}
}

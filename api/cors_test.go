// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Covers the cross-origin contract a browser frontend relies on: the daemon
// must allow the If-None-Match conditional-request header and expose ETag and
// X-Audio-Source so a client on another origin can revalidate its track list
// and read the DIRECT/TRANSCODED audio label. Regression guard for #66.
func TestCORSHeaders(t *testing.T) {
	srv := &Server{Library: library.New()}
	h := srv.Handler()

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	contains := func(header, want string) bool {
		for _, v := range strings.Split(header, ",") {
			if strings.EqualFold(strings.TrimSpace(v), want) {
				return true
			}
		}
		return false
	}

	// A preflight and a normal response carry the same CORS posture.
	for _, tc := range []struct {
		name   string
		method string
		code   int
	}{
		{"preflight", http.MethodOptions, http.StatusNoContent},
		{"normal", http.MethodGet, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := do(tc.method, "/api/tracks")
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("Allow-Origin = %q, want *", got)
			}
			allow := w.Header().Get("Access-Control-Allow-Headers")
			for _, want := range []string{"Content-Type", "If-None-Match"} {
				if !contains(allow, want) {
					t.Errorf("Allow-Headers %q missing %q", allow, want)
				}
			}
			expose := w.Header().Get("Access-Control-Expose-Headers")
			for _, want := range []string{"ETag", "X-Audio-Source"} {
				if !contains(expose, want) {
					t.Errorf("Expose-Headers %q missing %q", expose, want)
				}
			}
		})
	}
}

// TestCORSHeadersEveryRoute checks no handler sets CORS headers of its own:
// every route's preflight and normal responses carry exactly the
// middleware's. Handlers used to narrow Allow-Methods and Allow-Headers on
// their own responses, dropping If-None-Match.
func TestCORSHeadersEveryRoute(t *testing.T) {
	h := (&Server{Library: library.New()}).Handler()
	want := map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Allow-Methods":  "GET, POST, PUT, DELETE, HEAD, OPTIONS",
		"Access-Control-Allow-Headers":  "Content-Type, If-None-Match",
		"Access-Control-Expose-Headers": "ETag, X-Audio-Source",
	}
	// serve answers one request with its context already ended, so no
	// handler starts real work. A handler missing a store this bare server
	// lacks may panic; the CORS headers are set before it runs.
	serve := func(method, path string) (w *httptest.ResponseRecorder) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w = httptest.NewRecorder()
		defer func() { recover() }()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil).WithContext(ctx))
		return w
	}
	for _, path := range []string{
		"/api/status", "/api/version", "/api/peers", "/api/players", "/api/nowplaying",
		"/api/events", "/api/overlay/config", "/api/history", "/api/load", "/api/link", "/api/unlink",
		"/api/tracks", "/api/tracks/rev", "/api/tracks/color", "/api/tracks/rating", "/api/tracks/tags",
		"/api/tracks/add", "/api/tracks/add/status", "/api/tracks/reimport", "/api/fs/list",
		"/api/tracks/1/metadata", "/api/tracks/1/path", "/api/tracks/1/color", "/api/tracks/1/rating",
		"/api/tracks/1/tags", "/api/tracks/1/phrases", "/api/tracks/1/beats", "/api/tracks/1/lock",
		"/api/tracks/1/related",
		"/api/import/rekordbox", "/api/import/status", "/api/library/remap-paths",
		"/api/export", "/api/export/preview", "/api/diag", "/api/diag/logs", "/api/diag/status",
		"/api/audio/1", "/api/analysis/1", "/api/analysis/ext/1/2/3", "/api/analysis/reanalyze/1",
		"/api/analysis/beatgrid/adjust", "/api/analysis/waveform/1", "/api/analysis/waveform-png/1",
		"/api/artwork/1", "/api/artwork/ext/1/2/3", "/api/cues", "/api/cues/1",
		"/api/settings", "/api/settings/enums", "/api/menu-items", "/api/menu-items/reset",
		"/api/tags", "/api/tags/1", "/api/tag-categories", "/api/tag-categories/1",
		"/api/playlists", "/api/playlists/1", "/api/hotcuebanks", "/api/hotcuebanks/1",
	} {
		for _, method := range []string{http.MethodOptions, http.MethodGet} {
			w := serve(method, path)
			for name, value := range want {
				if got := w.Header().Get(name); got != value {
					t.Errorf("%s %s: %s = %q, want %q", method, path, name, got, value)
				}
			}
		}
	}
}

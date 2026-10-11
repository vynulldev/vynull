// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
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

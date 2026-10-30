// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// TestJSONError checks an error answer: its status, a JSON body naming the
// error, and no Content-Length left from content it replaced.
func TestJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Length", "12345")
	jsonError(w, "track not found", http.StatusNotFound)
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	if cl := w.Header().Get("Content-Length"); cl != "" {
		t.Errorf("stale Content-Length %q kept", cl)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"track not found"}` {
		t.Errorf("body %s", got)
	}
}

// TestAllow checks a method the endpoint takes passes untouched, and any
// other gets 405 with an Allow header and a JSON error.
func TestAllow(t *testing.T) {
	w := httptest.NewRecorder()
	if !allow(w, httptest.NewRequest(http.MethodPut, "/", nil), http.MethodGet, http.MethodPut) {
		t.Fatal("PUT refused by an endpoint taking it")
	}
	if w.Body.Len() != 0 || len(w.Header()) != 0 {
		t.Errorf("an allowed method wrote %q, %v", w.Body.String(), w.Header())
	}

	w = httptest.NewRecorder()
	if allow(w, httptest.NewRequest(http.MethodDelete, "/", nil), http.MethodGet, http.MethodPut) {
		t.Fatal("DELETE allowed by an endpoint taking GET and PUT")
	}
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, PUT" {
		t.Errorf("status %d, Allow %q; want 405, \"GET, PUT\"", w.Code, w.Header().Get("Allow"))
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"method not allowed"}` {
		t.Errorf("body %s", got)
	}
}

// TestErrorsAreJSON sends every method to every route of a server with no
// device or stores: whatever fails must answer {"error": ...} as JSON, never
// text. Unknown /api/ paths too, while the docs pages stay HTML.
func TestErrorsAreJSON(t *testing.T) {
	h := (&Server{Library: library.New()}).Handler()
	serve := func(method, path string) (w *httptest.ResponseRecorder, panicked bool) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // no handler starts real work
		w = httptest.NewRecorder()
		defer func() { panicked = recover() != nil }()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader("{")).WithContext(ctx))
		return w, false
	}
	errors := 0
	for _, path := range append(everyRoute, "/api/no-such-endpoint", "/api/tracks/1/no-such-part") {
		if path == "/api/events" {
			continue // a stream, not a request and answer
		}
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			w, panicked := serve(method, path)
			if panicked || w.Code < 400 {
				continue // a handler needing what this bare server lacks
			}
			errors++
			var body struct{ Error string }
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s %s: %d with Content-Type %q, body %q", method, path, w.Code, ct, w.Body.String())
			} else if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Errorf("%s %s: %d with body %q", method, path, w.Code, w.Body.String())
			}
			if w.Code == http.StatusMethodNotAllowed && w.Header().Get("Allow") == "" {
				t.Errorf("%s %s: 405 without an Allow header", method, path)
			}
		}
	}
	if errors < 100 {
		t.Errorf("only %d error answers checked; the sweep reaches too little", errors)
	}

	w, _ := serve(http.MethodGet, "/api/no-such-endpoint")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown endpoint: status %d, want 404", w.Code)
	}
	if w, _ := serve(http.MethodGet, "/api/docs"); w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("/api/docs: %d %q, want the HTML page", w.Code, w.Header().Get("Content-Type"))
	}
}

// TestWritesAcknowledgeOK checks a write with nothing to return answers
// exactly {"ok":true}: here, creating a tag category and renaming it.
func TestWritesAcknowledgeOK(t *testing.T) {
	srv := &Server{Library: library.New(), Tags: NewTagStore(t.TempDir())}
	h := srv.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	w := do(http.MethodPost, "/api/tag-categories", `{"name":"Mood"}`)
	var created struct{ ID int }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.ID == 0 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = do(http.MethodPut, "/api/tag-categories/"+strconv.Itoa(created.ID), `{"name":"Energy"}`)
	if got := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK || got != `{"ok":true}` {
		t.Errorf("rename: %d %s, want 200 {\"ok\":true}", w.Code, got)
	}
}

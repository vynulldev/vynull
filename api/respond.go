// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// How the API answers:
//   - A success is a 2xx. A read returns its data; a write returns the data
//     it has to give back, or {"ok": true} (ok) when it has none.
//   - An error keeps its status code and answers {"error": "message"}
//     (jsonError), unknown /api/ paths included.
//   - A request with a method the endpoint doesn't take gets 405 with an
//     Allow header (allow).

// okBody is the answer of a write that has nothing else to return.
type okBody struct {
	OK bool `json:"ok"`
}

// errorBody is the answer of a request that failed.
type errorBody struct {
	Error string `json:"error"`
}

// ok answers a write that has nothing else to return.
func ok(w http.ResponseWriter) {
	writeJSON(w, okBody{OK: true})
}

// jsonError answers with code and {"error": msg}. Like http.Error, it drops a
// Content-Length set for other content and keeps Content-Encoding.
func jsonError(w http.ResponseWriter, msg string, code int) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errorBody{Error: msg})
}

// allow reports whether r's method is one of methods, answering it with
// notAllowed if not.
func allow(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	if slices.Contains(methods, r.Method) {
		return true
	}
	notAllowed(w, methods...)
	return false
}

// notAllowed answers 405 with an Allow header listing the methods the
// endpoint takes.
func notAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleUnknownAPI answers a path under /api/ that no endpoint serves.
func handleUnknownAPI(w http.ResponseWriter, r *http.Request) {
	jsonError(w, "no such endpoint", http.StatusNotFound)
}

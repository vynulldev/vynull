// SPDX-License-Identifier: GPL-3.0-or-later
package api

import "net/http"

// Optional precondition for clients retaining track IDs across HTTP requests.
// Server.Library is startup wiring; replacing it while serving is unsupported.
func (s *Server) librarySessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := ""
		if s.Library != nil {
			session = s.Library.SessionID()
		}
		if session != "" {
			w.Header().Set("X-Vynull-Library-Session", session)
		}
		if expected := r.Header.Get("X-Vynull-Library-Session"); expected != "" && expected != session {
			jsonError(w, "library session changed; reload the library", http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r)
	})
}

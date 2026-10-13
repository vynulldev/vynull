// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/internal/buildinfo"
	"github.com/vynulldev/vynull/library"
)

func TestVersionEndpoint(t *testing.T) {
	srv := &Server{Library: library.New()}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got buildinfo.Info
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version == "" || got != buildinfo.Get() {
		t.Errorf("got %+v, want %+v", got, buildinfo.Get())
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestWebAssets checks every stylesheet and script the page links is served,
// with its type and an ETag a browser revalidates against (304), and that a
// name the page doesn't use is not found.
func TestWebAssets(t *testing.T) {
	h := (&Server{Web: true}).Handler()
	get := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := get("/", "").Body.String()
	links := regexp.MustCompile(`(?:href|src)="(/(?:css|js)/[a-z-]+\.(?:css|js))"`).FindAllStringSubmatch(page, -1)
	if len(links) < 20 {
		t.Fatalf("the page links %d stylesheets and scripts; want the 26 it was split into", len(links))
	}
	for _, l := range links {
		path := l[1]
		w := get(path, "")
		want := "text/css; charset=utf-8"
		if strings.HasSuffix(path, ".js") {
			want = "text/javascript; charset=utf-8"
		}
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != want || w.Body.Len() == 0 {
			t.Errorf("%s: %d %q, %d bytes", path, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
			continue
		}
		etag := w.Header().Get("ETag")
		if etag == "" || get(path, etag).Code != http.StatusNotModified {
			t.Errorf("%s: ETag %q not answered 304", path, etag)
		}
	}
	if w := get("/js/no-such-file.js", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown script: %d, want 404", w.Code)
	}
}

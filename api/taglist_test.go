// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// The TAG LIST endpoints add at the end, remove one track or all of them,
// and always answer with the list as it is afterwards.
func TestTagListAPI(t *testing.T) {
	lib := library.New()
	for id := uint32(1); id <= 3; id++ {
		lib.AddTrack(&library.Track{ID: id, FilePath: "/music/" + string(rune('a'+id)) + ".mp3"})
	}
	h := (&Server{Library: lib}).Handler()
	call := func(method, path, body string, wantCode int, want ...uint32) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != wantCode {
			t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body, wantCode)
		}
		if wantCode != http.StatusOK {
			return
		}
		var got tagListReply
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.TrackIDs == nil || !slices.Equal(got.TrackIDs, append([]uint32{}, want...)) {
			t.Fatalf("%s %s: %s, want track_ids %v", method, path, w.Body, want)
		}
	}
	call("GET", "/api/taglist", "", 200)
	call("POST", "/api/taglist", `{"track_id":3}`, 200, 3)
	call("POST", "/api/taglist", `{"track_id":1}`, 200, 3, 1)
	call("POST", "/api/taglist", `{"track_id":3}`, 200, 3, 1)
	call("POST", "/api/taglist", `{"track_id":9}`, 404)
	call("POST", "/api/taglist", `{`, 400)
	call("DELETE", "/api/taglist/3", "", 200, 1)
	call("DELETE", "/api/taglist/x", "", 400)
	call("PUT", "/api/taglist", "", 405)
	call("DELETE", "/api/taglist", "", 200)
	call("GET", "/api/taglist", "", 200)
}

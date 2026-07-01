// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMenuStoreDefaults seeds an empty dir and confirms NewMenuStore
// fills it with the default set. Reload from the same dir should yield
// the same items (persistence works).
func TestMenuStoreDefaults(t *testing.T) {
	dir := t.TempDir()
	ms := NewMenuStore(dir)
	all := ms.All()
	if len(all) != len(defaultMenuItems) {
		t.Fatalf("want %d defaults got %d", len(defaultMenuItems), len(all))
	}
	if all[0].Key != "artist" {
		t.Fatalf("first item should be artist, got %q", all[0].Key)
	}
	// Default-active items should be visible; items rekordbox keeps in
	// the "inactive" list (bitrate, genre, matching, original_artist,
	// remixer) should default to hidden.
	wantInactive := map[string]bool{
		"bitrate": true, "genre": true, "matching": true,
		"original_artist": true, "remixer": true,
	}
	for _, m := range all {
		if wantInactive[m.Key] {
			if m.Visible {
				t.Fatalf("default %q should be hidden (inactive list)", m.Key)
			}
			continue
		}
		if !m.Visible {
			t.Fatalf("default %q should be visible", m.Key)
		}
	}

	// Reload
	ms2 := NewMenuStore(dir)
	if len(ms2.All()) != len(defaultMenuItems) {
		t.Fatalf("reload lost items: %d", len(ms2.All()))
	}
}

func TestMenuStoreReplace(t *testing.T) {
	dir := t.TempDir()
	ms := NewMenuStore(dir)

	// Hide BPM, reorder so PLAYLIST is first.
	err := ms.Replace([]MenuItem{
		{Key: "playlist", Visible: true},
		{Key: "artist", Visible: true},
		{Key: "bpm", Visible: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := ms.All()
	// Replace + mergeMissingLocked should keep all known keys but in the
	// supplied order at the top and append the rest with Visible=false.
	if all[0].Key != "playlist" || all[1].Key != "artist" {
		t.Fatalf("order wrong: %+v", all[:3])
	}
	vis := ms.Visible()
	visKeys := map[string]bool{}
	for _, m := range vis {
		visKeys[m.Key] = true
	}
	if !visKeys["playlist"] || !visKeys["artist"] {
		t.Fatalf("visible set missing playlist/artist: %+v", vis)
	}
	if visKeys["bpm"] {
		t.Fatal("bpm should be hidden")
	}
	if visKeys["genre"] {
		// Items not in Replace get re-added as hidden.
		t.Fatalf("not-mentioned item should default hidden, got visible")
	}

	// Empty input is rejected.
	if err := ms.Replace(nil); err == nil {
		t.Fatal("empty Replace should error")
	}

	// Reset restores defaults — visible-count matches the default set.
	ms.ResetToDefaults()
	wantVisible := 0
	for _, d := range defaultMenuItems {
		if d.Visible {
			wantVisible++
		}
	}
	if got := len(ms.Visible()); got != wantVisible {
		t.Fatalf("reset visible count: want %d got %d", wantVisible, got)
	}
}

// TestMenuAPI hits every endpoint end-to-end through httptest.
func TestMenuAPI(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{Menu: NewMenuStore(dir)}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	do := func(method, path, body string) (int, []byte) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, r)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// GET defaults
	code, body := do("GET", "/api/menu-items", "")
	if code != 200 {
		t.Fatalf("get: %d %s", code, body)
	}
	var got struct{ Items []MenuItem }
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(defaultMenuItems) {
		t.Fatalf("want %d got %d", len(defaultMenuItems), len(got.Items))
	}

	// PUT — hide rating + folder, reorder.
	put := `{"items":[
	  {"key":"track","visible":true},
	  {"key":"playlist","visible":true},
	  {"key":"artist","visible":true},
	  {"key":"rating","visible":false}
	]}`
	if c, b := do("PUT", "/api/menu-items", put); c != 200 {
		t.Fatalf("put: %d %s", c, b)
	}
	// First three visible should be track, playlist, artist; rating hidden.
	vis := srv.Menu.Visible()
	if len(vis) < 3 || vis[0].Key != "track" || vis[1].Key != "playlist" || vis[2].Key != "artist" {
		t.Fatalf("reorder wrong: %+v", vis)
	}
	for _, m := range vis {
		if m.Key == "rating" {
			t.Fatal("rating should be hidden")
		}
	}

	// Reset
	if c, _ := do("POST", "/api/menu-items/reset", ""); c != 200 {
		t.Fatalf("reset: %d", c)
	}
	wantVisibleAPI := 0
	for _, d := range defaultMenuItems {
		if d.Visible {
			wantVisibleAPI++
		}
	}
	if got := len(srv.Menu.Visible()); got != wantVisibleAPI {
		t.Fatalf("reset visible count: want %d got %d", wantVisibleAPI, got)
	}

	// Track-detail round-trip — default is "bpm", PUT changes it, reject unknown.
	if srv.Menu.TrackDetail() != "bpm" {
		t.Fatalf("default detail should be bpm, got %q", srv.Menu.TrackDetail())
	}
	if c, b := do("PUT", "/api/menu-items", `{"track_detail":"artist"}`); c != 200 {
		t.Fatalf("set detail: %d %s", c, b)
	}
	if srv.Menu.TrackDetail() != "artist" {
		t.Fatalf("detail not saved: %q", srv.Menu.TrackDetail())
	}
	if c, _ := do("PUT", "/api/menu-items", `{"track_detail":"bogus"}`); c != http.StatusBadRequest {
		t.Fatalf("unknown detail should 400, got %d", c)
	}
}

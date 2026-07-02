// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// TestPlaylistAPI exercises every playlist endpoint end-to-end through
// httptest so the wiring stays honest. No real ports, no other services
// — just the PlaylistStore + the mux from Server.Handler().
func TestPlaylistAPI(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{Playlists: NewPlaylistStore(dir)}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	do := func(method, path, body string) (int, []byte) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, r)
		if err != nil {
			t.Fatal(err)
		}
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

	mustOK := func(method, path, body string) []byte {
		t.Helper()
		code, b := do(method, path, body)
		if code != 200 {
			t.Fatalf("%s %s: want 200, got %d: %s", method, path, code, b)
		}
		return b
	}

	// create folder at root
	got := mustOK("POST", "/api/playlists", `{"name":"Trance","is_folder":true}`)
	var folder PlaylistInfo
	if err := json.Unmarshal(got, &folder); err != nil {
		t.Fatalf("decode folder: %v", err)
	}
	if folder.ID == 0 || !folder.IsFolder || folder.ParentID != 0 {
		t.Fatalf("unexpected folder: %+v", folder)
	}

	// create playlist inside folder
	got = mustOK("POST", "/api/playlists", `{"name":"Friday Warmup","parent_id":1}`)
	var pl PlaylistInfo
	if err := json.Unmarshal(got, &pl); err != nil {
		t.Fatalf("decode playlist: %v", err)
	}
	if pl.IsFolder || pl.ParentID != folder.ID {
		t.Fatalf("unexpected playlist: %+v", pl)
	}

	// list — should have 2 entries, folder first by SortOrder
	got = mustOK("GET", "/api/playlists", "")
	var all []PlaylistInfo
	if err := json.Unmarshal(got, &all); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 entries, got %d: %s", len(all), got)
	}

	// set tracks
	mustOK("POST", "/api/playlists/2/tracks", `{"track_ids":[10,20,30]}`)

	// playlist-tracks endpoint without a Library returns empty list — the
	// store has the IDs but libTrackToInfo can't enrich them. That's the
	// correct behaviour for a Library-less test instance; just verify the
	// store kept them.
	if got := srv.Playlists.Tracks(2); len(got) != 3 || got[0] != 10 || got[2] != 30 {
		t.Fatalf("store tracks not persisted: %v", got)
	}

	// rename via PUT
	mustOK("PUT", "/api/playlists/2", `{"name":"Friday Opener"}`)
	if p := srv.Playlists.Get(2); p == nil || p.Name != "Friday Opener" {
		t.Fatalf("rename didn't stick: %+v", p)
	}

	// cycle-rejecting move: trying to move folder into itself
	code, body := do("PUT", "/api/playlists/1", `{"parent_id":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("self-parent move should 400, got %d: %s", code, body)
	}

	// move playlist to root (parent_id=0)
	mustOK("PUT", "/api/playlists/2", `{"parent_id":0}`)
	if p := srv.Playlists.Get(2); p == nil || p.ParentID != 0 {
		t.Fatalf("move-to-root didn't stick: %+v", p)
	}

	// delete folder (now empty) — should succeed
	mustOK("DELETE", "/api/playlists/1", "")
	if p := srv.Playlists.Get(1); p != nil {
		t.Fatalf("folder not deleted: %+v", p)
	}

	// final list should have just the moved playlist
	got = mustOK("GET", "/api/playlists", "")
	if err := json.Unmarshal(got, &all); err != nil {
		t.Fatalf("decode final: %v", err)
	}
	if len(all) != 1 || all[0].ID != 2 {
		t.Fatalf("want only playlist 2, got: %s", got)
	}

	// 404 paths
	if code, _ := do("PUT", "/api/playlists/999", `{"name":"nope"}`); code == 200 {
		t.Fatal("rename of missing playlist should not be 200")
	}
	if code, _ := do("DELETE", "/api/playlists/999", ""); code == 200 {
		t.Fatal("delete of missing playlist should not be 200")
	}
}

// TestSmartPlaylistAPI exercises the smart-playlist surface end-to-end:
// create via is_smart=true + rules, GET /tracks evaluates against a
// fake library, PUT /rules updates the rules, SetTracks is rejected.
// Uses a real library.Library (not a mock) so we exercise the same
// path the dbserver adapter does.
func TestSmartPlaylistAPI(t *testing.T) {
	dir := t.TempDir()
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Slow", Artist: "A", BPM: 100, Genre: "House"})
	lib.AddTrack(&library.Track{ID: 2, Title: "Mid", Artist: "B", BPM: 124, Genre: "Trance"})
	lib.AddTrack(&library.Track{ID: 3, Title: "Fast", Artist: "C", BPM: 138, Genre: "Trance"})
	lib.AddTrack(&library.Track{ID: 4, Title: "Faster", Artist: "D", BPM: 150, Genre: "Hardstyle"})

	srv := &Server{
		Library:   lib,
		Playlists: NewPlaylistStore(dir),
	}
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

	// Create smart playlist: Trance, 130-140 BPM.
	body := `{
	  "name": "Peak-time Trance",
	  "is_smart": true,
	  "rules": {
	    "combinator": "all",
	    "conditions": [
	      {"field": "genre", "operator": "is", "value": "Trance"},
	      {"field": "bpm", "operator": "between", "value": [130, 140]}
	    ]
	  }
	}`
	code, resp := do("POST", "/api/playlists", body)
	if code != 200 {
		t.Fatalf("create smart: %d %s", code, resp)
	}
	var p PlaylistInfo
	if err := json.Unmarshal(resp, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !p.IsSmart || p.Rules == nil {
		t.Fatalf("unexpected create response: %+v", p)
	}

	// GET /tracks should evaluate rules and return only track 3 (Fast,
	// 138 BPM Trance) — track 2 is Trance but 124 BPM, track 4 is
	// 150 BPM but Hardstyle.
	code, resp = do("GET", "/api/playlists/1/tracks", "")
	if code != 200 {
		t.Fatalf("get tracks: %d", code)
	}
	var tracks []TrackInfo
	if err := json.Unmarshal(resp, &tracks); err != nil {
		t.Fatalf("decode tracks: %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != 3 {
		t.Fatalf("want only track 3, got: %s", resp)
	}

	// SetTracks via POST /tracks must fail on smart playlists.
	code, resp = do("POST", "/api/playlists/1/tracks", `{"track_ids":[1,2,3]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("SetTracks on smart should 400, got %d: %s", code, resp)
	}

	// PUT /rules updates the rules — drop the BPM constraint so all
	// three Trance tracks pass.
	newRules := `{
	  "combinator": "all",
	  "conditions": [{"field":"genre","operator":"is","value":"Trance"}]
	}`
	code, _ = do("PUT", "/api/playlists/1/rules", newRules)
	if code != 200 {
		t.Fatalf("put rules: %d", code)
	}
	code, resp = do("GET", "/api/playlists/1/tracks", "")
	if code != 200 {
		t.Fatalf("re-get tracks: %d", code)
	}
	if err := json.Unmarshal(resp, &tracks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 Trance tracks after rule update, got %d: %s", len(tracks), resp)
	}

	// is_smart + is_folder together → 400.
	code, _ = do("POST", "/api/playlists", `{"name":"bad","is_smart":true,"is_folder":true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("smart+folder should 400, got %d", code)
	}
}

// TestPlaylistRecursiveDelete verifies that deleting a folder removes
// every descendant — playlists and nested folders alike.
func TestPlaylistRecursiveDelete(t *testing.T) {
	dir := t.TempDir()
	ps := NewPlaylistStore(dir)

	root, _ := ps.Create("Sets", 0, true)
	sub, _ := ps.Create("April", root.ID, true)
	a, _ := ps.Create("Set A", sub.ID, false)
	b, _ := ps.Create("Set B", root.ID, false)
	_ = ps.SetTracks(a.ID, []uint32{1, 2, 3})
	_ = ps.SetTracks(b.ID, []uint32{4, 5})

	if err := ps.Delete(root.ID); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	if len(ps.All()) != 0 {
		t.Fatalf("want everything removed, got: %+v", ps.All())
	}

	// Reopen and confirm persistence reflects the deletion.
	ps2 := NewPlaylistStore(dir)
	if len(ps2.All()) != 0 {
		t.Fatalf("persistence kept stale entries: %+v", ps2.All())
	}
}

// TestPlaylistStoreReload writes a small tree, opens a fresh store
// against the same directory, and checks every field round-trips.
func TestPlaylistStoreReload(t *testing.T) {
	dir := t.TempDir()
	ps := NewPlaylistStore(dir)

	folder, _ := ps.Create("F", 0, true)
	pl, _ := ps.Create("P", folder.ID, false)
	_ = ps.SetTracks(pl.ID, []uint32{7, 8, 9})

	ps2 := NewPlaylistStore(dir)
	got := ps2.Get(pl.ID)
	if got == nil {
		t.Fatal("playlist disappeared after reload")
	}
	if got.Name != "P" || got.ParentID != folder.ID || len(got.TrackIDs) != 3 || got.TrackIDs[2] != 9 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// nextID survives — next Create should produce ID 3, not 1.
	next, _ := ps2.Create("Q", 0, false)
	if next.ID != 3 {
		t.Fatalf("want next ID 3, got %d", next.ID)
	}
}

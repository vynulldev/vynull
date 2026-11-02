// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/library"
)

func batchServer(t *testing.T) *Server {
	t.Helper()
	lib := library.New()
	for id := uint32(1); id <= 3; id++ {
		lib.AddTrack(&library.Track{ID: id})
	}
	tags := NewTagStore(t.TempDir())
	t.Cleanup(func() { tags.Close() })
	return &Server{Library: lib, Tags: tags}
}

// batch sends a batch edit and returns the status and the "updated" count.
func batch(t *testing.T, s *Server, method, path, body string) (int, int) {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var res batchResult
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("%s %s: %s: %v", method, path, w.Body, err)
		}
	}
	return w.Code, res.Updated
}

func TestBatchColorAndRating(t *testing.T) {
	s := batchServer(t)
	if code, n := batch(t, s, "PUT", "/api/tracks/color", `{"track_ids":[1,2,2,99],"color_id":3}`); code != 200 || n != 2 {
		t.Fatalf("colour: %d, updated %d; want 200, 2 (a repeat and an unknown track skipped)", code, n)
	}
	if c1, c2, c3 := s.Library.Track(1).ColorID, s.Library.Track(2).ColorID, s.Library.Track(3).ColorID; c1 != 3 || c2 != 3 || c3 != 0 {
		t.Errorf("colours %d %d %d, want 3 3 0", c1, c2, c3)
	}
	if code, n := batch(t, s, "PUT", "/api/tracks/rating", `{"track_ids":[2,3],"rating":7}`); code != 200 || n != 2 {
		t.Fatalf("rating: %d, updated %d; want 200, 2", code, n)
	}
	if r := s.Library.Track(3).Rating; r != 5 {
		t.Errorf("rating %d, want 7 clamped to 5", r)
	}

	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/tracks/color", `{"track_ids":[1],"color_id":9}`},
		{"PUT", "/api/tracks/color", `{"track_ids":[],"color_id":1}`},
		{"PUT", "/api/tracks/rating", `{"rating":1}`},
		{"PUT", "/api/tracks/rating", `not json`},
	} {
		if code, _ := batch(t, s, c.method, c.path, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s: %d, want 400", c.path, c.body, code)
		}
	}
	if code, _ := batch(t, s, "GET", "/api/tracks/color", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", code)
	}
}

func TestBatchTags(t *testing.T) {
	s := batchServer(t)
	a, _ := s.Tags.CreateTag("A", 0)
	b, _ := s.Tags.CreateTag("B", 0)
	s.Tags.SetTagsForTrack(1, []uint32{a})
	tagsOf := func(id uint32) []uint32 { return s.Tags.(*TagStore).TrackTagIDs(id) }

	body := func(ids string, op string, tag uint32) string {
		return fmt.Sprintf(`{"track_ids":%s,%q:[%d]}`, ids, op, tag)
	}
	if code, n := batch(t, s, "POST", "/api/tracks/tags", body("[1,2,99]", "add", b)); code != 200 || n != 2 {
		t.Fatalf("add: %d, updated %d; want 200, 2", code, n)
	}
	if !slices.Equal(tagsOf(1), []uint32{a, b}) || !slices.Equal(tagsOf(2), []uint32{b}) || tagsOf(99) != nil {
		t.Errorf("after add: %v %v %v, want [A B] [B] and nothing for the unknown track", tagsOf(1), tagsOf(2), tagsOf(99))
	}
	if code, n := batch(t, s, "POST", "/api/tracks/tags", body("[1,2,3]", "remove", a)); code != 200 || n != 1 {
		t.Fatalf("remove: %d, updated %d; want 200, 1 (only track 1 had it)", code, n)
	}
	if code, _ := batch(t, s, "POST", "/api/tracks/tags", body("[3]", "add", 9)); code != http.StatusBadRequest || tagsOf(3) != nil {
		t.Errorf("unknown tag: %d, track 3 %v; want 400 and no change", code, tagsOf(3))
	}
	if code, _ := batch(t, s, "POST", "/api/tracks/tags", `{"track_ids":[1]}`); code != http.StatusBadRequest {
		t.Errorf("nothing to add or remove: %d, want 400", code)
	}
}

// playlistServer is a library of tracks 1-4 with a playlist store holding
// regular playlists A and B, a folder and a smart playlist.
func playlistServer(t *testing.T) (s *Server, a, b, folder, smart uint32) {
	t.Helper()
	s = batchServer(t)
	s.Library.AddTrack(&library.Track{ID: 4})
	s.Playlists = NewPlaylistStore(t.TempDir())
	t.Cleanup(func() { s.Playlists.Close() })
	create := func(name string, isFolder bool) uint32 {
		p, err := s.Playlists.Create(name, 0, isFolder)
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	a, b, folder = create("A", false), create("B", false), create("F", true)
	p, err := s.Playlists.CreateSmart("S", 0, &SmartRules{})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b, folder, p.ID
}

func TestBatchPlaylists(t *testing.T) {
	s, a, b, folder, smart := playlistServer(t)
	s.Playlists.SetTracks(a, []uint32{1, 2})
	s.Playlists.SetTracks(b, []uint32{3})
	edit := func(ids string, add, remove []uint32) (int, int) {
		body, _ := json.Marshal(map[string]any{"add": add, "remove": remove})
		return batch(t, s, "POST", "/api/tracks/playlists", fmt.Sprintf(`{"track_ids":%s,%s`, ids, body[1:]))
	}
	tracks := func(id uint32) []uint32 { return s.Playlists.Tracks(id) }

	// Appended in the order given, once each, skipping what's already there
	// and tracks the library doesn't hold.
	if code, n := edit("[4,3,1,4,99]", []uint32{a}, nil); code != 200 || n != 2 {
		t.Fatalf("add: %d, updated %d; want 200, 2", code, n)
	}
	if got := tracks(a); !slices.Equal(got, []uint32{1, 2, 4, 3}) {
		t.Errorf("A after add: %v, want [1 2 4 3]", got)
	}
	if code, n := edit("[1]", []uint32{a, b}, nil); code != 200 || n != 1 || !slices.Equal(tracks(b), []uint32{3, 1}) {
		t.Errorf("add to two: %d, updated %d, B %v; want 200, 1, [3 1]", code, n, tracks(b))
	}
	// Every entry goes, and a playlist in both lists gets them at its end.
	s.Playlists.SetTracks(a, []uint32{1, 2, 1, 3})
	if code, n := edit("[1,4]", nil, []uint32{a}); code != 200 || n != 1 || !slices.Equal(tracks(a), []uint32{2, 3}) {
		t.Errorf("remove: %d, updated %d, A %v; want 200, 1, [2 3]", code, n, tracks(a))
	}
	if code, n := edit("[2]", []uint32{a}, []uint32{a}); code != 200 || n != 1 || !slices.Equal(tracks(a), []uint32{3, 2}) {
		t.Errorf("remove and add: %d, updated %d, A %v; want 200, 1, [3 2]", code, n, tracks(a))
	}

	// A playlist that can't hold tracks fails the whole request.
	for _, c := range []struct {
		name     string
		add, rem []uint32
		want     int
	}{
		{"folder", []uint32{b, folder}, nil, http.StatusBadRequest},
		{"smart", nil, []uint32{smart}, http.StatusBadRequest},
		{"missing", []uint32{b, 77}, nil, http.StatusBadRequest},
		{"usb", []uint32{b, usbPlaylistIDBit | 1}, nil, http.StatusForbidden},
		{"nothing", nil, nil, http.StatusBadRequest},
	} {
		if code, _ := edit("[4]", c.add, c.rem); code != c.want || !slices.Equal(tracks(b), []uint32{3, 1}) {
			t.Errorf("%s: %d, B %v; want %d and B unchanged", c.name, code, tracks(b), c.want)
		}
	}
	if code, _ := batch(t, s, "POST", "/api/tracks/playlists", fmt.Sprintf(`{"add":[%d]}`, a)); code != http.StatusBadRequest {
		t.Errorf("no tracks: %d, want 400", code)
	}
}

// A batch delete takes the tracks out of the library, their playlists, the
// tag store and the cue store, as the single DELETE does, and skips a repeat
// or a track the library doesn't hold.
func TestBatchDelete(t *testing.T) {
	s, a, b, _, _ := playlistServer(t)
	cues := &CueStoreAdapter{Store: dbserver.NewCueStore(t.TempDir())}
	t.Cleanup(func() { cues.Store.Close() })
	s.Cues = cues
	s.Playlists.SetTracks(a, []uint32{1, 2, 3, 1})
	s.Playlists.SetTracks(b, []uint32{4})
	tag, _ := s.Tags.CreateTag("T", 0)
	for id := uint32(1); id <= 4; id++ {
		s.Tags.SetTagsForTrack(id, []uint32{tag})
		cues.SaveCue(id, CueInfo{Number: 1, TimeMs: 1000})
	}

	if code, n := batch(t, s, "POST", "/api/tracks/delete", `{"track_ids":[3,1,99,1]}`); code != 200 || n != 2 {
		t.Fatalf("delete: %d, updated %d; want 200, 2", code, n)
	}
	for id := uint32(1); id <= 4; id++ {
		kept := id == 2 || id == 4
		if (s.Library.Track(id) != nil) != kept || (len(s.Tags.GetTagsForTrack(id)) > 0) != kept || (len(cues.GetCues(id)) > 0) != kept {
			t.Errorf("track %d: in library %v, tags %v, cues %v; want all %v",
				id, s.Library.Track(id) != nil, s.Tags.GetTagsForTrack(id), cues.GetCues(id), kept)
		}
	}
	if pa, pb := s.Playlists.Tracks(a), s.Playlists.Tracks(b); !slices.Equal(pa, []uint32{2}) || !slices.Equal(pb, []uint32{4}) {
		t.Errorf("playlists A %v, B %v; want [2], [4]", pa, pb)
	}

	if code, n := batch(t, s, "POST", "/api/tracks/delete", `{"track_ids":[1]}`); code != 200 || n != 0 {
		t.Errorf("deleting a deleted track: %d, updated %d; want 200, 0", code, n)
	}
	if code, _ := batch(t, s, "POST", "/api/tracks/delete", `{"track_ids":[]}`); code != http.StatusBadRequest {
		t.Errorf("no tracks: %d, want 400", code)
	}
	if code, _ := batch(t, s, "DELETE", "/api/tracks/delete", `{"track_ids":[2]}`); code != http.StatusMethodNotAllowed || s.Library.Track(2) == nil {
		t.Errorf("DELETE: %d; want 405 and track 2 kept", code)
	}
	// The single DELETE goes the same way.
	if code, _ := batch(t, s, "DELETE", "/api/tracks/2", ""); code != 200 || s.Library.Track(2) != nil || s.Playlists.Tracks(a) != nil {
		t.Errorf("single DELETE: %d, track %v, A %v; want 200 and both gone", code, s.Library.Track(2), s.Playlists.Tracks(a))
	}
	if code, _ := batch(t, s, "DELETE", "/api/tracks/2", ""); code != http.StatusNotFound {
		t.Errorf("single DELETE again: %d, want 404", code)
	}
}

// An import stores each track's master.db cues as one change, numbering
// memory cues 9.. per track in rekordbox's order.
func TestImportMasterCuesPerTrack(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1})
	lib.AddTrack(&library.Track{ID: 2})
	cues := &CueStoreAdapter{Store: dbserver.NewCueStore(t.TempDir())}
	t.Cleanup(func() { cues.Store.Close() })
	s := &Server{Library: lib, Cues: cues}
	bundle := &library.ImportBundle{Cues: []library.ImportedCue{
		{TrackID: 1, HotCue: 1, TimeMs: 100, LoopMs: -1},
		{TrackID: 2, HotCue: 2, TimeMs: 200, LoopMs: -1},
		{TrackID: 1, HotCue: 0, TimeMs: 300, LoopMs: -1},
		{TrackID: 1, HotCue: 0, TimeMs: 400, LoopMs: 800},
	}}
	rec := httptest.NewRecorder()
	s.applyImportBundle(rec, bundle, importInclude{Cues: true}, "", "")
	var res library.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("result %s: %v", rec.Body, err)
	}
	if res.CuesImported != 4 {
		t.Errorf("CuesImported %d, want 4", res.CuesImported)
	}
	got := map[uint16]CueInfo{}
	for _, c := range cues.GetCues(1) {
		got[c.Number] = c
	}
	if len(got) != 3 || got[1].TimeMs != 100 || got[9].TimeMs != 300 || got[10].Type != "loop" || got[10].LoopMs != 800 {
		t.Errorf("track 1 cues %+v, want hot cue 1, memory 9 and loop 10", got)
	}
	if c := cues.GetCues(2); len(c) != 1 || c[0].Number != 2 {
		t.Errorf("track 2 cues %+v, want hot cue 2", c)
	}
}

func TestColorLabel(t *testing.T) {
	for id, want := range map[uint8]string{0: "none (0)", 4: "Yellow (4)", 7: "Blue (7)", 9: "none (9)"} {
		if got := colorLabel(id); got != want {
			t.Errorf("colorLabel(%d) = %q, want %q", id, got, want)
		}
	}
}

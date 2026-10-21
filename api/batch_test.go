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

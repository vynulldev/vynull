// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/library"
)

func get(h http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func rev(t *testing.T, h http.Handler, path string) uint64 {
	t.Helper()
	var body struct{ Rev uint64 }
	if err := json.Unmarshal(get(h, path).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Rev
}

// TestTrackListFollowsTags checks the track list's revision and ETag move
// when only a tag changes: every row carries its tags' names, and a client
// revalidating would otherwise keep a renamed tag's old name.
func TestTrackListFollowsTags(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Night Signal", FilePath: "/m/a.mp3"})
	tags := NewTagStore(t.TempDir())
	id, err := tags.CreateTag("Warmup", 0)
	if err != nil {
		t.Fatal(err)
	}
	tags.SetTagsForTrack(1, []uint32{id})
	h := (&Server{Library: lib, Tags: tags}).Handler()

	first := get(h, "/api/tracks")
	etag, before := first.Header().Get("ETag"), rev(t, h, "/api/tracks/rev")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/tags/"+strconv.Itoa(int(id)), strings.NewReader(`{"name":"Peak"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}

	if after := rev(t, h, "/api/tracks/rev"); after == before {
		t.Errorf("track list revision stayed %d after a tag rename", after)
	}
	again := get(h, "/api/tracks", "If-None-Match", etag)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"Peak"`) {
		t.Errorf("revalidating after the rename: %d %s; want 200 with the new name", again.Code, again.Body.String())
	}
}

// TestTrackListCached checks GET /api/tracks serves one body per revision,
// the same JSON gzipped or not, and a new one once the library changes.
func TestTrackListCached(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Night Signal", FilePath: "/m/a.mp3"})
	h := (&Server{Library: lib}).Handler()

	plain := get(h, "/api/tracks").Body.Bytes()
	zipped := get(h, "/api/tracks", "Accept-Encoding", "gzip")
	if zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("no gzip: %v", zipped.Header())
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(zr)
	if !bytes.Equal(unzipped, plain) {
		t.Errorf("gzipped body %q, plain %q", unzipped, plain)
	}
	if again := get(h, "/api/tracks").Body.Bytes(); !bytes.Equal(again, plain) {
		t.Errorf("second body %q, first %q", again, plain)
	}

	lib.Update(1, func(t *library.Track) error { t.Title = "Night Signal (Edit)"; return nil })
	if body := get(h, "/api/tracks").Body.String(); !strings.Contains(body, "Night Signal (Edit)") {
		t.Errorf("after an edit: %s", body)
	}
}

// TestCuesRevision checks the cue revision moves when a cue is saved, and
// GET /api/cues answers 304 to its current ETag and the new cues to an old
// one.
func TestCuesRevision(t *testing.T) {
	s := &Server{Cues: &CueStoreAdapter{Store: dbserver.NewCueStore(t.TempDir())}}
	h := s.Handler()
	before := rev(t, h, "/api/cues/rev")
	etag := get(h, "/api/cues").Header().Get("ETag")
	if got := get(h, "/api/cues", "If-None-Match", etag); got.Code != http.StatusNotModified {
		t.Errorf("unchanged cues: %d, want 304", got.Code)
	}

	s.Cues.SaveCue(7, CueInfo{Number: 1, Type: "cue", TimeMs: 1500, LoopMs: -1})
	if after := rev(t, h, "/api/cues/rev"); after == before {
		t.Errorf("cue revision stayed %d after a save", after)
	}
	got := get(h, "/api/cues", "If-None-Match", etag)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"7"`) {
		t.Errorf("after a save, the old ETag got %d %s; want 200 with track 7's cue", got.Code, got.Body.String())
	}
}

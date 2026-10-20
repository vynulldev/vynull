// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func putMetadata(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleTrackMetadata(rec, httptest.NewRequest(http.MethodPut, "/api/tracks/1/metadata", strings.NewReader(body)))
	return rec
}

// A metadata edit with one invalid field is refused as a whole: the fields
// before it in the request don't change either.
func TestMetadataEditAllOrNothing(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Old", Artist: "A", Year: 1999})
	s := &Server{Library: lib}
	if rec := putMetadata(s, `{"title":"New","artist":"B","year":"soon"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := lib.Track(1); got.Title != "Old" || got.Artist != "A" || got.Year != 1999 {
		t.Errorf("after a refused edit: %q by %q (%d), want the track unchanged", got.Title, got.Artist, got.Year)
	}
	if rec := putMetadata(s, `{"title":"New","key":"H#"}`); rec.Code != http.StatusBadRequest || lib.Track(1).Title != "Old" {
		t.Errorf("bad key: status %d, title %q; want 400 and the title unchanged", rec.Code, lib.Track(1).Title)
	}
}

// Editing a track's artist files it under the new artist, so the deck's
// ARTIST browse finds it there, and the response shows the edited track.
func TestMetadataEditReindexes(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "Song", Artist: "A"})
	s := &Server{Library: lib}
	rec := putMetadata(s, `{"artist":" B ","year":"2024"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(lib.TracksByArtist("A")) != 0 || len(lib.TracksByArtist("B")) != 1 || !slices.Equal(lib.Artists(), []string{"B"}) {
		t.Errorf("artist A %d, B %d, artists %v; want 0, 1, [B]", len(lib.TracksByArtist("A")), len(lib.TracksByArtist("B")), lib.Artists())
	}
	var got library.Track
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Artist != "B" || got.Year != 2024 {
		t.Errorf("response %s (%v), want the edited track", rec.Body, err)
	}
}

// After a relink, looking the track up by path finds it at the new path and
// not the old one, which adding files and importing rely on to skip it.
func TestRelinkUpdatesPathIndex(t *testing.T) {
	dir := t.TempDir()
	oldPath, newPath := filepath.Join(dir, "old.mp3"), filepath.Join(dir, "new.mp3")
	os.WriteFile(newPath, []byte("x"), 0o644)
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, FilePath: oldPath, FileType: "mp3"})
	s := &Server{Library: lib}
	rec := httptest.NewRecorder()
	s.handleTrackPath(rec, httptest.NewRequest(http.MethodPut, "/api/tracks/1/path", strings.NewReader(`{"path":"`+newPath+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if lib.TrackByPath(oldPath) != nil || lib.TrackByPath(newPath) != lib.Track(1) {
		t.Errorf("by path: old %v, new %v; want nil and the track", lib.TrackByPath(oldPath), lib.TrackByPath(newPath))
	}
}

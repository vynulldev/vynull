// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Relinking a track to a .aif file records the codec as "aiff", as every
// other way into the library does (the deck picks its decoder from it), and
// a file Vynull can't serve is refused without changing the track.
func TestRelinkTrackFileType(t *testing.T) {
	dir := t.TempDir()
	aif := filepath.Join(dir, "moved.aif")
	ogg := filepath.Join(dir, "moved.ogg")
	for _, p := range []string{aif, ogg} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "T", FilePath: filepath.Join(dir, "old.mp3"), FileType: "mp3"})
	s := &Server{Library: lib}
	relink := func(path string) int {
		rec := httptest.NewRecorder()
		s.handleTrackPath(rec, httptest.NewRequest(http.MethodPut, "/api/tracks/1/path",
			strings.NewReader(`{"path":"`+path+`"}`)))
		return rec.Code
	}

	if code := relink(aif); code != http.StatusOK {
		t.Fatalf("relink to .aif: status %d", code)
	}
	if got := lib.Track(1); got.FileType != "aiff" || got.FilePath != aif {
		t.Errorf("after relink: type %q path %q, want aiff %q", got.FileType, got.FilePath, aif)
	}

	if code := relink(ogg); code != http.StatusBadRequest {
		t.Errorf("relink to .ogg: status %d, want 400", code)
	}
	if got := lib.Track(1); got.FilePath != aif || got.FileType != "aiff" {
		t.Errorf("a refused relink changed the track: %q %q", got.FilePath, got.FileType)
	}
}

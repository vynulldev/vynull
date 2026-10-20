// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// Applying an import standardizes keys to Camelot and flags tracks whose
// audio isn't on this machine, counting them in the result.
func TestImportApplyKeysAndMissing(t *testing.T) {
	dir := t.TempDir()
	here := filepath.Join(dir, "here.mp3")
	os.WriteFile(here, []byte("x"), 0o644)
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, FilePath: here, Key: "Am"})
	lib.AddTrack(&library.Track{ID: 2, FilePath: filepath.Join(dir, "gone.mp3"), Key: "8A"})
	s := &Server{Library: lib}
	rec := httptest.NewRecorder()
	s.applyImportBundle(rec, &library.ImportBundle{}, importInclude{Tracks: true}, "", "")
	var res library.ImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("result %s: %v", rec.Body, err)
	}
	if res.FilesMissing != 1 || lib.Track(2).FileMissing != true || lib.Track(1).FileMissing {
		t.Errorf("missing: result %d, flags %v %v; want 1, track 2 only", res.FilesMissing, lib.Track(1).FileMissing, lib.Track(2).FileMissing)
	}
	if lib.Track(1).Key != "8A" || lib.Track(2).Key != "8A" {
		t.Errorf("keys %q, %q; want both 8A", lib.Track(1).Key, lib.Track(2).Key)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/api"
	"github.com/vynulldev/vynull/library"
)

// Colours and ratings an older version kept in the tag store move into the
// library once; the tag store's files are kept as .migrated backups.
func TestMoveLegacyColorsAndRatings(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "track_colors.json"), []byte(`{"1":4}`), 0o644)
	os.WriteFile(filepath.Join(dir, "track_ratings.json"), []byte(`{"1":5,"2":2}`), 0o644)
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "A", ColorID: 2, Rating: 1})
	lib.AddTrack(&library.Track{ID: 2, Title: "B"})

	ts := api.NewTagStore(dir)
	colors, ratings := ts.TakeLegacyColorsAndRatings()
	moveLegacyColorsAndRatings(lib, colors, ratings)
	if a, b := lib.Track(1), lib.Track(2); a.ColorID != 4 || a.Rating != 5 || b.Rating != 2 {
		t.Errorf("after the move: A colour %d rating %d, B rating %d; want 4, 5, 2", a.ColorID, a.Rating, b.Rating)
	}
	for _, f := range []string{"track_colors.json", "track_ratings.json"} {
		if _, err := os.Stat(filepath.Join(dir, f+".migrated")); err != nil {
			t.Errorf("%s not kept as a backup: %v", f, err)
		}
	}
	if c, r := api.NewTagStore(dir).TakeLegacyColorsAndRatings(); len(c)+len(r) != 0 {
		t.Errorf("legacy values taken twice: %v %v", c, r)
	}
}

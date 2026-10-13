// SPDX-License-Identifier: GPL-3.0-or-later

package related

import (
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
)

func add(lib *library.Library, title string, bpm float64, key, genre string) uint32 {
	return lib.AddTrack(&library.Track{Title: title, BPM: bpm, Key: key, Genre: genre, Artist: title + " Artist"})
}

func titles(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Track.Title)
	}
	return out
}

func find(ms []Match, title string) *Match {
	for i := range ms {
		if ms[i].Track.Title == title {
			return &ms[i]
		}
	}
	return nil
}

func TestFindRanksAndFilters(t *testing.T) {
	lib := library.New()
	seed := add(lib, "Seed", 126, "8A", "Techno")
	add(lib, "SameKey", 126.5, "8A", "House")  // same key, close tempo
	add(lib, "Adjacent", 127, "9A", "House")   // one step on the wheel
	add(lib, "Relative", 125, "C", "House")    // C major = 8B, relative of 8A
	add(lib, "HalfTime", 63, "8A", "Dub")      // same key at half time
	add(lib, "GenreOnly", 126, "3B", "Techno") // clashing key, same genre
	add(lib, "Clash", 126, "3B", "House")      // clashing key, other genre
	add(lib, "TooFast", 140, "8A", "Techno")   // right key, tempo out of range
	add(lib, "NoTempo", 0, "8A", "Techno")     // unknown tempo
	missing := add(lib, "Missing", 126, "8A", "Techno")
	lib.Track(missing).FileMissing = true

	got := Find(lib, seed, Options{})
	for _, bad := range []string{"Seed", "Clash", "TooFast", "NoTempo", "Missing"} {
		if find(got, bad) != nil {
			t.Errorf("%s should not be suggested: %v", bad, titles(got))
		}
	}
	for _, want := range []string{"SameKey", "Adjacent", "Relative", "HalfTime", "GenreOnly"} {
		if find(got, want) == nil {
			t.Errorf("%s not suggested: %v", want, titles(got))
		}
	}
	if got[0].Track.Title != "SameKey" {
		t.Errorf("best match = %s, want SameKey (%v)", got[0].Track.Title, titles(got))
	}
	if m := find(got, "HalfTime"); m != nil && !strings.Contains(strings.Join(m.Reasons, ";"), "half time") {
		t.Errorf("HalfTime reasons: %v", m.Reasons)
	}
	if m := find(got, "Relative"); m != nil && !strings.Contains(strings.Join(m.Reasons, ";"), "relative key (8A to 8B)") {
		t.Errorf("Relative reasons: %v", m.Reasons)
	}
}

// A rekordbox pair is always suggested, and ranks first, even when nothing
// else matches.
func TestFindRekordboxPair(t *testing.T) {
	lib := library.New()
	seed := add(lib, "Seed", 126, "8A", "Techno")
	add(lib, "SameKey", 126, "8A", "Techno")
	paired := add(lib, "Paired", 90, "2B", "Ambient")
	lib.AddRelated(seed, paired)

	got := Find(lib, seed, Options{})
	if len(got) == 0 || got[0].Track.Title != "Paired" || got[0].Reasons[0] != "paired in rekordbox" {
		t.Fatalf("pair not first: %v", titles(got))
	}
}

func TestFindLimitAndTolerance(t *testing.T) {
	lib := library.New()
	seed := add(lib, "Seed", 120, "8A", "")
	for i := 0; i < 10; i++ {
		add(lib, "T"+string(rune('a'+i)), 120+float64(i), "8A", "")
	}
	if got := Find(lib, seed, Options{Limit: 3}); len(got) != 3 {
		t.Errorf("limit 3 returned %d", len(got))
	}
	// 120 to 129 BPM is up to 7.5%; a 2% tolerance keeps 120, 121, 122.
	if got := Find(lib, seed, Options{BPMTolerance: 2}); len(got) != 3 {
		t.Errorf("2%% tolerance returned %v", titles(got))
	}
	if got := Find(lib, 9999, Options{}); got != nil {
		t.Errorf("unknown seed returned %v", titles(got))
	}
}

// Another version of the seed (same artist and title, different mix) is not a
// mixing suggestion.
func TestFindSkipsOtherVersions(t *testing.T) {
	lib := library.New()
	seed := lib.AddTrack(&library.Track{Title: "Melody (Extended Mix)", Artist: "A", BPM: 125, Key: "8A"})
	lib.AddTrack(&library.Track{Title: "Melody (Original Mix)", Artist: "A", BPM: 125, Key: "8A"})
	lib.AddTrack(&library.Track{Title: "Melody (Extended Mix)", Artist: "B", BPM: 125, Key: "8A"}) // a different song
	got := Find(lib, seed, Options{})
	if len(got) != 1 || got[0].Track.Artist != "B" {
		t.Fatalf("got %v", titles(got))
	}
}

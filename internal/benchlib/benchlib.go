// SPDX-License-Identifier: GPL-3.0-or-later

// Package benchlib generates a synthetic library for benchmarks: the same
// tracks for every run, with the spread of a real collection (a few thousand
// artists, repeated albums and labels, mixed file types and key notations,
// titles that exercise the deck's text collation, and cue counts from none
// to a full set of hot and memory cues). It imports only package library, so
// benchmarks in dbserver, api and library itself can all use it.
package benchlib

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/vynulldev/vynull/library"
)

// Size is the library size the refactor's benchmarks use.
const Size = 20000

var (
	words = []string{
		"Night", "Drive", "Echo", "Signal", "Ocean", "Glass", "Fire", "Static",
		"Velvet", "Orbit", "Shadow", "Pulse", "River", "Neon", "Dust", "Gravity",
		"Mirror", "Silence", "Storm", "Garden", "Motion", "Paper", "Summer", "Wire",
		"Café", "Ünder", "Señorita", "Déjà", "Rhythm", "Lights", "Machine", "Heart",
	}
	// prefixes put some titles out of plain alphabetical order: digits sort
	// after letters, and text starting with punctuation or a space sorts last.
	prefixes = []string{"", "", "", "", "", "", "", "", "The ", "A ", "2 ", "(", " ", "#"}
	mixes    = []string{"", "", "", "Original Mix", "Extended Mix", "Radio Edit", "Dub", "Remix"}
	genres   = []string{
		"House", "Deep House", "Tech House", "Techno", "Minimal", "Progressive House",
		"Trance", "Drum & Bass", "Dubstep", "Breaks", "Disco", "Nu Disco", "Funk",
		"Hip-Hop", "R&B", "Pop", "Electronica", "Ambient", "Garage", "UK Garage",
		"Afro House", "Melodic Techno", "Electro", "Jungle", "",
	}
	classicKeys = []string{
		"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B",
		"Cm", "C#m", "Dm", "Ebm", "Em", "Fm", "F#m", "Gm", "G#m", "Am", "Bbm", "Bm",
	}
	camelotKeys = []string{
		"1A", "2A", "3A", "4A", "5A", "6A", "7A", "8A", "9A", "10A", "11A", "12A",
		"1B", "2B", "3B", "4B", "5B", "6B", "7B", "8B", "9B", "10B", "11B", "12B",
	}
	fileTypes = []struct {
		ext, typ             string
		weight, bitrate, bit int
	}{
		{"mp3", "mp3", 50, 320, 16},
		{"flac", "flac", 30, 1411, 24},
		{"wav", "wav", 10, 1411, 16},
		{"aiff", "aiff", 5, 1411, 16},
		{"m4a", "m4a", 5, 256, 16},
	}
)

func pick[T any](r *rand.Rand, s []T) T { return s[r.IntN(len(s))] }

func phrase(r *rand.Rand, n int) string {
	s := pick(r, words)
	for i := 1; i < n; i++ {
		s += " " + pick(r, words)
	}
	return s
}

// Tracks returns n synthetic tracks with IDs 1..n, the same on every call.
func Tracks(n int) []*library.Track {
	r := rand.New(rand.NewPCG(20261018, uint64(n)))
	artists := make([]string, max(n/8, 1)) // ~8 tracks per artist
	for i := range artists {
		artists[i] = fmt.Sprintf("%s %s %d", pick(r, words), pick(r, words), i)
	}
	albums := make([]string, max(n/5, 1)) // ~5 tracks per album
	for i := range albums {
		albums[i] = pick(r, prefixes) + phrase(r, 2)
	}
	labels := make([]string, max(n/50, 1))
	for i := range labels {
		labels[i] = phrase(r, 2) + " Records"
	}
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

	tracks := make([]*library.Track, n)
	for i := range tracks {
		artist := pick(r, artists)
		album := pick(r, albums)
		title := pick(r, prefixes) + phrase(r, 1+r.IntN(3))
		ft := fileTypes[0]
		w := r.IntN(100) // weights sum to 100
		for _, f := range fileTypes {
			if w < f.weight {
				ft = f
				break
			}
			w -= f.weight
		}
		key := pick(r, classicKeys)
		switch x := r.IntN(100); {
		case x < 3:
			key = "" // not analysed
		case x < 30:
			key = pick(r, camelotKeys)
		}
		bpm := float64(70 + r.IntN(111))
		if r.IntN(5) == 0 {
			bpm += float64(r.IntN(100)) / 100 // some fractional tempos
		}
		dur := time.Duration(120+r.IntN(480)) * time.Second
		t := &library.Track{
			ID:          uint32(i + 1),
			Title:       title,
			Artist:      artist,
			Album:       album,
			Genre:       pick(r, genres),
			Duration:    library.DurationSec(dur),
			BPM:         bpm,
			Key:         key,
			Rating:      uint8(r.IntN(6)),
			Year:        1980 + r.IntN(47),
			TrackNum:    1 + r.IntN(12),
			FilePath:    fmt.Sprintf("/music/%s/%s/%02d %s.%s", artist, album, 1+r.IntN(12), title, ft.ext),
			FileType:    ft.typ,
			FileSize:    int64(dur.Seconds()) * int64(ft.bitrate) * 125,
			Label:       pick(r, labels),
			MixName:     pick(r, mixes),
			DateAdded:   base.Add(-time.Duration(r.IntN(10*365*24)) * time.Hour),
			Bitrate:     ft.bitrate,
			SampleRate:  44100,
			SampleDepth: ft.bit,
			PlayCount:   r.IntN(40),
			ColorID:     uint8(r.IntN(9)),
		}
		if r.IntN(3) == 0 {
			t.Comment = "Bought from " + pick(r, labels)
		}
		if r.IntN(8) == 0 {
			t.Remixer = pick(r, artists)
		}
		if r.IntN(10) == 0 {
			t.OriginalArtist = pick(r, artists)
		}
		tracks[i] = t
	}
	return tracks
}

// Library returns a library of n synthetic tracks (see Tracks).
func Library(n int) *library.Library {
	return library.NewLibrary(Tracks(n), library.NewArtworkCache())
}

// Cue is a synthetic cue point: a hot cue (Number 1-8) or a memory cue
// (Number 9 up), optionally a loop.
type Cue struct {
	Number  uint16
	TimeMs  uint32
	LoopMs  int32 // loop end, or -1 for a plain cue
	ColorID uint32
}

// Cues returns the cue points of track (the same on every call): a quarter
// of tracks have none, the rest 1-8 hot cues and 0-3 memory cues, which
// averages about six per track.
func Cues(t *library.Track) []Cue {
	r := rand.New(rand.NewPCG(uint64(t.ID), 0xc0e))
	if r.IntN(4) == 0 {
		return nil
	}
	durMs := uint32(t.Duration.Duration().Milliseconds())
	var out []Cue
	add := func(n uint16) {
		c := Cue{Number: n, TimeMs: uint32(r.Int64N(int64(max(durMs, 1)))), LoopMs: -1, ColorID: uint32(r.IntN(17))}
		if r.IntN(6) == 0 {
			c.LoopMs = int32(c.TimeMs) + 4000
		}
		out = append(out, c)
	}
	hot, memory := 1+r.IntN(8), r.IntN(4)
	for n := range hot {
		add(uint16(1 + n))
	}
	for n := range memory {
		add(uint16(9 + n))
	}
	return out
}

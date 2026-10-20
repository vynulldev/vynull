// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/internal/benchlib"
	"github.com/vynulldev/vynull/library"
)

// BenchmarkImportByPath measures adding a 20k-track library the way the
// importers do: look each track up by path to skip one already present, then
// add it.
func BenchmarkImportByPath(b *testing.B) {
	tracks := benchlib.Tracks(benchlib.Size)
	b.ReportAllocs()
	for b.Loop() {
		lib := library.New()
		for _, t := range tracks {
			if lib.TrackByPath(t.FilePath) == nil {
				c := *t
				lib.AddTrackBulk(&c)
			}
		}
		lib.FinalizeBulk()
	}
}

// BenchmarkUpdateArtist measures one indexed edit in a 20k-track library,
// without the save: moving a track to another artist and back.
func BenchmarkUpdateArtist(b *testing.B) {
	lib := benchlib.Library(benchlib.Size)
	t := lib.Tracks()[benchlib.Size/2]
	names := [2]string{t.Artist, "Someone Else"}
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		lib.UpdateBulk(t.ID, func(tr *library.Track) error { tr.Artist = names[i%2]; return nil })
	}
}

// BenchmarkTrackByPath measures one lookup by path in a 20k-track library.
func BenchmarkTrackByPath(b *testing.B) {
	lib := benchlib.Library(benchlib.Size)
	tracks := lib.Tracks()
	i := 0
	for b.Loop() {
		if lib.TrackByPath(tracks[i%len(tracks)].FilePath) == nil {
			b.Fatal("track not found by its path")
		}
		i += 7919 // a prime stride, so lookups spread across the library
	}
}

// BenchmarkLibrarySave measures one Save of a 20k-track library: the full
// library.json rewrite that every play count, rating, analysis result and
// edit triggers today.
func BenchmarkLibrarySave(b *testing.B) {
	lib := benchlib.Library(benchlib.Size)
	path := filepath.Join(b.TempDir(), "library.json")
	lib.SetDBPath(path)
	lib.Save()
	fi, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(fi.Size())
	b.ReportAllocs()
	for b.Loop() {
		lib.Save()
	}
}

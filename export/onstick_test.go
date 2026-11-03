// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vynulldev/vynull/pdb"
)

// audioFile writes size bytes of a pattern seeded by seed, standing in for
// an audio file.
func audioFile(t *testing.T, path string, size int, seed byte) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7) ^ seed
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stick writes a first export of tracks to dir, as a fresh export does, and
// returns its PDB path.
func stick(t *testing.T, src, dir string, tracks []*pdb.Track, playlist []uint32) string {
	t.Helper()
	if err := pdb.PrepareUSBLayout(tracks, src, dir, true); err != nil {
		t.Fatal(err)
	}
	if err := pdb.GenerateWithOptions(tracks, SinglePlaylist("First", playlist), nil, nil, dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "PIONEER", "rekordbox", "export.pdb")
}

// A merge export keeps a track the stick already holds at its file and ID,
// even after its artist changed (which gives it another folder), and lays out
// only the new track: the stick ends with three tracks, not four, and its
// first playlist still plays the same two.
func TestMergeKeepsTracksOnTheStick(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	audioFile(t, filepath.Join(src, "a.mp3"), 200_000, 1)
	audioFile(t, filepath.Join(src, "b.mp3"), 200_000, 2)
	audioFile(t, filepath.Join(src, "c.mp3"), 50_000, 3)
	pdbPath := stick(t, src, dir, []*pdb.Track{
		{ID: 1, Title: "A", Artist: "One", Album: "X", FilePath: filepath.Join(src, "a.mp3")},
		{ID: 2, Title: "B", Artist: "Two", Album: "X", FilePath: filepath.Join(src, "b.mp3")},
	}, []uint32{1, 2})
	existing, err := pdb.Open(pdbPath)
	if err != nil {
		t.Fatal(err)
	}
	pathOf := map[uint32]string{}
	for _, e := range existing.Tracks {
		pathOf[e.ID] = e.FilePath
	}

	a := &pdb.Track{ID: 7, Title: "A", Artist: "One (edited)", Album: "X", FilePath: filepath.Join(src, "a.mp3")}
	b := &pdb.Track{ID: 8, Title: "B", Artist: "Two", Album: "X", FilePath: filepath.Join(src, "b.mp3")}
	c := &pdb.Track{ID: 9, Title: "C", Artist: "Three", Album: "X", FilePath: filepath.Join(src, "c.mp3")}
	second := []*pdb.Track{b, a, c}
	rest := keepOnStick(second, dir, existing.Tracks)
	if len(rest) != 1 || rest[0] != c {
		t.Fatalf("left to lay out: %v, want only the new track", rest)
	}
	if a.FilePath != pathOf[1] || b.FilePath != pathOf[2] {
		t.Errorf("kept at %q and %q, want the stick's %q and %q", a.FilePath, b.FilePath, pathOf[1], pathOf[2])
	}
	if err := pdb.PrepareUSBLayout(rest, src, dir, true); err != nil {
		t.Fatal(err)
	}
	tracks, playlists := mergeWithExisting(pdbPath, second, SinglePlaylist("Second", []uint32{9, 8, 7}))

	if len(tracks) != 3 {
		t.Errorf("%d tracks after the merge, want 3", len(tracks))
	}
	byID := map[uint32]*pdb.Track{}
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}
	if byID[1] == nil || byID[1].Artist != "One (edited)" || byID[2] == nil || byID[2].Title != "B" {
		t.Errorf("rows 1 and 2: %+v, %+v; want A (with its new artist) and B under their IDs", byID[1], byID[2])
	}
	for _, p := range playlists {
		var got []string
		for _, id := range p.TrackIDs {
			if tr := byID[id]; tr != nil {
				got = append(got, tr.Title)
			}
		}
		want := map[string][]string{"First": {"A", "B"}, "Second": {"C", "B", "A"}}[p.Name]
		if !slices.Equal(got, want) {
			t.Errorf("playlist %q plays %v, want %v", p.Name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Contents", "One (edited)")); err == nil {
		t.Error("the edited track was copied again, under its new artist")
	}
}

// A different file with the same name and size isn't taken for the stick's:
// only the same bytes are.
func TestKeepOnStickNeedsTheSameBytes(t *testing.T) {
	src, other, dir := t.TempDir(), t.TempDir(), t.TempDir()
	audioFile(t, filepath.Join(src, "a.mp3"), 200_000, 1)
	audioFile(t, filepath.Join(other, "a.mp3"), 200_000, 5)
	pdbPath := stick(t, src, dir, []*pdb.Track{
		{ID: 1, Title: "A", Artist: "One", Album: "X", FilePath: filepath.Join(src, "a.mp3")},
	}, []uint32{1})
	existing, err := pdb.Open(pdbPath)
	if err != nil {
		t.Fatal(err)
	}
	same := &pdb.Track{ID: 2, Title: "A", Artist: "One", Album: "X", FilePath: filepath.Join(other, "a.mp3")}
	reencoded := &pdb.Track{ID: 3, Title: "A", Artist: "One", Album: "X", FilePath: filepath.Join(src, "a.mp3"), NeedsReencode: true}
	if rest := keepOnStick([]*pdb.Track{same, reencoded}, dir, existing.Tracks); len(rest) != 2 {
		t.Errorf("left to lay out: %d tracks, want both: different bytes, and a track the export re-encodes", len(rest))
	}
}

func TestSameEnds(t *testing.T) {
	dir := t.TempDir()
	base := bytes.Repeat([]byte("abcdefgh"), 40_000) // 320,000 bytes
	write := func(name string, edit func([]byte)) string {
		b := slices.Clone(base)
		edit(b)
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	a := write("a", func([]byte) {})
	for _, c := range []struct {
		name string
		edit func([]byte)
		want bool
	}{
		{"identical", func([]byte) {}, true},
		{"first byte", func(b []byte) { b[0] = 'X' }, false},
		{"last byte", func(b []byte) { b[len(b)-1] = 'X' }, false},
		{"inside the first 64 KiB", func(b []byte) { b[sameFileEnds-1] = 'X' }, false},
	} {
		if got := sameEnds(a, write(c.name, c.edit), int64(len(base))); got != c.want {
			t.Errorf("%s: sameEnds %v, want %v", c.name, got, c.want)
		}
	}
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("tiny"), 0o644)
	if !sameEnds(short, short, 4) {
		t.Error("a file shorter than sameFileEnds doesn't match itself")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestPrepareUSBLayoutKeepsCollidingNamesApart lays out tracks whose USB file
// names collide: two mixes sharing their first 44 characters, two names that
// differ only in case (one file on FAT), and the same name from two folders.
// Before the fix each pair got one path, the second copy overwrote the first,
// and both PDB rows played the same audio. The later track of a pair gets
// rekordbox's "-1" suffix (rekordbox 7 export: "Same Name.mp3" and
// "Same Name-1.mp3", "...Shared Title .mp3" and "...Shared Titl-1.mp3").
func TestPrepareUSBLayoutKeepsCollidingNamesApart(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	const long = "Fixture Artist - A Very Long Shared Title For Two Mixes"
	files := []string{
		long + " (Mix A).mp3",
		long + " (Mix B).mp3",
		long + " (Mix C).mp3",
		"Case Test.mp3",
		"CASE TEST.mp3",
		"one/Same Name.mp3",
		"two/Same Name.mp3",
	}
	want := []string{
		"Fixture Artist - A Very Long Shared Title Fo.mp3",
		"Fixture Artist - A Very Long Shared Title -1.mp3",
		"Fixture Artist - A Very Long Shared Title -2.mp3",
		"Case Test.mp3",
		"CASE TEST-1.mp3",
		"Same Name.mp3",
		"Same Name-1.mp3",
	}
	var tracks []*Track
	for i, f := range files {
		p := filepath.Join(src, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, &Track{ID: uint32(i + 1), Artist: "Fixture Artist", Album: "Collisions", FilePath: p})
	}

	if err := PrepareUSBLayout(tracks, src, out, true); err != nil {
		t.Fatal(err)
	}

	for i, tr := range tracks {
		if got := "/Contents/Fixture Artist/Collisions/" + want[i]; tr.FilePath != got {
			t.Errorf("%s: USB path %q, want %q", files[i], tr.FilePath, got)
		}
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(tr.FilePath)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != files[i] {
			t.Errorf("%s plays %q, want %q", tr.FilePath, got, files[i])
		}
	}
}

// A cut never splits a character: a two-byte "é" that the 48-byte cap or a
// suffix would cut in half is left out whole.
func TestPrepareUSBLayoutCutsWholeCharacters(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	// In the first two names "é" takes bytes 42 and 43: the 44-byte base of
	// the plain cap keeps it, the 42-byte base before "-1" would split it.
	// In the third it takes bytes 44 and 45, split by the plain cap.
	prefix := strings.Repeat("x", 41)
	var tracks []*Track
	for i, f := range []string{prefix + "é one.mp3", prefix + "é two.mp3", strings.Repeat("y", 43) + "é.mp3"} {
		p := filepath.Join(src, f)
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, &Track{ID: uint32(i + 1), Artist: "Fixture Artist", Album: "Cuts", FilePath: p})
	}

	if err := PrepareUSBLayout(tracks, src, out, true); err != nil {
		t.Fatal(err)
	}

	for i, want := range []string{prefix + "é .mp3", prefix + "-1.mp3", strings.Repeat("y", 43) + ".mp3"} {
		name := filepath.Base(tracks[i].FilePath)
		if !utf8.ValidString(name) {
			t.Errorf("track %d: %q is not valid UTF-8", i+1, name)
		} else if name != want {
			t.Errorf("track %d: %q, want %q", i+1, name, want)
		}
	}
}

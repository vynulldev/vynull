// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareUSBLayoutKeepsCollidingNamesApart lays out tracks whose USB file
// names collide once capped: two mixes sharing their first 44 characters, and
// two names that differ only in case (one file on FAT). Before the fix each
// pair got one path, the second copy overwrote the first, and both PDB rows
// played the same audio.
func TestPrepareUSBLayoutKeepsCollidingNamesApart(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	names := []string{
		"S3RL & Synthwulf - Everybody Wants to be a DJ [Wav File].mp3",
		"S3RL & Synthwulf - Everybody Wants to be a DJ (S3RL hard mix) [Wav File].mp3",
		"Intro.mp3",
		"INTRO.mp3",
	}
	var tracks []*Track
	for i, n := range names {
		p := filepath.Join(src, n)
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, &Track{ID: uint32(i + 1), Artist: "S3RL", Album: "Pack", FilePath: p})
	}

	if err := PrepareUSBLayout(tracks, src, out, true); err != nil {
		t.Fatal(err)
	}

	seen := map[string]string{}
	for i, tr := range tracks {
		key := strings.ToLower(tr.FilePath)
		if other, dup := seen[key]; dup {
			t.Errorf("%q and %q share the USB path %s", other, names[i], tr.FilePath)
		}
		seen[key] = names[i]
		if len(tr.FilePath) > 126 {
			t.Errorf("USB path %q is %d chars, over the 126 cap", tr.FilePath, len(tr.FilePath))
		}
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(tr.FilePath)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != names[i] {
			t.Errorf("%s plays %q, want %q", tr.FilePath, got, names[i])
		}
	}
}

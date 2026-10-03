// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSanitizeFilenameControlChars pins the fix for a real-library export
// failure: an album tag with an embedded NUL (a mangled apostrophe) made
// mkdir fail with EINVAL. Control characters are dropped, FAT-illegal
// punctuation is replaced, trailing dots/spaces are trimmed, and the
// result of sanitizing any input must be mkdir-able.
func TestSanitizeFilenameControlChars(t *testing.T) {
	cases := map[string]string{
		"Album\x00Name (Remixes)": "AlbumName (Remixes)",
		"Line\r\nBreak":           "LineBreak",
		"Tab\tName":               "TabName",
		"AC/DC: Back?":            "AC_DC_ Back_",
		"Trailing dot.":           "Trailing dot",
		"Trailing space ":         "Trailing space",
		"\x00\x01\x02":            "_",
		"normal name!!":           "normal name!!",
	}
	dir := t.TempDir()
	for in, want := range cases {
		got := SanitizeFilename(in)
		if got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
		if err := os.Mkdir(filepath.Join(dir, got+"-"+got[:1]), 0o755); err != nil {
			t.Errorf("sanitized name %q is not mkdir-able: %v", got, err)
		}
	}
}

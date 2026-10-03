// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

// TestTruncateComponent pins the directory-component cap used for artist and
// album names: byte-capped on a rune boundary, no ".ext" preservation
// (a dot in an artist name is just a dot), trailing space/dot re-trimmed,
// and never empty. Guards the export "file name too long" fix.
func TestTruncateComponent(t *testing.T) {
	long := "A Long Artist Name. With A Dot That Goes On And On And On And On Past Sixty Four Characters"
	got := truncateComponent(long, 64)
	if len(got) > 64 {
		t.Errorf("len = %d, want <= 64", len(got))
	}
	if got != long[:64] { // no dot-as-extension mangling; plain prefix (ends on a letter here)
		t.Errorf("got %q, want plain 64-byte prefix", got)
	}
	if truncateComponent("short", 64) != "short" {
		t.Error("short names must pass through unchanged")
	}
	// A cut landing mid-rune backs up to a boundary (never produces invalid UTF-8).
	multibyte := "Café " + strings.Repeat("ñ", 60)
	if r := truncateComponent(multibyte, 20); !utf8.ValidString(r) {
		t.Errorf("truncation produced invalid UTF-8: %q", r)
	}
}

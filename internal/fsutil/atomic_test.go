// SPDX-License-Identifier: GPL-3.0-or-later

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// Both writes replace the file whole and leave no temp file behind.
func TestAtomicWrites(t *testing.T) {
	for name, write := range map[string]func(string, []byte, os.FileMode) error{
		"WriteFile": WriteFile, "WriteCache": WriteCache,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.json")
			for _, data := range []string{"first version", "second"} {
				if err := write(path, []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
				if got, _ := os.ReadFile(path); string(got) != data {
					t.Fatalf("read %q, want %q", got, data)
				}
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("%d files in the directory, want only the written one", len(entries))
			}
		})
	}
}

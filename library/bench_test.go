// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/internal/benchlib"
)

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

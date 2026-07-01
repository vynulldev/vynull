// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"fmt"
	"os"
	"path/filepath"
)

// Extension PDB table types. rekordbox writes nine tables in
// `exportExt.pdb` containing the newer NXS2+ data the original
// `export.pdb` schema doesn't carry: my-tag categories, my-tag
// entries, my-tag↔track links, history, hot-cue banks, etc.
//
// The exact per-row formats vary per table and are reverse-engineered
// piecewise. v1 here writes empty tables for every type so the file
// is present and structurally valid — populated rows can be added one
// table at a time later (compare against a rekordbox
// `exportExt.pdb` from a USB export).
const (
	TableExt0 = 0x00
	TableExt1 = 0x01
	TableExt2 = 0x02
	TableExt3 = 0x03 // observed: my-tag entries (e.g. "Genre", "Components", "Situation")
	TableExt4 = 0x04 // observed: largest table — likely track ↔ my-tag links
	TableExt5 = 0x05
	TableExt6 = 0x06
	TableExt7 = 0x07
	TableExt8 = 0x08
)

// GenerateExt writes an empty `exportExt.pdb` skeleton under outDir.
// The file contains all 9 tables rekordbox produces, but every
// table is empty — useful as a presence sentinel for CDJs that look
// for the file before enabling NXS2+ features. Future revs can fill
// individual tables in place by extending this function.
func GenerateExt(outDir string) error {
	tables := make([]*tableBuilder, 9)
	for i := range tables {
		tables[i] = newTableBuilder(i)
	}
	pdbDir := filepath.Join(outDir, "PIONEER", "rekordbox")
	if err := os.MkdirAll(pdbDir, 0o755); err != nil {
		return fmt.Errorf("create PIONEER dir: %w", err)
	}
	path := filepath.Join(pdbDir, "exportExt.pdb")
	return writeFile(path, tables, defaultPageSize, true /*isExt*/)
}

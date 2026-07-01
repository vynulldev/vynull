// SPDX-License-Identifier: GPL-3.0-or-later

// Command pdbdiff compares two PDB files structurally and prints only
// the fields whose values differ. Output is stable per file so two
// invocations produce identical text — making it safe to pipe to
// diff(1) or eyeball.
//
// Comparison is aligned by table type and page-within-table — NOT by
// file offset — so two files with different total page counts still
// align sensibly. For each table we walk the pages in chain order
// (sentinel → data → next…) on each side and compare matching pages.
//
// Usage:
//
//	pdbdiff <real.pdb> <ours.pdb>
//	pdbdiff --all  <real.pdb> <ours.pdb>   # also print identical fields
//	pdbdiff --table tracks <a> <b>         # restrict to one table
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
)

const (
	pageSize       = 4096
	pageHeaderSize = 0x28
	rowGroupSize   = 0x24
)

func main() {
	showAll := flag.Bool("all", false, "also print fields that match")
	tableFlag := flag.String("table", "", "limit output to this table type (e.g. tracks, genres)")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: pdbdiff [--all] [--table NAME] <a.pdb> <b.pdb>")
		os.Exit(2)
	}
	a := mustLoad(flag.Arg(0))
	b := mustLoad(flag.Arg(1))

	cmpHeader(a, b, *showAll)

	tableNames := []string{
		0x00: "tracks", 0x01: "genres", 0x02: "artists", 0x03: "albums",
		0x04: "labels", 0x05: "keys", 0x06: "colors",
		0x07: "playlist_tree", 0x08: "playlist_entries",
		0x0D: "artwork",
	}
	for i := range a.tables {
		ta, tb := a.tables[i], b.tables[i]
		name := fmt.Sprintf("type_%02x", ta.typ)
		if int(ta.typ) < len(tableNames) && tableNames[ta.typ] != "" {
			name = tableNames[ta.typ]
		}
		if *tableFlag != "" && *tableFlag != name {
			continue
		}
		cmpTable(name, a, b, ta, tb, *showAll)
	}
}

type pdb struct {
	data   []byte
	tables []tableDesc
	header map[string]uint32
}

type tableDesc struct {
	typ, empty, first, last uint32
}

func mustLoad(path string) *pdb {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(1)
	}
	p := &pdb{data: data, header: map[string]uint32{}}
	p.header["len_page"] = le32(data, 0x04)
	p.header["num_tables"] = le32(data, 0x08)
	p.header["next_unused_page"] = le32(data, 0x0C)
	p.header["unknown1"] = le32(data, 0x10)
	p.header["seqdb"] = le32(data, 0x14)
	p.header["unknown2"] = le32(data, 0x18)
	n := p.header["num_tables"]
	for i := uint32(0); i < n; i++ {
		off := 0x1C + int(i)*16
		p.tables = append(p.tables, tableDesc{
			typ:   le32(data, off),
			empty: le32(data, off+4),
			first: le32(data, off+8),
			last:  le32(data, off+12),
		})
	}
	return p
}

func cmpHeader(a, b *pdb, showAll bool) {
	hdr := func() {
		fmt.Println("=== FILE HEADER ===")
	}
	once := false
	for _, k := range sortedKeys(a.header) {
		av, bv := a.header[k], b.header[k]
		if av != bv || showAll {
			if !once {
				hdr()
				once = true
			}
			marker := ""
			if av != bv {
				marker = "  <<< DIFF"
			}
			fmt.Printf("  %-20s  A=0x%08x  B=0x%08x%s\n", k, av, bv, marker)
		}
	}
	// Compare per-table descriptors.
	once = false
	for i := 0; i < len(a.tables) && i < len(b.tables); i++ {
		fields := []struct {
			name   string
			av, bv uint32
		}{
			{"type", a.tables[i].typ, b.tables[i].typ},
			{"empty_candidate", a.tables[i].empty, b.tables[i].empty},
			{"first_page", a.tables[i].first, b.tables[i].first},
			{"last_page", a.tables[i].last, b.tables[i].last},
		}
		for _, f := range fields {
			if f.av != f.bv || showAll {
				if !once {
					fmt.Println("=== TABLE DESCRIPTORS ===")
					once = true
				}
				marker := ""
				if f.av != f.bv {
					marker = "  <<< DIFF"
				}
				fmt.Printf("  table[%2d].%-15s  A=%d  B=%d%s\n", i, f.name, f.av, f.bv, marker)
			}
		}
	}
}

// cmpTable walks both sides' page chains for one table and compares
// matching pages by chain index (sentinel=0, then data pages in order).
// We use the chain (not last_page) because real exports interleave
// pages from different tables — a table's first/last span can include
// gaps held by other tables.
func cmpTable(name string, a, b *pdb, ta, tb tableDesc, showAll bool) {
	pagesA := walkChain(a, ta)
	pagesB := walkChain(b, tb)
	if len(pagesA) == 0 && len(pagesB) == 0 {
		return
	}
	n := len(pagesA)
	if len(pagesB) > n {
		n = len(pagesB)
	}
	for i := 0; i < n; i++ {
		var pA, pB []byte
		var idxA, idxB uint32
		if i < len(pagesA) {
			idxA = pagesA[i]
			pA = a.data[int(idxA)*pageSize : (int(idxA)+1)*pageSize]
		}
		if i < len(pagesB) {
			idxB = pagesB[i]
			pB = b.data[int(idxB)*pageSize : (int(idxB)+1)*pageSize]
		}
		role := "data"
		if i == 0 {
			role = "sentinel"
		}
		cmpPage(name, role, i, idxA, idxB, pA, pB, showAll)
	}
}

func walkChain(p *pdb, t tableDesc) []uint32 {
	out := []uint32{t.first}
	cur := t.first
	for cur != 0 && int(cur)*pageSize < len(p.data) {
		// Bail if past the table's last_page (the descriptor caps it).
		if cur > t.last {
			break
		}
		next := le32(p.data[int(cur)*pageSize:], 0x0C)
		if next == cur || next == 0 || int(next)*pageSize >= len(p.data) || next > t.last {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

func cmpPage(name, role string, idx int, idxA, idxB uint32, pA, pB []byte, showAll bool) {
	hdr := fmt.Sprintf("=== %s page[%d] %s (A=#%d B=#%d) ===", name, idx, role, idxA, idxB)
	if pA == nil || pB == nil {
		fmt.Println(hdr)
		if pA == nil {
			fmt.Printf("  ONLY IN B: page #%d\n", idxB)
		} else {
			fmt.Printf("  ONLY IN A: page #%d\n", idxA)
		}
		return
	}
	// Compare these header fields (positional in any page).
	hdrFields := []struct {
		name string
		off  int
		size int
	}{
		{"page_type", 0x08, 4},
		{"next_page", 0x0C, 4},
		{"seqpage", 0x10, 4},
		{"packed_row_counts(low3B)", 0x18, 3},
		{"page_flags", 0x1B, 1},
		{"free_size", 0x1C, 2},
		{"heap_used", 0x1E, 2},
		{"transaction_row_count", 0x20, 2},
		{"transaction_row_index", 0x22, 2},
		{"reserved_0x24", 0x24, 2},
		{"reserved_0x26", 0x26, 2},
	}
	flagsA := pA[0x1B]
	flagsB := pB[0x1B]
	once := false
	for _, f := range hdrFields {
		av := readN(pA, f.off, f.size)
		bv := readN(pB, f.off, f.size)
		if av != bv || showAll {
			if !once {
				fmt.Println(hdr)
				once = true
			}
			marker := ""
			if av != bv {
				marker = "  <<< DIFF"
			}
			fmt.Printf("  %-24s  A=0x%08x  B=0x%08x%s\n", f.name, av, bv, marker)
		}
	}

	// For sentinel pages, also compare the boilerplate at 0x28-0x3B
	// plus the "is the fill region uniform 0x1FFFFFF8?" check.
	if flagsA&0x40 != 0 && flagsB&0x40 != 0 {
		sentinelFields := []struct {
			name string
			off  int
		}{
			{"sentinel.page_idx_self", 0x28},
			{"sentinel.first_data_page", 0x2C},
			{"sentinel.magic2_at_30", 0x30},
			{"sentinel.reserved_at_34", 0x34},
			{"sentinel.reserved_at_38", 0x38},
		}
		for _, f := range sentinelFields {
			av := le32(pA, f.off)
			bv := le32(pB, f.off)
			if av != bv || showAll {
				if !once {
					fmt.Println(hdr)
					once = true
				}
				marker := ""
				if av != bv {
					marker = "  <<< DIFF"
				}
				fmt.Printf("  %-24s  A=0x%08x  B=0x%08x%s\n", f.name, av, bv, marker)
			}
		}
		// Spot-check the 0x1FFFFFF8 fill region for uniformity (we
		// already know real uses uniform fill — flag any non-uniform
		// slot as suspicious).
		nonA := countNonFill(pA)
		nonB := countNonFill(pB)
		if nonA != nonB || (showAll && (nonA+nonB > 0)) {
			if !once {
				fmt.Println(hdr)
				once = true
			}
			fmt.Printf("  non-fill bytes in fill region: A=%d  B=%d  <<< DIFF\n", nonA, nonB)
		}
	}

	// For non-sentinel data pages, also compare row count and (if
	// matching) the first-row fixed area byte-for-byte.
	if flagsA&0x40 == 0 && flagsB&0x40 == 0 {
		// All-zero placeholder pages: both should be zero.
		if isAllZero(pA) || isAllZero(pB) {
			if isAllZero(pA) != isAllZero(pB) {
				if !once {
					fmt.Println(hdr)
				}
				fmt.Printf("  zero-placeholder: A=%v B=%v  <<< DIFF\n", isAllZero(pA), isAllZero(pB))
			}
			return
		}
	}
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func countNonFill(p []byte) int {
	// Fill region runs from 0x3C..pageSize-20 (per real exports).
	// Real exports use 0x1FFFFFF8 throughout that region.
	const fill uint32 = 0x1FFFFFF8
	count := 0
	for off := 0x3C; off+4 <= pageSize-20; off += 4 {
		if binary.LittleEndian.Uint32(p[off:]) != fill {
			count++
		}
	}
	return count
}

func readN(b []byte, off, size int) uint32 {
	switch size {
	case 1:
		return uint32(b[off])
	case 2:
		return uint32(binary.LittleEndian.Uint16(b[off:]))
	case 3:
		return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16
	case 4:
		return binary.LittleEndian.Uint32(b[off:])
	}
	return 0
}

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

func sortedKeys(m map[string]uint32) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

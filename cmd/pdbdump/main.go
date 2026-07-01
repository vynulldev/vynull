// SPDX-License-Identifier: GPL-3.0-or-later

// Command pdbdump parses a rekordbox export.pdb byte-by-byte and
// prints each page header, sentinel-page boilerplate, and row body
// with field-name annotations. It exists to reverse-engineer the
// track row layout from a known-good rekordbox export so we can
// match it byte-for-byte in pdb.encodeTrackRow.
//
// Usage:
//
//	pdbdump <path/to/export.pdb>                     # full dump
//	pdbdump --table tracks <path/to/export.pdb>      # tracks only
//	pdbdump --rows-only --table tracks <path>        # only row bytes
//
// The output format is plain text with one field per line so two
// dumps can be diffed with `diff -u real.txt ours.txt` to see exactly
// which row fields differ.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

const (
	pageSize       = 4096
	pageHeaderSize = 0x28
	rowGroupSize   = 0x24
)

func main() {
	tableFlag := flag.String("table", "", "limit output to this table type (tracks, genres, artists, albums, labels, keys, colors, playlist_tree, playlist_entries, artwork)")
	rowsOnly := flag.Bool("rows-only", false, "skip page-header dumps; show row bytes only")
	pageFlag := flag.Int("page", -1, "dump only this page (file-page-index, 0-based)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pdbdump [--table NAME] [--rows-only] [--page N] <export.pdb>")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	if len(data) < pageSize {
		log.Fatal("file too small")
	}

	dumpHeader(data)

	// Build table-index list (page → table type).
	numTables := binary.LittleEndian.Uint32(data[0x08:])
	tables := make([]tableDesc, numTables)
	for i := range tables {
		off := 0x1C + i*16
		tables[i] = tableDesc{
			typ:   binary.LittleEndian.Uint32(data[off:]),
			first: binary.LittleEndian.Uint32(data[off+8:]),
			last:  binary.LittleEndian.Uint32(data[off+12:]),
		}
	}
	pageOwner := make(map[uint32]int, len(data)/pageSize)
	for ti, t := range tables {
		// Walk the chain starting from `first`; rely on next_page values
		// rather than guessing the page range. Most real tables only
		// have 2-3 pages in their chain even with `last` far ahead.
		cur := t.first
		for cur != 0 && cur < uint32(len(data)/pageSize) {
			pageOwner[cur] = ti
			off := int(cur)*pageSize + 0x0C
			next := binary.LittleEndian.Uint32(data[off:])
			if next == cur || next > t.last+10 {
				break
			}
			cur = next
		}
	}

	tableTypeName := func(typ uint32) string {
		switch typ {
		case 0x00:
			return "tracks"
		case 0x01:
			return "genres"
		case 0x02:
			return "artists"
		case 0x03:
			return "albums"
		case 0x04:
			return "labels"
		case 0x05:
			return "keys"
		case 0x06:
			return "colors"
		case 0x07:
			return "playlist_tree"
		case 0x08:
			return "playlist_entries"
		case 0x0D:
			return "artwork"
		default:
			return fmt.Sprintf("type_%02x", typ)
		}
	}

	for pageIdx := 1; pageIdx*pageSize <= len(data); pageIdx++ {
		if *pageFlag >= 0 && *pageFlag != pageIdx {
			continue
		}
		ti, ok := pageOwner[uint32(pageIdx)]
		if !ok {
			continue
		}
		t := tables[ti]
		name := tableTypeName(t.typ)
		if *tableFlag != "" && *tableFlag != name {
			continue
		}
		base := pageIdx * pageSize
		page := data[base : base+pageSize]
		isSentinel := page[0x1B]&0x40 != 0

		fmt.Printf("\n========== page %d  (%s%s) ==========\n", pageIdx, name, sentinelTag(isSentinel))
		if !*rowsOnly {
			dumpPageHeader(page)
		}
		if isSentinel {
			if !*rowsOnly {
				dumpSentinelBody(page)
			}
			continue
		}
		dumpDataPage(page, t.typ)
	}
}

type tableDesc struct {
	typ, first, last uint32
}

func sentinelTag(s bool) string {
	if s {
		return ", sentinel"
	}
	return ", data"
}

func dumpHeader(data []byte) {
	fmt.Printf("========== FILE HEADER ==========\n")
	fmt.Printf("  0x00 reserved          = 0x%08x\n", u32(data, 0x00))
	fmt.Printf("  0x04 len_page          = %d\n", u32(data, 0x04))
	fmt.Printf("  0x08 num_tables        = %d\n", u32(data, 0x08))
	fmt.Printf("  0x0C next_unused_page  = %d\n", u32(data, 0x0C))
	fmt.Printf("  0x10 unknown1          = 0x%08x\n", u32(data, 0x10))
	fmt.Printf("  0x14 seqdb             = %d\n", u32(data, 0x14))
	fmt.Printf("  0x18 unknown2          = 0x%08x\n", u32(data, 0x18))
	n := u32(data, 0x08)
	for i := uint32(0); i < n; i++ {
		off := 0x1C + int(i)*16
		fmt.Printf("    table %2d: type=%d empty_candidate=%d first=%d last=%d\n",
			i, u32(data, off), u32(data, off+4), u32(data, off+8), u32(data, off+12))
	}
}

func dumpPageHeader(p []byte) {
	flags := p[0x1B]
	fmt.Printf("  0x00 reserved          = 0x%08x\n", u32(p, 0x00))
	fmt.Printf("  0x04 page_index        = %d\n", u32(p, 0x04))
	fmt.Printf("  0x08 page_type         = %d\n", u32(p, 0x08))
	fmt.Printf("  0x0C next_page         = %d\n", u32(p, 0x0C))
	fmt.Printf("  0x10 seqpage           = %d\n", u32(p, 0x10))
	fmt.Printf("  0x14 unknown           = 0x%08x\n", u32(p, 0x14))
	// 24-bit packed: bits 0-12 = num_row_offsets, bits 13-23 = num_rows.
	packed := uint32(p[0x18]) | uint32(p[0x19])<<8 | uint32(p[0x1A])<<16
	fmt.Printf("  0x18 packed row counts = 0x%06x  (num_row_offsets=%d, num_rows=%d)\n",
		packed, packed&0x1FFF, (packed>>13)&0x07FF)
	fmt.Printf("  0x1B page_flags        = 0x%02x  (%s)\n", flags, flagsStr(flags))
	fmt.Printf("  0x1C free_size         = %d\n", u16(p, 0x1C))
	fmt.Printf("  0x1E heap_used         = %d\n", u16(p, 0x1E))
	fmt.Printf("  0x20 tx_row_count      = 0x%04x\n", u16(p, 0x20))
	fmt.Printf("  0x22 tx_row_index      = 0x%04x\n", u16(p, 0x22))
	fmt.Printf("  0x24 reserved          = 0x%04x\n", u16(p, 0x24))
	fmt.Printf("  0x26 reserved          = 0x%04x\n", u16(p, 0x26))
}

func dumpSentinelBody(p []byte) {
	fmt.Printf("  --- sentinel boilerplate ---\n")
	fmt.Printf("  0x28 page_idx (self)   = %d\n", u32(p, 0x28))
	fdp := u32(p, 0x2C)
	if fdp == 0x03ffffff {
		fmt.Printf("  0x2C first_data_page   = 0x03ffffff (none)\n")
	} else {
		fmt.Printf("  0x2C first_data_page   = %d\n", fdp)
	}
	fmt.Printf("  0x30 magic2 (always)   = 0x%08x\n", u32(p, 0x30))
	fmt.Printf("  0x34 reserved          = 0x%08x\n", u32(p, 0x34))
	fmt.Printf("  0x38 reserved          = 0x%08x\n", u32(p, 0x38))
	// Spot-check the fill region for the 0x1FFFFFF8 free-slot marker.
	fillStart := 0x3C
	if u32(p, fillStart) == 0x1FFFFFF8 {
		fmt.Printf("  0x3C..%04x        free-slot fill: 0x1FFFFFF8 (verified)\n", pageSize-20)
	} else {
		fmt.Printf("  0x3C                   = 0x%08x (UNEXPECTED — real has 0x1FFFFFF8 here)\n", u32(p, fillStart))
	}
	// Trailing zeros.
	tail := p[pageSize-20:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		fmt.Printf("  page tail (20 B)       = all zero (verified)\n")
	} else {
		fmt.Printf("  page tail (20 B)       = NON-ZERO: % x\n", tail)
	}
}

func dumpDataPage(p []byte, tableType uint32) {
	packed := uint32(p[0x18]) | uint32(p[0x19])<<8 | uint32(p[0x1A])<<16
	numRows := int((packed >> 13) & 0x07FF)
	numOff := int(packed & 0x1FFF)
	if numRows == 0 && numOff == 0 {
		// Fall back on num_rows_small.
		numRows = int(p[0x18])
		numOff = numRows
	}

	// Read row index from end of page. Row groups are 0x24 bytes each.
	// Within a group, row N offset is at base - 6 - 2*N where base =
	// pageSize - groupIdx*0x24.
	rowOffsets := make([]uint16, 0, numOff)
	for r := 0; r < numOff; r++ {
		groupIdx := r / 16
		within := r % 16
		base := pageSize - groupIdx*rowGroupSize
		pos := base - 6 - 2*within
		if pos < 0 || pos+1 >= pageSize {
			break
		}
		rowOffsets = append(rowOffsets, binary.LittleEndian.Uint16(p[pos:]))
	}

	for i, ro := range rowOffsets {
		off := pageHeaderSize + int(ro)
		if off >= pageSize {
			fmt.Printf("  row %d: offset %d out of range\n", i, ro)
			continue
		}
		fmt.Printf("  --- row %d (heap off 0x%04x → file pos 0x%04x in page) ---\n", i, ro, off)
		if tableType == 0x00 {
			dumpTrackRow(p[off:])
		} else {
			// For non-track tables, hex-dump the first 64 bytes.
			end := off + 64
			if end > pageSize {
				end = pageSize
			}
			dumpHexBlock(p[off:end], "    ")
		}
	}
}

// dumpTrackRow decodes the fixed portion of a track row (0x88 bytes)
// using best-known field names from the kaitai spec plus inline
// observations from real exports. Strings are followed via the
// 21-slot offset table at 0x5E.
func dumpTrackRow(row []byte) {
	if len(row) < 0x88 {
		fmt.Printf("    SHORT ROW (%d bytes)\n", len(row))
		return
	}
	fmt.Printf("    0x00 magic              = 0x%04x  (expect 0x0024)\n", u16(row, 0x00))
	fmt.Printf("    0x02 index_shift        = 0x%04x\n", u16(row, 0x02))
	fmt.Printf("    0x04 bitmask            = 0x%08x\n", u32(row, 0x04))
	fmt.Printf("    0x08 sample_rate        = %d\n", u32(row, 0x08))
	fmt.Printf("    0x0C composer_id        = %d\n", u32(row, 0x0C))
	fmt.Printf("    0x10 file_size          = %d\n", u32(row, 0x10))
	fmt.Printf("    0x14 unknown_id_1       = 0x%08x  (per-row, looks like CRC/hash)\n", u32(row, 0x14))
	fmt.Printf("    0x18 unknown_id_2       = 0x%08x  (per-row)\n", u32(row, 0x18))
	fmt.Printf("    0x1C artwork_id         = %d\n", u32(row, 0x1C))
	fmt.Printf("    0x20 key_id             = %d\n", u32(row, 0x20))
	fmt.Printf("    0x24 original_artist_id = %d\n", u32(row, 0x24))
	fmt.Printf("    0x28 label_id           = %d\n", u32(row, 0x28))
	fmt.Printf("    0x2C remixer_id         = %d\n", u32(row, 0x2C))
	fmt.Printf("    0x30 bitrate            = %d\n", u32(row, 0x30))
	fmt.Printf("    0x34 track_num          = %d\n", u32(row, 0x34))
	fmt.Printf("    0x38 tempo (×100)       = %d  (= %.2f BPM)\n", u32(row, 0x38), float64(u32(row, 0x38))/100)
	fmt.Printf("    0x3C genre_id           = %d\n", u32(row, 0x3C))
	fmt.Printf("    0x40 album_id           = %d\n", u32(row, 0x40))
	fmt.Printf("    0x44 artist_id          = %d\n", u32(row, 0x44))
	fmt.Printf("    0x48 track_id           = %d\n", u32(row, 0x48))
	fmt.Printf("    0x4C disc_number        = %d\n", u16(row, 0x4C))
	fmt.Printf("    0x4E play_count         = %d\n", u16(row, 0x4E))
	fmt.Printf("    0x50 year               = %d\n", u16(row, 0x50))
	fmt.Printf("    0x52 sample_depth       = %d\n", u16(row, 0x52))
	fmt.Printf("    0x54 duration (sec)     = %d\n", u16(row, 0x54))
	fmt.Printf("    0x56 unknown_u16_1      = %d\n", u16(row, 0x56))
	fmt.Printf("    0x58 color_id           = %d\n", row[0x58])
	fmt.Printf("    0x59 rating             = %d\n", row[0x59])
	fmt.Printf("    0x5A file_type          = 0x%04x  (%s)\n", u16(row, 0x5A), fileTypeName(u16(row, 0x5A)))
	fmt.Printf("    0x5C unknown_u16_3      = 0x%04x\n", u16(row, 0x5C))
	fmt.Printf("    --- string offset table (21 × u16 at 0x5E) ---\n")
	stringNames := []string{
		"00 ofs_isrc",
		"01 ofs_texter",
		"02 ofs_subtitle",
		"03 ofs_mix_name",
		"04 ofs_dj_play_count_str",
		"05 ofs_unknown_string_2",
		"06 ofs_unknown_string_3",
		"07 ofs_unknown_string_4",
		"08 ofs_message",
		"09 ofs_kuvo_public_flag",
		"10 ofs_autoload_hotcues",
		"11 ofs_unknown_string_5",
		"12 ofs_unknown_string_6",
		"13 ofs_date_added",
		"14 ofs_release_date",
		"15 ofs_mix_name_2",
		"16 ofs_unknown_string_7",
		"17 ofs_analyze_path",
		"18 ofs_analyze_date",
		"19 ofs_comment",
		"20 ofs_title",
	}
	// NOTE: the 21 names above are tentative — kaitai/deepsymmetry order
	// changed several times. The dump still shows the actual content so
	// we can re-derive ordering from real exports.
	for i, name := range stringNames {
		ofs := u16(row, 0x5E+i*2)
		preview := "(out of row)"
		if int(ofs) < len(row) {
			preview = previewString(row[ofs:])
		}
		fmt.Printf("    [+%02d at 0x%04x → 0x%04x] %s = %s\n", i, 0x5E+i*2, ofs, name, preview)
	}
}

// previewString decodes a DeviceSQL string at the start of buf.
// Format: 1 byte length-and-kind, then payload.
//   - kind bit 0 = 1 → short ASCII, length_in_bytes = lk >> 1
//   - kind bit 0 = 0, kind & 0x40 = 0x40 → long string (length encoded
//     differently); for the dump tool we only fully decode short ASCII
//     and show raw bytes for the rest, which is enough to compare.
func previewString(buf []byte) string {
	if len(buf) == 0 {
		return "(empty)"
	}
	lk := buf[0]
	if lk == 0x03 {
		return "\"\" (empty marker)"
	}
	if lk&1 == 1 {
		total := int(lk >> 1)
		if total <= 0 || total > len(buf) {
			return fmt.Sprintf("(invalid lk=0x%02x)", lk)
		}
		// Total includes the length byte itself.
		strBytes := buf[1:total]
		return fmt.Sprintf("%q (%d B, ASCII)", string(strBytes), len(strBytes))
	}
	// Long string or UTF-16; just show raw bytes.
	end := 24
	if end > len(buf) {
		end = len(buf)
	}
	return fmt.Sprintf("<lk=0x%02x raw=% x ...>", lk, buf[1:end])
}

func dumpHexBlock(b []byte, indent string) {
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		parts := []string{}
		for _, by := range b[off:end] {
			parts = append(parts, fmt.Sprintf("%02x", by))
		}
		fmt.Printf("%s0x%04x  %s\n", indent, off, strings.Join(parts, " "))
	}
}

func flagsStr(f byte) string {
	var parts []string
	if f&0x40 != 0 {
		parts = append(parts, "sentinel")
	} else {
		parts = append(parts, "data")
	}
	if f&0x20 != 0 {
		parts = append(parts, "bit5")
	}
	if f&0x04 != 0 {
		parts = append(parts, "bit2")
	}
	return strings.Join(parts, "|")
}

func u16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off:]) }
func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

// fileTypeName decodes the FileType enum values per rekordcrate.
func fileTypeName(v uint16) string {
	switch v {
	case 0x0000:
		return "Unknown"
	case 0x0001:
		return "Mp3"
	case 0x0004:
		return "M4a"
	case 0x0005:
		return "Flac"
	case 0x000B:
		return "Wav"
	case 0x000C:
		return "Aiff"
	default:
		return fmt.Sprintf("Other(%#x)", v)
	}
}

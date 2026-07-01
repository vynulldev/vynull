// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	defaultPageSize = 4096
	pageHeaderSize  = 0x28 // 40 bytes
	rowGroupSize    = 0x24 // 36 bytes (16 offsets + flags + txref)
)

// MenuConfig describes which CDJ menu categories are visible (in display
// order) and which are hidden, for the PDB Menu table. Pass nil to use the
// hard-coded defaults (matches rekordbox 6.6.4 export).
type MenuConfig struct {
	Visible []uint16 // PDB category IDs in display order
	Hidden  []uint16 // PDB category IDs to mark as Hidden
}

// MergeBase carries state from an existing PDB so a new export can extend
// it instead of replacing it. Build via LoadForMerge(). Writer pre-seeds
// its artist/album/genre/key/label ID maps from this, so existing tracks
// keep their assigned IDs across re-exports and the CDJ's library cache
// stays stable.
type MergeBase struct {
	Artists  map[string]uint32
	Albums   map[string]uint32
	Genres   map[string]uint32
	Keys     map[string]uint32
	Labels   map[string]uint32
	MaxTrackID uint32 // highest track ID seen in existing PDB
}

// LoadForMerge opens an existing PDB and returns the merge base data the
// writer needs. Returns nil, nil if the file doesn't exist (fresh export).
func LoadForMerge(pdbPath string) (*MergeBase, error) {
	if _, err := os.Stat(pdbPath); os.IsNotExist(err) {
		return nil, nil
	}
	db, err := Open(pdbPath)
	if err != nil {
		return nil, fmt.Errorf("open existing pdb for merge: %w", err)
	}
	m := &MergeBase{
		Artists: make(map[string]uint32, len(db.Artists)),
		Albums:  make(map[string]uint32, len(db.Albums)),
		Genres:  make(map[string]uint32, len(db.Genres)),
		Keys:    make(map[string]uint32, len(db.Keys)),
		Labels:  make(map[string]uint32, len(db.Labels)),
	}
	// Invert id→name to name→id (drop empty / duplicate names — last wins).
	for id, n := range db.Artists { if n != "" { m.Artists[n] = id } }
	for id, n := range db.Albums  { if n != "" { m.Albums[n]  = id } }
	for id, n := range db.Genres  { if n != "" { m.Genres[n]  = id } }
	for id, n := range db.Keys    { if n != "" { m.Keys[n]    = id } }
	for id, n := range db.Labels  { if n != "" { m.Labels[n]  = id } }
	for _, t := range db.Tracks {
		if t.ID > m.MaxTrackID {
			m.MaxTrackID = t.ID
		}
	}
	return m, nil
}

// GenerateWithOptions is the most-flexible PDB writer entry point.
// When merge != nil, artist/album/genre/key/label IDs are pre-seeded from
// the existing PDB so re-exports keep stable foreign-key IDs (helpful for
// the CDJ's library cache; required for merging in tracks from a previous
// export).
func GenerateWithOptions(tracks []*Track, playlists []*FolderNode, menu *MenuConfig, merge *MergeBase, outDir string) error {
	// Empty exports are allowed — useful for diagnostics (compare our
	// "empty library" output bit-by-bit against rekordcrate's
	// known-good empty fixture) and also because the CDJ accepts a
	// zero-track database (it just shows an empty library).

	// Build lookup tables: artists, albums, genres, keys, labels.
	// Pre-seed from existing PDB if merging so IDs stay stable.
	artists := newIDMapSeeded(seed(merge, func(m *MergeBase) map[string]uint32 { return m.Artists }))
	albums := newIDMapSeeded(seed(merge, func(m *MergeBase) map[string]uint32 { return m.Albums }))
	genres := newIDMapSeeded(seed(merge, func(m *MergeBase) map[string]uint32 { return m.Genres }))
	keys := newIDMapSeeded(seed(merge, func(m *MergeBase) map[string]uint32 { return m.Keys }))
	labels := newIDMapSeeded(seed(merge, func(m *MergeBase) map[string]uint32 { return m.Labels }))

	for _, t := range tracks {
		if t.Artist != "" {
			artists.getOrCreate(t.Artist)
		}
		if t.Album != "" {
			albums.getOrCreate(t.Album)
		}
		if t.Genre != "" {
			genres.getOrCreate(t.Genre)
		}
		if t.Key != "" {
			keys.getOrCreate(t.Key)
		}
		if t.Label != "" {
			labels.getOrCreate(t.Label)
		}
	}

	// Build table data.
	trackTable := newTableBuilder(TableTracks)
	genreTable := newTableBuilder(TableGenres)
	artistTable := newTableBuilder(TableArtists)
	albumTable := newTableBuilder(TableAlbums)
	labelTable := newTableBuilder(TableLabels)
	keyTable := newTableBuilder(TableKeys)
	artworkTable := newTableBuilder(TableArtwork)

	for name, id := range artists.ids {
		artistTable.addRow(encodeArtistRow(id, name))
	}
	for name, id := range genres.ids {
		genreTable.addRow(encodeGenreRow(id, name))
	}
	for name, id := range keys.ids {
		keyTable.addRow(encodeKeyRow(id, name))
	}
	for name, id := range labels.ids {
		labelTable.addRow(encodeLabelRow(id, name))
	}
	for name, id := range albums.ids {
		// Album rows in real exports never carry an artist_id; the
		// track→artist relationship is the source of truth. Real album
		// rows have zero at every "unknown" u32 field except album_id.
		albumTable.addRow(encodeAlbumRow(id, name))
	}
	for _, t := range tracks {
		trackTable.addRow(encodeTrackRow(t,
			artists.ids[t.Artist],
			albums.ids[t.Album],
			genres.ids[t.Genre],
			keys.ids[t.Key],
			labels.ids[t.Label],
		))
	}

	// Artwork: one row per distinct ArtworkID > 0 pointing at the JPEG
	// path the CDJ will resolve via NFS. The actual JPEG bytes are
	// written by pdb.WriteArtworkFiles (called separately by the
	// export pipeline) so the writer here only needs the path strings.
	seenArtwork := make(map[uint32]bool)
	for _, t := range tracks {
		if t.ArtworkID == 0 || seenArtwork[t.ArtworkID] {
			continue
		}
		seenArtwork[t.ArtworkID] = true
		artworkTable.addRow(encodeArtworkRow(t.ArtworkID, ArtworkPath(t.ArtworkID)))
	}

	// Build folder/playlist tables. When the caller supplied an explicit
	// node list, encode that directly. Otherwise fall back to the
	// filesystem-derived tree (the original --generate behaviour).
	var playlistTreeTable, playlistEntryTable *tableBuilder
	if playlists != nil {
		playlistTreeTable, playlistEntryTable = WritePlaylistTablesFromNodes(playlists)
	} else {
		playlistTreeTable, playlistEntryTable = WriteFolderTables(tracks, outDir)
	}

	// Create output directory structure.
	pdbDir := filepath.Join(outDir, "PIONEER", "rekordbox")
	if err := os.MkdirAll(pdbDir, 0o755); err != nil {
		return fmt.Errorf("create PIONEER dir: %w", err)
	}

	// The CDJ expects 20 tables (0x00..0x13) in ascending type order.
	// Missing or out-of-order types make the CDJ reject the USB.
	//
	// Four tables MUST contain rows even on an "empty" export —
	// Colors (8 standard colors), Columns (browse-menu categories),
	// Menu (CDJ menu state), and History (export metadata). Without
	// them the CDJ freezes on USB insert because it has no
	// menu structure to render. Defaults come from defaults.go,
	// extracted from rekordcrate's known-good empty-export fixture.
	colors := newTableBuilder(TableColors)
	for _, row := range colorsRows {
		colors.addRow(row)
	}
	columns := newTableBuilder(TableColumns)
	for _, row := range columnsRows {
		columns.addRow(row)
	}
	menuTable := newTableBuilder(TableMenu)
	rows := menuRows
	if menu != nil {
		rows = BuildMenuRows(menu.Visible, menu.Hidden)
	}
	for _, row := range rows {
		menuTable.addRow(row)
	}
	history := newTableBuilder(TableHistory)
	history.addRow(makeHistoryRow(len(tracks), time.Now().Format("2006-01-02")))
	unknown12 := newTableBuilder(TableUnknown12)
	for _, row := range type12Rows {
		unknown12.addRow(row)
	}

	tables := []*tableBuilder{
		trackTable,                              // 0x00 tracks
		genreTable,                              // 0x01 genres
		artistTable,                             // 0x02 artists
		albumTable,                              // 0x03 albums
		labelTable,                              // 0x04 labels
		keyTable,                                // 0x05 keys
		colors,                                  // 0x06 colors (populated)
		playlistTreeTable,                       // 0x07 playlist tree
		playlistEntryTable,                      // 0x08 playlist entries
		newTableBuilder(TableUnknown9),          // 0x09 (empty)
		newTableBuilder(TableUnknownA),          // 0x0a (empty)
		newTableBuilder(TableHistoryPlaylists),  // 0x0b (empty)
		newTableBuilder(TableHistoryEntries),    // 0x0c (empty)
		artworkTable,                            // 0x0d artwork
		newTableBuilder(TableUnknownE),          // 0x0e (empty)
		newTableBuilder(TableUnknownF),          // 0x0f (empty)
		columns,                                 // 0x10 columns (populated)
		menuTable,                               // 0x11 menu (populated)
		unknown12,                               // 0x12 (populated — purpose unknown)
		history,                                 // 0x13 history (populated)
	}

	pdbPath := filepath.Join(pdbDir, "export.pdb")
	return writeFile(pdbPath, tables, defaultPageSize, false /*isExt*/)
}

// writeFile serializes all tables into a PDB file.
//
// isExt distinguishes exportExt.pdb from export.pdb. Table-type
// numbering overlaps between the two files (both start at 0), but
// the *meaning* of those types differs — table 0 in export.pdb is
// the tracks table (with special sentinel markers), while table 0
// in exportExt.pdb is something else entirely. isExt suppresses the
// tracks-specific behavior so we don't apply phantom markers to
// exportExt sentinel pages.
func writeFile(path string, tables []*tableBuilder, pageSize int, isExt bool) error {
	// Real exports always pair every table's sentinel with at least
	// one data page (real for populated tables, zero-filled for empty
	// ones). Without that pairing the CDJ freezes on USB load.
	for _, t := range tables {
		t.finalizeEmpty()
	}

	// Assign page indices. Page 0 = header. rekordbox lays pages out
	// in two contiguous regions:
	//
	//   indices 1..N:    "real" pages (sentinel + data) for each table,
	//                    interleaved by table.
	//   indices N+1..M:  the per-table empty placeholder pages, all at
	//                    the end of the file.
	//
	// We previously interleaved placeholders right after each table's
	// data, which produced a valid PDB that the deck could browse but
	// (empirically) prevented analysis loading. Matching real's layout
	// resolves that.
	pageIdx := uint32(1)
	for _, t := range tables {
		for _, p := range t.pages {
			if p.flags == 0 {
				continue // placeholder — index assigned in second pass
			}
			p.pageIndex = pageIdx
			pageIdx++
		}
	}
	for _, t := range tables {
		for _, p := range t.pages {
			if p.flags != 0 {
				continue
			}
			p.pageIndex = pageIdx
			pageIdx++
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// File header (one page). Kaitai spec field semantics:
	//   0x00 always 0
	//   0x04 len_page
	//   0x08 num_tables
	//   0x0C next_unused_page (past end of file)
	//   0x10 unnamed (often 0; possibly extension of seqdb)
	//   0x14 seqdb — sequence number, incremented on every edit. Use
	//        the next_unused_page value as the "edit count" so it's a
	//        plausible number rather than 1 (real exports have values
	//        in the dozens, matching their page count).
	//   0x18 unnamed (often 0)
	header := make([]byte, pageSize)
	le32put(header, 0x00, 0)
	le32put(header, 0x04, uint32(pageSize))
	le32put(header, 0x08, uint32(len(tables)))
	le32put(header, 0x0C, pageIdx) // next_unused_page
	le32put(header, 0x10, 5)       // observed value in real exports — meaning unknown
	le32put(header, 0x14, pageIdx) // seqdb (use page count as a reasonable proxy)
	le32put(header, 0x18, 0)

	// Per-table descriptors: type, empty_candidate, first_page, last_page.
	//
	// Real exports allocate a zero placeholder page per table (sized
	// in the file) but DO NOT include it in the table's page chain —
	// last_page points to the last "real" page (sentinel for empty
	// tables, last data page for populated ones), and empty_candidate
	// points to the placeholder. We mirror that: our finalizeEmpty
	// appends a flags==0 placeholder; here we treat it as the
	// empty_candidate, not part of first..last.
	for i, t := range tables {
		off := 0x1C + i*16
		realPages := t.pages
		var emptyCandidate uint32 = pageIdx
		if last := realPages[len(realPages)-1]; last.flags == 0 {
			emptyCandidate = last.pageIndex
			realPages = realPages[:len(realPages)-1]
		}
		le32put(header, off+0, uint32(t.tableType))
		le32put(header, off+4, emptyCandidate)
		le32put(header, off+8, realPages[0].pageIndex)
		le32put(header, off+12, realPages[len(realPages)-1].pageIndex)
	}

	if _, err := f.Write(header); err != nil {
		return err
	}

	// Write all "real" pages (sentinel + data) for each table, in
	// table order. Placeholder pages are written afterward.
	for _, t := range tables {
		var firstDataPage uint32 = 0x03ffffff
		if len(t.pages) > 1 && t.pages[1].flags != 0 {
			firstDataPage = t.pages[1].pageIndex
		}

		// Tracks-table sentinel page (export.pdb only) carries an
		// extra summary: total row count across all data pages of
		// this table.
		var totalRows uint32
		isTracksTable := !isExt && t.tableType == TableTracks
		if isTracksTable {
			for _, p := range t.pages[1:] {
				totalRows += uint32(len(p.rows))
			}
		}

		var placeholderIdx uint32
		realCount := len(t.pages)
		if last := t.pages[len(t.pages)-1]; last.flags == 0 {
			placeholderIdx = last.pageIndex
			realCount--
		}

		for i := 0; i < realCount; i++ {
			p := t.pages[i]
			var nextPage uint32
			switch {
			case i+1 < realCount:
				nextPage = t.pages[i+1].pageIndex
			case placeholderIdx != 0:
				nextPage = placeholderIdx
			default:
				nextPage = pageIdx
			}
			data := p.serialize(pageSize, nextPage, firstDataPage, totalRows, isTracksTable)
			if _, err := f.Write(data); err != nil {
				return err
			}
		}
	}

	// Second pass: write all placeholder pages contiguously at the end.
	// next_page stays 0 (placeholders don't chain anywhere).
	for _, t := range tables {
		for _, p := range t.pages {
			if p.flags != 0 {
				continue
			}
			data := p.serialize(pageSize, 0, 0x03ffffff, 0, false)
			if _, err := f.Write(data); err != nil {
				return err
			}
		}
	}

	return nil
}

// tableBuilder accumulates rows and paginates them.
type tableBuilder struct {
	tableType int
	pages     []*pageBuilder
	pageSize  int
}

func newTableBuilder(tableType int) *tableBuilder {
	tb := &tableBuilder{
		tableType: tableType,
		pageSize:  defaultPageSize,
	}
	// First page is always an empty sentinel (flags 0x64).
	sentinel := &pageBuilder{
		pageType: uint32(tableType),
		flags:    0x64,
	}
	tb.pages = append(tb.pages, sentinel)
	return tb
}

// finalizeEmpty appends an all-zero "empty placeholder" page to EVERY
// table. rekordbox exports allocate sentinel + zero or more data
// pages + ONE trailing empty placeholder per table; that placeholder
// is used as the table's `empty_candidate` (where the next inserted
// row would land). Without it, our `empty_candidate` falls back to a
// shared sentinel page across tables — empirically the CDJ
// still browses tracks fine in that state but refuses to load ANLZ
// analysis (no waveform, no beat grid, no real-time BPM), as if the
// row layout is rejected once it actually has to consult per-row
// metadata.
//
// pageBuilders use flags==0 to indicate "emit as all zeros" (a normal
// data page uses flags 0x24/0x34, a sentinel uses 0x64). serialize()
// short-circuits to a zero buffer for the flags==0 case.
func (tb *tableBuilder) finalizeEmpty() {
	tb.pages = append(tb.pages, &pageBuilder{
		pageType: uint32(tb.tableType),
		flags:    0,
	})
}

func (tb *tableBuilder) addRow(rowData []byte) {
	// Pad EVERY row to a multiple of 4 bytes. CDJ freezes on
	// USB insert when any row in a multi-row data page has a length
	// not divisible by 4. See padTo4 for the bisection evidence.
	rowData = padTo4(rowData)

	// Find or create a data page with space.
	var current *pageBuilder
	if len(tb.pages) > 1 {
		current = tb.pages[len(tb.pages)-1]
	}

	if current == nil || !current.canFit(rowData, tb.pageSize) {
		current = &pageBuilder{
			pageType: uint32(tb.tableType),
			// 0x24 (bit 2 + bit 5) is the canonical data-page flag in
			// real exports. The 0x34 variant (extra bit 4) was observed
			// only on a single page whose num_row_offsets exceeded its
			// num_rows — likely "this page has experienced row
			// deletions". Fresh exports never need that, so use 0x24.
			flags: 0x24,
		}
		tb.pages = append(tb.pages, current)
	}

	current.appendRow(rowData)
}

// pageBuilder holds rows for a single page.
type pageBuilder struct {
	pageType  uint32
	pageIndex uint32
	flags     byte
	rows      [][]byte
	heapUsed  int
}

func (pb *pageBuilder) numGroups() int {
	if len(pb.rows) == 0 {
		return 0
	}
	return (len(pb.rows)-1)/16 + 1
}

// indexSizeFor returns the row index size in bytes for a given row
// count, using REAL exports' variable-size groups (each group is
// rowsInGroup*2 + 4 bytes, NOT the fixed rowGroupSize=36). For a
// page with 8 rows that's 20 bytes (not 36); for 22 rows it's
// 36 (full group 0) + 16 (partial group 1) = 52 bytes.
//
// This was a significant correctness bug: with fixed 36-byte groups
// our row index extended further into the page than real's, so the
// CDJ would read 16 bytes of "extra index slots" past the actual
// row offsets — most likely the cause of the on-insert freeze.
func indexSizeFor(numRows int) int {
	if numRows == 0 {
		return 0
	}
	total := 0
	remaining := numRows
	for remaining > 0 {
		rowsInGroup := remaining
		if rowsInGroup > 16 {
			rowsInGroup = 16
		}
		total += rowsInGroup*2 + 4
		remaining -= rowsInGroup
	}
	return total
}

func (pb *pageBuilder) indexSize() int {
	return indexSizeFor(len(pb.rows))
}

func (pb *pageBuilder) canFit(rowData []byte, pageSize int) bool {
	newNumRows := len(pb.rows) + 1
	indexCost := indexSizeFor(newNumRows)
	heapCost := pb.heapUsed + len(rowData)
	return pageHeaderSize+heapCost+indexCost <= pageSize
}

func (pb *pageBuilder) appendRow(rowData []byte) {
	pb.rows = append(pb.rows, rowData)
	pb.heapUsed += len(rowData)
}

// noPageMarker is the "no such page" value used in sentinel page
// boilerplate slots when no data page exists. Observed on every
// empty-table sentinel in real exports.
const noPageMarker uint32 = 0x03ffffff

// freeSlotMarker fills the unused middle of sentinel pages on real
// exports. The exact semantics aren't fully reverse-engineered but
// the pattern is uniform across every sentinel we've seen.
const freeSlotMarker uint32 = 0x1FFFFFF8

// sentinelTrailerZeros is the count of trailing zero bytes at the end
// of a sentinel page (observed: last 20 bytes are 0 instead of the
// freeSlotMarker fill).
const sentinelTrailerZeros = 20

func (pb *pageBuilder) serialize(pageSize int, nextPage, firstDataPage, tracksTotalRows uint32, isTracksTable bool) []byte {
	buf := make([]byte, pageSize)

	// flags==0 is our "all-zero placeholder data page" marker (created
	// by tableBuilder.finalizeEmpty for tables with no rows). Real
	// rekordbox writes such pages as a literal 4096-byte zero block —
	// no page header, no nothing — so we just return the zero buffer.
	if pb.flags == 0 {
		return buf
	}

	isSentinel := pb.flags&0x40 != 0
	if isSentinel {
		writeSentinelPage(buf, pageSize, pb.pageIndex, pb.pageType, pb.flags, nextPage, firstDataPage, tracksTotalRows, isTracksTable)
		return buf
	}

	numRows := len(pb.rows)

	// Header.
	le32put(buf, 0x00, 0)
	le32put(buf, 0x04, pb.pageIndex)
	le32put(buf, 0x08, pb.pageType)
	le32put(buf, 0x0C, nextPage)
	// seqpage — per-page edit sequence number. Real exports have unique
	// non-zero values per page. Writing 0 for every page may make the
	// deck treat the pages as uninitialised / collide in its cache,
	// which empirically blocks the deck from loading ANLZ analysis.
	// Using page_index gives every page a distinct, deterministic value.
	le32put(buf, 0x10, pb.pageIndex)
	le32put(buf, 0x14, 0)
	// Row counts at 0x18-0x1A are a packed 24-bit little-endian field:
	//   bits  0-12 (13 bits): num_row_offsets
	//   bits 13-23 (11 bits): num_rows
	// For a fresh export with no deletions these are equal.
	packed := uint32(numRows&0x1FFF) | uint32(numRows&0x07FF)<<13
	buf[0x18] = byte(packed)
	buf[0x19] = byte(packed >> 8)
	buf[0x1A] = byte(packed >> 16)
	buf[0x1B] = pb.flags
	freeSize := pageSize - pageHeaderSize - pb.heapUsed - pb.indexSize()
	if freeSize < 0 {
		freeSize = 0
	}
	le16put(buf, 0x1C, uint16(freeSize))
	le16put(buf, 0x1E, uint16(pb.heapUsed))
	// transaction_row_count and transaction_row_index follow the
	// SAME bulk-vs-user-data split as row_present_flags/trf:
	//
	//   * User-data tables (tracks/genres/artists/etc.): rows added
	//     one at a time. tx_count=1, tx_index=num_rows-1 (last row).
	//   * Bulk-loaded default tables (colors/columns/menu/type12/
	//     history): all rows added at once. tx_count=num_rows,
	//     tx_index=0.
	//
	// Verified against rekordcrate's demo_tracks fixture and real
	// user exports — both follow this split exactly.
	bulkLoadedTx := pb.pageType == TableColors || pb.pageType == TableColumns ||
		pb.pageType == TableMenu || pb.pageType == TableUnknown12 ||
		pb.pageType == TableHistory
	if bulkLoadedTx {
		le16put(buf, 0x20, uint16(numRows))
		le16put(buf, 0x22, 0)
	} else {
		le16put(buf, 0x20, 1)
		if numRows > 0 {
			le16put(buf, 0x22, uint16(numRows-1))
		} else {
			le16put(buf, 0x22, 0)
		}
	}
	le16put(buf, 0x24, 0)
	le16put(buf, 0x26, 0)

	// Write rows into heap (offset 0x28).
	//
	// Rows with a u16 subtype at offset 0 carry an `index_shift` field
	// at offset 0x02 — the row's zero-based index in the page scaled
	// by 0x20. That index isn't known when the row is encoded (the
	// encoder doesn't see siblings), so we patch it here. Subtypes
	// with this layout observed in real exports:
	//   0x0024 (tracks), 0x0060 (artists), 0x0080 (albums).
	// Genre/label/key rows have no subtype prefix and skip patching.
	heapOff := 0
	rowOffsets := make([]uint16, numRows)
	for i, row := range pb.rows {
		rowOffsets[i] = uint16(heapOff)
		copy(buf[pageHeaderSize+heapOff:], row)
		if len(row) >= 4 && row[1] == 0x00 {
			switch row[0] {
			case 0x24, 0x60, 0x80:
				le16put(buf, pageHeaderSize+heapOff+0x02, uint16(i*0x20))
			}
		}
		heapOff += len(row)
	}

	// Write row index groups from page end backwards. Each group is
	// VARIABLE size: rowsInGroup*2 + 4 bytes (NOT a fixed 36-byte
	// slot). Real exports use this packing; we previously used fixed
	// 36-byte groups which left phantom row offset slots that the CDJ
	// may have read as garbage on insert.
	//
	// row_present_flags and transaction_row_flags have two distinct
	// conventions in real exports:
	//
	//   * User-data tables (tracks/genres/artists/albums/labels/keys/
	//     playlist_tree/playlist_entries/artwork): trf = bit for the
	//     LAST row in this group ONLY (rows are added one-at-a-time,
	//     so the most-recent transaction touched a single row).
	//   * Bulk-loaded default tables (colors/columns/menu/type12/
	//     history): trf = rpf (all rows touched in one bulk insert).
	//
	// Using rpf for trf on user-data tables caused the CDJ to freeze
	// on insert for any tracks page with >1 row — multi-bit trf
	// claims multiple simultaneous transactions, which is invalid.
	bulkLoaded := pb.pageType == TableColors || pb.pageType == TableColumns ||
		pb.pageType == TableMenu || pb.pageType == TableUnknown12 ||
		pb.pageType == TableHistory

	numGroups := pb.numGroups()
	groupTop := pageSize // top byte (exclusive) of the next group to write
	for g := 0; g < numGroups; g++ {
		rowsInGroup := min(16, numRows-g*16)
		groupSize := rowsInGroup*2 + 4

		trfPos := groupTop - 2
		rpfPos := groupTop - 4

		var rpf uint16
		for r := 0; r < rowsInGroup; r++ {
			rpf |= 1 << uint(r)
		}
		var trf uint16
		if bulkLoaded {
			trf = rpf
		} else if rowsInGroup > 0 {
			trf = 1 << uint(rowsInGroup-1)
		}
		le16put(buf, rpfPos, rpf)
		le16put(buf, trfPos, trf)

		for r := 0; r < rowsInGroup; r++ {
			pos := groupTop - 6 - 2*r
			globalIdx := g*16 + r
			if globalIdx < numRows {
				le16put(buf, pos, rowOffsets[globalIdx])
			}
		}

		groupTop -= groupSize
	}

	return buf
}

// writeSentinelPage fills buf as a table's sentinel (index) page,
// matching the byte pattern observed on rekordbox exports.
//
// The page is essentially an empty index: report num_rows=0,
// free_size=0, heap_used=0; the magic 0x1fff transaction markers and
// 0x03ec reserved field; then a small boilerplate (5 u32 starting at
// 0x28) followed by 0x1FFFFFF8 free-slot fill until the last 20 zero
// bytes. Without this content the CDJ accepts the sentinel page but
// can't locate the data pages it leads to.
func writeSentinelPage(buf []byte, pageSize int, pageIndex, pageType uint32, flags byte, nextPage, firstDataPage, tracksTotalRows uint32, isTracksTable bool) {
	isHistoryTable := pageType == TableHistory
	// Sentinel page layout. Earlier I tried adding tracks-specific
	// "has data" markers (0x26=0x0001, 0x38=0x0001, 0x3C=totalRows)
	// when isTracksTable and tracksTotalRows > 0 — that matched
	// rekordcrate's demo fixture. But the user's own fresh real
	// export does NOT use those markers (0x26=0x0000, 0x38=0x0000,
	// 0x3C starts the fill) and STILL LOADS on the CDJ. So both
	// shapes are valid; the simpler "always zero" form is safer and
	// matches the loading shape closest to ours.
	_ = isTracksTable
	_ = tracksTotalRows

	// Header.
	le32put(buf, 0x00, 0)
	le32put(buf, 0x04, pageIndex)
	le32put(buf, 0x08, pageType)
	le32put(buf, 0x0C, nextPage)
	// Real history sentinels have 0x10 here (edit/session counter?); all
	// other tables have 1. Without it the CDJ may treat the History
	// table as untouched / unauthored.
	if isHistoryTable {
		le32put(buf, 0x10, 0x10)
	} else {
		le32put(buf, 0x10, 1)
	}
	le32put(buf, 0x14, 0)
	buf[0x18] = 0
	buf[0x19] = 0
	buf[0x1A] = 0
	buf[0x1B] = flags
	le16put(buf, 0x1C, 0)
	le16put(buf, 0x1E, 0)
	le16put(buf, 0x20, 0x1fff)
	le16put(buf, 0x22, 0x1fff)
	le16put(buf, 0x24, 0x03ec)
	// next_offset (u16): real history has 1 entry, others have 0.
	if isHistoryTable {
		le16put(buf, 0x26, 1)
	} else {
		le16put(buf, 0x26, 0)
	}

	// Boilerplate: 5 u32 starting at 0x28.
	le32put(buf, 0x28, pageIndex)
	le32put(buf, 0x2C, firstDataPage)
	le32put(buf, 0x30, noPageMarker)
	le32put(buf, 0x34, 0)
	// num_entries (u16) at 0x38: 1 IndexEntry for History (pointing at its
	// data page), 0 for everything else.
	if isHistoryTable {
		le16put(buf, 0x38, 1)
	} else {
		le16put(buf, 0x38, 0)
	}
	le16put(buf, 0x3A, 0x1fff)

	fillStart := 0x3C
	// For History, write one IndexEntry at 0x3C: (page << 3) | flags. Real
	// uses (40 << 3) | 0 = 0x140 pointing at the data page.
	if isHistoryTable && firstDataPage != noPageMarker {
		le32put(buf, 0x3C, firstDataPage<<3)
		fillStart = 0x40
	}
	fillEnd := pageSize - sentinelTrailerZeros
	for off := fillStart; off+4 <= fillEnd; off += 4 {
		le32put(buf, off, freeSlotMarker)
	}
}

// Row encoders.

// encodeArtistRow creates an artist row.
//
// Format (verified against real exports via cmd/pdbdump):
//
//	u16  subtype = 0x0060
//	u16  index_shift
//	u32  id
//	u8   magic = 0x03
//	u8   ofs_name = 10  (relative to row start)
//	str  name
func encodeArtistRow(id uint32, name string) []byte {
	nameBytes := encodeString(name)
	row := make([]byte, 10+len(nameBytes))
	le16put(row, 0, 0x0060)
	le16put(row, 2, 0)
	le32put(row, 4, id)
	row[8] = 0x03
	row[9] = 10
	copy(row[10:], nameBytes)
	return row
}

// encodeGenreRow creates a genre row.
//
// Format (verified against real exports — distinct from artist!):
//
//	u32  id
//	str  name        (DeviceSQL-encoded, starts immediately after id)
//
// No subtype, no index_shift, no magic byte. Our earlier encoder
// reused encodeArtistRow here and emitted a 10-byte preamble the CDJ
// would misparse as a malformed genre row, which is one of the
// reasons the deck couldn't open the database.
func encodeGenreRow(id uint32, name string) []byte {
	nameBytes := encodeString(name)
	row := make([]byte, 4+len(nameBytes))
	le32put(row, 0, id)
	copy(row[4:], nameBytes)
	return row
}

// encodeLabelRow creates a label row.
//
// Same simple format as genre: u32 id, then DeviceSQL string.
func encodeLabelRow(id uint32, name string) []byte {
	nameBytes := encodeString(name)
	row := make([]byte, 4+len(nameBytes))
	le32put(row, 0, id)
	copy(row[4:], nameBytes)
	return row
}

// encodeKeyRow creates a key row.
//
// Format (verified against real exports):
//
//	u32  id
//	u32  id2 (matches id in real exports — purpose unknown)
//	str  name
//
// Real exports always set both u32s to the same value.
func encodeKeyRow(id uint32, name string) []byte {
	nameBytes := encodeString(name)
	row := make([]byte, 8+len(nameBytes))
	le32put(row, 0, id)
	le32put(row, 4, id)
	copy(row[8:], nameBytes)
	return row
}

// encodeAlbumRow creates an album row.
//
// Format (verified against real exports):
//
//	u16  subtype = 0x0080
//	u16  index_shift  (patched by pageBuilder.serialize)
//	u32  unknown1     (real: always 0)
//	u32  unknown2     (real: always 0 — NOT artist_id as our earlier
//	                   encoder assumed; track→artist is the only
//	                   artist link)
//	u32  album_id
//	u32  unknown3     (real: always 0)
//	u8   magic = 0x03
//	u8   ofs_name = 22
//	str  name
func encodeAlbumRow(id uint32, name string) []byte {
	nameBytes := encodeString(name)
	row := make([]byte, 22+len(nameBytes))
	le16put(row, 0, 0x0080)
	le16put(row, 2, 0) // index_shift placeholder
	le32put(row, 4, 0)
	le32put(row, 8, 0)
	le32put(row, 12, id)
	le32put(row, 16, 0)
	row[20] = 0x03
	row[21] = 22
	copy(row[22:], nameBytes)
	return row
}

// encodeTrackRow creates a track row.
func encodeTrackRow(t *Track, artistID, albumID, genreID, keyID, labelID uint32) []byte {
	// Prepare strings.
	strTitle := encodeString(t.Title)
	strFileName := encodeString(t.FileName)
	strFilePath := encodeString(t.FilePath)
	strComment := encodeString(t.Comment)
	strDateAdded := encodeString(t.DateAdded)
	strAnalyzePath := encodeString(t.AnalyzePath)
	strAutoloadHotcues := encodeString("ON")
	// unknown_string2 / unknown_string3 are always populated in real
	// exports (single-digit values like "2", "3", "6"). Per the kaitai
	// docs the purpose is unclear, but every working PDB sets them.
	// We use "3"/"3" which matches rekordcrate's demo_tracks fixture.
	strUnk2 := encodeString("3")
	strUnk3 := encodeString("3")
	emptyStr := encodeString("")

	// Fixed part: 0x5E bytes + 21*2 bytes string offset table = 0x88 bytes.
	fixedSize := 0x88

	// Compute string offsets relative to row start.
	// Strings are packed after the fixed part.
	strOff := fixedSize

	// 21 string slots. Most point to empty.
	offsets := make([]uint16, 21)
	var strData []byte

	// Helper to add a string and return its offset.
	addStr := func(s []byte) uint16 {
		off := uint16(strOff + len(strData))
		strData = append(strData, s...)
		return off
	}

	// Assign each of the 21 slots.
	for i := 0; i < 21; i++ {
		switch i {
		case 2: // unknown_string2 — purpose unknown but always populated in real
			offsets[i] = addStr(strUnk2)
		case 3: // unknown_string3 — purpose unknown but always populated in real
			offsets[i] = addStr(strUnk3)
		case 7: // autoload_hotcues — real exports populate this with "ON"
			offsets[i] = addStr(strAutoloadHotcues)
		case 10: // date_added
			offsets[i] = addStr(strDateAdded)
		case 14: // analyze_path
			offsets[i] = addStr(strAnalyzePath)
		case 15: // analyze_date — deck may use this as a "track is analyzed" sentinel
			offsets[i] = addStr(strDateAdded)
		case 16: // comment
			offsets[i] = addStr(strComment)
		case 17: // title
			offsets[i] = addStr(strTitle)
		case 19: // filename
			offsets[i] = addStr(strFileName)
		case 20: // file_path
			offsets[i] = addStr(strFilePath)
		default:
			offsets[i] = addStr(emptyStr)
		}
	}

	row := make([]byte, fixedSize+len(strData))

	// Fixed fields. Values verified against real exports via the
	// cmd/pdbdump tool.
	//
	// Per-row varying fields are listed inline. The two "unknown"
	// fields at 0x14 and 0x18 are not documented but real exports
	// follow a precise pattern:
	//   * 0x14 is unique per row and looks like a precomputed hash —
	//     possibly used as a key in a fast-lookup index on the CDJ. We
	//     use the track ID so every row gets a distinct value (a zero
	//     value here caused the CDJ to freeze, likely from an infinite
	//     loop on hash collisions).
	//   * 0x18 is a constant 0x3D0F7FC7 across every row of every real
	//     export we've inspected — meaning unknown, but copy it verbatim.
	//   * 0x04 bitmask is a constant 0x000C0700 (probably "field-valid"
	//     flags; the CDJ may skip fields when their bit is clear).
	//
	// 0x02 index_shift is also per-row (rowIdx_in_page * 0x20) but the
	// row's index within its page isn't known here — pageBuilder.serialize
	// patches it in just before writing the row into the heap.
	sampleRate := t.SampleRate
	if sampleRate == 0 {
		sampleRate = 44100
	}
	le16put(row, 0x00, 0x0024) // magic
	le16put(row, 0x02, 0)      // index_shift placeholder; patched by serialize()
	le32put(row, 0x04, 0x000C0700)
	le32put(row, 0x08, sampleRate)
	le32put(row, 0x0C, 0)            // composer_id
	le32put(row, 0x10, t.FileSize)
	// 0x14 is per-row unique. Real exports use 28-bit hash-looking
	// values uniformly distributed in the 0..2^28 range. A small int
	// like t.ID lands in low buckets and may collide in the CDJ's
	// internal index. Use a multiplicative hash (FNV-style mixing)
	// of the track ID to spread across the 28-bit space.
	le32put(row, 0x14, hash28(t.ID))
	le32put(row, 0x18, 0x3D0F7FC7)   // constant; see comment above
	le32put(row, 0x1C, t.ArtworkID)
	le32put(row, 0x20, keyID)
	le32put(row, 0x24, 0)       // original_artist_id
	le32put(row, 0x28, labelID) // label_id
	le32put(row, 0x2C, 0)       // remixer_id
	// Real exports store bitrate in kbps (e.g. 192), not bps. Storing the
	// raw bps value (e.g. 856353) puts a wildly out-of-range number in
	// this slot which the deck may use to invalidate the whole row /
	// reject analysis loading.
	le32put(row, 0x30, t.Bitrate/1000)
	le32put(row, 0x34, t.TrackNum)
	le32put(row, 0x38, t.Tempo)
	le32put(row, 0x3C, genreID)
	le32put(row, 0x40, albumID)
	le32put(row, 0x44, artistID)
	le32put(row, 0x48, t.ID)
	le16put(row, 0x4C, t.DiscNumber)
	le16put(row, 0x4E, t.PlayCount)
	le16put(row, 0x50, t.Year)
	// Real exports always populate this with the source bit depth (16/24).
	// Storing 0 may make the deck treat the file as malformed audio.
	sampleDepth := t.SampleDepth
	if sampleDepth == 0 {
		sampleDepth = 16
	}
	le16put(row, 0x52, sampleDepth)
	le16put(row, 0x54, t.Duration)
	le16put(row, 0x56, 41) // constant per kaitai spec ("always 41?")
	row[0x58] = t.ColorID
	row[0x59] = t.Rating
	// 0x5A is the FileType enum (per rekordcrate's reverse engineering):
	//   0x0000 Unknown, 0x0001 Mp3, 0x0004 M4a, 0x0005 Flac,
	//   0x000B Wav,     0x000C Aiff
	// Verified against real exports — a FLAC track has 0x0005 and MP3
	// tracks have 0x0001. We used to hardcode 0x0001 for every track,
	// which is plausibly what was freezing the CDJ when it tried to
	// load a FLAC as MP3.
	le16put(row, 0x5A, fileTypeFromPath(t.FilePath))
	le16put(row, 0x5C, 0x0003) // observed in every real track row

	// String offset table at 0x5E.
	for i, off := range offsets {
		le16put(row, 0x5E+i*2, off)
	}

	// String data after fixed part.
	copy(row[fixedSize:], strData)

	// 4-byte alignment is applied universally in tableBuilder.addRow,
	// no need to pad here.
	return row
}

// padTo4 rounds a row's length up to the next multiple of 4 bytes by
// appending zero bytes. The CDJ freezes on USB insert when
// ANY row in a multi-row data page has a length not divisible by 4.
//
// Discovered by binary bisection: every row size that freezes the
// deck is non-multiple-of-4 (11, 30, 166, 170, 173, 175, 233, 235);
// every row size that loads is a multiple of 4 (12, 16, 168, 176,
// 180, 200, 236, 240, ...). No separate "minimum size" rule —
// 4-byte alignment alone is the constraint, and applies uniformly
// across track / genre / artist / album / etc. row types.
//
// Padding with zeros works because rows are read via offset lookups
// (or, for variable-shape fields, via the row's own embedded
// length prefixes); trailing bytes past the last meaningful field
// are inert.
func padTo4(row []byte) []byte {
	if r := len(row) & 3; r != 0 {
		row = append(row, make([]byte, 4-r)...)
	}
	return row
}

// fileTypeFromPath returns the rekordbox FileType enum value for the
// given audio file path. Falls back to Unknown for unrecognized
// extensions rather than guessing a default — a wrong file_type
// could trip the CDJ when it tries to load the audio.
// hash28 maps a track ID to a 28-bit value distributed across the
// full 0..2^28 space, matching the shape of unknown_id_1 values
// observed in rekordbox exports. Real values look like
// content-derived hashes (we couldn't crack the exact function),
// but spreading our values across the same range avoids the low-
// bucket clustering that small sequential IDs would produce in any
// hash-table index the CDJ might build over this field.
func hash28(id uint32) uint32 {
	// FNV-1a-style mixing of the 4 bytes of id, then mask to 28 bits.
	const (
		fnvOffset uint32 = 2166136261
		fnvPrime  uint32 = 16777619
	)
	h := fnvOffset
	for i := 0; i < 4; i++ {
		h ^= uint32(byte(id >> (i * 8)))
		h *= fnvPrime
	}
	return h & 0x0FFFFFFF
}

func fileTypeFromPath(path string) uint16 {
	ext := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			ext = path[i+1:]
			break
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	switch strings.ToLower(ext) {
	case "mp3":
		return 0x0001
	case "m4a", "mp4", "aac":
		return 0x0004
	case "flac":
		return 0x0005
	case "wav":
		return 0x000B
	case "aif", "aiff":
		return 0x000C
	default:
		return 0x0000
	}
}

// DeviceSQL string encoding.

func encodeString(s string) []byte {
	if s == "" {
		// Empty string: short ASCII with length=1 (just the length byte, no content).
		return []byte{0x03} // (1 << 1) | 1 = 3
	}

	isASCII := true
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7F {
			isASCII = false
			break
		}
	}

	if isASCII {
		dataLen := len(s) + 1 // include the length byte itself
		if dataLen <= 127 {
			// Short ASCII: length_and_kind = (dataLen << 1) | 1
			b := make([]byte, 1+len(s))
			b[0] = byte((dataLen << 1) | 1)
			copy(b[1:], s)
			return b
		}
		// Long ASCII.
		totalLen := len(s) + 4
		b := make([]byte, totalLen)
		b[0] = 0x40
		binary.LittleEndian.PutUint16(b[1:], uint16(totalLen))
		b[3] = 0x00
		copy(b[4:], s)
		return b
	}

	// UTF-16LE.
	runes := []rune(s)
	u16 := utf16.Encode(runes)
	u16Data := make([]byte, len(u16)*2)
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(u16Data[i*2:], v)
	}
	totalLen := len(u16Data) + 4
	b := make([]byte, totalLen)
	b[0] = 0x90
	binary.LittleEndian.PutUint16(b[1:], uint16(totalLen))
	b[3] = 0x00
	copy(b[4:], u16Data)
	return b
}

// idMap assigns sequential IDs to unique strings.
type idMap struct {
	ids    map[string]uint32
	nextID uint32
}

func newIDMap() *idMap {
	return &idMap{ids: make(map[string]uint32), nextID: 1}
}

// newIDMapSeeded pre-populates the map with existing name→ID assignments
// (so they stay stable across re-exports) and bumps nextID past the
// highest seen value so freshly allocated IDs don't collide.
func newIDMapSeeded(seed map[string]uint32) *idMap {
	m := newIDMap()
	for name, id := range seed {
		if name == "" || id == 0 {
			continue
		}
		m.ids[name] = id
		if id >= m.nextID {
			m.nextID = id + 1
		}
	}
	return m
}

// seed safely extracts a name→ID map from a (possibly nil) MergeBase.
func seed(m *MergeBase, pick func(*MergeBase) map[string]uint32) map[string]uint32 {
	if m == nil {
		return nil
	}
	return pick(m)
}

func (m *idMap) getOrCreate(name string) uint32 {
	if name == "" {
		return 0
	}
	if id, ok := m.ids[name]; ok {
		return id
	}
	id := m.nextID
	m.nextID++
	m.ids[name] = id
	return id
}

// Helpers.

func le32put(b []byte, off int, v uint32) {
	binary.LittleEndian.PutUint32(b[off:], v)
}

func le16put(b []byte, off int, v uint16) {
	binary.LittleEndian.PutUint16(b[off:], v)
}

// SanitizeFilename cleans a string for use in FAT32 paths.
func SanitizeFilename(s string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(s)
}

// TruncateFilename shortens a filename to maxLen chars, preserving extension.
func TruncateFilename(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	keep := maxLen - len(ext)
	if keep < 1 {
		keep = 1
	}
	return base[:keep] + ext
}

// reencodeAudio runs ffmpeg to re-encode `src` into `dst`, choosing the
// codec based on the destination extension. Used by PrepareUSBLayout to
// salvage tracks whose source has decode errors the CDJ can't tolerate
// — re-encoding produces a frame-clean copy at roughly the original
// quality. Returns an error if ffmpeg isn't installed or the re-encode
// fails (PrepareUSBLayout falls back to a plain copy in that case).
func reencodeAudio(src, dst string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not in PATH")
	}
	ext := strings.ToLower(filepath.Ext(dst))
	var codecArgs []string
	switch ext {
	case ".mp3":
		// Keep ID3 tags (essential for CDJ display); CBR 320 kbps gives
		// the most predictable wire behavior for the CDJ (fixed byte/
		// time conversion, no VBR-header lookups) and matches what most
		// "max quality" source MP3s are anyway.
		codecArgs = []string{"-c:a", "libmp3lame", "-b:a", "320k", "-id3v2_version", "3", "-map_metadata", "0"}
	case ".flac", ".wav", ".aiff", ".aif":
		// Lossless re-encode for lossless formats.
		codecArgs = []string{"-c:a", "flac", "-map_metadata", "0"}
		if ext != ".flac" {
			// Keep original container (WAV/AIFF) with PCM.
			codecArgs = []string{"-c:a", "pcm_s16le", "-map_metadata", "0"}
		}
	case ".m4a":
		codecArgs = []string{"-c:a", "aac", "-b:a", "256k", "-map_metadata", "0"}
	default:
		return fmt.Errorf("unsupported extension %s", ext)
	}
	args := append([]string{"-y", "-v", "error", "-i", src}, codecArgs...)
	args = append(args, dst)
	cmd := exec.Command("ffmpeg", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// PrepareUSBLayout copies music files into the Contents/ directory structure
// and updates track FilePath/FileName fields to the USB-relative paths.
// If copyFiles is false, creates symlinks instead (faster for testing).
func PrepareUSBLayout(tracks []*Track, srcDir, outDir string, copyFiles bool) error {
	contentsDir := filepath.Join(outDir, "Contents")
	now := time.Now().Format("2006-01-02")

	for _, t := range tracks {
		artist := t.Artist
		if artist == "" {
			artist = "Unknown Artist"
		}
		album := t.Album
		if album == "" {
			album = "Unknown"
		}

		safeArtist := SanitizeFilename(artist)
		safeAlbum := SanitizeFilename(album)

		ext := filepath.Ext(t.FilePath)
		baseName := strings.TrimSuffix(filepath.Base(t.FilePath), ext)
		safeName := SanitizeFilename(baseName) + ext
		safeName = TruncateFilename(safeName, 48)

		// Cap the final USB path so the title field stays in DeviceSQL
		// short-ASCII encoding (≤126 chars). The CDJ freezes
		// when pressing INFO on a track whose title field uses the
		// long-ASCII (0x40-marker) encoding — pcap-confirmed against
		// rekordbox exports, which always emit short ASCII for
		// this slot. Shrink safeName first; if that's not enough, also
		// shrink safeAlbum (artist last, since it's usually the most
		// recognizable component to keep intact).
		const maxUSBPath = 126
		fixed := len("/Contents/") + len(safeArtist) + 1 + len(safeAlbum) + 1
		if budget := maxUSBPath - fixed; budget < len(safeName) {
			minName := len(ext) + 5 // keep at least 5 base chars
			if budget >= minName {
				safeName = TruncateFilename(safeName, budget)
			} else {
				// Steal characters from album too, in chunks of 4 so
				// disambiguation prefix stays sensible.
				deficit := minName - budget
				newAlbum := len(safeAlbum) - deficit
				if newAlbum < 4 {
					newAlbum = 4
				}
				safeAlbum = safeAlbum[:newAlbum]
				fixed = len("/Contents/") + len(safeArtist) + 1 + len(safeAlbum) + 1
				if newBudget := maxUSBPath - fixed; newBudget >= minName {
					safeName = TruncateFilename(safeName, newBudget)
				} else {
					safeName = TruncateFilename(safeName, minName)
				}
			}
		}

		destDir := filepath.Join(contentsDir, safeArtist, safeAlbum)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", destDir, err)
		}

		destPath := filepath.Join(destDir, safeName)

		switch {
		case t.NeedsReencode:
			// Source file has decode errors that empirically freeze the
			// CDJ mid-playback. Re-encode into a clean file at
			// the destination instead of copying the broken bytes. Falls
			// back to plain copy if ffmpeg fails (so we never block the
			// export entirely).
			absSource, _ := filepath.Abs(t.FilePath)
			os.Remove(destPath)
			if err := reencodeAudio(absSource, destPath); err != nil {
				fmt.Printf("WARN  re-encode of %s failed (%v); falling back to copy\n", t.FilePath, err)
				if data, rerr := os.ReadFile(t.FilePath); rerr == nil {
					if werr := os.WriteFile(destPath, data, 0o644); werr != nil {
						fmt.Printf("WARN  fallback copy of %s failed: %v\n", t.FilePath, werr)
					}
				} else {
					fmt.Printf("WARN  fallback copy of %s failed to read source: %v\n", t.FilePath, rerr)
				}
			}
			// CRITICAL: after a re-encode the file is a different size
			// (and usually different bitrate) than the source. The PDB
			// record was built from the source's stat — if we leave it,
			// the CDJ uses the wrong byte count for buffer / seek math
			// and freezes mid-playback. Re-stat the actual on-disk file
			// and patch t.FileSize (and t.Bitrate, recomputed from the
			// new size + original duration) before the PDB row writer
			// emits its bytes downstream.
			if info, err := os.Stat(destPath); err == nil {
				t.FileSize = uint32(info.Size())
				if t.Duration > 0 {
					// bps = bytes * 8 / duration_seconds (matches
					// export.safeBitrate; PDB writer divides by 1000
					// downstream when serializing the kbps field).
					t.Bitrate = uint32(int64(t.FileSize) * 8 / int64(t.Duration))
				}
			}
		case copyFiles:
			data, err := os.ReadFile(t.FilePath)
			if err != nil {
				return fmt.Errorf("read %s: %w", t.FilePath, err)
			}
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", destPath, err)
			}
		default:
			// Create symlink for testing.
			absSource, _ := filepath.Abs(t.FilePath)
			os.Remove(destPath)
			if err := os.Symlink(absSource, destPath); err != nil {
				return fmt.Errorf("symlink %s: %w", destPath, err)
			}
		}

		// Update track with USB-relative path.
		t.FileName = safeName
		t.FilePath = "/Contents/" + safeArtist + "/" + safeAlbum + "/" + safeName
		if t.DateAdded == "" {
			t.DateAdded = now
		}
	}

	return nil
}

// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// Table types in PDB.
const (
	// Sequential table types 0x00 .. 0x13 — every real export.pdb has
	// all 20 in this order. The CDJ rejects exports with missing or
	// out-of-order tables, so we always emit the full set (empty for
	// the ones we don't populate yet) in ascending type order.
	// Plain (export.pdb) table types. Authoritative source: rekordcrate's
	// PlainPageType enum. Tables not in the enum are typed UnknownN and
	// observed empty in every real export.
	TableTracks           = 0x00
	TableGenres           = 0x01
	TableArtists          = 0x02
	TableAlbums           = 0x03
	TableLabels           = 0x04
	TableKeys             = 0x05
	TableColors           = 0x06
	TablePlaylistTree     = 0x07
	TablePlaylistEntries  = 0x08
	TableUnknown9         = 0x09
	TableUnknownA         = 0x0a
	TableHistoryPlaylists = 0x0b
	TableHistoryEntries   = 0x0c
	TableArtwork          = 0x0d
	TableUnknownE         = 0x0e
	TableUnknownF         = 0x0f
	TableColumns          = 0x10 // browse categories shown on the CDJ menu
	TableMenu             = 0x11 // active CDJ menu state
	TableUnknown12        = 0x12
	TableHistory          = 0x13 // export metadata (date, version, label)
)

// Track represents a parsed track row from the PDB.
type Track struct {
	ID          uint32
	Title       string
	Artist      string
	Album       string
	Genre       string
	Key         string
	Label       string // record label name; the writer hashes this to a LabelID
	FilePath    string
	FileName    string
	Comment     string
	DateAdded   string
	ArtistID    uint32
	AlbumID     uint32
	GenreID     uint32
	KeyID       uint32
	LabelID     uint32
	ArtworkID   uint32
	Tempo       uint32 // BPM * 100
	Duration    uint16 // seconds
	Bitrate     uint32
	Rating      uint8
	TrackNum    uint32
	Year        uint16
	FileSize    uint32
	ColorID     uint8
	SampleRate  uint32 // Hz (e.g. 44100, 48000, 96000)
	SampleDepth uint16 // bits per sample
	PlayCount   uint16
	DiscNumber  uint16
	AnalyzePath string // path to ANLZ0000.DAT file

	// NeedsReencode, when true, signals PrepareUSBLayout to re-encode
	// the source audio file via ffmpeg instead of copying/symlinking.
	// The export pipeline currently never sets it (the re-encode path is
	// dormant), but the plumbing stays available.
	NeedsReencode bool
}

// NamedRow represents a simple ID+name row (artist, album, genre, key).
type NamedRow struct {
	ID   uint32
	Name string
}

// Database holds all parsed PDB data.
type Database struct {
	Tracks  []*Track
	Artists map[uint32]string
	Albums  map[uint32]string
	Genres  map[uint32]string
	Keys    map[uint32]string
	Labels  map[uint32]string

	// PlaylistTree is the playlist hierarchy, flattened to a list. Each
	// node carries its own ID + ParentID + Name; reconstruct the tree
	// via ParentID links.
	PlaylistTree []*FolderNode

	ExportRoot string // root path of the USB export (derived from PDB file location)
	trackByID  map[uint32]*Track
}

// TrackByID returns a track by its PDB ID.
func (db *Database) TrackByID(id uint32) *Track {
	return db.trackByID[id]
}

// AddTrack adds a track to the database. If the track ID is 0, assigns the next available ID.
func (db *Database) AddTrack(t *Track) {
	if t.ID == 0 {
		t.ID = uint32(len(db.Tracks)) + 1
		for db.trackByID[t.ID] != nil {
			t.ID++
		}
	}
	if db.trackByID == nil {
		db.trackByID = make(map[uint32]*Track)
	}
	if db.trackByID[t.ID] != nil {
		return // already exists
	}
	db.Tracks = append(db.Tracks, t)
	db.trackByID[t.ID] = t
}

// Open parses a PDB file from disk and returns the database.
func Open(path string) (*Database, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pdb: %w", err)
	}

	// Derive export root: /path/to/USB/PIONEER/rekordbox/export.pdb → /path/to/USB
	absPath, _ := filepath.Abs(path)
	exportRoot := ""
	if idx := strings.Index(strings.ToUpper(absPath), "/PIONEER/"); idx >= 0 {
		exportRoot = absPath[:idx]
	}
	return OpenBytes(data, exportRoot)
}

// OpenBytes parses a PDB from an in-memory byte slice (e.g. one downloaded from
// a player's NFS export, which never touches the local disk). exportRoot is the
// media root the track file paths are relative to, or "" when unknown (metadata
// lookups by ID don't need it).
func OpenBytes(data []byte, exportRoot string) (*Database, error) {
	if len(data) < 0x1c {
		return nil, fmt.Errorf("pdb too short: %d bytes", len(data))
	}

	pageSize := int(le32(data, 0x04))
	numTables := int(le32(data, 0x08))

	if pageSize == 0 || pageSize > 1<<20 {
		return nil, fmt.Errorf("invalid page size: %d", pageSize)
	}

	log.Printf("pdb: page_size=%d num_tables=%d file_size=%d", pageSize, numTables, len(data))

	db := &Database{
		Artists:    make(map[uint32]string),
		Albums:     make(map[uint32]string),
		Genres:     make(map[uint32]string),
		Keys:       make(map[uint32]string),
		Labels:     make(map[uint32]string),
		trackByID:  make(map[uint32]*Track),
		ExportRoot: exportRoot,
	}

	// playlistTracks[playlistID] = ordered list of trackIDs in that playlist.
	playlistTracks := make(map[uint32][]uint32)

	// Parse table pointers at offset 0x1c, each 16 bytes.
	for i := 0; i < numTables; i++ {
		off := 0x1c + i*16
		if off+16 > len(data) {
			break
		}
		tableType := le32(data, off)
		firstPage := int(le32(data, off+8))
		lastPage := int(le32(data, off+12))

		switch tableType {
		case TableArtists:
			db.parseNamedTable(data, pageSize, firstPage, lastPage, db.Artists, "artists")
		case TableAlbums:
			db.parseNamedTable(data, pageSize, firstPage, lastPage, db.Albums, "albums")
		case TableGenres:
			db.parseNamedTable(data, pageSize, firstPage, lastPage, db.Genres, "genres")
		case TableLabels:
			db.parseNamedTable(data, pageSize, firstPage, lastPage, db.Labels, "labels")
		case TableKeys:
			db.parseKeysTable(data, pageSize, firstPage, lastPage)
		case TableTracks:
			db.parseTracks(data, pageSize, firstPage, lastPage)
		case TablePlaylistTree:
			db.parsePlaylistTree(data, pageSize, firstPage, lastPage)
		case TablePlaylistEntries:
			db.parsePlaylistEntries(data, pageSize, firstPage, lastPage, playlistTracks)
		}
	}

	// Attach track lists to playlist nodes after both tables are parsed.
	for _, node := range db.PlaylistTree {
		if ids, ok := playlistTracks[node.ID]; ok {
			node.TrackIDs = ids
		}
	}

	// Resolve foreign keys.
	for _, t := range db.Tracks {
		if name, ok := db.Artists[t.ArtistID]; ok {
			t.Artist = name
		}
		if name, ok := db.Albums[t.AlbumID]; ok {
			t.Album = name
		}
		if name, ok := db.Genres[t.GenreID]; ok {
			t.Genre = name
		}
		if name, ok := db.Keys[t.KeyID]; ok {
			t.Key = name
		}
		if name, ok := db.Labels[t.LabelID]; ok {
			t.Label = name
		}
	}

	log.Printf("pdb: loaded %d tracks, %d artists, %d albums, %d genres, %d keys",
		len(db.Tracks), len(db.Artists), len(db.Albums), len(db.Genres), len(db.Keys))

	return db, nil
}

func (db *Database) parseTracks(data []byte, pageSize, firstPage, lastPage int) {
	iterRows(data, pageSize, firstPage, lastPage, func(row []byte) {
		if len(row) < 0x88 {
			return
		}

		t := &Track{
			ArtworkID: le32(row, 0x1c),
			KeyID:     le32(row, 0x20),
			LabelID:   le32(row, 0x28),
			Bitrate:   le32(row, 0x30),
			TrackNum:  le32(row, 0x34),
			Tempo:     le32(row, 0x38),
			GenreID:   le32(row, 0x3c),
			AlbumID:   le32(row, 0x40),
			ArtistID:  le32(row, 0x44),
			ID:        le32(row, 0x48),
			Duration:  le16(row, 0x54),
			Year:      le16(row, 0x50),
			ColorID:   row[0x58],
			Rating:    row[0x59],
			FileSize:  le32(row, 0x10),
		}

		// Parse strings via offset table at 0x5e.
		if le16(row, 0x5c) == 0x0003 && len(row) >= 0x88 {
			t.Title = readStringAt(row, 17)       // index 17
			t.FileName = readStringAt(row, 19)    // index 19
			t.FilePath = readStringAt(row, 20)    // index 20
			t.Comment = readStringAt(row, 16)     // index 16
			t.DateAdded = readStringAt(row, 10)   // index 10
			t.AnalyzePath = readStringAt(row, 14) // index 14
		}

		if t.ID > 0 && db.trackByID[t.ID] == nil {
			db.Tracks = append(db.Tracks, t)
			db.trackByID[t.ID] = t
		}
	})
}

func (db *Database) parseNamedTable(data []byte, pageSize, firstPage, lastPage int, dest map[uint32]string, name string) {
	iterRows(data, pageSize, firstPage, lastPage, func(row []byte) {
		if len(row) < 8 {
			return
		}
		subtype := le16(row, 0)

		var id uint32
		var strOff int

		switch subtype {
		case 0x0060, 0x0064:
			// Artist: subtype(2) + index(2) + ID(4) + ... + string
			id = uint32(le16(row, 4))
			strOff = 10
			if subtype == 0x0064 {
				strOff = 12 // far string offset variant
			}
		case 0x0080, 0x0084:
			// Album: subtype(2) + ... + ID at offset 12 + ... + string
			if len(row) < 24 {
				return
			}
			id = uint32(le16(row, 12))
			strOff = 22
			if subtype == 0x0084 {
				strOff = 24
			}
		default:
			// Simple format (genres, labels): u32 ID + DeviceSQL string at offset 4
			id = le32(row, 0)
			strOff = 4
		}

		if strOff < len(row) && id > 0 {
			s := readDeviceSQLString(row[strOff:])
			if s != "" {
				dest[id] = s
			}
		}
	})
}

// parsePlaylistTree parses the playlist_tree table (table 0x07).
// Row layout: parent_id(u32) + unknown(u32) + sort_order(u32) + id(u32)
// + is_folder(u32) + name(DeviceSQL string).
func (db *Database) parsePlaylistTree(data []byte, pageSize, firstPage, lastPage int) {
	iterRows(data, pageSize, firstPage, lastPage, func(row []byte) {
		if len(row) < 24 {
			return
		}
		parentID := le32(row, 0)
		// le32(row, 4) is unknown
		// le32(row, 8) is sort_order
		id := le32(row, 12)
		isFolder := le32(row, 16) != 0
		name := readDeviceSQLString(row[20:])
		if id == 0 {
			return
		}
		db.PlaylistTree = append(db.PlaylistTree, &FolderNode{
			ID:       id,
			ParentID: parentID,
			Name:     name,
			IsFolder: isFolder,
		})
	})
}

// parsePlaylistEntries parses table 0x08: entry_index(u32) + track_id(u32)
// + playlist_id(u32). Build playlist→trackIDs map; tracks are listed in
// entry_index order.
func (db *Database) parsePlaylistEntries(data []byte, pageSize, firstPage, lastPage int, out map[uint32][]uint32) {
	type entry struct{ idx, trackID, playlistID uint32 }
	var entries []entry
	iterRows(data, pageSize, firstPage, lastPage, func(row []byte) {
		if len(row) < 12 {
			return
		}
		entries = append(entries, entry{
			idx:        le32(row, 0),
			trackID:    le32(row, 4),
			playlistID: le32(row, 8),
		})
	})
	// Group by playlist, sort by index, extract trackIDs.
	byList := make(map[uint32][]entry)
	for _, e := range entries {
		byList[e.playlistID] = append(byList[e.playlistID], e)
	}
	for plID, es := range byList {
		// sort by entry_index
		for i := 1; i < len(es); i++ {
			for j := i; j > 0 && es[j-1].idx > es[j].idx; j-- {
				es[j-1], es[j] = es[j], es[j-1]
			}
		}
		ids := make([]uint32, len(es))
		for i, e := range es {
			ids[i] = e.trackID
		}
		out[plID] = ids
	}
}

// parseKeysTable parses the keys table which has a different row format:
// u32 id, u32 id2, DeviceSQL string (no subtype prefix).
func (db *Database) parseKeysTable(data []byte, pageSize, firstPage, lastPage int) {
	iterRows(data, pageSize, firstPage, lastPage, func(row []byte) {
		if len(row) < 12 {
			return
		}
		id := le32(row, 0)
		// DeviceSQL string starts at offset 8.
		if id > 0 {
			s := readDeviceSQLString(row[8:])
			if s != "" {
				db.Keys[id] = s
			}
		}
	})
}

// iterRows walks all rows in a table's pages.
func iterRows(data []byte, pageSize, firstPage, lastPage int, fn func([]byte)) {
	page := firstPage
	for page != 0 && page*pageSize+pageSize <= len(data) {
		pageData := data[page*pageSize : page*pageSize+pageSize]

		flags := pageData[0x1b]
		if flags&0x40 != 0 {
			// Index page, skip.
			page = int(le32(pageData, 0x0c))
			continue
		}

		// Row counts from bytes 0x18-0x1a (24 bits).
		rowBits := uint32(pageData[0x18]) | uint32(pageData[0x19])<<8 | uint32(pageData[0x1a])<<16
		numRowOffsets := int(rowBits & 0x1FFF)
		numRows := int((rowBits >> 13) & 0x7FF)
		_ = numRows

		// Row index is at the end of the page, building backward.
		// Each group of 16 rows: 16 * 2-byte offsets + 2-byte rowpf + 2-byte tranrf
		for i := 0; i < numRowOffsets; i++ {
			groupIdx := i / 16
			rowInGroup := i % 16

			// Position of this group in the row index (from end of page).
			groupSize := 0
			rowsInThisGroup := min(16, numRowOffsets-groupIdx*16)
			groupSize = rowsInThisGroup*2 + 2 + 2 // offsets + rowpf + tranrf

			groupEnd := pageSize
			for g := 0; g < groupIdx; g++ {
				rig := min(16, numRowOffsets-g*16)
				groupEnd -= rig*2 + 2 + 2
			}
			groupStart := groupEnd - groupSize

			if groupStart < 0x28 || groupStart >= pageSize {
				break
			}

			// Read row presence flags.
			rowpfOff := groupStart + rowsInThisGroup*2
			if rowpfOff+2 > pageSize {
				break
			}
			rowpf := le16(pageData, rowpfOff)
			if rowpf&(1<<uint(rowInGroup)) == 0 {
				continue // Row not present.
			}

			// Read row offset.
			offIdx := groupStart + rowInGroup*2
			if offIdx+2 > pageSize {
				break
			}
			rowOff := int(le16(pageData, offIdx))

			// Row data starts at heap (offset 0x28) + rowOff.
			rowStart := 0x28 + rowOff
			if rowStart >= pageSize {
				break
			}

			// Row extends to next row or end of heap.
			rowEnd := pageSize - (pageSize - groupStart) // approximate
			// Better: find next row offset or use page used_size.
			usedSize := int(le16(pageData, 0x1e))
			rowEnd = 0x28 + usedSize
			if rowEnd > pageSize {
				rowEnd = pageSize
			}

			if rowStart < rowEnd {
				fn(pageData[rowStart:rowEnd])
			}
		}

		nextPage := int(le32(pageData, 0x0c))
		if nextPage == page {
			break // Avoid infinite loop.
		}
		page = nextPage
	}
}

// readStringAt reads a DeviceSQL string using the offset table in a track row.
// The offset table starts at row offset 0x5e, with 21 x 2-byte offsets.
func readStringAt(row []byte, index int) string {
	if index >= 21 || len(row) < 0x5e+index*2+2 {
		return ""
	}
	strOff := int(le16(row, 0x5e+index*2))
	if strOff == 0 || strOff >= len(row) {
		return ""
	}
	return readDeviceSQLString(row[strOff:])
}

// readDeviceSQLString reads a DeviceSQL encoded string.
func readDeviceSQLString(data []byte) string {
	if len(data) < 1 {
		return ""
	}

	lk := data[0]
	isShort := lk&0x01 != 0

	if isShort {
		// Short ASCII string.
		strLen := int(lk >> 1)
		if strLen <= 1 || strLen > len(data) {
			return ""
		}
		return string(data[1:strLen])
	}

	// Long string.
	if len(data) < 4 {
		return ""
	}
	totalLen := int(le16(data, 1))
	if totalLen < 4 || totalLen > len(data) {
		totalLen = len(data)
	}
	strData := data[4:totalLen]

	isASCII := lk&0x40 != 0
	isUTF16 := lk&0x10 != 0

	if isASCII {
		return string(strData)
	}
	if isUTF16 {
		// UTF-16LE.
		if len(strData) < 2 {
			return ""
		}
		u16s := make([]uint16, len(strData)/2)
		for i := range u16s {
			u16s[i] = binary.LittleEndian.Uint16(strData[i*2:])
		}
		// Trim NUL.
		for len(u16s) > 0 && u16s[len(u16s)-1] == 0 {
			u16s = u16s[:len(u16s)-1]
		}
		return string(utf16.Decode(u16s))
	}

	// Fallback: treat as ASCII.
	return string(strData)
}

func le32(data []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(data[off:])
}

func le16(data []byte, off int) uint16 {
	return binary.LittleEndian.Uint16(data[off:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

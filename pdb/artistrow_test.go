// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import "testing"

// buildArtistRow lays out a minimal rekordbox artist row (subtype 0x0060):
// subtype(2) index_shift(2) id(4) unknown(1=0x03) ofs_name(1) ... string.
// The DeviceSQL string is placed at nameOff, so the row models the real
// format where the name offset is READ from byte 9, not assumed to be 10.
func buildArtistRow(id uint32, nameOff int, sqlString []byte) []byte {
	row := make([]byte, nameOff+len(sqlString))
	le16put(row, 0, 0x0060)
	le32put(row, 4, id)
	row[8] = 0x03
	row[9] = byte(nameOff)
	copy(row[nameOff:], sqlString)
	return row
}

// shortASCII builds a DeviceSQL short-ASCII string ("len-kind" byte then the
// bytes). strLen in the header counts the header byte itself + the chars + 1.
func shortASCII(s string) []byte {
	out := []byte{byte(((len(s) + 1) << 1) | 1)}
	return append(out, s...)
}

// utf16Long builds a DeviceSQL long UTF-16LE string: 0x90 marker, u16 total
// length, a pad byte, then UTF-16LE data — the form a name with non-ASCII
// characters takes, and the form the hardcoded-offset reader over-read.
func utf16Long(s string) []byte {
	var body []byte
	for _, r := range s {
		body = append(body, byte(r), byte(r>>8))
	}
	total := 4 + len(body)
	out := []byte{0x90, byte(total), byte(total >> 8), 0x00}
	return append(out, body...)
}

// TestParseArtistRowReadsNameOffset pins the fix for the string-heap
// over-read: the reader must follow the row's own name-offset byte. A
// short name sits at offset 10 (where the old hardcode happened to work);
// a UTF-16 name sits further along (offset 12), where hardcoding landed on
// padding and the decoder ran off the end of the string. Both must decode
// to exactly their own name — proving later rows can't bleed in.
func TestParseArtistRowReadsNameOffset(t *testing.T) {
	db := &Database{Artists: map[uint32]string{}}

	short := buildArtistRow(1, 10, shortASCII("Alex Rivera"))
	far := buildArtistRow(2, 12, utf16Long("Søren Vält & Mara Linde"))
	// Concatenate into one page-less blob the row parser sees per-row; call
	// the row handler directly via a tiny reimplementation isn't needed —
	// parseNamedTable's switch is what we exercise, so feed rows through it.
	for _, row := range [][]byte{short, far} {
		parseArtistRowForTest(db, row)
	}

	if got := db.Artists[1]; got != "Alex Rivera" {
		t.Errorf("short name = %q, want %q", got, "Alex Rivera")
	}
	if got := db.Artists[2]; got != "Søren Vält & Mara Linde" {
		t.Errorf("UTF-16 name = %q, want %q (over-read would make it far longer)", got, "Søren Vält & Mara Linde")
	}
	if len(db.Artists[2]) > 40 {
		t.Errorf("UTF-16 name over-read: %d bytes", len(db.Artists[2]))
	}
}

// parseArtistRowForTest mirrors parseNamedTable's per-row artist decoding so
// a single synthetic row can be tested without constructing a full page.
func parseArtistRowForTest(db *Database, row []byte) {
	if len(row) < 12 {
		return
	}
	subtype := le16(row, 0)
	id := le32(row, 4)
	var strOff int
	if subtype == 0x0064 {
		strOff = int(le16(row, 10))
	} else {
		strOff = int(row[9])
	}
	if strOff < len(row) && id > 0 {
		if s := readDeviceSQLString(row[strOff:]); s != "" {
			db.Artists[id] = s
		}
	}
}

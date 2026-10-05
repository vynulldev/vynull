// SPDX-License-Identifier: GPL-3.0-or-later

package pdb

import "testing"

// TestMakeHistoryRowLayout pins the History row (table 0x13) to the inline-string
// layout real rekordbox writes (issue #53). The interop-critical byte is the u32
// zero magic at 0x08: the old offset-table layout put a u16 offset there, which
// third-party parsers (rekordcrate) read as a u32 magic and reject ("bad magic").
func TestMakeHistoryRowLayout(t *testing.T) {
	const date = "2024-02-29"
	row := makeHistoryRow(42, date)

	if got := le16(row, 0x00); got != 0x0280 {
		t.Errorf("subtype = %#x, want 0x0280", got)
	}
	if got := le32(row, 0x04); got != 42 {
		t.Errorf("num_tracks = %d, want 42", got)
	}
	// The fix: offset 0x08 is a u32 zero magic, not an offset table.
	if got := le32(row, 0x08); got != 0 {
		t.Errorf("magic@0x08 = %#x, want 0 (offset-table layout regressed — breaks rekordcrate)", got)
	}

	// date is an inline DeviceSQLString at 0x0C, decodable back to itself.
	if got := readDeviceSQLString(row[0x0C:]); got != date {
		t.Errorf("date = %q, want %q", got, date)
	}
	// ...followed by the 0x1E19 magic, then the "1000" version string.
	dateLen := len(encodeString(date))
	if got := le16(row, 0x0C+dateLen); got != 0x1E19 {
		t.Errorf("separator after date = %#x, want 0x1E19", got)
	}
	if got := readDeviceSQLString(row[0x0C+dateLen+2:]); got != "1000" {
		t.Errorf("version = %q, want %q", got, "1000")
	}

	// Full size: 12 (prefix) + date + 2 (magic) + version + label(0x03 0x00).
	wantLen := 12 + dateLen + 2 + len(encodeString("1000")) + 2
	if len(row) != wantLen {
		t.Errorf("row length = %d, want %d", len(row), wantLen)
	}
}

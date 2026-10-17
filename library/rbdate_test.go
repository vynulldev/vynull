// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"testing"
	"time"
)

// Values from link-spec conformance smart-date-format-matrix (rekordbox
// 7.2.19): which strings rekordbox reads as a date, and as which day.
func TestParseRekordboxDate(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2025-01-31", day(2025, 1, 31)},
		{"2024-02-29", day(2024, 2, 29)},
		{"2025/01/31", day(2025, 1, 31)},
		{"2025.01.31", day(2025, 1, 31)},
		{"2025 01 31", day(2025, 1, 31)},
		{"2025x01y31", day(2025, 1, 31)},
		{"2025Ω01Ω31", day(2025, 1, 31)},
		{"2025\n01\n31", day(2025, 1, 31)},
		{"2025-02-29", day(2025, 3, 1)}, // rolls over, as the C runtime does
		{"2025-13-01", day(2026, 1, 1)},
		{"2025-01-00", day(2024, 12, 31)},
		{"202A-01-31", day(2037, 1, 31)}, // 'A' is 17 past '0'
		{"1970-01-02", day(1970, 1, 2)},
		{"1970-01-01", time.Time{}},
		{"1969-12-31", time.Time{}},
		{"0000-01-01", time.Time{}},
		{"9999-12-31", time.Time{}},
		{"2025-1-31", time.Time{}},
		{"02025-01-31", time.Time{}},
		{"2025-01-31T00:00", time.Time{}},
		{"2025-01-31 00:00:00", time.Time{}},
		{" 2025-01-31", time.Time{}},
		{"2025-01-31 ", time.Time{}},
		{"not-a-date", time.Time{}},
		{"abcdefghij", time.Time{}},
		{"0000000000", time.Time{}},
		{"+025-01-31", time.Time{}},
		{"-001-01-01", time.Time{}},
		{"２０２５-０１-３１", time.Time{}},
		{"2025-0\x00-31", time.Time{}},
		{"", time.Time{}},
	}
	for _, tc := range cases {
		if got := ParseRekordboxDate(tc.in); !got.Equal(tc.want) {
			t.Errorf("ParseRekordboxDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"strings"
	"time"
)

// ParseRekordboxDate reads a rekordbox date (StockDate, DateCreated,
// ReleaseDate, or a smart-playlist rule's date) the way rekordbox does, or
// returns the zero time when rekordbox would treat it as no date
// (link-spec conformance smart-date-format-matrix, rekordbox 7.2.19).
//
// rekordbox reads the year, month, and day from fixed positions of a
// 10-character value and ignores the characters between them, so 2025/01/31
// and 2025 01 31 are both January 31. Each position's character counts as
// its distance from '0', so a letter makes a large digit. Out-of-range days
// and months still make a date: 2025-02-29 is rekordbox's own date, distinct
// from every other, and is kept here as the day it rolls over to. Anything
// of another length (single-digit fields, a time part, surrounding spaces),
// on or before 1970-01-01, or in year 9999 or later is no date.
func ParseRekordboxDate(s string) time.Time {
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) != 10 {
		return time.Time{}
	}
	num := func(from, to int) int {
		n := 0
		for _, c := range r[from:to] {
			n = n*10 + int(c-'0')
		}
		return n
	}
	year := num(0, 4)
	if year <= 1969 || year >= 9999 {
		return time.Time{}
	}
	t := time.Date(year, time.Month(num(5, 7)), num(8, 10), 0, 0, 0, 0, time.UTC)
	if !t.After(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) || t.Year() >= 9999 {
		return time.Time{}
	}
	return t
}

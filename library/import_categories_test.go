// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"slices"
	"testing"
)

// rekordboxDefaultCategories are the djmdCategory rows (ID, MenuItemID, Seq,
// Disable) of the database link-spec recorded its goldens on: rekordbox
// 7.2.19's defaults, MATCHING with Disable 2.
var rekordboxDefaultCategories = []masterDBCategory{
	{"1", "1", 4, 0}, {"2", "2", 5, 0}, {"3", "3", 6, 0}, {"4", "4", 1, 0}, {"5", "17", 9, 0},
	{"6", "5", 3, 0}, {"7", "6", 18, 0}, {"8", "7", 21, 0}, {"9", "8", 19, 0}, {"10", "9", 16, 0},
	{"11", "10", 17, 0}, {"12", "11", 2, 0}, {"15", "13", 13, 0}, {"17", "24", 11, 0}, {"18", "20", 8, 0},
	{"19", "14", 20, 0}, {"20", "15", 12, 0}, {"21", "16", 14, 0}, {"22", "19", 10, 0}, {"23", "18", 15, 0},
	{"26", "27", 7, 2},
}

func shownItems(imp []CategoryImport) []uint32 {
	var out []uint32
	for _, c := range imp {
		if c.Visible {
			out = append(out, c.MenuItem)
		}
	}
	return out
}

// Categories come in Seq order, every default one shown, MATCHING included
// despite its Disable 2 (link-spec CONFIGURATION.md and CATEGORY_ORACLE.md:
// Track, Key, BPM, Genre, Artist, Album, Matching, Search, Playlist, History,
// Folder, Bitrate, Color, File Name, Hot Cue Bank, Label, Original Artist,
// Rating, Remixer, Time, Year).
func TestImportCategoriesDefaults(t *testing.T) {
	want := []uint32{4, 11, 5, 1, 2, 3, 27, 20, 17, 19, 24, 15, 13, 16, 18, 9, 10, 6, 8, 14, 7}
	if got := shownItems(importCategories(rekordboxDefaultCategories)); !slices.Equal(got, want) {
		t.Errorf("shown menu items %v, want %v", got, want)
	}
}

// Any Disable value hides a category, except MATCHING (bit 0 only) and DATE
// ADDED (exactly 1); a row naming no menu item is skipped.
func TestImportCategoriesDisable(t *testing.T) {
	rows := []masterDBCategory{
		{"2", "2", 1, 1}, {"3", "3", 2, 2}, {"26", "27", 3, 2}, {"27", "27", 4, 3},
		{"24", "22", 5, 2}, {"25", "22", 6, 1}, {"4", "4", 7, 0}, {"99", "", 8, 0},
	}
	if got, want := shownItems(importCategories(rows)), []uint32{27, 22, 4}; !slices.Equal(got, want) {
		t.Errorf("shown menu items %v, want %v", got, want)
	}
	if n := len(importCategories(rows)); n != 7 {
		t.Errorf("%d categories, want 7 (the row without a menu item skipped)", n)
	}
}

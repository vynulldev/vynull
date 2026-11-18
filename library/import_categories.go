// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"sort"
	"strconv"
)

// masterDBCategory is one djmdCategory row: a root menu category's ID, its
// djmdMenuItems ID, its menu order, and its Disable bits.
type masterDBCategory struct {
	ID         string `json:"id"`
	MenuItemID string `json:"menu_item_id"`
	Seq        int    `json:"seq"`
	Disable    int    `json:"disable"`
}

// CategoryImport is one category of an imported root menu, in menu order:
// its rekordbox menu item (djmdMenuItems: 1 GENRE, 2 ARTIST ... 27 MATCHING)
// and whether the menu shows it.
type CategoryImport struct {
	MenuItem uint32
	Visible  bool
}

// rekordbox's menu items with their own Disable rule.
const (
	menuItemDateAdded = 22
	menuItemMatching  = 27
)

// importCategories orders a library's djmdCategory rows as rekordbox's root
// menu does (by Seq) and marks which it shows: any Disable value hides a
// category, except that MATCHING hides only on bit 0 and DATE ADDED only on
// exactly 1, both using bit 1 for something else (link-spec CONFIGURATION.md,
// "Exact category predicate", rekordbox 7.2.19). Rows naming no menu item are
// skipped.
func importCategories(rows []masterDBCategory) []CategoryImport {
	type row struct {
		item  uint32
		seq   int
		shown bool
	}
	var known []row
	for _, r := range rows {
		item, err := strconv.ParseUint(r.MenuItemID, 10, 32)
		if err != nil || item == 0 {
			continue
		}
		hidden := r.Disable != 0
		switch item {
		case menuItemMatching:
			hidden = r.Disable&1 != 0
		case menuItemDateAdded:
			hidden = r.Disable == 1
		}
		known = append(known, row{uint32(item), r.Seq, !hidden})
	}
	sort.SliceStable(known, func(a, b int) bool { return known[a].seq < known[b].seq })
	out := make([]CategoryImport, 0, len(known))
	for _, r := range known {
		out = append(out, CategoryImport{MenuItem: r.item, Visible: r.shown})
	}
	return out
}

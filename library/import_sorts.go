// SPDX-License-Identifier: GPL-3.0-or-later

package library

import "sort"

// masterDBSort is one djmdSort row: a deck sort's djmdMenuItems ID, its menu
// order, and its Disable bits (0x01 hides it from the SORT menu, 0x02 makes
// it the track lists' second column).
type masterDBSort struct {
	MenuItemID string `json:"menu_item_id"`
	Seq        int    `json:"seq"`
	Disable    int    `json:"disable"`
}

// SortImport is one entry of an imported SORT menu, in menu order: a deck
// sort ID (0 DEFAULT ... 17 DATE ADDED), whether the menu shows it, and
// whether it is the selected second column.
type SortImport struct {
	ID       uint32
	Visible  bool
	Selected bool
}

// sortIDByMenuItem maps rekordbox's fixed djmdMenuItems IDs to the sort IDs
// the deck uses (link-spec SECONDARY_COLUMNS.md, rekordbox 7.2.19).
var sortIDByMenuItem = map[string]uint32{
	"25": 0, "26": 1, "2": 2, "3": 3, "5": 4, "6": 5, "1": 6, "21": 7, "14": 8,
	"8": 9, "9": 10, "10": 11, "11": 12, "15": 13, "13": 15, "23": 16, "22": 17,
}

// importSorts orders a library's djmdSort rows as rekordbox's SORT menu does
// (by Seq, hidden rows holding no position) and marks the first selected
// row in table order, the one rekordbox's track lists use. Unknown menu
// items are skipped.
func importSorts(rows []masterDBSort) []SortImport {
	type row struct {
		masterDBSort
		id    uint32
		index int
	}
	var known []row
	selected := -1
	for i, r := range rows {
		id, ok := sortIDByMenuItem[r.MenuItemID]
		if !ok {
			continue
		}
		if selected < 0 && r.Disable&2 != 0 {
			selected = len(known)
		}
		known = append(known, row{r, id, i})
	}
	order := make([]int, len(known))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return known[order[a]].Seq < known[order[b]].Seq })
	out := make([]SortImport, 0, len(known))
	for _, i := range order {
		out = append(out, SortImport{ID: known[i].id, Visible: known[i].Disable&1 == 0, Selected: i == selected})
	}
	return out
}

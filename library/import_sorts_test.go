// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"fmt"
	"slices"
	"testing"
)

// A real library's djmdSort rows (MenuItemID, Seq, Disable): six hidden
// sorts at Seq 0, then the menu in order, with BPM (menu item 5) selected
// as the second column. The deck showed exactly this menu, in this order.
func TestImportSortsFollowsSeq(t *testing.T) {
	rows := []masterDBSort{
		{"21", 0, 1}, {"14", 0, 1}, {"8", 0, 1}, {"10", 0, 1}, {"15", 0, 1}, {"23", 0, 1},
		{"25", 1, 0}, {"26", 2, 0}, {"22", 3, 0}, {"2", 4, 0}, {"3", 5, 0}, {"5", 6, 2},
		{"6", 7, 0}, {"11", 8, 0}, {"13", 9, 0}, {"1", 10, 0}, {"9", 11, 0}, {"99", 12, 0},
	}
	var shown []uint32
	var selected []uint32
	for _, s := range importSorts(rows) {
		if s.Visible {
			shown = append(shown, s.ID)
		}
		if s.Selected {
			selected = append(selected, s.ID)
		}
	}
	if want := []uint32{0, 1, 17, 2, 3, 4, 5, 12, 15, 6, 10}; !slices.Equal(shown, want) {
		t.Errorf("shown sorts %v, want %v", shown, want)
	}
	if fmt.Sprint(selected) != "[4]" {
		t.Errorf("selected %v, want [4] (BPM)", selected)
	}
}

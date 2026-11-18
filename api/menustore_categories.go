// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
)

// menuKeyByItem maps rekordbox's menu items (djmdMenuItems IDs) to the root
// menu's keys: pdb.MenuCategoryByKey uses the same numbering, and DATE ADDED
// (menu item 22) has no PDB category.
var menuKeyByItem = func() map[uint32]string {
	m := map[uint32]string{22: "date_added"}
	for key, item := range pdb.MenuCategoryByKey {
		m[uint32(item)] = key
	}
	return m
}()

// ImportCategories adopts a rekordbox library's root menu (djmdCategory):
// its categories in its order, shown or hidden as rekordbox shows them.
// Locked categories stay shown, as rekordbox's own settings keep them, and
// categories the library has no row for follow, hidden.
func (ms *MenuStore) ImportCategories(imp []library.CategoryImport) {
	if len(imp) == 0 {
		return
	}
	defaults := make(map[string]MenuItem, len(defaultMenuItems))
	for _, d := range defaultMenuItems {
		defaults[d.Key] = d
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	// A label the user changed survives the import.
	labels := make(map[string]string, len(ms.items))
	for _, it := range ms.items {
		labels[it.Key] = it.Label
	}
	items := make([]MenuItem, 0, len(defaultMenuItems))
	seen := make(map[string]bool, len(imp))
	for _, c := range imp {
		key, ok := menuKeyByItem[c.MenuItem]
		d, known := defaults[key]
		if !ok || !known || seen[key] {
			continue
		}
		seen[key] = true
		d.Visible = c.Visible || d.Locked
		if l := labels[key]; l != "" {
			d.Label = l
		}
		items = append(items, d)
	}
	ms.items = items
	ms.mergeMissingLocked()
	for i := range ms.items {
		if ms.items[i].Locked {
			ms.items[i].Visible = true
		}
	}
	ms.saveLocked()
	dlog.Infof("api: imported rekordbox's root menu (%d categories, %d shown)", len(ms.items), countVisible(ms.items))
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"log"

	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/library"
)

// SortItem is one entry of the deck's SORT menu: a rekordbox sort ID (0
// DEFAULT, 1 ALPHABET, 2 ARTIST ... 17 DATE ADDED) and whether the menu
// shows it. The store keeps them in menu order; Label is filled in for the
// API and isn't stored.
type SortItem struct {
	ID      uint32 `json:"id"`
	Label   string `json:"label,omitempty"`
	Visible bool   `json:"visible"`
}

// defaultSortItems is rekordbox's out-of-the-box SORT menu, followed by the
// sorts it hides.
func defaultSortItems() []SortItem {
	var out []SortItem
	for _, e := range dbserver.DefaultSortMenu() {
		out = append(out, SortItem{ID: e.ID, Visible: true})
	}
	for _, id := range []uint32{7, 8, 9, 11, 13, 15} {
		out = append(out, SortItem{ID: id})
	}
	return out
}

// trackDetailBySort maps a sort ID to the track-detail column it selects:
// rekordbox keeps the track lists' second column as the djmdSort row marked
// selected.
var trackDetailBySort = map[uint32]string{
	2: "artist", 3: "album", 4: "bpm", 5: "rating", 6: "genre", 7: "comments", 8: "time",
	9: "remixer", 10: "label", 11: "original_artist", 12: "key", 13: "bitrate", 15: "color",
	16: "dj_play_count", 17: "date_added",
}

// normalizeSorts keeps known sort IDs once each, in order, and appends any
// missing ones hidden, so the stored list always covers every sort.
func normalizeSorts(in []SortItem) []SortItem {
	seen := map[uint32]bool{}
	var out []SortItem
	for _, s := range in {
		if _, _, ok := dbserver.SortSpecLabel(s.ID); !ok || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, SortItem{ID: s.ID, Visible: s.Visible})
	}
	for _, d := range defaultSortItems() {
		if !seen[d.ID] {
			out = append(out, SortItem{ID: d.ID})
		}
	}
	return out
}

// Sorts returns the SORT menu entries (shown and hidden) in menu order, with
// their labels.
func (ms *MenuStore) Sorts() []SortItem {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	out := make([]SortItem, len(ms.sorts))
	for i, s := range ms.sorts {
		label, _, _ := dbserver.SortSpecLabel(s.ID)
		out[i] = SortItem{ID: s.ID, Label: label, Visible: s.Visible}
	}
	return out
}

// SortMenu returns the shown SORT menu entries, in order, as the deck lists
// them.
func (ms *MenuStore) SortMenu() []dbserver.SortMenuEntry {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var out []dbserver.SortMenuEntry
	for _, s := range ms.sorts {
		if label, itemType, ok := dbserver.SortSpecLabel(s.ID); ok && s.Visible {
			out = append(out, dbserver.SortMenuEntry{ID: s.ID, Label: label, ItemType: itemType})
		}
	}
	return out
}

// ReplaceSorts sets the SORT menu's order and visibility. Unknown IDs are
// dropped and missing ones kept hidden; at least one entry must be shown.
func (ms *MenuStore) ReplaceSorts(items []SortItem) error {
	sorts := normalizeSorts(items)
	shown := 0
	for _, s := range sorts {
		if s.Visible {
			shown++
		}
	}
	if shown == 0 {
		return fmt.Errorf("the sort menu needs at least one shown entry")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.sorts = sorts
	ms.saveLocked()
	log.Printf("api: CDJ sort menu replaced — %d sorts (%d shown)", len(sorts), shown)
	return nil
}

// ImportSorts adopts a rekordbox library's SORT menu and its selected
// second column (djmdSort). A library with no selected column gets title-only
// rows, as rekordbox serves them.
func (ms *MenuStore) ImportSorts(imp []library.SortImport) {
	if len(imp) == 0 {
		return
	}
	items := make([]SortItem, 0, len(imp))
	column := "not_specified"
	for _, s := range imp {
		items = append(items, SortItem{ID: s.ID, Visible: s.Visible})
		if s.Selected {
			if key, ok := trackDetailBySort[s.ID]; ok {
				column = key
			}
		}
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.sorts = normalizeSorts(items)
	ms.trackDetail = column
	ms.saveLocked()
	log.Printf("api: imported rekordbox's sort menu (%d sorts) and track column %q", len(imp), column)
}

// ResetSorts restores rekordbox's default SORT menu.
func (ms *MenuStore) ResetSorts() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.sorts = defaultSortItems()
	ms.saveLocked()
	log.Printf("api: CDJ sort menu reset to defaults")
}

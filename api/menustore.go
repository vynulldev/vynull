// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"vynull/internal/fsutil"
	"vynull/pdb"
)

// MenuItem describes one entry in the CDJ's top-level LINK menu. The
// user can toggle Visible and reorder the list; everything else is
// determined server-side by which categories we know how to handle.
//
// ID and ItemType are the on-the-wire values the CDJ uses to identify
// the category — see python-prodj-link / rekordbox pcaps. Changing
// them silently here would break the menu drill-down, so the schema
// keeps them as authoritative server-side values rather than user-editable.
type MenuItem struct {
	Key      string `json:"key"`       // stable identifier (e.g. "artist") used in the API
	Label    string `json:"label"`     // display label shown on the CDJ
	ID       uint32 `json:"id"`        // category ID the CDJ uses in sub-queries
	ItemType uint32 `json:"item_type"` // menu ItemType byte (0x80 + slot)
	Visible  bool   `json:"visible"`
	Locked   bool   `json:"locked"` // true = always active, can't be hidden / deactivated
}

// defaultMenuItems lists every category the user can show or hide in the
// CDJ root menu. Order matches rekordbox's "Active Categories"
// default arrangement (see docs/reference/cdj-menu-config.md in the vynull-tools repo).
//
// Wire IDs and ItemTypes verified against rekordbox's root-menu
// response in a packet capture.
// The Locked flag marks rows rekordbox greys out (always active
// on the deck, can't be moved to the inactive list).
//
// DATE ADDED is intentionally absent — rekordbox doesn't surface
// it to the CDJ in any capture we have, and the wire opcode is unknown.
// If someone later figures it out, drop a new entry in here.
var defaultMenuItems = []MenuItem{
	{Key: "artist", Label: "ARTIST", ID: 2, ItemType: 0x81, Visible: true},
	{Key: "album", Label: "ALBUM", ID: 3, ItemType: 0x82, Visible: true},
	{Key: "track", Label: "TRACK", ID: 4, ItemType: 0x83, Visible: true, Locked: true},
	{Key: "key", Label: "KEY", ID: 12, ItemType: 0x8b, Visible: true},
	{Key: "playlist", Label: "PLAYLIST", ID: 5, ItemType: 0x84, Visible: true, Locked: true},
	{Key: "history", Label: "HISTORY", ID: 22, ItemType: 0x95, Visible: true, Locked: true},
	{Key: "search", Label: "SEARCH", ID: 18, ItemType: 0x91, Visible: true, Locked: true},
	{Key: "folder", Label: "FOLDER", ID: 13, ItemType: 0x8d, Visible: true, Locked: true},
	{Key: "bpm", Label: "BPM", ID: 6, ItemType: 0x85, Visible: true},
	{Key: "label", Label: "LABEL", ID: 10, ItemType: 0x89, Visible: true},
	{Key: "year", Label: "YEAR", ID: 8, ItemType: 0x87, Visible: true},
	{Key: "color", Label: "COLOR", ID: 15, ItemType: 0x8e, Visible: true},
	{Key: "file_name", Label: "FILE NAME", ID: 21, ItemType: 0x94, Visible: true},
	{Key: "hot_cue_bank", Label: "HOT CUE BANK", ID: 23, ItemType: 0x98, Visible: true},
	{Key: "rating", Label: "RATING", ID: 7, ItemType: 0x86, Visible: true},
	{Key: "time", Label: "TIME", ID: 19, ItemType: 0x92, Visible: true},
	// Inactive by default — present in rekordbox's "Inactive" list.
	{Key: "bitrate", Label: "BITRATE", ID: 20, ItemType: 0x93, Visible: false},
	{Key: "genre", Label: "GENRE", ID: 1, ItemType: 0x80, Visible: false},
	{Key: "matching", Label: "MATCHING", ID: 26, ItemType: 0xaa, Visible: false},
	{Key: "original_artist", Label: "ORIGINAL ARTIST", ID: 11, ItemType: 0x8a, Visible: false},
	{Key: "remixer", Label: "REMIXER", ID: 9, ItemType: 0x88, Visible: false},
}

// MenuStore persists the user's preferred order + visibility of the
// CDJ root menu, plus the choice of "track detail" column the CDJ
// shows next to each track title (BPM, key, artist, comment, etc.).
// Reads are O(n) but n=13, so callers don't need to cache the result.
type MenuStore struct {
	mu          sync.RWMutex
	dir         string
	items       []MenuItem
	trackDetail string // key from TrackDetailFields; default "bpm"
}

// TrackDetailFields lists every track-detail column the CDJ knows how
// to render in the second column of a track list. Each entry maps a
// stable key (used in the API + persistence) to the on-the-wire
// ItemType high byte and a human label for the UI dropdown.
//
// See the deepsymmetry beat-link project for the wire-format mapping —
// summary: ItemType = (highByte << 8) | 0x04, ParentID carries the
// raw value (hash, count, ID, BPM*100), Label2 carries the display
// string (CDJ renders some fields from raw value alone).
type TrackDetailField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	HighByte uint8  `json:"high_byte"`
}

var TrackDetailFields = []TrackDetailField{
	{Key: "bpm", Label: "BPM", HighByte: 0x0d},
	{Key: "artist", Label: "Artist", HighByte: 0x07},
	{Key: "album", Label: "Album", HighByte: 0x02},
	{Key: "genre", Label: "Genre", HighByte: 0x06},
	{Key: "key", Label: "Key", HighByte: 0x0f},
	{Key: "rating", Label: "Rating", HighByte: 0x0a},
	{Key: "time", Label: "Time", HighByte: 0x0b},
	{Key: "label", Label: "Record label", HighByte: 0x0e},
	{Key: "bitrate", Label: "Bitrate", HighByte: 0x10},
	{Key: "color", Label: "Track colour", HighByte: 0x17},
	{Key: "comments", Label: "Comment", HighByte: 0x23},
	{Key: "original_artist", Label: "Original artist", HighByte: 0x28},
	{Key: "remixer", Label: "Remixer", HighByte: 0x29},
	{Key: "dj_play_count", Label: "Play count", HighByte: 0x2a},
	{Key: "date_added", Label: "Date added", HighByte: 0x2e},
	{Key: "not_specified", Label: "(none)", HighByte: 0x00},
}

const defaultTrackDetail = "bpm"

// NewMenuStore loads menu.json from dir, or seeds it with the defaults
// (and writes the file) if it doesn't exist yet. This way a fresh
// install gets a complete editable starter set.
func NewMenuStore(dir string) *MenuStore {
	os.MkdirAll(dir, 0755)
	ms := &MenuStore{dir: dir}
	ms.load()
	if len(ms.items) == 0 {
		ms.items = append(ms.items, defaultMenuItems...)
		ms.saveLocked()
	} else {
		// Heal: if the on-disk set is missing any item we know how to
		// serve (e.g. a future version added one), append it as hidden
		// so the user can opt in without losing their existing order.
		ms.mergeMissingLocked()
	}
	if ms.trackDetail == "" || !isKnownTrackDetail(ms.trackDetail) {
		ms.trackDetail = defaultTrackDetail
	}
	return ms
}

func isKnownTrackDetail(key string) bool {
	for _, f := range TrackDetailFields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// TrackDetail returns the configured detail-column key (e.g. "bpm",
// "key", "artist"). Always one of TrackDetailFields[].Key.
func (ms *MenuStore) TrackDetail() string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.trackDetail
}

// SetTrackDetail replaces the active detail column. Rejected if the
// key isn't a known field — keeps the store honest so the dbserver
// never has to handle an unknown value.
func (ms *MenuStore) SetTrackDetail(key string) error {
	if !isKnownTrackDetail(key) {
		return fmt.Errorf("unknown track detail %q", key)
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.trackDetail = key
	ms.saveLocked()
	log.Printf("api: CDJ track-detail column set to %q", key)
	return nil
}

// All returns the configured items (visible + hidden) in order.
func (ms *MenuStore) All() []MenuItem {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	out := make([]MenuItem, len(ms.items))
	copy(out, ms.items)
	return out
}

// PDBMenuConfig converts the user's saved menu preferences into the
// PDB-internal format expected by pdb.GenerateWithOptions. Items the
// user has marked Visible become the menu's display-order list; any
// known PDB category not visible (or not in the user's list) goes to
// the Hidden bucket. Categories the MenuStore doesn't surface (e.g.
// DATE ADDED, CUE) keep their default visibility from pdb defaults.
func (ms *MenuStore) PDBMenuConfig() *pdb.MenuConfig {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	cfg := &pdb.MenuConfig{}
	seen := make(map[uint16]bool)
	for _, it := range ms.items {
		catID, ok := pdb.MenuCategoryByKey[it.Key]
		if !ok {
			continue
		}
		seen[catID] = true
		if it.Visible {
			cfg.Visible = append(cfg.Visible, catID)
		} else {
			cfg.Hidden = append(cfg.Hidden, catID)
		}
	}
	for _, catID := range pdb.MenuCategoryByKey {
		if !seen[catID] {
			cfg.Hidden = append(cfg.Hidden, catID)
		}
	}
	return cfg
}

// Visible returns only the items marked visible, in order. This is
// what the dbserver renders on the CDJ.
func (ms *MenuStore) Visible() []MenuItem {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	out := make([]MenuItem, 0, len(ms.items))
	for _, m := range ms.items {
		if m.Visible {
			out = append(out, m)
		}
	}
	return out
}

// Replace overwrites the configured menu with the supplied list. Keys
// are validated against the known defaults so the user can't smuggle
// in a category we don't serve; per-item Visible and order are
// preserved from the input. Unknown keys are dropped silently.
func (ms *MenuStore) Replace(items []MenuItem) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	known := make(map[string]MenuItem, len(defaultMenuItems))
	for _, d := range defaultMenuItems {
		known[d.Key] = d
	}
	out := make([]MenuItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		d, ok := known[it.Key]
		if !ok || seen[it.Key] {
			continue
		}
		seen[it.Key] = true
		// Keep server-side authoritative fields (Label/ID/ItemType/Locked)
		// but accept the user's Visible toggle. Locked items are forced
		// visible regardless of the request — they're always active.
		if d.Locked {
			d.Visible = true
		} else {
			d.Visible = it.Visible
		}
		// Honour user-edited label if provided — limited freedom, but
		// useful for translations / "TEMPO" instead of "BPM".
		if it.Label != "" {
			d.Label = it.Label
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return fmt.Errorf("no recognised menu keys")
	}
	ms.items = out
	ms.mergeMissingLocked()
	ms.saveLocked()
	log.Printf("api: CDJ menu replaced — %d items (%d visible)",
		len(ms.items), countVisible(ms.items))
	return nil
}

// ResetToDefaults restores the factory order + visibility.
func (ms *MenuStore) ResetToDefaults() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.items = append(ms.items[:0], defaultMenuItems...)
	ms.saveLocked()
	log.Printf("api: CDJ menu reset to defaults (%d items)", len(ms.items))
}

// mergeMissingLocked appends any defaultMenuItems whose key isn't
// already present, with Visible=false so the addition is opt-in. Called
// after load() and after Replace() to keep the on-disk set complete as
// the server gains new categories over time.
func (ms *MenuStore) mergeMissingLocked() {
	have := make(map[string]bool, len(ms.items))
	for _, m := range ms.items {
		have[m.Key] = true
	}
	for _, d := range defaultMenuItems {
		if have[d.Key] {
			continue
		}
		d.Visible = false
		ms.items = append(ms.items, d)
	}
}

func countVisible(items []MenuItem) int {
	n := 0
	for _, m := range items {
		if m.Visible {
			n++
		}
	}
	return n
}

// ── persistence ─────────────────────────────────────────────────────────

type menuFileData struct {
	Items       []MenuItem `json:"items"`
	TrackDetail string     `json:"track_detail"`
}

func (ms *MenuStore) saveLocked() {
	if ms.dir == "" {
		return
	}
	b, err := json.MarshalIndent(menuFileData{
		Items:       ms.items,
		TrackDetail: ms.trackDetail,
	}, "", "  ")
	if err != nil {
		log.Printf("menu: marshal: %v", err)
		return
	}
	path := filepath.Join(ms.dir, "menu.json")
	if err := fsutil.WriteFile(path, b, 0644); err != nil {
		log.Printf("menu: write %s: %v", path, err)
	}
}

func (ms *MenuStore) load() {
	if ms.dir == "" {
		return
	}
	path := filepath.Join(ms.dir, "menu.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // fresh install
	}
	var fd menuFileData
	if err := json.Unmarshal(data, &fd); err != nil {
		log.Printf("menu: parse %s: %v", path, err)
		return
	}
	// Reconcile against current defaults: ID / ItemType / Locked are
	// server-side authoritative wire fields, so reset them from the
	// current defaults on every load. The user only "owns" Visible,
	// Order, and (optionally) a custom Label. Without this, persisted
	// state from earlier builds keeps shipping stale wire IDs and the
	// deck silently refuses to drill into those categories (most
	// visibly: SEARCH returning to an empty screen instead of opening
	// the on-screen keyboard).
	known := make(map[string]MenuItem, len(defaultMenuItems))
	for _, d := range defaultMenuItems {
		known[d.Key] = d
	}
	ms.items = make([]MenuItem, 0, len(fd.Items))
	for _, persisted := range fd.Items {
		d, ok := known[persisted.Key]
		if !ok {
			continue // dropped category — skip
		}
		// Carry forward user-owned fields, refresh authoritative fields.
		if d.Locked {
			d.Visible = true
		} else {
			d.Visible = persisted.Visible
		}
		if persisted.Label != "" && persisted.Label != d.Label {
			d.Label = persisted.Label
		}
		ms.items = append(ms.items, d)
	}
	ms.trackDetail = fd.TrackDetail
	log.Printf("api: loaded %d CDJ menu items (%d visible, detail=%q)",
		len(ms.items), countVisible(ms.items), ms.trackDetail)
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"vynull/internal/fsutil"
	"vynull/library"
)

// PlaylistInfo is the API/persistence representation of a user playlist
// or playlist folder. Folders contain other playlists/folders (TrackIDs
// is empty); leaf playlists carry an ordered list of track IDs.
//
// Smart playlists (IsSmart=true) ignore TrackIDs and resolve tracks
// at read time by evaluating Rules against the library. They can't be
// edited via SetTracks; the API rejects that path with an error.
//
// Mirrors the columns in the PDB playlist_tree table so we can lift the
// store contents straight into the dbserver browse menu and into a real-
// rekordbox-style USB export later.
type PlaylistInfo struct {
	ID        uint32      `json:"id"`
	Name      string      `json:"name"`
	ParentID  uint32      `json:"parent_id"` // 0 = root
	IsFolder  bool        `json:"is_folder"`
	SortOrder int         `json:"sort_order"` // within parent; lower = first
	TrackIDs  []uint32    `json:"track_ids"`  // ordered; empty for folders and smart playlists
	IsSmart   bool        `json:"is_smart,omitempty"`
	Rules     *SmartRules `json:"rules,omitempty"` // present when IsSmart
}

// PlaylistStore persists user-defined playlists + folders to a single
// JSON file. Thread-safe. The structure intentionally mirrors what we
// need for the PDB playlist_tree + playlist_entries tables so wiring
// it into the dbserver in phase 2 is mostly a translation step.
type PlaylistStore struct {
	mu        sync.RWMutex
	dir       string
	playlists []*PlaylistInfo
	nextID    uint32
}

// NewPlaylistStore creates a store backed by dir/playlists.json.
// Loads any existing file on construction so playlists survive restart.
func NewPlaylistStore(dir string) *PlaylistStore {
	os.MkdirAll(dir, 0755)
	ps := &PlaylistStore{
		dir:    dir,
		nextID: 1,
	}
	ps.load()
	return ps
}

// All returns a snapshot of every playlist + folder, sorted by parent
// then sort_order. The web UI builds the tree client-side from this
// flat list.
func (ps *PlaylistStore) All() []*PlaylistInfo {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]*PlaylistInfo, len(ps.playlists))
	for i, p := range ps.playlists {
		// Shallow copy so callers can't mutate our state by accident.
		cp := *p
		cp.TrackIDs = append([]uint32(nil), p.TrackIDs...)
		out[i] = &cp
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ParentID != out[j].ParentID {
			return out[i].ParentID < out[j].ParentID
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out
}

// Get returns one playlist by ID, or nil.
func (ps *PlaylistStore) Get(id uint32) *PlaylistInfo {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for _, p := range ps.playlists {
		if p.ID == id {
			cp := *p
			cp.TrackIDs = append([]uint32(nil), p.TrackIDs...)
			return &cp
		}
	}
	return nil
}

// Create adds a new playlist or folder under parentID and returns it.
// parentID==0 places it at the root. Returns an error if parentID
// references a non-folder or a non-existent entry.
func (ps *PlaylistStore) Create(name string, parentID uint32, isFolder bool) (*PlaylistInfo, error) {
	return ps.create(name, parentID, isFolder, false, nil)
}

// CreateSmart adds a smart playlist whose tracks are computed by
// evaluating rules against the library at read time. Folders can't be
// smart; isFolder is fixed to false.
func (ps *PlaylistStore) CreateSmart(name string, parentID uint32, rules *SmartRules) (*PlaylistInfo, error) {
	return ps.create(name, parentID, false, true, rules)
}

func (ps *PlaylistStore) create(name string, parentID uint32, isFolder, isSmart bool, rules *SmartRules) (*PlaylistInfo, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if parentID != 0 {
		parent := ps.find(parentID)
		if parent == nil {
			return nil, fmt.Errorf("parent %d not found", parentID)
		}
		if !parent.IsFolder {
			return nil, fmt.Errorf("parent %d is not a folder", parentID)
		}
	}
	p := &PlaylistInfo{
		ID:        ps.nextID,
		Name:      name,
		ParentID:  parentID,
		IsFolder:  isFolder,
		SortOrder: ps.nextSortOrderLocked(parentID),
		IsSmart:   isSmart,
		Rules:     rules,
	}
	ps.nextID++
	ps.playlists = append(ps.playlists, p)
	ps.saveLocked()
	kind := typeName(isFolder)
	if isSmart {
		kind = "smart playlist"
	}
	log.Printf("api: created %s #%d %q (parent=%d)", kind, p.ID, name, parentID)
	cp := *p
	return &cp, nil
}

// SetRules replaces the rule tree on a smart playlist. Returns an
// error on a folder or a regular (non-smart) playlist.
func (ps *PlaylistStore) SetRules(id uint32, rules *SmartRules) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := ps.find(id)
	if p == nil {
		return fmt.Errorf("playlist %d not found", id)
	}
	if !p.IsSmart {
		return fmt.Errorf("playlist %d is not a smart playlist", id)
	}
	p.Rules = rules
	ps.saveLocked()
	return nil
}

// Rename updates the display name.
func (ps *PlaylistStore) Rename(id uint32, name string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := ps.find(id)
	if p == nil {
		return fmt.Errorf("playlist %d not found", id)
	}
	p.Name = name
	ps.saveLocked()
	return nil
}

// Move changes parent + sort order. parentID==0 moves to root.
// Order==-1 means "append to end of new parent".
func (ps *PlaylistStore) Move(id uint32, parentID uint32, order int) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := ps.find(id)
	if p == nil {
		return fmt.Errorf("playlist %d not found", id)
	}
	if parentID == id {
		return fmt.Errorf("cannot move playlist into itself")
	}
	if parentID != 0 {
		parent := ps.find(parentID)
		if parent == nil {
			return fmt.Errorf("parent %d not found", parentID)
		}
		if !parent.IsFolder {
			return fmt.Errorf("parent %d is not a folder", parentID)
		}
		// Reject moves that would create a cycle (moving a folder into
		// its own descendant).
		if ps.isDescendant(parentID, id) {
			return fmt.Errorf("cannot move folder into its own descendant")
		}
	}
	p.ParentID = parentID
	if order < 0 {
		p.SortOrder = ps.nextSortOrderLocked(parentID)
	} else {
		p.SortOrder = order
	}
	ps.saveLocked()
	return nil
}

// Delete removes a playlist. If it's a folder, every descendant
// playlist + folder is removed too.
func (ps *PlaylistStore) Delete(id uint32) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.find(id) == nil {
		return fmt.Errorf("playlist %d not found", id)
	}
	// Collect this ID and every descendant.
	toRemove := map[uint32]bool{id: true}
	for {
		grew := false
		for _, p := range ps.playlists {
			if toRemove[p.ParentID] && !toRemove[p.ID] {
				toRemove[p.ID] = true
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	kept := ps.playlists[:0]
	for _, p := range ps.playlists {
		if !toRemove[p.ID] {
			kept = append(kept, p)
		}
	}
	ps.playlists = kept
	ps.saveLocked()
	log.Printf("api: deleted playlist %d (%d removed including descendants)", id, len(toRemove))
	return nil
}

// SetTracks replaces a leaf playlist's ordered track list. Folders
// and smart playlists can't carry an explicit track list and return
// an error.
func (ps *PlaylistStore) SetTracks(id uint32, trackIDs []uint32) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := ps.find(id)
	if p == nil {
		return fmt.Errorf("playlist %d not found", id)
	}
	if p.IsFolder {
		return fmt.Errorf("playlist %d is a folder", id)
	}
	if p.IsSmart {
		return fmt.Errorf("playlist %d is a smart playlist (edit rules, not tracks)", id)
	}
	p.TrackIDs = append([]uint32(nil), trackIDs...)
	ps.saveLocked()
	log.Printf("api: playlist %d (%q) now has %d tracks", id, p.Name, len(trackIDs))
	return nil
}

// Tracks returns a regular playlist's stored track IDs. For smart
// playlists call TracksFor with a library + tag context to evaluate
// the rules — Tracks() returns nil for smart since there's no static
// list to hand back.
func (ps *PlaylistStore) Tracks(id uint32) []uint32 {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	p := ps.find(id)
	if p == nil || p.IsFolder || p.IsSmart {
		return nil
	}
	return append([]uint32(nil), p.TrackIDs...)
}

// RemoveTrackFromAll drops trackID from every (non-smart, non-folder)
// playlist it appears in and persists. Used when the track is being
// deleted from the library so playlists don't keep dangling references.
// Returns the number of playlists touched.
func (ps *PlaylistStore) RemoveTrackFromAll(trackID uint32) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	touched := 0
	for _, p := range ps.playlists {
		if p.IsFolder || p.IsSmart {
			continue
		}
		kept := p.TrackIDs[:0]
		removed := false
		for _, id := range p.TrackIDs {
			if id == trackID {
				removed = true
				continue
			}
			kept = append(kept, id)
		}
		if removed {
			p.TrackIDs = kept
			touched++
		}
	}
	if touched > 0 {
		ps.saveLocked()
	}
	return touched
}

// TracksFor resolves a playlist's tracks, dispatching to rule
// evaluation when IsSmart. lib supplies the universe of candidate
// tracks; tags is used by tag conditions (may be nil). For regular
// playlists this is equivalent to Tracks() — same return shape so
// callers can use this method uniformly.
func (ps *PlaylistStore) TracksFor(id uint32, lib *library.Library, tags TagLookup) []uint32 {
	ps.mu.RLock()
	p := ps.find(id)
	if p == nil || p.IsFolder {
		ps.mu.RUnlock()
		return nil
	}
	if !p.IsSmart {
		out := append([]uint32(nil), p.TrackIDs...)
		ps.mu.RUnlock()
		return out
	}
	rules := p.Rules
	ps.mu.RUnlock()

	if lib == nil {
		return nil
	}
	now := time.Now()
	all := lib.Tracks()
	out := make([]uint32, 0, len(all))
	for _, t := range all {
		if rules.Match(t, tags, now) {
			out = append(out, t.ID)
		}
	}
	return out
}

// Children returns immediate children of parentID (0 = root) sorted by
// sort_order. Used by the dbserver in phase 2 to populate the CDJ
// PLAYLIST menu.
func (ps *PlaylistStore) Children(parentID uint32) []*PlaylistInfo {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	var out []*PlaylistInfo
	for _, p := range ps.playlists {
		if p.ParentID == parentID {
			cp := *p
			cp.TrackIDs = append([]uint32(nil), p.TrackIDs...)
			out = append(out, &cp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out
}

// ── internal helpers ────────────────────────────────────────────────────

func (ps *PlaylistStore) find(id uint32) *PlaylistInfo {
	for _, p := range ps.playlists {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (ps *PlaylistStore) nextSortOrderLocked(parentID uint32) int {
	max := 0
	for _, p := range ps.playlists {
		if p.ParentID == parentID && p.SortOrder >= max {
			max = p.SortOrder + 1
		}
	}
	return max
}

// isDescendant reports whether candidate is anywhere under root in the
// folder tree. Used to block self-referential moves.
func (ps *PlaylistStore) isDescendant(candidate, root uint32) bool {
	for {
		p := ps.find(candidate)
		if p == nil || p.ParentID == 0 {
			return false
		}
		if p.ParentID == root {
			return true
		}
		candidate = p.ParentID
	}
}

func typeName(isFolder bool) string {
	if isFolder {
		return "folder"
	}
	return "playlist"
}

// ── persistence ─────────────────────────────────────────────────────────

type playlistFileData struct {
	NextID    uint32          `json:"next_id"`
	Playlists []*PlaylistInfo `json:"playlists"`
}

func (ps *PlaylistStore) saveLocked() {
	if ps.dir == "" {
		return
	}
	data := playlistFileData{
		NextID:    ps.nextID,
		Playlists: ps.playlists,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Printf("playlists: marshal error: %v", err)
		return
	}
	path := filepath.Join(ps.dir, "playlists.json")
	if err := fsutil.WriteFile(path, b, 0644); err != nil {
		log.Printf("playlists: write %s: %v", path, err)
	}
}

func (ps *PlaylistStore) load() {
	if ps.dir == "" {
		return
	}
	path := filepath.Join(ps.dir, "playlists.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // first run, no file yet
	}
	var fd playlistFileData
	if err := json.Unmarshal(data, &fd); err != nil {
		log.Printf("playlists: parse error: %v", err)
		return
	}
	ps.playlists = fd.Playlists
	if fd.NextID > 0 {
		ps.nextID = fd.NextID
	}
	log.Printf("api: loaded %d playlists/folders", len(ps.playlists))
}

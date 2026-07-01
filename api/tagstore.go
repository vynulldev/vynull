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
)

// TagCategoryInfo represents a tag category.
type TagCategoryInfo struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

// TagInfo represents a user-defined tag.
type TagInfo struct {
	ID         uint32 `json:"id"`
	Name       string `json:"name"`
	CategoryID uint32 `json:"category_id"`
	// Color is a palette index shared with hot-cues / track colours
	// (see CUE_COLORS in api/web/index.html). 0 means "no colour".
	Color uint8 `json:"color,omitempty"`
}

// TagStoreInterface abstracts the tag store for the API layer.
type TagStoreInterface interface {
	GetAllCategories() []TagCategoryInfo
	CreateCategory(name string) (uint32, error)
	RenameCategory(id uint32, name string) error
	DeleteCategory(id uint32)
	GetAllTags() []TagInfo
	CreateTag(name string, categoryID uint32) (uint32, error)
	RenameTag(id uint32, name string) error
	SetTagCategory(id uint32, categoryID uint32)
	SetTagColor(id uint32, colorID uint8) error
	DeleteTag(id uint32)
	GetTagsForTrack(trackID uint32) []TagInfo
	SetTagsForTrack(trackID uint32, tagIDs []uint32)
	SetTrackColor(trackID uint32, colorID uint8)
	GetTrackColor(trackID uint32) uint8
	SetTrackRating(trackID uint32, rating uint8)
	GetTrackRating(trackID uint32) uint8
	RemoveAllTrackData(trackID uint32)
	BeginBatch()
	EndBatch()
}

// TagStore persists tags, categories, and track-tag assignments to JSON files.
type TagStore struct {
	mu          sync.RWMutex
	dir         string
	categories  []TagCategoryInfo
	tags        []TagInfo
	trackTags   map[uint32][]uint32 // trackID → tag IDs
	trackColor  map[uint32]uint8    // trackID → color_id
	trackRating map[uint32]uint8    // trackID → rating (1-5; 0 = unset)
	nextCatID   uint32
	nextTagID   uint32
	batch       bool // when true, save* are deferred until EndBatch (bulk import)
}

// BeginBatch suppresses per-mutation disk writes until EndBatch — used by the
// importer, which would otherwise rewrite the whole tag/color JSON once per
// assignment (O(n²) writes). EndBatch persists everything once.
func (ts *TagStore) BeginBatch() {
	ts.mu.Lock()
	ts.batch = true
	ts.mu.Unlock()
}

func (ts *TagStore) EndBatch() {
	ts.mu.Lock()
	ts.batch = false
	ts.saveAll() // tags.json + track_tags.json
	ts.saveColors()
	ts.saveRatings()
	ts.mu.Unlock()
}

// NewTagStore creates a tag store that persists to the given directory.
func NewTagStore(dir string) *TagStore {
	os.MkdirAll(dir, 0755)
	ts := &TagStore{
		dir:         dir,
		trackTags:   make(map[uint32][]uint32),
		trackColor:  make(map[uint32]uint8),
		trackRating: make(map[uint32]uint8),
		nextCatID:   1,
		nextTagID:   1,
	}
	ts.load()
	return ts
}

// ── Categories ──────────────────────────────────────────────────────────

func (ts *TagStore) GetAllCategories() []TagCategoryInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]TagCategoryInfo, len(ts.categories))
	copy(out, ts.categories)
	return out
}

func (ts *TagStore) CreateCategory(name string) (uint32, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, c := range ts.categories {
		if c.Name == name {
			return c.ID, nil
		}
	}
	id := ts.nextCatID
	ts.nextCatID++
	ts.categories = append(ts.categories, TagCategoryInfo{ID: id, Name: name})
	ts.saveAll()
	log.Printf("api: created tag category #%d %q", id, name)
	return id, nil
}

func (ts *TagStore) RenameCategory(id uint32, name string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, c := range ts.categories {
		if c.ID == id {
			ts.categories[i].Name = name
			ts.saveAll()
			return nil
		}
	}
	return fmt.Errorf("category %d not found", id)
}

func (ts *TagStore) DeleteCategory(id uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, c := range ts.categories {
		if c.ID == id {
			ts.categories = append(ts.categories[:i], ts.categories[i+1:]...)
			break
		}
	}
	// Move tags to uncategorized
	for i, t := range ts.tags {
		if t.CategoryID == id {
			ts.tags[i].CategoryID = 0
		}
	}
	ts.saveAll()
}

// ── Tags ────────────────────────────────────────────────────────────────

func (ts *TagStore) GetAllTags() []TagInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]TagInfo, len(ts.tags))
	copy(out, ts.tags)
	return out
}

func (ts *TagStore) CreateTag(name string, categoryID uint32) (uint32, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, t := range ts.tags {
		if t.Name == name && t.CategoryID == categoryID {
			return t.ID, nil
		}
	}
	id := ts.nextTagID
	ts.nextTagID++
	ts.tags = append(ts.tags, TagInfo{ID: id, Name: name, CategoryID: categoryID})
	ts.saveAll()
	log.Printf("api: created tag #%d %q (category %d)", id, name, categoryID)
	return id, nil
}

func (ts *TagStore) RenameTag(id uint32, name string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.tags {
		if t.ID == id {
			ts.tags[i].Name = name
			ts.saveAll()
			return nil
		}
	}
	return fmt.Errorf("tag %d not found", id)
}

func (ts *TagStore) SetTagCategory(id uint32, categoryID uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.tags {
		if t.ID == id {
			ts.tags[i].CategoryID = categoryID
			ts.saveAll()
			return
		}
	}
}

func (ts *TagStore) SetTagColor(id uint32, colorID uint8) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.tags {
		if t.ID == id {
			ts.tags[i].Color = colorID
			ts.saveAll()
			return nil
		}
	}
	return fmt.Errorf("tag %d not found", id)
}

func (ts *TagStore) DeleteTag(id uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.tags {
		if t.ID == id {
			ts.tags = append(ts.tags[:i], ts.tags[i+1:]...)
			break
		}
	}
	for trackID, tagIDs := range ts.trackTags {
		filtered := tagIDs[:0]
		for _, tid := range tagIDs {
			if tid != id {
				filtered = append(filtered, tid)
			}
		}
		if len(filtered) == 0 {
			delete(ts.trackTags, trackID)
		} else {
			ts.trackTags[trackID] = filtered
		}
	}
	ts.saveAll()
}

// ── Track assignments ───────────────────────────────────────────────────

func (ts *TagStore) GetTagsForTrack(trackID uint32) []TagInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	tagIDs := ts.trackTags[trackID]
	var out []TagInfo
	for _, id := range tagIDs {
		for _, t := range ts.tags {
			if t.ID == id {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func (ts *TagStore) SetTagsForTrack(trackID uint32, tagIDs []uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(tagIDs) == 0 {
		delete(ts.trackTags, trackID)
	} else {
		ts.trackTags[trackID] = tagIDs
	}
	ts.saveTrackTags()
	log.Printf("api: set %d tags for track %d", len(tagIDs), trackID)
}

func (ts *TagStore) SetTrackColor(trackID uint32, colorID uint8) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if colorID == 0 {
		delete(ts.trackColor, trackID)
	} else {
		ts.trackColor[trackID] = colorID
	}
	ts.saveColors()
}

func (ts *TagStore) GetTrackColor(trackID uint32) uint8 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.trackColor[trackID]
}

func (ts *TagStore) SetTrackRating(trackID uint32, rating uint8) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if rating > 5 {
		rating = 5
	}
	if rating == 0 {
		delete(ts.trackRating, trackID)
	} else {
		ts.trackRating[trackID] = rating
	}
	ts.saveRatings()
}

func (ts *TagStore) GetTrackRating(trackID uint32) uint8 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.trackRating[trackID]
}

// RemoveAllTrackData wipes a track's color, rating, and tag-assignment
// data and persists. Used when the track is being deleted from the
// library so we don't leave dangling metadata behind.
func (ts *TagStore) RemoveAllTrackData(trackID uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	hadColor := false
	hadRating := false
	hadTags := false
	if _, ok := ts.trackColor[trackID]; ok {
		delete(ts.trackColor, trackID)
		hadColor = true
	}
	if _, ok := ts.trackRating[trackID]; ok {
		delete(ts.trackRating, trackID)
		hadRating = true
	}
	if _, ok := ts.trackTags[trackID]; ok {
		delete(ts.trackTags, trackID)
		hadTags = true
	}
	if hadColor {
		ts.saveColors()
	}
	if hadRating {
		ts.saveRatings()
	}
	if hadTags {
		ts.saveTrackTags()
	}
}

// ── Persistence ─────────────────────────────────────────────────────────

type storeFileData struct {
	NextCatID  uint32            `json:"next_cat_id"`
	NextTagID  uint32            `json:"next_tag_id"`
	Categories []TagCategoryInfo `json:"categories"`
	Tags       []TagInfo         `json:"tags"`
}

func (ts *TagStore) saveAll() {
	if ts.batch {
		return
	}
	data := storeFileData{
		NextCatID:  ts.nextCatID,
		NextTagID:  ts.nextTagID,
		Categories: ts.categories,
		Tags:       ts.tags,
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	fsutil.WriteFile(filepath.Join(ts.dir, "tags.json"), b, 0644)
	ts.saveTrackTags()
}

func (ts *TagStore) saveTrackTags() {
	if ts.batch {
		return
	}
	b, _ := json.MarshalIndent(ts.trackTags, "", "  ")
	fsutil.WriteFile(filepath.Join(ts.dir, "track_tags.json"), b, 0644)
}

func (ts *TagStore) saveColors() {
	if ts.batch {
		return
	}
	b, _ := json.MarshalIndent(ts.trackColor, "", "  ")
	fsutil.WriteFile(filepath.Join(ts.dir, "track_colors.json"), b, 0644)
}

func (ts *TagStore) saveRatings() {
	if ts.batch {
		return
	}
	b, _ := json.MarshalIndent(ts.trackRating, "", "  ")
	fsutil.WriteFile(filepath.Join(ts.dir, "track_ratings.json"), b, 0644)
}

func (ts *TagStore) load() {
	if data, err := os.ReadFile(filepath.Join(ts.dir, "tags.json")); err == nil {
		var fd storeFileData
		if json.Unmarshal(data, &fd) == nil {
			ts.categories = fd.Categories
			ts.tags = fd.Tags
			ts.nextCatID = fd.NextCatID
			ts.nextTagID = fd.NextTagID
			if ts.nextCatID == 0 {
				ts.nextCatID = 1
			}
			if ts.nextTagID == 0 {
				ts.nextTagID = 1
			}
		} else {
			// Try old format (just tags, no categories)
			type oldFormat struct {
				NextID uint32    `json:"next_id"`
				Tags   []TagInfo `json:"tags"`
			}
			var old oldFormat
			if json.Unmarshal(data, &old) == nil {
				ts.tags = old.Tags
				ts.nextTagID = old.NextID
				if ts.nextTagID == 0 {
					ts.nextTagID = 1
				}
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(ts.dir, "track_tags.json")); err == nil {
		json.Unmarshal(data, &ts.trackTags)
	}
	if ts.trackTags == nil {
		ts.trackTags = make(map[uint32][]uint32)
	}

	if data, err := os.ReadFile(filepath.Join(ts.dir, "track_colors.json")); err == nil {
		json.Unmarshal(data, &ts.trackColor)
	}
	if ts.trackColor == nil {
		ts.trackColor = make(map[uint32]uint8)
	}

	if data, err := os.ReadFile(filepath.Join(ts.dir, "track_ratings.json")); err == nil {
		json.Unmarshal(data, &ts.trackRating)
	}
	if ts.trackRating == nil {
		ts.trackRating = make(map[uint32]uint8)
	}

	log.Printf("api: loaded %d categories, %d tags, %d track-tag assignments, %d track colors, %d track ratings",
		len(ts.categories), len(ts.tags), len(ts.trackTags), len(ts.trackColor), len(ts.trackRating))
}

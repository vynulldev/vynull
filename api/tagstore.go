// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/vynulldev/vynull/internal/fsutil"
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
	RemoveAllTrackData(trackID uint32)
	BeginBatch()
	EndBatch()
}

// TagStore persists tags, categories, and track-tag assignments to JSON files.
type TagStore struct {
	mu         sync.RWMutex
	dir        string
	categories []TagCategoryInfo
	tags       []TagInfo
	trackTags  map[uint32][]uint32 // trackID → tag IDs
	nextCatID  uint32
	nextTagID  uint32
	batch      bool // when true, save* are deferred until EndBatch (bulk import)
}

// BeginBatch suppresses per-mutation disk writes until EndBatch — used by the
// importer, which would otherwise rewrite the whole tag JSON once per
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
	ts.mu.Unlock()
}

// NewTagStore creates a tag store that persists to the given directory.
func NewTagStore(dir string) *TagStore {
	os.MkdirAll(dir, 0755)
	ts := &TagStore{
		dir:       dir,
		trackTags: make(map[uint32][]uint32),
		nextCatID: 1,
		nextTagID: 1,
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

// TrackTagIDs returns the IDs of the tags assigned to a track.
func (ts *TagStore) TrackTagIDs(trackID uint32) []uint32 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]uint32(nil), ts.trackTags[trackID]...)
}

// TagTrackCounts returns how many tracks carry each tag.
func (ts *TagStore) TagTrackCounts() map[uint32]int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	counts := make(map[uint32]int)
	for _, ids := range ts.trackTags {
		for _, id := range ids {
			counts[id]++
		}
	}
	return counts
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

// Track colours and ratings used to be kept here too, in track_colors.json
// and track_ratings.json, beside a second copy in the library; the library
// now owns them. TakeLegacyColorsAndRatings hands any still on disk to the
// caller to move into the library once.

// TakeLegacyColorsAndRatings returns the track colours and ratings an older
// version kept in this store, then renames their files with a .migrated
// suffix so they are taken only once (and kept as a backup). Missing files
// give empty maps.
func (ts *TagStore) TakeLegacyColorsAndRatings() (colors, ratings map[uint32]uint8) {
	read := func(name string) map[uint32]uint8 {
		path := filepath.Join(ts.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		m := map[uint32]uint8{}
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("tags: %s: %v (left in place)", path, err)
			return nil
		}
		if err := os.Rename(path, path+".migrated"); err != nil {
			log.Printf("tags: %s: %v", path, err)
		}
		return m
	}
	return read("track_colors.json"), read("track_ratings.json")
}

// RemoveAllTrackData wipes a track's tag assignments and persists. Used when the track is being deleted from the
// library so we don't leave dangling metadata behind.
func (ts *TagStore) RemoveAllTrackData(trackID uint32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if _, ok := ts.trackTags[trackID]; ok {
		delete(ts.trackTags, trackID)
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

	log.Printf("api: loaded %d categories, %d tags, %d track-tag assignments",
		len(ts.categories), len(ts.tags), len(ts.trackTags))
}

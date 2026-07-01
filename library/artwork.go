// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"vynull/internal/fsutil"
)

// Artwork holds a cached piece of album artwork.
type Artwork struct {
	ID       uint32
	MIMEType string
	Data     []byte
}

// ArtworkCache deduplicates artwork by content hash.
// When a cacheDir is set, artwork is persisted to disk and loaded on-demand.
type ArtworkCache struct {
	mu       sync.RWMutex
	byID     map[uint32]*Artwork
	byHash   map[[32]byte]uint32
	nextID   uint32
	cacheDir string // if set, persist artwork to this directory
}

// NewArtworkCache creates a new empty artwork cache.
func NewArtworkCache() *ArtworkCache {
	return &ArtworkCache{
		byID:   make(map[uint32]*Artwork),
		byHash: make(map[[32]byte]uint32),
		nextID: 1,
	}
}

// SetCacheDir enables disk persistence for artwork.
// Existing artwork on disk is indexed (not loaded into memory until requested).
func (c *ArtworkCache) SetCacheDir(dir string) {
	os.MkdirAll(dir, 0755)
	c.cacheDir = dir
	c.loadIndex()
}

// Add stores artwork data and returns its ID. If identical artwork was
// already stored, the existing ID is returned.
func (c *ArtworkCache) Add(mimeType string, data []byte) uint32 {
	hash := sha256.Sum256(data)

	c.mu.Lock()
	defer c.mu.Unlock()

	if id, ok := c.byHash[hash]; ok {
		return id
	}

	id := c.nextID
	c.nextID++
	c.byID[id] = &Artwork{
		ID:       id,
		MIMEType: mimeType,
		Data:     append([]byte(nil), data...),
	}
	c.byHash[hash] = id

	// Persist to disk.
	if c.cacheDir != "" {
		c.saveToDisk(id, data)
	}

	return id
}

// AddWithID stores artwork with a specific ID (for analysis-extracted artwork).
func (c *ArtworkCache) AddWithID(id uint32, mimeType string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = &Artwork{
		ID:       id,
		MIMEType: mimeType,
		Data:     append([]byte(nil), data...),
	}
	if id >= c.nextID {
		c.nextID = id + 1
	}
	if c.cacheDir != "" {
		c.saveToDisk(id, data)
	}
}

// Get retrieves artwork by ID. Loads from disk cache if not in memory.
func (c *ArtworkCache) Get(id uint32) *Artwork {
	c.mu.RLock()
	art := c.byID[id]
	c.mu.RUnlock()

	if art != nil {
		// Load data from disk if we only have the index entry (nil Data).
		if art.Data == nil && c.cacheDir != "" {
			data := c.loadFromDisk(id)
			if data != nil {
				c.mu.Lock()
				art.Data = data
				c.mu.Unlock()
			}
		}
		return art
	}
	return nil
}

// Count returns the number of artwork entries.
func (c *ArtworkCache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byID)
}

// ── Disk persistence ──────────────────────────────────────────────────

func (c *ArtworkCache) artPath(id uint32) string {
	return filepath.Join(c.cacheDir, fmt.Sprintf("art_%d.jpg", id))
}

func (c *ArtworkCache) saveToDisk(id uint32, data []byte) {
	path := c.artPath(id)
	if err := fsutil.WriteFile(path, data, 0644); err != nil {
		log.Printf("artwork-cache: write %s: %v", path, err)
	}
}

func (c *ArtworkCache) loadFromDisk(id uint32) []byte {
	path := c.artPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// loadIndex scans the cache directory and creates index entries for existing artwork.
// Artwork data is NOT loaded into memory — it's loaded on-demand via Get().
func (c *ArtworkCache) loadIndex() {
	entries, err := os.ReadDir(c.cacheDir)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var id uint32
		if _, err := fmt.Sscanf(e.Name(), "art_%d.jpg", &id); err != nil {
			continue
		}
		if c.byID[id] == nil {
			// Create index entry with nil Data — loaded on-demand.
			c.byID[id] = &Artwork{
				ID:       id,
				MIMEType: "image/jpeg",
			}
			count++
		}
		if id >= c.nextID {
			c.nextID = id + 1
		}
	}

	if count > 0 {
		log.Printf("artwork-cache: indexed %d cached artworks from %s", count, c.cacheDir)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/vynulldev/vynull/internal/fsutil"
)

// PlayMarks records tracks a deck has marked unplayed (the deck's
// CMD_DEL_HISTORY_TRACK, 0x3401). Vynull counts a track as played on the
// link when its History lists it; a mark overrides that without touching
// the History playlists, until the track is played again. Persisted as
// unplayed.json in the data directory.
type PlayMarks struct {
	mu       sync.Mutex
	path     string
	unplayed map[uint32]bool
}

// NewPlayMarks loads the marks from dir (empty if none are saved yet).
func NewPlayMarks(dir string) *PlayMarks {
	pm := &PlayMarks{unplayed: map[uint32]bool{}}
	if dir == "" {
		return pm
	}
	pm.path = filepath.Join(dir, "unplayed.json")
	if data, err := os.ReadFile(pm.path); err == nil {
		var ids []uint32
		if err := json.Unmarshal(data, &ids); err != nil {
			log.Printf("playmarks: parse %s: %v", pm.path, err)
		}
		for _, id := range ids {
			pm.unplayed[id] = true
		}
	}
	return pm
}

// IsUnplayed reports whether a deck has marked the track unplayed since it
// was last played.
func (pm *PlayMarks) IsUnplayed(trackID uint32) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.unplayed[trackID]
}

// MarkUnplayed records a deck's request to mark the track unplayed.
func (pm *PlayMarks) MarkUnplayed(trackID uint32) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.unplayed[trackID] {
		pm.unplayed[trackID] = true
		pm.saveLocked()
	}
}

// Played clears the track's unplayed mark: it has been played again.
func (pm *PlayMarks) Played(trackID uint32) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.unplayed[trackID] {
		delete(pm.unplayed, trackID)
		pm.saveLocked()
	}
}

func (pm *PlayMarks) saveLocked() {
	if pm.path == "" {
		return
	}
	ids := make([]uint32, 0, len(pm.unplayed))
	for id := range pm.unplayed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	b, _ := json.Marshal(ids)
	if err := fsutil.WriteFile(pm.path, b, 0o644); err != nil {
		log.Printf("playmarks: write %s: %v", pm.path, err)
	}
}

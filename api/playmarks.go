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

// PlayMarks records tracks a deck has marked played or unplayed from a
// track's menu (CMD_INSERT_HISTORY 0x3001 and CMD_DEL_HISTORY_TRACK 0x3401).
// Vynull counts a track as played on the link when its History lists it; a
// mark overrides that without touching the History playlists, until the
// track is next played. Persisted as playmarks.json in the data directory.
type PlayMarks struct {
	mu    sync.Mutex
	path  string
	marks map[uint32]bool // track ID -> marked played (true) or unplayed (false)
}

type playMarksFile struct {
	Played   []uint32 `json:"played"`
	Unplayed []uint32 `json:"unplayed"`
}

// NewPlayMarks loads the marks from dir (empty if none are saved yet).
func NewPlayMarks(dir string) *PlayMarks {
	pm := &PlayMarks{marks: map[uint32]bool{}}
	if dir == "" {
		return pm
	}
	pm.path = filepath.Join(dir, "playmarks.json")
	if data, err := os.ReadFile(pm.path); err == nil {
		var f playMarksFile
		if err := json.Unmarshal(data, &f); err != nil {
			log.Printf("playmarks: parse %s: %v", pm.path, err)
		}
		for _, id := range f.Played {
			pm.marks[id] = true
		}
		for _, id := range f.Unplayed {
			pm.marks[id] = false
		}
	}
	return pm
}

// PlayMark returns the track's mark: played or unplayed, and whether a deck
// has marked it since it was last played.
func (pm *PlayMarks) PlayMark(trackID uint32) (played, marked bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	played, marked = pm.marks[trackID]
	return played, marked
}

// MarkPlayed records a deck marking the track played (true) or unplayed.
func (pm *PlayMarks) MarkPlayed(trackID uint32, played bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if cur, ok := pm.marks[trackID]; !ok || cur != played {
		pm.marks[trackID] = played
		pm.saveLocked()
	}
}

// Played clears the track's mark: it has been played again, so History
// speaks for it.
func (pm *PlayMarks) Played(trackID uint32) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, ok := pm.marks[trackID]; ok {
		delete(pm.marks, trackID)
		pm.saveLocked()
	}
}

func (pm *PlayMarks) saveLocked() {
	if pm.path == "" {
		return
	}
	var f playMarksFile
	for id, played := range pm.marks {
		if played {
			f.Played = append(f.Played, id)
		} else {
			f.Unplayed = append(f.Unplayed, id)
		}
	}
	sort.Slice(f.Played, func(i, j int) bool { return f.Played[i] < f.Played[j] })
	sort.Slice(f.Unplayed, func(i, j int) bool { return f.Unplayed[i] < f.Unplayed[j] })
	b, _ := json.Marshal(f)
	if err := fsutil.WriteFile(pm.path, b, 0o644); err != nil {
		log.Printf("playmarks: write %s: %v", pm.path, err)
	}
}

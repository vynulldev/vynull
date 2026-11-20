// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"maps"
	"os"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/fsutil"
)

// A rekordbox library can rename its eight track colours (djmdColor's
// Commnt), and rekordbox then serves those names wherever a colour is named:
// the COLOR menu, a colour second column, TRACK INFO (link-spec
// SORT_AND_COLOR_ORACLE.md, "Custom color labels"). A library keeps the names
// that differ from the stock ones (TrackColorName), persisted beside the
// track list.

type colorNamesFile struct {
	Names map[uint8]string `json:"names"`
}

// ColorName is colour id's name in this library: its own name, else the
// stock one.
func (l *Library) ColorName(id uint8) string {
	l.mu.RLock()
	name, ok := l.colorNames[id]
	l.mu.RUnlock()
	if ok {
		return name
	}
	return TrackColorName(id)
}

// SetColorNames replaces the library's own colour names with names (colour
// ID 1-8 to name; a stock or empty name, or another ID, is dropped), saves
// them, and moves the library's revision so served rows pick them up.
func (l *Library) SetColorNames(names map[uint8]string) {
	own := make(map[uint8]string)
	for id, name := range names {
		if id >= 1 && id <= 8 && name != "" && name != TrackColorName(id) {
			own[id] = name
		}
	}
	l.mu.Lock()
	l.colorNames = own
	l.mu.Unlock()
	l.rev.Add(1)
	l.saveColorNames(own)
}

// colorNamesPath is where the colour names persist, beside the track list.
func (l *Library) colorNamesPath() string { return l.sidePath("colors.json") }

func (l *Library) saveColorNames(names map[uint8]string) {
	path := l.colorNamesPath()
	if path == "" {
		return
	}
	data, err := json.Marshal(colorNamesFile{Names: names})
	if err != nil {
		dlog.Errorf("library: colour names: %v", err)
		return
	}
	if err := fsutil.WriteFile(path, data, 0644); err != nil {
		dlog.Errorf("library: write %s: %v", path, err)
	}
}

func (l *Library) loadColorNames() {
	path := l.colorNamesPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // none saved
	}
	var f colorNamesFile
	if err := json.Unmarshal(data, &f); err != nil {
		dlog.Errorf("library: parse %s: %v", path, err)
		return
	}
	l.mu.Lock()
	l.colorNames = maps.Clone(f.Names)
	l.mu.Unlock()
}

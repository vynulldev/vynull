// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/fsutil"
)

// The tag list is the decks' TAG LIST: an ordered list of tracks, which
// rekordbox keeps in djmdSongTagList (its Prepare list) and serves to a
// linked deck. The deck reads it, adds a track to its end, removes tracks or
// all of them, and marks the listed tracks in every browse list; the
// dbserver's taglist.go serves those requests. It persists beside the track
// list.

type tagListFile struct {
	Tracks []uint32 `json:"tracks"`
}

// TagList returns the tag list's track IDs in list order.
func (l *Library) TagList() []uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return slices.Clone(l.tagList)
}

// InTagList reports whether the track is in the tag list.
func (l *Library) InTagList(id uint32) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.tagged[id]
	return ok
}

// SetTagged adds the track to the end of the tag list, or removes it, and
// reports whether the list changed. A track the library doesn't hold isn't
// added.
func (l *Library) SetTagged(id uint32, on bool) bool {
	return l.changeTagList(func(list []uint32) []uint32 {
		if slices.Contains(list, id) == on || (on && l.byID[id] == nil) {
			return nil
		}
		if on {
			return append(slices.Clone(list), id)
		}
		return slices.DeleteFunc(slices.Clone(list), func(t uint32) bool { return t == id })
	})
}

// ClearTagList empties the tag list and reports whether it held any track.
func (l *Library) ClearTagList() bool {
	return l.changeTagList(func(list []uint32) []uint32 {
		if len(list) == 0 {
			return nil
		}
		return []uint32{}
	})
}

// SetTagList replaces the tag list with ids, in order, leaving out repeats
// and tracks the library doesn't hold.
func (l *Library) SetTagList(ids []uint32) {
	l.changeTagList(func([]uint32) []uint32 {
		out := make([]uint32, 0, len(ids))
		for _, id := range ids {
			if l.byID[id] != nil && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out
	})
}

// dropFromTagList removes tracks leaving the library.
func (l *Library) dropFromTagList(gone map[uint32]bool) {
	l.changeTagList(func(list []uint32) []uint32 {
		if !slices.ContainsFunc(list, func(t uint32) bool { return gone[t] }) {
			return nil
		}
		return slices.DeleteFunc(slices.Clone(list), func(t uint32) bool { return gone[t] })
	})
}

// changeTagList applies edit to the list under the library lock; edit
// returns the new list, or nil to leave it as it is. A change is saved
// before the next one starts, so the file always holds the latest list.
func (l *Library) changeTagList(edit func(list []uint32) []uint32) bool {
	l.tagListMu.Lock()
	defer l.tagListMu.Unlock()
	l.mu.Lock()
	next := edit(l.tagList)
	if next == nil {
		l.mu.Unlock()
		return false
	}
	l.tagList = next
	l.tagged = make(map[uint32]struct{}, len(next))
	for _, id := range next {
		l.tagged[id] = struct{}{}
	}
	l.mu.Unlock()
	l.saveTagList(next)
	return true
}

// tagListPath is where the tag list persists: beside the track list, or for
// a library built from a music dir, beside its overlay of edits.
func (l *Library) tagListPath() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	switch {
	case l.dbPath != "":
		return filepath.Join(filepath.Dir(l.dbPath), "taglist.json")
	case l.overlay != nil:
		return filepath.Join(filepath.Dir(l.overlay.path), "taglist.json")
	}
	return ""
}

func (l *Library) saveTagList(list []uint32) {
	path := l.tagListPath()
	if path == "" {
		return
	}
	data, err := json.Marshal(tagListFile{Tracks: list})
	if err != nil {
		dlog.Errorf("library: tag list: %v", err)
		return
	}
	if err := fsutil.WriteFile(path, data, 0644); err != nil {
		dlog.Errorf("library: write %s: %v", path, err)
	}
}

func (l *Library) loadTagList() {
	path := l.tagListPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // none saved yet
	}
	var f tagListFile
	if err := json.Unmarshal(data, &f); err != nil {
		dlog.Errorf("library: parse %s: %v", path, err)
		return
	}
	l.mu.Lock()
	l.tagList = f.Tracks
	l.tagged = make(map[uint32]struct{}, len(f.Tracks))
	for _, id := range f.Tracks {
		l.tagged[id] = struct{}{}
	}
	l.mu.Unlock()
}

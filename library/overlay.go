// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/fsutil"
	"github.com/vynulldev/vynull/internal/saver"
)

// A library built from a music dir (a served USB export's PDB, or a scan)
// is rebuilt from it at every start, so the music dir stays the source for
// every field nobody edited: a fresh rekordbox export updates them. What was
// edited is kept apart, in an overlay file in the data dir, and applied over
// the rebuilt library:
//
//   - edits: each edited track's changed fields, by its path within the
//     music dir (the path, unlike an ID, survives a fresh export);
//   - added: tracks added on top of the music dir, in full;
//   - removed: the music dir's tracks that were deleted from the library.
//
// Entries whose path the music dir doesn't hold (another stick served from
// the same data dir, or a track since removed from the export) are kept as
// they are, so they apply again when it's back.
type overlayFile struct {
	Edits   map[string]map[string]json.RawMessage `json:"edits,omitempty"`
	Added   []*Track                              `json:"added,omitempty"`
	Removed []string                              `json:"removed,omitempty"`
}

// overlayState is a library's overlay: the music dir's tracks as built, to
// tell what changed, and the entries for paths it doesn't hold.
type overlayState struct {
	path    string
	base    map[uint32]baseTrack
	dormant overlayFile
}

type baseTrack struct {
	rel   string // path within the music dir, slash-separated
	track Track
}

// overlayFields maps the JSON name of each Track field an overlay records to
// its index: every field but the ID, which the path stands for, and the
// artwork and file-presence state, which are found again when needed. (The
// music dir's artwork is loaded under the export's own artwork IDs, which a
// fresh export can renumber, so a recorded artwork ID could point at another
// cover; extracting again finds the cached copy by its content.)
var overlayFields = func() map[string]int {
	skip := map[string]bool{"id": true, "art_id": true, "art_checked": true, "file_missing": true}
	out := map[string]int{}
	t := reflect.TypeFor[Track]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !skip[name] {
			out[name] = i
		}
	}
	return out
}()

// UseOverlay makes the library keep its edits in an overlay at path: it
// records the tracks it holds now, all from musicDir, as they were built,
// applies the overlay's edits, additions and removals, and from then on
// saves the overlay instead of the whole library. Call it once, right after
// building the library from musicDir.
func (l *Library) UseOverlay(path, musicDir string) {
	base := make(map[uint32]baseTrack, len(l.tracks))
	byRel := make(map[string]uint32, len(l.tracks))
	for _, t := range l.Tracks() {
		rel, err := filepath.Rel(musicDir, t.FilePath)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue // not in the music dir: nothing to key it by
		}
		rel = filepath.ToSlash(rel)
		base[t.ID] = baseTrack{rel: rel, track: *t}
		byRel[rel] = t.ID
	}
	o := &overlayState{path: path, base: base}

	var f overlayFile
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &f); err != nil {
			dlog.Errorf("library: parse %s: %v (edits not applied, and the file is left as it is)", path, err)
			return
		}
	}
	edited, removed, added := 0, 0, 0
	for rel, fields := range f.Edits {
		id, ok := byRel[rel]
		if !ok {
			if o.dormant.Edits == nil {
				o.dormant.Edits = map[string]map[string]json.RawMessage{}
			}
			o.dormant.Edits[rel] = fields
			continue
		}
		l.UpdateBulk(id, func(t *Track) error { applyFields(t, fields); return nil })
		edited++
	}
	var gone []uint32
	for _, rel := range f.Removed {
		if id, ok := byRel[rel]; ok {
			gone = append(gone, id)
		} else {
			o.dormant.Removed = append(o.dormant.Removed, rel)
		}
	}
	removed = l.RemoveTracks(gone)
	for _, t := range f.Added {
		if l.TrackByPath(t.FilePath) != nil {
			dlog.Infof("library: added track %d is now in the music dir; keeping the music dir's", t.ID)
			continue
		}
		if l.Track(t.ID) != nil {
			// The music dir now holds this ID (a fresh export renumbered
			// it): the track gets a new one, and whatever else refers to
			// the old ID (tags, cues, playlists) now refers to the music
			// dir's track instead.
			dlog.Warnf("library: added track %d (%s): ID now taken by the music dir, renumbered", t.ID, t.FilePath)
			t.ID = 0
		}
		l.AddTrackBulk(t)
		added++
	}
	l.FinalizeBulk()
	if edited+removed+added > 0 || len(o.dormant.Edits)+len(o.dormant.Removed) > 0 {
		dlog.Infof("library: %s: %d edited, %d removed, %d added; %d entries for paths not in %s kept",
			path, edited, removed, added, len(o.dormant.Edits)+len(o.dormant.Removed), musicDir)
	}

	os.MkdirAll(filepath.Dir(path), 0o755)
	l.mu.Lock()
	l.overlay = o
	if l.saver == nil {
		l.saver = saver.New("library", saver.Interval, l.writeOverlay)
	}
	l.mu.Unlock()
	l.loadTagList()
}

// writeOverlay saves the library as an overlay over its music dir.
func (l *Library) writeOverlay() error {
	l.mu.RLock()
	o := l.overlay
	f := overlayFile{Edits: map[string]map[string]json.RawMessage{}}
	held := make(map[uint32]bool, len(l.tracks))
	for _, t := range l.tracks {
		b, ok := o.base[t.ID]
		if !ok {
			cp := *t
			// Extract the artwork again after a restart (see overlayFields).
			cp.ArtID, cp.ArtChecked = 0, false
			f.Added = append(f.Added, &cp)
			continue
		}
		held[t.ID] = true
		if d := diffFields(&b.track, t); len(d) > 0 {
			f.Edits[b.rel] = d
		}
	}
	for id, b := range o.base {
		if !held[id] {
			f.Removed = append(f.Removed, b.rel)
		}
	}
	l.mu.RUnlock()
	for rel, fields := range o.dormant.Edits {
		f.Edits[rel] = fields
	}
	f.Removed = append(f.Removed, o.dormant.Removed...)
	slices.Sort(f.Removed)
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(o.path, data, 0644)
}

// diffFields returns the overlay fields that differ between base and t, as
// t has them. Each is encoded on its own, so a field cleared to its zero
// value is recorded too.
func diffFields(base, t *Track) map[string]json.RawMessage {
	bv, tv := reflect.ValueOf(base).Elem(), reflect.ValueOf(t).Elem()
	var out map[string]json.RawMessage
	for name, i := range overlayFields {
		if fieldEqual(bv.Field(i), tv.Field(i)) {
			continue
		}
		raw, err := json.Marshal(tv.Field(i).Interface())
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		out[name] = raw
	}
	return out
}

// fieldEqual reports whether two values of one field are equal. Most of a
// library is never edited, so strings, numbers and lists of them compare
// without boxing; other kinds (the dates) fall back to DeepEqual.
func fieldEqual(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.String:
		return a.String() == b.String()
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return a.Uint() == b.Uint()
	case reflect.Float32, reflect.Float64:
		return a.Float() == b.Float()
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !fieldEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// applyFields sets t's fields from an overlay's recorded values, skipping
// names it doesn't know (a field a later or earlier version recorded).
func applyFields(t *Track, fields map[string]json.RawMessage) {
	tv := reflect.ValueOf(t).Elem()
	for name, raw := range fields {
		i, ok := overlayFields[name]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, tv.Field(i).Addr().Interface()); err != nil {
			dlog.Warnf("library: overlay field %q of %q: %v", name, t.FilePath, err)
		}
	}
}

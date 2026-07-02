// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/vynulldev/vynull/internal/fsutil"
)

// HashID generates a stable rekordbox-style 32-bit ID from a type and name.
// rekordbox uses large IDs (e.g., 0x0addf33c) from its master database.
// We generate them as a hash so the same entity always gets the same ID.
func HashID(entityType, name string) uint32 {
	h := sha256.Sum256([]byte(entityType + ":" + name))
	id := binary.BigEndian.Uint32(h[:4])
	if id == 0 {
		id = 1 // avoid zero ID
	}
	return id
}

// Library holds all tracks and provides lookup indices.
type Library struct {
	Artwork *ArtworkCache

	mu       sync.RWMutex
	tracks   []*Track // all tracks, ordered by ID
	nextID   uint32   // next sequential track ID
	byID     map[uint32]*Track
	byArtist map[string][]*Track
	byAlbum  map[string][]*Track
	byGenre  map[string][]*Track

	artists []string
	albums  []string
	genres  []string

	dbPath string // if set, persist track list to this JSON file
}

// New creates an empty Library for adding tracks dynamically.
func New() *Library {
	return NewLibrary(nil, NewArtworkCache())
}

// NewLibrary builds a Library with indices from the given tracks.
func NewLibrary(tracks []*Track, artwork *ArtworkCache) *Library {
	lib := &Library{
		Artwork:  artwork,
		tracks:   tracks,
		nextID:   1,
		byID:     make(map[uint32]*Track, len(tracks)),
		byArtist: make(map[string][]*Track),
		byAlbum:  make(map[string][]*Track),
		byGenre:  make(map[string][]*Track),
	}

	artistSet := make(map[string]bool)
	albumSet := make(map[string]bool)
	genreSet := make(map[string]bool)

	for _, t := range tracks {
		lib.byID[t.ID] = t
		if t.ID >= lib.nextID {
			lib.nextID = t.ID + 1
		}

		if t.Artist != "" {
			key := strings.ToLower(t.Artist)
			lib.byArtist[key] = append(lib.byArtist[key], t)
			artistSet[t.Artist] = true
		}
		if t.Album != "" {
			key := strings.ToLower(t.Album)
			lib.byAlbum[key] = append(lib.byAlbum[key], t)
			albumSet[t.Album] = true
		}
		if t.Genre != "" {
			key := strings.ToLower(t.Genre)
			lib.byGenre[key] = append(lib.byGenre[key], t)
			genreSet[t.Genre] = true
		}
	}

	lib.artists = sortedKeys(artistSet)
	lib.albums = sortedKeys(albumSet)
	lib.genres = sortedKeys(genreSet)

	return lib
}

// Track returns a track by its ID.
func (l *Library) Track(id uint32) *Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byID[id]
}

// Tracks returns all tracks.
func (l *Library) Tracks() []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.tracks
}

// TrackCount returns the total number of tracks.
func (l *Library) TrackCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.tracks)
}

// Artists returns all unique artist names, sorted.
func (l *Library) Artists() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.artists
}

// Albums returns all unique album names, sorted.
func (l *Library) Albums() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.albums
}

// Genres returns all unique genre names, sorted.
func (l *Library) Genres() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.genres
}

// TracksByArtist returns tracks for the given artist (case-insensitive).
func (l *Library) TracksByArtist(artist string) []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byArtist[strings.ToLower(artist)]
}

// TracksByAlbum returns tracks for the given album (case-insensitive).
func (l *Library) TracksByAlbum(album string) []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byAlbum[strings.ToLower(album)]
}

// TracksByGenre returns tracks for the given genre (case-insensitive).
func (l *Library) TracksByGenre(genre string) []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byGenre[strings.ToLower(genre)]
}

// addTrackLocked indexes a track (assigning a sequential ID if unset) without
// rebuilding the sorted name lists or persisting. Caller must hold l.mu.
func (l *Library) addTrackLocked(t *Track) uint32 {
	if t.ID == 0 {
		t.ID = l.nextID
		l.nextID++
	}
	// Skip duplicates.
	if l.byID[t.ID] != nil {
		return t.ID
	}
	// Keep nextID ahead of any externally-assigned IDs.
	if t.ID >= l.nextID {
		l.nextID = t.ID + 1
	}

	l.tracks = append(l.tracks, t)
	l.byID[t.ID] = t

	if t.Artist != "" {
		key := strings.ToLower(t.Artist)
		l.byArtist[key] = append(l.byArtist[key], t)
	}
	if t.Album != "" {
		key := strings.ToLower(t.Album)
		l.byAlbum[key] = append(l.byAlbum[key], t)
	}
	if t.Genre != "" {
		key := strings.ToLower(t.Genre)
		l.byGenre[key] = append(l.byGenre[key], t)
	}
	return t.ID
}

// AddTrack adds a single track, rebuilds the sorted lists, and persists.
// Returns the assigned ID. For adding many tracks at once (import), use
// AddTrackBulk + FinalizeBulk to avoid rebuilding/saving on every track.
func (l *Library) AddTrack(t *Track) uint32 {
	l.mu.Lock()
	id := l.addTrackLocked(t)
	l.rebuildLists()
	l.mu.Unlock()
	l.Save()
	return id
}

// AddTrackBulk adds a track to the in-memory indices WITHOUT rebuilding the
// sorted lists or saving to disk. Intended for bulk imports — call
// FinalizeBulk once after the batch. Returns the assigned ID. (Routing a
// full import through AddTrack instead is O(n²): every track triggers a full
// library.json rewrite + list rebuild.)
func (l *Library) AddTrackBulk(t *Track) uint32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.addTrackLocked(t)
}

// FinalizeBulk rebuilds the sorted name lists and persists the library —
// call once after a batch of AddTrackBulk calls.
func (l *Library) FinalizeBulk() {
	l.mu.Lock()
	l.rebuildLists()
	l.mu.Unlock()
	l.Save()
}

// RemoveTrack drops a track from the in-memory library and persists the
// change. Does NOT touch the underlying audio file. Returns true if the
// track existed.
func (l *Library) RemoveTrack(id uint32) bool {
	l.mu.Lock()
	t := l.byID[id]
	if t == nil {
		l.mu.Unlock()
		return false
	}
	delete(l.byID, id)
	for i, tr := range l.tracks {
		if tr.ID == id {
			l.tracks = append(l.tracks[:i], l.tracks[i+1:]...)
			break
		}
	}
	removeFromIndex := func(idx map[string][]*Track, key string) {
		key = strings.ToLower(key)
		list := idx[key]
		for i, tr := range list {
			if tr.ID == id {
				idx[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(idx[key]) == 0 {
			delete(idx, key)
		}
	}
	if t.Artist != "" {
		removeFromIndex(l.byArtist, t.Artist)
	}
	if t.Album != "" {
		removeFromIndex(l.byAlbum, t.Album)
	}
	if t.Genre != "" {
		removeFromIndex(l.byGenre, t.Genre)
	}
	l.rebuildLists()
	l.mu.Unlock()
	l.Save()
	return true
}

// SetDBPath sets the path for persisting the track list. If the file exists, loads it.
func (l *Library) SetDBPath(path string) {
	l.dbPath = path
	l.loadFromDisk()
}

// IncrementPlayCount bumps the play count for trackID by 1 and persists the
// library. Called from PlayerMonitor when a CDJ has played a track for at
// least half its duration — matches the rekordbox "scrobble" threshold so
// brief previews don't inflate the count. Returns the new count, or 0 if
// the track isn't in the library.
func (l *Library) IncrementPlayCount(trackID uint32) int {
	l.mu.Lock()
	t := l.byID[trackID]
	if t == nil {
		l.mu.Unlock()
		return 0
	}
	t.PlayCount++
	count := t.PlayCount
	l.mu.Unlock()
	l.Save()
	return count
}

// SetArtwork records an extracted artwork ID for a track and marks it checked,
// under the library lock — so lazy artwork extraction (which runs on request
// goroutines) doesn't race the Save() that marshals the track list.
func (l *Library) SetArtwork(trackID, artID uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.byID[trackID]
	if t == nil {
		return
	}
	t.ArtChecked = true
	if artID != 0 {
		t.ArtID = artID
	}
}

// Save persists the track list to disk.
func (l *Library) Save() {
	if l.dbPath == "" {
		return
	}
	l.mu.RLock()
	data, err := json.MarshalIndent(l.tracks, "", "  ")
	l.mu.RUnlock()
	if err != nil {
		log.Printf("library: save error: %v", err)
		return
	}
	if err := fsutil.WriteFile(l.dbPath, data, 0644); err != nil {
		log.Printf("library: write error: %v", err)
	}
}

func (l *Library) loadFromDisk() {
	if l.dbPath == "" {
		return
	}
	data, err := os.ReadFile(l.dbPath)
	if err != nil {
		return // file doesn't exist yet
	}
	var tracks []*Track
	if err := json.Unmarshal(data, &tracks); err != nil {
		log.Printf("library: parse %s: %v", l.dbPath, err)
		return
	}
	// Bulk-load: build the indexes directly and rebuild the sorted lists once
	// at the end. Do NOT route through AddTrack here — it calls Save() and
	// rebuildLists() per track, which on a full load is O(n²) (each Save
	// rewrites the whole growing library.json). That made startup take ~9s
	// for 1977 tracks; this brings it down to tens of ms. The in-memory
	// result is identical — we just skip the per-track disk churn.
	l.mu.Lock()
	for _, t := range tracks {
		// Preserve each track's persisted ID. IDs MUST be stable across loads —
		// other stores (cues, tags, the waveform-PNG disk cache) key off the
		// track ID, so reassigning by load order would re-point them at the
		// wrong tracks the moment any track is added or removed. addTrackLocked
		// assigns a sequential ID only when one is missing (t.ID == 0), so old
		// records without an ID still get one.
		l.addTrackLocked(t)
	}
	l.rebuildLists()
	l.mu.Unlock()
	log.Printf("library: loaded %d tracks from %s", len(tracks), l.dbPath)
}

// rebuildLists regenerates the sorted artist/album/genre name lists from the maps.
func (l *Library) rebuildLists() {
	artistSet := make(map[string]bool)
	for key := range l.byArtist {
		if tracks := l.byArtist[key]; len(tracks) > 0 {
			artistSet[tracks[0].Artist] = true
		}
	}
	albumSet := make(map[string]bool)
	for key := range l.byAlbum {
		if tracks := l.byAlbum[key]; len(tracks) > 0 {
			albumSet[tracks[0].Album] = true
		}
	}
	genreSet := make(map[string]bool)
	for key := range l.byGenre {
		if tracks := l.byGenre[key]; len(tracks) > 0 {
			genreSet[tracks[0].Genre] = true
		}
	}
	l.artists = sortedKeys(artistSet)
	l.albums = sortedKeys(albumSet)
	l.genres = sortedKeys(genreSet)
}

// TrackByPath finds a track by its file path, or nil.
func (l *Library) TrackByPath(path string) *Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, t := range l.tracks {
		if t.FilePath == path {
			return t
		}
	}
	return nil
}

// RemapPaths rewrites every track whose FilePath starts with `from` so
// the prefix is replaced with `to`. Returns the number of tracks
// changed and a slice of (old, new) pairs so the caller can rename
// path-keyed caches (analysis .gob files in particular).
func (l *Library) RemapPaths(from, to string) (int, [][2]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var changes [][2]string
	for _, t := range l.tracks {
		if strings.HasPrefix(t.FilePath, from) {
			old := t.FilePath
			t.FilePath = to + strings.TrimPrefix(t.FilePath, from)
			changes = append(changes, [2]string{old, t.FilePath})
		}
	}
	return len(changes), changes
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

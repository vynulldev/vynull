// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"errors"
	"slices"
	"sort"
	"strings"
)

// ErrTrackNotFound is returned for a track ID the library doesn't hold.
var ErrTrackNotFound = errors.New("track not found")

// Update changes the track with the given ID through fn, then saves the
// library. fn edits a copy of the track: when it returns an error the track is
// left exactly as it was, so fn can validate as it goes. Otherwise the copy
// becomes the track, the artist, album, genre and path indexes follow any
// change to those fields, and the revision moves. The ID can't be changed. fn
// runs under the library's lock and must not call the Library.
func (l *Library) Update(id uint32, fn func(*Track) error) error {
	l.mu.Lock()
	names, err := l.updateLocked(id, fn)
	if err == nil && names {
		l.rebuildLists()
	}
	l.mu.Unlock()
	if err != nil {
		return err
	}
	l.Save()
	return nil
}

// UpdateBulk is Update for a batch: it leaves rebuilding the sorted name
// lists and saving to one FinalizeBulk call after the batch, as AddTrackBulk
// does.
func (l *Library) UpdateBulk(id uint32, fn func(*Track) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.updateLocked(id, fn)
	return err
}

// updateLocked applies fn to a copy of the track and, if it succeeds, makes
// the copy the track and reindexes it. It reports whether a name changed.
func (l *Library) updateLocked(id uint32, fn func(*Track) error) (names bool, err error) {
	t := l.byID[id]
	if t == nil {
		return false, ErrTrackNotFound
	}
	next := *t
	next.Related = slices.Clone(t.Related)
	if err := fn(&next); err != nil {
		return false, err
	}
	next.ID = id
	names = l.reindexLocked(t, &next)
	*t = next
	return names, nil
}

// reindexLocked moves t in the indexes wherever next, its new state, differs
// in path, artist, album or genre. It reports whether a name changed, so the
// sorted name lists need rebuilding once next is in place.
func (l *Library) reindexLocked(t, next *Track) (names bool) {
	if t.FilePath != next.FilePath {
		l.unindexLocked(l.byPath, t.FilePath, t)
		l.indexLocked(l.byPath, next.FilePath, t)
	}
	for _, f := range []struct {
		idx      map[string][]*Track
		was, now string
	}{
		{l.byArtist, t.Artist, next.Artist},
		{l.byAlbum, t.Album, next.Album},
		{l.byGenre, t.Genre, next.Genre},
	} {
		if f.was == f.now {
			continue
		}
		names = true
		if strings.ToLower(f.was) != strings.ToLower(f.now) { // the index keys
			l.unindexNameLocked(f.idx, f.was, t)
			l.indexNameLocked(f.idx, f.now, t)
		}
	}
	return names
}

// indexLocked adds t to idx[key], keeping the list in the order tracks were
// added. A track added after every other one is appended, which leaves the
// list's existing elements alone; otherwise it is inserted into a new list,
// since callers may hold the old one.
func (l *Library) indexLocked(idx map[string][]*Track, key string, t *Track) {
	list := idx[key]
	at := l.order[t.ID]
	i := sort.Search(len(list), func(i int) bool { return l.order[list[i].ID] > at })
	if i == len(list) {
		idx[key] = append(list, t)
		return
	}
	idx[key] = slices.Insert(slices.Clone(list), i, t)
}

// unindexLocked removes t from idx[key] into a new list, since callers may
// hold the old one, and drops the key once no track is left under it.
func (l *Library) unindexLocked(idx map[string][]*Track, key string, t *Track) {
	list := slices.DeleteFunc(slices.Clone(idx[key]), func(x *Track) bool { return x == t })
	if len(list) == 0 {
		delete(idx, key)
		return
	}
	idx[key] = list
}

// indexNameLocked indexes t under a name's case-insensitive key; an empty
// name isn't indexed.
func (l *Library) indexNameLocked(idx map[string][]*Track, name string, t *Track) {
	if name != "" {
		l.indexLocked(idx, strings.ToLower(name), t)
	}
}

// unindexNameLocked undoes indexNameLocked.
func (l *Library) unindexNameLocked(idx map[string][]*Track, name string, t *Track) {
	if name != "" {
		l.unindexLocked(idx, strings.ToLower(name), t)
	}
}

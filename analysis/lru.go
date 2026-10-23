// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "container/list"

// The store keeps the results it has loaded or been given in memory, but not
// all of them: at about 320 KB each (waveforms, grid, artwork), a library
// browsed end to end would end up holding every one. It keeps the most
// recently used up to its byte budget and drops older ones that it can load
// again from their cache file; a result without one stays.

// DefaultMaxResultBytes is the store's budget for results held in memory, when
// its MaxResultBytes is 0: about 800 average tracks.
const DefaultMaxResultBytes = 256 << 20

type resultLRU struct {
	order *list.List // of uint32 track IDs, most recently used at the front
	elems map[uint32]*list.Element
	sizes map[uint32]int64
	bytes int64
}

// putLocked stores r as the track's result, as most recently used, then drops
// least recently used results over the budget. Callers hold s.mu.
func (s *Store) putLocked(trackID uint32, r *Result) {
	l := &s.lru
	if l.order == nil {
		l.order = list.New()
		l.elems = make(map[uint32]*list.Element)
		l.sizes = make(map[uint32]int64)
	}
	s.results[trackID] = r
	size := resultSize(r)
	if e, ok := l.elems[trackID]; ok {
		l.bytes += size - l.sizes[trackID]
		l.order.MoveToFront(e)
	} else {
		l.elems[trackID] = l.order.PushFront(trackID)
		l.bytes += size
	}
	l.sizes[trackID] = size
	s.evictLocked(trackID)
}

// touchLocked marks the track's result as just used. Callers hold s.mu.
func (s *Store) touchLocked(trackID uint32) {
	if e, ok := s.lru.elems[trackID]; ok {
		s.lru.order.MoveToFront(e)
	}
}

// dropLocked removes the track's result from memory. Callers hold s.mu.
func (s *Store) dropLocked(trackID uint32) {
	delete(s.results, trackID)
	l := &s.lru
	if e, ok := l.elems[trackID]; ok {
		l.order.Remove(e)
		l.bytes -= l.sizes[trackID]
		delete(l.elems, trackID)
		delete(l.sizes, trackID)
	}
}

// dropAllLocked removes every result from memory. Callers hold s.mu.
func (s *Store) dropAllLocked() {
	s.results = make(map[uint32]*Result)
	s.lru = resultLRU{}
}

// evictLocked drops least recently used results, other than keep, while the
// results held are over the budget. It drops only results it can load again
// from a cache file. Callers hold s.mu.
func (s *Store) evictLocked(keep uint32) {
	max := s.MaxResultBytes
	if max <= 0 {
		max = DefaultMaxResultBytes
	}
	l := &s.lru
	for e := l.order.Back(); e != nil && l.bytes > max; {
		prev := e.Prev()
		if id := e.Value.(uint32); id != keep && s.cacheDir != "" && s.pathMap[id] != "" {
			s.dropLocked(id)
		}
		e = prev
	}
}

// resultSize estimates the memory a result holds: its blobs, beats and
// phrases, and a little for the rest.
func resultSize(r *Result) int64 {
	if r == nil {
		return 0
	}
	n := len(r.Artwork) + len(r.WavePreview) + len(r.WavePreviewANLZ) + len(r.WaveTinyANLZ) +
		len(r.WaveColorPreview) + len(r.WaveDetail) + len(r.WaveDetail3Band) + len(r.WavePreview3Band) +
		len(r.Wave3BandColor) + len(r.WaveDetailMono) + len(r.BeatGrid) + len(r.BeatGridPQT2) +
		len(r.SongStructure) + 8*len(r.Beats) + 64*len(r.Phrases) + 512
	return int64(n)
}

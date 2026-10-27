// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"sync"

	"github.com/vynulldev/vynull/library"
)

// trackrows.go keeps the library's tracks as the dbserver serves them, built
// once per library revision and shared read-only by the server's sessions:
// each track's base row, its category IDs, the collation keys its sorts read,
// the TRACK list's order for each sort and SEARCH's folded text. A request
// copies the rows it serves, since handlers stamp their own columns,
// positions and context type on them.

// trackRows is the library's tracks at one revision.
type trackRows struct {
	lib       *library.Library
	rev       uint64
	detail    string // the configured detail column the rows show
	transcode bool

	tracks []*library.Track // in library order
	rows   []menuItem       // tracks[i]'s base row
	ids    []catIDs         // tracks[i]'s category IDs
	sv     []*sortValues    // rows[i].sv, allocated one by one (see holds)
	pos    map[uint32]int32 // track ID -> index
	// names holds the category ID of every (kind, name) pair the tracks
	// carry, looked up (or hashed) once each.
	names map[[2]string]uint32

	// Built on first use: each text sort's collation keys (indexed by sort
	// ID, the title's at ALPHABET), the TRACK list's order for each sort ID,
	// the BPM and KEY sorts' row labels, the ID-to-name reverse index of the
	// artist and album lists, and SEARCH's folded text.
	keys    [18]lazy[[]string]
	orders  [18]lazy[[]int32]
	tempo   lazy[[]string]
	tone    lazy[[]string]
	artists lazy[map[uint32]string]
	albums  lazy[map[uint32]string]
	search  lazy[[]searchText]
}

// lazy holds a value built on first use, once, however many sessions ask.
type lazy[T any] struct {
	once sync.Once
	v    T
}

func (l *lazy[T]) get(build func() T) T {
	l.once.Do(func() { l.v = build() })
	return l.v
}

// catIDs are a track's category IDs, as catID gives them for its names.
type catIDs struct {
	artist, album, genre, label, key, remixer, origArtist uint32
}

// trackCatIDs returns a track's category IDs, from id (catID, or a memo of
// it).
func trackCatIDs(t *library.Track, id func(kind, name string) uint32) catIDs {
	return catIDs{
		artist:     id("artist", t.Artist),
		album:      id("album", t.Album),
		genre:      id("genre", t.Genre),
		label:      id("label", t.Label),
		key:        id("key", t.Key),
		remixer:    id("remixer", t.Remixer),
		origArtist: id("origartist", t.OriginalArtist),
	}
}

// rowCache holds the latest trackRows, shared by a server's sessions.
type rowCache struct {
	mu   sync.Mutex
	rows *trackRows
}

// trackRows returns the library's rows at its current revision, building
// them when the library or the configured detail column has changed. The
// revision is read before the rows are built, so a change made meanwhile
// moves it past theirs and the next request builds again.
func (h *Handler) trackRows() *trackRows {
	if h.rowCache == nil {
		h.rowCache = new(rowCache) // a session without a server
	}
	var rev uint64
	if h.lib != nil {
		rev = h.lib.Rev()
	}
	detail := h.trackDetailKey()
	rc := h.rowCache
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if c := rc.rows; c != nil && c.lib == h.lib && c.rev == rev && c.detail == detail && c.transcode == h.transcode {
		return c
	}
	rc.rows = h.buildTrackRows(rev, detail)
	return rc.rows
}

// buildTrackRows builds every track's base row and category IDs.
func (h *Handler) buildTrackRows(rev uint64, detail string) *trackRows {
	c := &trackRows{lib: h.lib, rev: rev, detail: detail, transcode: h.transcode}
	if h.lib != nil {
		c.tracks = h.lib.Tracks()
	}
	c.rows = make([]menuItem, len(c.tracks))
	c.ids = make([]catIDs, len(c.tracks))
	c.sv = make([]*sortValues, len(c.tracks))
	c.pos = make(map[uint32]int32, len(c.tracks))
	c.names = make(map[[2]string]uint32)
	// Most names repeat across tracks, so each is looked up (or hashed) once.
	id := func(kind, name string) uint32 {
		k := [2]string{kind, name}
		v, ok := c.names[k]
		if !ok {
			v = h.catID(kind, name)
			c.names[k] = v
		}
		return v
	}
	for i, t := range c.tracks {
		c.ids[i] = trackCatIDs(t, id)
		sv := libSortValues(t)
		sv.pos = int32(i)
		c.sv[i] = &sv
		h.fillTrackRow(&c.rows[i], t, &c.ids[i], c.sv[i])
		c.pos[t.ID] = int32(i)
	}
	return c
}

// position returns a track's index in the rows, or false for a track they
// don't hold (one added since they were built, or a different track that
// has since taken its ID).
func (c *trackRows) position(t *library.Track) (int32, bool) {
	p, ok := c.pos[t.ID]
	return p, ok && c.tracks[p] == t
}

// holds reports whether a row's sort values are these rows' own, so their
// prebuilt keys and labels apply to it. A row copy points at nothing else of
// theirs, so a list a session keeps doesn't keep the rows alive.
func (c *trackRows) holds(v *sortValues) bool {
	return int(v.pos) < len(c.sv) && c.sv[v.pos] == v
}

// copyRows returns copies of the rows at the given indexes, in one
// allocation.
func (c *trackRows) copyRows(idx []int32) []*menuItem {
	rows := make([]menuItem, len(idx))
	items := make([]*menuItem, len(idx))
	for i, p := range idx {
		rows[i] = c.rows[p]
		items[i] = &rows[i]
	}
	return items
}

// libraryOrder returns every row's index, in library order.
func (c *trackRows) libraryOrder() []int32 {
	idx := make([]int32, len(c.rows))
	for i := range idx {
		idx[i] = int32(i)
	}
	return idx
}

// filter returns the indexes of the tracks keep accepts, in library order.
func (c *trackRows) filter(keep func(t *library.Track, ids *catIDs) bool) []int32 {
	var idx []int32
	for i, t := range c.tracks {
		if keep(t, &c.ids[i]) {
			idx = append(idx, int32(i))
		}
	}
	return idx
}

// namesWithAll returns an [ALL] row, then a row of itemType for each name
// (and its ID) that field gives the tracks at idx, empty names left out, in
// name order (sortItems' title order).
func (c *trackRows) namesWithAll(idx []int32, field func(*library.Track, *catIDs) (string, uint32), itemType uint32) []*menuItem {
	names := make(map[string]uint32)
	for _, p := range idx {
		if name, id := field(c.tracks[p], &c.ids[p]); name != "" {
			names[name] = id
		}
	}
	items := []*menuItem{{ID: 0xffffffff, Label1: "ALL", ItemType: 0x00a0}}
	for name, id := range names {
		items = append(items, &menuItem{ID: id, Label1: name, ItemType: itemType})
	}
	sortItems(items[1:], sortTitle)
	return items
}

// collationKeys returns every row's collation key for the text a sort ID
// orders by: its own field for a text sort, the title for any other.
func (c *trackRows) collationKeys(id uint32) []string {
	if spec, ok := sortSpecs[id]; !ok || spec.kind != sortText {
		id = sortTitle
	}
	return c.keys[id].get(func() []string {
		keys := make([]string, len(c.sv))
		for i := range c.sv {
			keys[i] = sortCollationKey(c.sv[i].text(id))
		}
		return keys
	})
}

// trackOrder returns the indexes of the TRACK list's rows for a sort ID:
// rekordbox's title order, re-sorted by the sort's own field when it has one,
// with ties keeping title order.
func (c *trackRows) trackOrder(order uint32) []int32 {
	spec, sorted := sortSpecFor(order)
	if !sorted {
		order = sortDefault
	}
	return c.orders[order].get(func() []int32 {
		var items []*menuItem
		if sorted {
			items = c.rowsAt(c.trackOrder(sortDefault))
		} else {
			items = c.rowsAt(c.libraryOrder()) // the order tracks were added
			order, spec = sortTitle, sortSpecs[sortTitle]
		}
		sortRows(c, items, order, spec)
		idx := make([]int32, len(items))
		for i, m := range items {
			idx[i] = m.sv.pos
		}
		return idx
	})
}

// rowsAt returns the base rows at the given indexes, shared, not copied.
func (c *trackRows) rowsAt(idx []int32) []*menuItem {
	items := make([]*menuItem, len(idx))
	for i, p := range idx {
		items[i] = &c.rows[p]
	}
	return items
}

// tempoLabels returns every row's BPM-sort label.
func (c *trackRows) tempoLabels() []string {
	return c.tempo.get(func() []string { return c.labels(formatTempoLabel) })
}

// toneLabels returns every row's KEY-sort label.
func (c *trackRows) toneLabels() []string {
	return c.tone.get(func() []string { return c.labels(formatToneLabel) })
}

// tempoLabel is a row's BPM-sort label (formatTempoLabel).
func (c *trackRows) tempoLabel(m *menuItem) string {
	if c.holds(m.sv) {
		return c.tempoLabels()[m.sv.pos]
	}
	return formatTempoLabel(m)
}

// toneLabel is a row's KEY-sort label (formatToneLabel).
func (c *trackRows) toneLabel(m *menuItem) string {
	if c.holds(m.sv) {
		return c.toneLabels()[m.sv.pos]
	}
	return formatToneLabel(m)
}

func (c *trackRows) labels(format func(*menuItem) string) []string {
	labels := make([]string, len(c.rows))
	for i := range c.rows {
		labels[i] = format(&c.rows[i])
	}
	return labels
}

// categoryName returns the name in the library's artist or album list whose
// ID is id (the first, should names share one), or "" for none.
func (c *trackRows) categoryName(kind string, id uint32) string {
	index := &c.artists
	if kind == "album" {
		index = &c.albums
	}
	return index.get(func() map[uint32]string {
		m := make(map[uint32]string)
		if c.lib == nil {
			return m
		}
		names := c.lib.Artists()
		if kind == "album" {
			names = c.lib.Albums()
		}
		for _, name := range names {
			id := c.nameID(kind, name)
			if _, ok := m[id]; !ok {
				m[id] = name
			}
		}
		return m
	})[id]
}

// nameID returns a category name's ID: the one the build looked up, which
// every name a track carries has, else catID's.
func (c *trackRows) nameID(kind, name string) uint32 {
	if id, ok := c.names[[2]string{kind, name}]; ok {
		return id
	}
	return categoryID(c.lib, kind, name)
}

// searchText is a track's text that SEARCH matches, folded as rekordbox
// compares it (searchTokens).
type searchText struct {
	title, file, artist, remixer, origArtist, album string
}

// searchTexts returns every track's SEARCH text.
func (c *trackRows) searchTexts() []searchText {
	return c.search.get(func() []searchText {
		texts := make([]searchText, len(c.tracks))
		for i, t := range c.tracks {
			texts[i] = searchText{
				title:      asciiUpper(t.Title),
				file:       asciiUpper(baseName(t.FilePath)),
				artist:     asciiUpper(t.Artist),
				remixer:    asciiUpper(t.Remixer),
				origArtist: asciiUpper(t.OriginalArtist),
				album:      asciiUpper(t.Album),
			}
		}
		return texts
	})
}

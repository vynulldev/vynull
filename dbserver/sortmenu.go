// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
	"golang.org/x/text/unicode/norm"
)

// sortmenu.go serves the deck's SORT menu (request 0x1400) and sorts track
// lists by the sort the deck picks from it, as rekordbox does (decoded from a
// real rekordbox 7.2.19 capture of a CDJ-2000NXS2 and link-spec's sort and
// secondary-column goldens).
//
// The deck asks for the menu with [ctx at location 5, 0, the list's request
// kind] and re-requests the list with the chosen sort ID. A sorted list
// orders its tracks by that sort and shows the sort's field as every row's
// second column; DEFAULT keeps the configured detail column.

// SortMenuEntry is one entry of the deck's SORT menu: a sort ID, the label
// the deck shows, and the menu row's item type.
type SortMenuEntry struct {
	ID       uint32
	Label    string
	ItemType uint32
}

// SortMenuSource supplies the configured SORT menu (the user's or an
// imported rekordbox library's order and visibility). A MenuSource that
// doesn't implement it gets rekordbox's default menu.
type SortMenuSource interface {
	SortMenu() []SortMenuEntry
}

// sortKind says how a sort orders tracks and fills its rows.
type sortKind int

const (
	sortText     sortKind = iota // ascending by text, empty last; arg 0 is 0
	sortNumber                   // by a number, value in arg 0, no label
	sortTempo                    // ascending BPM, zero last; "BPM - key"
	sortTone                     // ascending key index, keyless first; "key - BPM"
	sortDate                     // newest first, undated last; arg 0 is 0
	sortTitleOrd                 // title order (DEFAULT, ALPHABET and the unused ID 14)
)

// sortSpec describes one rekordbox sort ID.
type sortSpec struct {
	label    string
	itemType uint32 // SORT menu row type
	column   byte   // second-column type byte; 0 keeps the configured column
	kind     sortKind
	desc     bool // numbers: highest first
}

// sortSpecs maps rekordbox's sort IDs (0-17; 14 is unused) to their menu
// row and second column (link-spec SECONDARY_COLUMNS.md's selector map and
// sort-secondary-render-6 golden, rekordbox 7.2.19). Rating, time, bitrate
// and DJ play count sort highest first, date added newest first, the rest
// ascending.
var sortSpecs = map[uint32]sortSpec{
	0:  {"DEFAULT", 0xa1, 0, sortTitleOrd, false},
	1:  {"ALPHABET", 0xa2, 0x04, sortTitleOrd, false},
	2:  {"ARTIST", 0x81, 0x07, sortText, false},
	3:  {"ALBUM", 0x82, 0x02, sortText, false},
	4:  {"BPM", 0x85, 0x0d, sortTempo, false},
	5:  {"RATING", 0x86, 0x0a, sortNumber, true},
	6:  {"GENRE", 0x80, 0x06, sortText, false},
	7:  {"COMMENTS", 0x96, 0x23, sortText, false},
	8:  {"TIME", 0x92, 0x0b, sortNumber, true},
	9:  {"REMIXER", 0x88, 0x29, sortText, false},
	10: {"LABEL", 0x89, 0x0e, sortText, false},
	11: {"ORIGINAL ARTIST", 0x8a, 0x28, sortText, false},
	12: {"KEY", 0x8b, 0x0f, sortTone, false},
	13: {"BITRATE", 0x93, 0x10, sortNumber, true},
	15: {"COLOR", 0x8e, 0x13, sortNumber, false},
	16: {"DJ PLAY COUNT", 0x97, 0x2a, sortNumber, true},
	17: {"DATE ADDED", 0x8c, 0x2e, sortDate, false},
}

// defaultSortOrder is rekordbox's out-of-the-box SORT menu (link-spec
// SORT_AND_COLOR_ORACLE.md); comments, time, remixer, original artist,
// bitrate and colour are hidden.
var defaultSortOrder = []uint32{0, 1, 2, 3, 4, 5, 12, 10, 6, 17, 16}

// DefaultSortMenu returns rekordbox's out-of-the-box SORT menu.
func DefaultSortMenu() []SortMenuEntry {
	out := make([]SortMenuEntry, 0, len(defaultSortOrder))
	for _, id := range defaultSortOrder {
		s := sortSpecs[id]
		out = append(out, SortMenuEntry{ID: id, Label: s.label, ItemType: s.itemType})
	}
	return out
}

// SortSpecLabel returns the SORT menu label and row type of a sort ID, and
// whether rekordbox has that sort.
func SortSpecLabel(id uint32) (string, uint32, bool) {
	s, ok := sortSpecs[id]
	return s.label, s.itemType, ok
}

func (h *Handler) sortMenu() []SortMenuEntry {
	if sm, ok := h.menu.(SortMenuSource); ok {
		if entries := sm.SortMenu(); len(entries) > 0 {
			return entries
		}
	}
	return DefaultSortMenu()
}

// handleSortMenu handles 0x1400 [ctx, 0, list request kind]: the SORT menu,
// rows [0, sort ID, label, 2, "", item type].
func (h *Handler) handleSortMenu(msg *proto.DBMessage) []*proto.DBMessage {
	entries := h.sortMenu()
	items := make([]*menuItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, &menuItem{ID: e.ID, Label1: e.Label, ItemType: e.ItemType})
	}
	dlog.Debugf("dbserver: sort menu (0x1400) for list %#x returning %d sorts", msgArg(msg, 2), len(items))
	return h.countReply(msg, items)
}

// sortValues holds the track fields the sorts read, filled when a track row
// is built from either a library or a PDB track.
type sortValues struct {
	title, artist, album, genre, comment, remixer, label, origArtist string
	tempo                                                            int64 // BPM x 100
	rating                                                           int64
	seconds, bitrate, color, plays                                   int64
	dateAdded                                                        string // YYYY-MM-DD, "" when undated

	// pos is the values' index in the library's prebuilt rows, when they
	// hold them (trackRows.holds).
	pos int32
}

func libSortValues(t *library.Track) sortValues {
	v := sortValues{
		title: t.Title, artist: t.Artist, album: t.Album, genre: t.Genre,
		comment: t.Comment, remixer: t.Remixer, label: t.Label, origArtist: t.OriginalArtist,
		tempo:   int64(math.Round(t.BPM * 100)),
		rating:  int64(int8(t.Rating)),
		seconds: int64(t.Duration.Seconds()), bitrate: int64(t.Bitrate),
		color: int64(t.ColorID), plays: int64(t.PlayCount),
	}
	if !t.DateAdded.IsZero() {
		v.dateAdded = t.DateAdded.Format("2006-01-02")
	}
	return v
}

func (v *sortValues) text(id uint32) string {
	switch id {
	case 2:
		return v.artist
	case 3:
		return v.album
	case 6:
		return v.genre
	case 7:
		return v.comment
	case 9:
		return v.remixer
	case 10:
		return v.label
	case 11:
		return v.origArtist
	}
	return v.title
}

func (v *sortValues) number(id uint32) int64 {
	switch id {
	case 5:
		return v.rating
	case 8:
		return v.seconds
	case 13:
		return v.bitrate
	case 15:
		return v.color
	case 16:
		return v.plays
	}
	return 0
}

// sortCollationKey orders text as rekordbox's sorts and lists do, checked
// against a real rekordbox's lists and the deck's cursor positions in its
// sorted TRACK lists: case and accents are ignored, letters come first, then
// digits ("A380" after "About"), then every other character, spaces and
// punctuation included, by its code point (so a space sorts before a comma).
// So text starting with a digit sorts after text starting with a letter, and
// text starting with a space or a symbol after both.
func sortCollationKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		case r >= '0' && r <= '9':
			b.WriteRune(0x10FF00 + r - '0') // past every letter, in order
		default:
			b.WriteRune(0x10FFF0) // past every digit, then by the character
			b.WriteRune(r)
		}
	}
	return b.String()
}

// orderByName sorts category rows (artists, albums, labels and the like) by
// name in rekordbox's collation (sortCollationKey); names that collate equal
// keep ID order.
func orderByName(items []*menuItem) {
	type named struct {
		m   *menuItem
		key string
	}
	ns := make([]named, len(items))
	for i, m := range items {
		ns[i] = named{m, sortCollationKey(m.Label1)}
	}
	slices.SortFunc(ns, func(a, b named) int {
		if c := strings.Compare(a.key, b.key); c != 0 {
			return c
		}
		return cmp.Compare(a.m.ID, b.m.ID)
	})
	for i := range ns {
		items[i] = ns[i].m
	}
}

// orderByTitle sorts track rows by title in rekordbox's collation, rows whose
// titles collate equal keeping their order.
func (h *Handler) orderByTitle(items []*menuItem) {
	sortRows(h.trackRows(), items, sortTitle, sortSpecs[sortTitle])
}

// orderByTrackNumber sorts an album's track rows by their track number
// (argument 9), then title, as rekordbox lists an album's tracks.
func (h *Handler) orderByTrackNumber(items []*menuItem) {
	h.orderByTitle(items)
	slices.SortStableFunc(items, func(a, b *menuItem) int { return cmp.Compare(a.trackPos, b.trackPos) })
}

// sortSpecFor returns the spec of a sort ID that reorders a track list, or
// false for DEFAULT and the IDs that keep the list's own order and column (14
// and anything beyond 17).
func sortSpecFor(order uint32) (sortSpec, bool) {
	if order == sortDefault {
		return sortSpec{}, false
	}
	spec, ok := sortSpecs[order]
	return spec, ok
}

// sortTracks orders a track list by a deck-requested sort ID and gives its
// rows that sort's second column; sort 0 (DEFAULT) leaves the list and its
// configured column alone. Tracks the sort ranks equal keep the list's own
// order (title order for TRACK, playlist order for a playlist), as
// rekordbox's do.
func (h *Handler) sortTracks(items []*menuItem, order uint32) {
	spec, ok := sortSpecFor(order)
	if !ok {
		return
	}
	c := h.trackRows()
	sortRows(c, items, order, spec)
	h.applySortColumns(c, items, order, spec)
}

// sortEntry is a row being sorted: its collation key, for the sorts that
// order by text, and its position before the sort.
type sortEntry struct {
	m   *menuItem
	key string
	pos int
}

// sortRows orders rows by a sort's own field, rows the sort ranks equal
// keeping their order. Each row's collation key is looked up once (from c's
// prebuilt keys for the rows c holds), and ties are broken by position, which
// makes the unstable sort stable.
func sortRows(c *trackRows, items []*menuItem, order uint32, spec sortSpec) {
	text := spec.kind == sortText || spec.kind == sortTitleOrd
	if spec.kind == sortTitleOrd {
		order = sortTitle
	}
	var keys []string
	if text {
		keys = c.collationKeys(order)
	}
	entries := make([]sortEntry, len(items))
	for i, m := range items {
		entries[i] = sortEntry{m: m, pos: i}
		switch v := m.sv; {
		case !text || v == nil:
		case c.holds(v):
			entries[i].key = keys[v.pos]
		default:
			entries[i].key = sortCollationKey(v.text(order))
		}
	}
	slices.SortFunc(entries, func(a, b sortEntry) int {
		if c := compareRows(&a, &b, order, spec); c != 0 {
			return c
		}
		return cmp.Compare(a.pos, b.pos)
	})
	for i := range entries {
		items[i] = entries[i].m
	}
}

// applySortColumns gives sorted rows the sort's second column and the
// request's context track type in argument 7, as rekordbox's sorted rows
// carry.
func (h *Handler) applySortColumns(c *trackRows, items []*menuItem, order uint32, spec sortSpec) {
	for _, m := range items {
		applySortColumn(c, m, order, spec)
	}
	markContextType(items, h.ctxTrackType)
}

// compareRows orders two rows by a sort's own field: negative when a comes
// first, positive when b does, and 0 when the field ties.
func compareRows(a, b *sortEntry, order uint32, spec sortSpec) int {
	va, vb := a.m.sv, b.m.sv
	if va == nil || vb == nil {
		return 0
	}
	// last orders a row whose field is missing after one whose isn't.
	last := func(aMissing bool) int {
		if aMissing {
			return 1
		}
		return -1
	}
	switch spec.kind {
	case sortTitleOrd:
		return strings.Compare(a.key, b.key)
	case sortText:
		if (a.key == "") != (b.key == "") {
			return last(a.key == "") // empty last
		}
		return strings.Compare(a.key, b.key)
	case sortNumber:
		na, nb := va.number(order), vb.number(order)
		if order == 15 && (na == 0) != (nb == 0) {
			return last(na == 0) // uncoloured last
		}
		if spec.desc {
			return cmp.Compare(nb, na)
		}
		return cmp.Compare(na, nb)
	case sortTempo:
		if (va.tempo == 0) != (vb.tempo == 0) {
			return last(va.tempo == 0) // no tempo last
		}
		return cmp.Compare(va.tempo, vb.tempo)
	case sortTone:
		return cmp.Compare(a.m.newKeyID, b.m.newKeyID) // keyless (0) first
	case sortDate:
		if (va.dateAdded == "") != (vb.dateAdded == "") {
			return last(va.dateAdded == "") // undated last
		}
		return strings.Compare(vb.dateAdded, va.dateAdded) // newest first
	}
	return 0
}

// applySortColumn gives a sorted row the sort's second column: its type,
// label, and argument 0 (the sort's number, or 0 for text and dates).
func applySortColumn(c *trackRows, m *menuItem, order uint32, spec sortSpec) {
	v := m.sv
	if v == nil {
		return
	}
	m.ItemType = uint32(spec.column)<<8 | 0x04
	m.ParentID = 0
	m.Label2 = ""
	switch spec.kind {
	case sortTitleOrd: // ALPHABET: ordered by title, which it shows again
		m.Label2 = v.title
	case sortText:
		m.Label2 = v.text(order)
	case sortNumber:
		m.ParentID = uint32(v.number(order))
		if order == 15 && v.color >= 1 && v.color <= 8 {
			m.ItemType = uint32(0x13+v.color)<<8 | 0x04
		}
	case sortTempo:
		m.ParentID = uint32(v.tempo)
		m.Label2 = c.tempoLabel(m)
	case sortTone:
		if m.newKeyID > 0 {
			m.ParentID = m.newKeyID - 1
		}
		m.Label2 = c.toneLabel(m)
	case sortDate:
		m.Label2 = v.dateAdded
	}
}

// formatTempoLabel formats a row's BPM-sort label, "<bpm> bpm - <key>".
func formatTempoLabel(m *menuItem) string {
	label := fmt.Sprintf("%.1f bpm", float64(m.sv.tempo)/100)
	if key := m.sortKey; key != "" {
		label += " - " + key
	}
	return label
}

// formatToneLabel formats a row's KEY-sort label, "<key> - <bpm> bpm", or
// the key alone for a track with no tempo.
func formatToneLabel(m *menuItem) string {
	if m.sv.tempo == 0 {
		return m.sortKey
	}
	return fmt.Sprintf("%s - %.1f bpm", m.sortKey, math.Abs(float64(m.sv.tempo))/100)
}

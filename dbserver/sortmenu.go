// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
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
	h.setPendingAll(msg, items)
	var list uint32
	if len(msg.Args) > 2 {
		list = msg.Args[2].Int()
	}
	dlog.Debugf("dbserver: sort menu (0x1400) for list %#x returning %d sorts", list, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// sortValues holds the track fields the sorts read, filled when a track row
// is built from either a library or a PDB track.
type sortValues struct {
	title, artist, album, genre, comment, remixer, label, origArtist string
	tempo                                                            int64 // BPM x 100
	rating                                                           int64
	seconds, bitrate, color, plays                                   int64
	dateAdded                                                        string // YYYY-MM-DD, "" when undated
}

func libSortValues(t *library.Track) *sortValues {
	v := &sortValues{
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

func pdbSortValues(t *pdb.Track) *sortValues {
	v := &sortValues{
		title: t.Title, artist: t.Artist, album: t.Album, genre: t.Genre,
		comment: t.Comment, label: t.Label,
		tempo: int64(t.Tempo), rating: int64(t.Rating),
		seconds: int64(t.Duration), bitrate: int64(t.Bitrate),
		color: int64(t.ColorID), plays: int64(t.PlayCount),
	}
	if len(t.DateAdded) >= 10 {
		v.dateAdded = t.DateAdded[:10]
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

// sortCollationKey orders text as rekordbox's sorts do, checked against a
// real library's sorted positions: case and accents are ignored, spaces and
// punctuation don't count, and digits come after every letter, so "A380"
// sorts after "About" and titles starting with digits or symbols after the
// lettered ones.
func sortCollationKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsDigit(r):
			// Past every letter, keeping the digits' order.
			if d := unicode.ToLower(r) - '0'; d >= 0 && d <= 9 {
				b.WriteRune(0x10FF00 + d)
			} else {
				b.WriteRune(0x10FFF0)
			}
		}
	}
	return b.String()
}

// orderByTitle sorts tracks by title as rekordbox's TRACK list orders them.
func orderByTitle(items []*menuItem) {
	keys := make(map[*menuItem]string, len(items))
	for _, m := range items {
		keys[m] = sortCollationKey(m.Label1)
	}
	sort.SliceStable(items, func(i, j int) bool { return keys[items[i]] < keys[items[j]] })
}

// sortTracks orders a track list by a deck-requested sort ID and gives its
// rows that sort's second column; sort 0 (DEFAULT) leaves the list and its
// configured column alone. Tracks the sort ranks equal keep the list's own
// order (title order for TRACK, playlist order for a playlist), as
// rekordbox's do. Sorted rows carry the request's context track type in
// argument 7, as rekordbox's do.
func (h *Handler) sortTracks(items []*menuItem, order uint32) {
	if order == sortDefault {
		return
	}
	spec, ok := sortSpecs[order]
	if !ok {
		return // ID 14 and anything beyond 17: the list's order and column
	}
	texts := make(map[*menuItem]string, len(items))
	for _, m := range items {
		switch {
		case spec.kind == sortTitleOrd:
			texts[m] = sortCollationKey(m.Label1)
		case m.sv != nil && spec.kind == sortText:
			texts[m] = sortCollationKey(m.sv.text(order))
		}
		h.applySortColumn(m, order, spec)
	}
	markContextType(items, h.ctxTrackType)

	// less orders two rows by the sort's own field, reporting whether it
	// decided (false, false when the field ties).
	less := func(a, b *menuItem) (bool, bool) {
		va, vb := a.sv, b.sv
		if va == nil || vb == nil {
			return false, false
		}
		switch spec.kind {
		case sortTitleOrd:
			if texts[a] != texts[b] {
				return texts[a] < texts[b], true
			}
		case sortText:
			ta, tb := texts[a], texts[b]
			if (ta == "") != (tb == "") {
				return tb == "", true // empty last
			}
			if ta != tb {
				return ta < tb, true
			}
		case sortNumber:
			na, nb := va.number(order), vb.number(order)
			if order == 15 && (na == 0) != (nb == 0) {
				return nb == 0, true // uncoloured last
			}
			if na != nb {
				if spec.desc {
					return na > nb, true
				}
				return na < nb, true
			}
		case sortTempo:
			if (va.tempo == 0) != (vb.tempo == 0) {
				return vb.tempo == 0, true // no tempo last
			}
			if va.tempo != vb.tempo {
				return va.tempo < vb.tempo, true
			}
		case sortTone:
			if a.newKeyID != b.newKeyID {
				return a.newKeyID < b.newKeyID, true // keyless (0) first
			}
		case sortDate:
			if (va.dateAdded == "") != (vb.dateAdded == "") {
				return vb.dateAdded == "", true // undated last
			}
			if va.dateAdded != vb.dateAdded {
				return va.dateAdded > vb.dateAdded, true
			}
		}
		return false, false
	}
	sort.SliceStable(items, func(i, j int) bool {
		l, _ := less(items[i], items[j])
		return l
	})
}

// applySortColumn gives a sorted row the sort's second column: its type,
// label, and argument 0 (the sort's number, or 0 for text and dates).
func (h *Handler) applySortColumn(m *menuItem, order uint32, spec sortSpec) {
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
		m.Label2 = fmt.Sprintf("%.1f bpm", float64(v.tempo)/100)
		if key := m.sortKey; key != "" {
			m.Label2 += " - " + key
		}
	case sortTone:
		if m.newKeyID > 0 {
			m.ParentID = m.newKeyID - 1
		}
		m.Label2 = m.sortKey
		if v.tempo != 0 {
			m.Label2 = fmt.Sprintf("%s - %.1f bpm", m.sortKey, math.Abs(float64(v.tempo))/100)
		}
	case sortDate:
		m.Label2 = v.dateAdded
	}
}

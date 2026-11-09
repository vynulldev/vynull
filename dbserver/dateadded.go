// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"slices"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// DATE ADDED and DJ PLAY COUNT, as rekordbox serves them (link-spec
// extended-families oracle, rekordbox 7.2.19):
//
//	0x1708 [ctx, sort]                     the years tracks were added, newest first
//	0x1808 [ctx, sort, year]               ALL, then that year's months
//	0x1908 [ctx, sort, year, month]        ALL, then that month's days
//	0x1a08 [ctx, sort, year, month, day]   the tracks added that day
//	0x100e [ctx, sort]                     the play counts the tracks have
//	0x110e [ctx, sort, count]              the tracks played that many times
//
// Year, month, day and count rows carry the number as their ID and no label
// (the deck formats it). The month and day levels always start with ALL
// (0xffffffff), even when empty; ALL as the month leaves only ALL among the
// days, and ALL as the day takes the whole month's tracks.
//
// DJ PLAY COUNT reads counts as 16 bits: the count list shows them signed
// (65535 as 0xffffffff, first), and a track request matches a count equal
// to its selector's low byte, so counts above 255 can't be selected. Its
// track rows have their own builder: the track number in argument 9, the
// right column's plain value, not the Key/BPM composite, and the key name
// cut to 127 UTF-16 units (link-spec boundaries suite).

const (
	dateRowType  = 0x2e // a year, month or day of DATE ADDED
	countRowType = 0x2a // a DJ PLAY COUNT
)

// datePart selects tracks by when they were added: year, then month, then
// day, each 0xffffffff for any.
type datePart struct{ year, month, day uint32 }

func (d datePart) admits(t *library.Track) bool {
	if t.DateAdded.IsZero() {
		return false
	}
	match := func(want uint32, got int) bool { return want == allSelector || want == uint32(got) }
	return match(d.year, t.DateAdded.Year()) && match(d.month, int(t.DateAdded.Month())) && match(d.day, t.DateAdded.Day())
}

const allSelector = 0xffffffff

// numberRows are the rows of a level of numbers (years, days, counts).
func numberRows(nums []uint32, itemType uint32, all bool) []*menuItem {
	items := make([]*menuItem, 0, len(nums)+1)
	if all {
		items = append(items, &menuItem{ID: allSelector, Label1: "ALL", ItemType: 0x00a0})
	}
	for _, n := range nums {
		items = append(items, &menuItem{ID: n, ItemType: itemType})
	}
	return items
}

// distinct returns the distinct values f gives the library's tracks (those
// it reports ok for), ascending.
func (h *Handler) distinct(f func(t *library.Track) (uint32, bool)) []uint32 {
	if h.lib == nil {
		return nil
	}
	seen := map[uint32]bool{}
	var out []uint32
	for _, t := range h.lib.Tracks() {
		if v, ok := f(t); ok && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// trackLeaf returns the TRACK list's rows of the tracks keep admits, in the
// order the request's sort asks for (title order by default), and their
// tracks. playCount gives them DJ PLAY COUNT's row shape.
func (h *Handler) trackLeaf(msg *proto.DBMessage, keep func(t *library.Track) bool, playCount bool) []*menuItem {
	order := getSortOrder(msg)
	c := h.trackRows()
	var idx []int32
	for _, i := range c.trackOrder(order) {
		if keep(c.tracks[i]) {
			idx = append(idx, i)
		}
	}
	items := c.copyRows(idx)
	if spec, ok := sortSpecFor(order); ok {
		h.applySortColumns(c, items, order, spec)
	}
	if playCount {
		for n, m := range items {
			t := c.tracks[idx[n]]
			m.trackPos = uint32(t.TrackNum)
			m.sortKey = cutUTF16(m.sortKey, playCountKeyUnits)
			switch m.ItemType >> 8 {
			case 0x0f: // Key
				m.Label2 = cutUTF16(t.Key, playCountKeyUnits)
			case 0x0d: // BPM
				m.Label2 = fmt.Sprintf("%.1f bpm", t.BPM)
			}
		}
	}
	return h.applyTrackFilter(msg, items)
}

// playCountKeyUnits is how much of a key name DJ PLAY COUNT's rows keep.
const playCountKeyUnits = 127

// cutUTF16 cuts s to at most n UTF-16 code units, at a character boundary.
func cutUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

func (h *Handler) handleDateAddedYears(msg *proto.DBMessage) []*proto.DBMessage {
	years := h.distinct(func(t *library.Track) (uint32, bool) {
		return uint32(t.DateAdded.Year()), !t.DateAdded.IsZero()
	})
	slices.Reverse(years)
	dlog.Debugf("dbserver: DATE ADDED years returning %d items", len(years))
	return h.countReply(msg, numberRows(years, dateRowType, false))
}

func (h *Handler) handleDateAddedMonths(msg *proto.DBMessage) []*proto.DBMessage {
	in := datePart{year: msgArg(msg, 2), month: allSelector, day: allSelector}
	months := h.distinct(func(t *library.Track) (uint32, bool) {
		return uint32(t.DateAdded.Month()), in.admits(t)
	})
	dlog.Debugf("dbserver: DATE ADDED months of %d returning %d items", in.year, len(months))
	return h.countReply(msg, numberRows(months, dateRowType, true))
}

func (h *Handler) handleDateAddedDays(msg *proto.DBMessage) []*proto.DBMessage {
	in := datePart{year: msgArg(msg, 2), month: msgArg(msg, 3), day: allSelector}
	var days []uint32
	if in.month != allSelector {
		days = h.distinct(func(t *library.Track) (uint32, bool) {
			return uint32(t.DateAdded.Day()), in.admits(t)
		})
	}
	dlog.Debugf("dbserver: DATE ADDED days of %d-%d returning %d items", in.year, in.month, len(days))
	return h.countReply(msg, numberRows(days, dateRowType, true))
}

func (h *Handler) handleDateAddedTracks(msg *proto.DBMessage) []*proto.DBMessage {
	in := datePart{year: msgArg(msg, 2), month: msgArg(msg, 3), day: msgArg(msg, 4)}
	items := h.trackLeaf(msg, in.admits, false)
	dlog.Debugf("dbserver: DATE ADDED tracks of %d-%d-%d returning %d items", in.year, in.month, in.day, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handlePlayCounts(msg *proto.DBMessage) []*proto.DBMessage {
	// Distinct counts as signed 16-bit values, ascending, offset so the
	// unsigned sort keeps their signed order.
	keys := h.distinct(func(t *library.Track) (uint32, bool) {
		return uint32(int32(int16(uint16(t.PlayCount))) + 1<<15), true
	})
	counts := make([]uint32, len(keys))
	for i, k := range keys {
		counts[i] = uint32(int32(k) - 1<<15)
	}
	dlog.Debugf("dbserver: DJ PLAY COUNT returning %d counts", len(counts))
	return h.countReply(msg, numberRows(counts, countRowType, false))
}

func (h *Handler) handleTracksByPlayCount(msg *proto.DBMessage) []*proto.DBMessage {
	count := int(uint8(msgArg(msg, 2)))
	items := h.trackLeaf(msg, func(t *library.Track) bool { return t.PlayCount == count }, true)
	dlog.Debugf("dbserver: DJ PLAY COUNT %d returning %d tracks", count, len(items))
	return h.countReply(msg, items)
}

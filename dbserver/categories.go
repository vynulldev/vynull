// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"github.com/vynulldev/vynull/internal/dlog"
	"sort"
	"strings"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// categories.go contains the top-level "list of X" category handlers
// (0x1002 artists, 0x1003 albums, 0x1004 tracks, 0x1006 BPM, 0x1007
// rating, 0x1008 year, 0x100a label, 0x100d colour, 0x1010 time,
// 0x1011 bitrate, 0x1013 filename, 0x1014 keys, 0x1602 remixer,
// 0x1302 original artist, 0x2001 hot cue bank) plus the helpers that
// bucket tracks for each (e.g. tracksByRoundedBPM, tracksForBPMBucket).

func (h *Handler) handleGetTracks(msg *proto.DBMessage) []*proto.DBMessage {
	if h.pdb != nil {
		h.pendingItems = h.pdbTracksToItems(h.pdb.Tracks)
	} else {
		tracks := h.lib.Tracks()
		h.pendingItems = h.tracksToStdItems(tracks)
	}
	sortItems(h.pendingItems, getSortOrder(msg))
	dlog.Debugf("dbserver: get tracks returning %d items (sort=%d)", len(h.pendingItems), getSortOrder(msg))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetArtists(msg *proto.DBMessage) []*proto.DBMessage {
	if h.pdb != nil {
		var items []*menuItem
		for id, name := range h.pdb.Artists {
			items = append(items, &menuItem{
				ID:       id,
				Label1:   name,
				ItemType: 0x07, // artist item
			})
		}
		h.pendingItems = items
	} else {
		artists := h.lib.Artists()
		h.pendingItems = make([]*menuItem, len(artists))
		for i, name := range artists {
			h.pendingItems[i] = &menuItem{
				ID:       library.HashID("artist", name),
				Label1:   name,
				ItemType: 0x07,
			}
		}
	}
	sortItems(h.pendingItems, sortTitle) // artists always sorted by name
	dlog.Debugf("dbserver: get artists returning %d items", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetAlbums(msg *proto.DBMessage) []*proto.DBMessage {
	if h.pdb != nil {
		var items []*menuItem
		for id, name := range h.pdb.Albums {
			items = append(items, &menuItem{
				ID:       id,
				Label1:   name,
				ItemType: 0x02, // album item
			})
		}
		h.pendingItems = items
	} else {
		albums := h.lib.Albums()
		h.pendingItems = make([]*menuItem, len(albums))
		for i, name := range albums {
			h.pendingItems[i] = &menuItem{
				ID:       library.HashID("album", name),
				Label1:   name,
				ItemType: 0x02,
			}
		}
	}
	sortItems(h.pendingItems, sortTitle) // albums always sorted by name
	dlog.Debugf("dbserver: get albums returning %d items", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetGenres(msg *proto.DBMessage) []*proto.DBMessage {
	genres := h.lib.Genres()
	h.pendingItems = make([]*menuItem, len(genres))
	for i, name := range genres {
		h.pendingItems[i] = &menuItem{
			ID:       library.HashID("genre", name),
			Label1:   name,
			ItemType: 0x06, // genre
		}
	}
	dlog.Debugf("dbserver: get genres returning %d items", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetBPM(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1006: BPM list. Group tracks by integer (rounded) BPM and emit
	// one menu entry per non-empty bucket. tracksByRoundedBPM is the
	// single source of truth — drill handlers (0x1106 / 0x1206) reuse
	// it via tracksForBPMBucket so every entry in this list is
	// guaranteed to have at least one drillable track.
	buckets := h.tracksByRoundedBPM()
	bpms := make([]int, 0, len(buckets))
	for bpm := range buckets {
		bpms = append(bpms, bpm)
	}
	sort.Ints(bpms)
	items := make([]*menuItem, 0, len(bpms))
	for _, bpm := range bpms {
		items = append(items, &menuItem{
			ID:       uint32(bpm * 100),
			Label1:   fmt.Sprintf("%d", bpm),
			ItemType: 0x0d, // BPM item
		})
	}
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: get BPM returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// tracksByRoundedBPM groups every library track by its rounded integer
// BPM. Uses h.lib only — the drill handlers (0x1106 / 0x1206) also
// search h.lib, so building from the same source guarantees every
// bucket key has at least one drillable track.
func (h *Handler) tracksByRoundedBPM() map[int][]*library.Track {
	out := make(map[int][]*library.Track)
	if h.lib == nil {
		return out
	}
	for _, t := range h.lib.Tracks() {
		if t.BPM <= 0 {
			continue
		}
		bpm := int(t.BPM + 0.5)
		out[bpm] = append(out[bpm], t)
	}
	return out
}

func (h *Handler) handleGetBPMRanges(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1106: returns 7 BPM percentage ranges (0% to 6%).
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	targetBPM := msg.Args[2].Int() // BPM * 100

	// Count tracks for each percentage range.
	var items []*menuItem
	for pct := 0; pct <= 6; pct++ {
		count := h.countTracksForBPMRange(targetBPM, pct)
		items = append(items, &menuItem{
			ID:       uint32(pct),
			ParentID: 0,
			Label1:   fmt.Sprintf("%d", count),
			Label2:   "",
			ItemType: 0x0d, // BPM item
		})
	}
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: BPM ranges for %d returning 7 items", targetBPM/100)
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByBPM(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1206: tracks for BPM +/- percentage.
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	targetBPM100 := msg.Args[2].Int()
	pctRange := int(msg.Args[3].Int())
	tracks := h.tracksForBPMBucket(targetBPM100, pctRange)
	items := h.tracksToStdItems(tracks)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: tracks for BPM %.0f +/-%d%% returning %d items", float64(targetBPM100)/100.0, pctRange, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// tracksForBPMBucket returns tracks for a BPM list entry at the given
// ± percentage range. For ±0% it matches tracks in the same rounded-
// BPM bucket as the list entry — same semantics handleGetBPM used to
// build the list, so the per-entry count is guaranteed >0. For ±N%
// (N>0) it widens to a float range around the target.
func (h *Handler) tracksForBPMBucket(targetBPM100 uint32, pctRange int) []*library.Track {
	targetBPM := float64(targetBPM100) / 100.0
	targetInt := int(targetBPM + 0.5)
	if h.lib == nil {
		return nil
	}
	var tracks []*library.Track
	if pctRange == 0 {
		for _, t := range h.lib.Tracks() {
			if t.BPM > 0 && int(t.BPM+0.5) == targetInt {
				tracks = append(tracks, t)
			}
		}
		return tracks
	}
	margin := targetBPM * float64(pctRange) / 100.0
	for _, t := range h.lib.Tracks() {
		if t.BPM >= targetBPM-margin && t.BPM <= targetBPM+margin {
			tracks = append(tracks, t)
		}
	}
	return tracks
}

// countTracksForBPMRange counts tracks within a BPM +/- percentage range.
func (h *Handler) countTracksForBPMRange(targetBPM100 uint32, pctRange int) int {
	return len(h.tracksForBPMBucket(targetBPM100, pctRange))
}

func (h *Handler) handleGetYearsForDecade(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1108: returns [ALL] + individual years within a decade.
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	decadeStart := int(msg.Args[2].Int())

	yearSet := make(map[int]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Year >= decadeStart && t.Year < decadeStart+10 {
				yearSet[t.Year] = true
			}
		}
	}

	var items []*menuItem
	items = append(items, &menuItem{
		ID: 0xffffffff, Label1: "ALL", ItemType: 0x00a0,
	})

	var years []int
	for y := range yearSet {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	for _, y := range years {
		items = append(items, &menuItem{
			ID:       uint32(y),
			Label1:   fmt.Sprintf("%d", y),
			ItemType: 0x11,
		})
	}

	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: years for decade %d returning %d items", decadeStart, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByYear(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1208: tracks for decade + year.
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	decadeStart := int(msg.Args[2].Int())
	yearID := msg.Args[3].Int()

	var tracks []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if yearID == 0xffffffff {
				if t.Year >= decadeStart && t.Year < decadeStart+10 {
					tracks = append(tracks, t)
				}
			} else {
				if t.Year == int(yearID) {
					tracks = append(tracks, t)
				}
			}
		}
	}
	items := h.tracksToStdItems(tracks)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: tracks for decade %d year 0x%08x returning %d items", decadeStart, yearID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetYears(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1008: decade list (2020s, 2010s, etc.).
	decadeSet := make(map[int]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Year > 0 {
				decadeSet[(t.Year/10)*10] = true
			}
		}
	}
	var items []*menuItem
	for decade := range decadeSet {
		items = append(items, &menuItem{
			ID:       uint32(decade),
			Label1:   fmt.Sprintf("%ds", decade),
			ItemType: 0x11,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: get decades returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}
func (h *Handler) handleGetRating(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1007: RATING list. The deck shows 1★ … 5★ rows; selecting
	// one drills via 0x1107 to the tracks of that rating.
	items := []*menuItem{
		{ID: 1, Label1: "1", ItemType: 0x0a},
		{ID: 2, Label1: "2", ItemType: 0x0a},
		{ID: 3, Label1: "3", ItemType: 0x0a},
		{ID: 4, Label1: "4", ItemType: 0x0a},
		{ID: 5, Label1: "5", ItemType: 0x0a},
	}
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: RATING list returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByRating(msg *proto.DBMessage) []*proto.DBMessage {
	rating := uint32(0)
	if len(msg.Args) >= 3 {
		rating = msg.Args[2].Int()
	}
	var matches []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if uint32(t.Rating) == rating {
				matches = append(matches, t)
			}
		}
	}
	items := h.tracksToStdItems(matches)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: RATING drill rating=%d returning %d tracks", rating, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetTime — opcode 0x1010 (2-arg variant). Groups library by
// integer track duration in minutes. Each row's ID is the minute
// bucket; the drill (likely 0x1110) would return tracks in that
// bucket — TBD until we see one in a capture.
func (h *Handler) handleGetTime(msg *proto.DBMessage) []*proto.DBMessage {
	bucketSet := make(map[int]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			secs := int(t.Duration.Seconds())
			if secs > 0 {
				bucketSet[secs/60] = true
			}
		}
	}
	items := make([]*menuItem, 0, len(bucketSet))
	for m := range bucketSet {
		items = append(items, &menuItem{
			ID:       uint32(m),
			Label1:   fmt.Sprintf("%d:00", m),
			ItemType: 0x0b,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: TIME list returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetTracksByTime — opcode 0x1110. arg[2] is the minute
// bucket the user selected from the TIME list.
func (h *Handler) handleGetTracksByTime(msg *proto.DBMessage) []*proto.DBMessage {
	bucket := uint32(0)
	if len(msg.Args) >= 3 {
		bucket = msg.Args[2].Int()
	}
	var matches []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if uint32(int(t.Duration.Seconds())/60) == bucket {
				matches = append(matches, t)
			}
		}
	}
	items := h.tracksToStdItems(matches)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: TIME drill minute=%d returning %d tracks", bucket, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetTracksByBitrate — opcode 0x1111. arg[2] is the kbps the
// user selected from the BITRATE list.
func (h *Handler) handleGetTracksByBitrate(msg *proto.DBMessage) []*proto.DBMessage {
	bitrate := uint32(0)
	if len(msg.Args) >= 3 {
		bitrate = msg.Args[2].Int()
	}
	var matches []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if uint32(t.Bitrate) == bitrate {
				matches = append(matches, t)
			}
		}
	}
	items := h.tracksToStdItems(matches)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: BITRATE drill kbps=%d returning %d tracks", bitrate, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetBitrate — opcode 0x1011 (2-arg variant). Groups library
// by integer kbps.
func (h *Handler) handleGetBitrate(msg *proto.DBMessage) []*proto.DBMessage {
	set := make(map[int]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Bitrate > 0 {
				set[int(t.Bitrate)] = true
			}
		}
	}
	items := make([]*menuItem, 0, len(set))
	for b := range set {
		items = append(items, &menuItem{
			ID:       uint32(b),
			Label1:   fmt.Sprintf("%d", b),
			ItemType: 0x10,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: BITRATE list returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetFilename — opcode 0x1013 (2-arg variant). Returns every
// library track as a row, labelled with the filename portion of
// FilePath. Selecting one loads the track via the standard
// metadata fetch flow.
func (h *Handler) handleGetFilename(msg *proto.DBMessage) []*proto.DBMessage {
	var items []*menuItem
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			name := t.FilePath
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			items = append(items, &menuItem{
				ID:       t.ID,
				Label1:   name,
				ItemType: 0x0004, // track row
				ArtID:    t.ArtID,
				FileType: t.FileType,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label1 < items[j].Label1 })
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: FILENAME list returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetHotCueBank — opcode 0x2001. We don't store hot-cue banks
// separately yet, so return empty. The deck still shows the menu
// entry (single empty row).
func (h *Handler) handleGetHotCueBank(msg *proto.DBMessage) []*proto.DBMessage {
	h.setPendingAll(msg, nil)
	dlog.Debugf("dbserver: HOT CUE BANK list (empty — not stored)")
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetKeys(msg *proto.DBMessage) []*proto.DBMessage {
	if h.pdb != nil {
		var items []*menuItem
		for id, name := range h.pdb.Keys {
			items = append(items, &menuItem{
				ID:       id,
				Label1:   name,
				ItemType: 0x0f, // key item
			})
		}
		h.pendingItems = items
	} else if h.lib != nil {
		// Build key list from library tracks.
		keySet := make(map[string]bool)
		for _, t := range h.lib.Tracks() {
			if t.Key != "" {
				keySet[t.Key] = true
			}
		}
		var items []*menuItem
		for key := range keySet {
			items = append(items, &menuItem{
				ID:       library.HashID("key", key),
				Label1:   key,
				ItemType: 0x0f,
			})
		}
		h.pendingItems = items
	} else {
		h.pendingItems = nil
	}
	sortItems(h.pendingItems, sortTitle)
	dlog.Debugf("dbserver: get keys returning %d items", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetLabels(msg *proto.DBMessage) []*proto.DBMessage {
	var items []*menuItem
	if h.lib != nil {
		labelSet := make(map[string]bool)
		for _, t := range h.lib.Tracks() {
			if t.Label != "" {
				labelSet[t.Label] = true
			}
		}
		for label := range labelSet {
			items = append(items, &menuItem{
				ID: library.HashID("label", label), Label1: label, ItemType: 0x0e,
			})
		}
	}
	h.pendingItems = items
	sortItems(h.pendingItems, sortTitle)
	dlog.Debugf("dbserver: get labels returning %d items", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetRemixers(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1009: remixer list.
	remixerSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Remixer != "" {
				remixerSet[t.Remixer] = true
			}
		}
	}
	var items []*menuItem
	for name := range remixerSet {
		items = append(items, &menuItem{
			ID: library.HashID("remixer", name), Label1: name, ItemType: 0x08,
		})
	}
	sortItems(items, sortTitle)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: get remixers returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetOriginalArtists(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x100b: original artist list.
	artistSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.OriginalArtist != "" {
				artistSet[t.OriginalArtist] = true
			}
		}
	}
	var items []*menuItem
	for name := range artistSet {
		items = append(items, &menuItem{
			ID: library.HashID("origartist", name), Label1: name, ItemType: 0x0b,
		})
	}
	sortItems(items, sortTitle)
	h.setPendingAll(msg, items)
	dlog.Debugf("dbserver: get original artists returning %d items", len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

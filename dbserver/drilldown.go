// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"log"
	"strings"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// drilldown.go contains the multi-level drill handlers — anything the
// deck issues *after* a category has been opened. By-artist /
// by-album / by-genre walks, the SEARCH handler (0x1300 query +
// 0x1200 select), the NXS2 menu-load / drill setup, key compatibility
// with camelot distance grouping, label drill (artist → album →
// tracks), colour drill, and the 0x2005/0x2805 placeholder stubs.

func (h *Handler) handleNXS2MenuLoad(msg *proto.DBMessage) []*proto.DBMessage {
	menu := dmstMenu(msg)
	log.Printf("dbserver: NXS2 menu load 0x1010 menu=%d args=%d", menu, len(msg.Args))

	// Populate the category if we can.
	switch menu {
	case 1:
		h.populateGenres(menu)
	case 2:
		h.populateArtists(menu)
	case 3:
		h.populateAlbums(menu)
	case 4:
		h.populateTracks(menu)
	case 6:
		h.populateBPM(menu)
	case 8:
		h.populateYears(menu)
	case 10:
		h.populateLabels(menu)
	case 12:
		h.populateKeys(menu)
	}

	count := len(h.getPending(menu))
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(count))},
	}}
}

// handleNXS2DrillDown handles 0x1011/0x1012/0x1013 — NXS2 category drill-downs.
// Args: [spec, sort, id1, id2, id3...] where each id narrows the selection.
// The DMST menu byte identifies the category context.
func (h *Handler) handleNXS2DrillDown(msg *proto.DBMessage) []*proto.DBMessage {
	menu := dmstMenu(msg)
	log.Printf("dbserver: NXS2 drill-down 0x%04x menu=%d args=%d", msg.Type, menu, len(msg.Args))
	for i, a := range msg.Args {
		log.Printf("dbserver: NXS2 drill arg[%d] = 0x%08x (%d)", i, a.Int(), a.Int())
	}

	var items []*menuItem

	// The drill-down args after [spec, sort] are category-specific IDs.
	// Label flow: 0x1011(label_id) → artists, 0x1012(label_id, artist_id) → albums,
	//             0x1013(label_id, artist_id, album_id) → tracks
	switch msg.Type {
	case 0x1011: // Level 1: e.g., artists for label
		if len(msg.Args) >= 3 {
			labelID := msg.Args[2].Int()
			items = h.getArtistsForLabelItems(labelID)
		}
	case 0x1012: // Level 2: e.g., albums for label + artist
		if len(msg.Args) >= 4 {
			labelID := msg.Args[2].Int()
			artistID := msg.Args[3].Int()
			items = h.getAlbumsForLabelArtist(labelID, artistID)
		}
	case 0x1013: // Level 3: e.g., tracks for label + artist + album
		if len(msg.Args) >= 5 {
			labelID := msg.Args[2].Int()
			artistID := msg.Args[3].Int()
			albumID := msg.Args[4].Int()
			items = h.getTracksForLabelArtistAlbum(labelID, artistID, albumID)
		}
	}

	h.setPendingAll(msg, items)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(len(items)))},
	}}
}

// handleNXS2DrillOrSearchSetup dispatches opcode 0x1012, which is
// overloaded: it can be a real drill-down level 2 (albums for label +
// artist) or the SEARCH category's setup signal (sent when the user
// taps into SEARCH on the deck before typing). SEARCH setup has only
// 2 args; drill level 2 has 4. The setup response is identical in
// shape but the log line difference helps debugging which path fired.
func (h *Handler) handleNXS2DrillOrSearchSetup(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) <= 2 {
		log.Printf("dbserver: SEARCH category setup (0x1012 with %d args)", len(msg.Args))
		// Echo the request type and a 0-item count. The deck takes this
		// as the cue to open its on-screen keyboard; the first keystroke
		// then arrives as a 0x1300 query.
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
	return h.handleNXS2DrillDown(msg)
}

// handleSearch handles the SEARCH category's on-deck text-input flow.
// The CDJ sends 0x1300 once per keystroke with the current query
// string in arg[3]; we respond with the match count, then the CDJ
// drills into the result list via the standard 0x3000 render path.
//
// Results follow the standard shape: artists, then albums, then
// tracks — each list deduped and case-insensitive substring matched.
// Selecting an artist or album drills into the existing 0x1102 /
// 0x1202 handlers; selecting a track drills via 0x1200 (handled by
// handleSearchSelect).
func (h *Handler) handleSearch(msg *proto.DBMessage) []*proto.DBMessage {
	query := ""
	if len(msg.Args) >= 4 {
		// Inline-tagged parser hands us the raw UTF-16 BE bytes as a
		// Go string; decode to a normal string for case-insensitive
		// matching below. Trailing NULs (the CDJ sends "T\0") get
		// trimmed by DecodeUTF16BE.
		query = proto.DecodeUTF16BE([]byte(msg.Args[3].Str))
	}
	log.Printf("dbserver: SEARCH query=%q", query)

	var items []*menuItem
	if h.lib != nil && query != "" {
		q := strings.ToLower(query)
		seenArtist := make(map[string]bool)
		seenAlbum := make(map[string]bool)
		var artists, albums, tracks []*menuItem
		for _, t := range h.lib.Tracks() {
			if t.Artist != "" && containsFold(t.Artist, q) && !seenArtist[t.Artist] {
				seenArtist[t.Artist] = true
				artists = append(artists, &menuItem{
					ID:       library.HashID("artist", t.Artist),
					Label1:   t.Artist,
					ItemType: 0x07,
				})
			}
			if t.Album != "" && containsFold(t.Album, q) && !seenAlbum[t.Album] {
				seenAlbum[t.Album] = true
				albums = append(albums, &menuItem{
					ID:       library.HashID("album", t.Album),
					Label1:   t.Album,
					ItemType: 0x02,
				})
			}
			if containsFold(t.Title, q) {
				tracks = append(tracks, h.searchTrackItem(t))
			}
		}
		items = append(items, artists...)
		items = append(items, albums...)
		items = append(items, tracks...)
	}

	h.setPendingAll(msg, items)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(len(items)))},
	}}
}

// searchTrackItem builds a track menu item for SEARCH results. Forces
// the ItemType to the "BPM detail" shape (0x0d04, low byte = 0x04 =
// loadable track) so the deck always treats the row as a directly-
// loadable track on selection — regardless of the user's configured
// track-detail field. Without this, a user with track-detail = COLOR
// gets ItemType 0x1304 for uncoloured tracks, which the deck routes
// through a category drill-in (0x1200 with album context) and fails
// to actually load the picked track.
func (h *Handler) searchTrackItem(t *library.Track) *menuItem {
	m := &menuItem{
		ID:       t.ID,
		Label1:   t.Title,
		ArtID:    t.ArtID,
		FileType: t.FileType,
		ColorID:  uint32(t.ColorID),
		ItemType: 0x0d04,
		ParentID: uint32(t.BPM * 100),
		Label2:   fmt.Sprintf("%.1f bpm", t.BPM),
	}
	if t.Key != "" {
		m.Label2 += " - " + t.Key
	}
	return m
}

// handleGetColors handles opcode 0x100d — the deck navigating into
// the COLOR root-menu category. Returns the 8 standard rekordbox
// track colours (Pink, Red, …, Purple) plus a "no colour" row at
// the top. Each row's ID is the track-colour ID so the deck's
// follow-up "give me tracks of colour N" drill (0x110d) can route
// to handleGetTracksByColor.
func (h *Handler) handleGetColors(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver: COLOR list (0x100d)")
	items := []*menuItem{
		{ID: 0, Label1: "NO COLOR", ItemType: 0x13},
		{ID: 1, Label1: "PINK", ItemType: 0x14},
		{ID: 2, Label1: "RED", ItemType: 0x15},
		{ID: 3, Label1: "ORANGE", ItemType: 0x16},
		{ID: 4, Label1: "YELLOW", ItemType: 0x17},
		{ID: 5, Label1: "GREEN", ItemType: 0x18},
		{ID: 6, Label1: "AQUA", ItemType: 0x19},
		{ID: 7, Label1: "BLUE", ItemType: 0x1a},
		{ID: 8, Label1: "PURPLE", ItemType: 0x1b},
	}
	h.setPendingAll(msg, items)
	h.lastCategoryItems = items
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleGetTracksByColor handles opcode 0x110d — the deck drilling
// into a specific colour's tracks. arg[2] = the colour ID we returned
// in handleGetColors.
func (h *Handler) handleGetTracksByColor(msg *proto.DBMessage) []*proto.DBMessage {
	colorID := uint32(0)
	if len(msg.Args) >= 3 {
		colorID = msg.Args[2].Int()
	}
	log.Printf("dbserver: COLOR drill (0x110d) color=%d", colorID)

	var matches []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if uint32(t.ColorID) == colorID {
				matches = append(matches, t)
			}
		}
	}
	items := h.tracksToStdItems(matches)
	h.setPendingAll(msg, items)
	// Stash these as the "most recent category-query result" too —
	// the deck's follow-up drill render (menu=1, args[5]=0x0c) routes
	// through lastCategoryItems, and without this update it would
	// still hold the colour-list rows from the preceding 0x100d.
	h.lastCategoryItems = items
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// handleUndecodedStub responds to opcodes we recognise the deck
// sends but haven't fully decoded yet. Logs each request's args with
// any embedded ASCII (e.g. "PVB2" tag names in 0x2805) and returns a
// 0-item success ack so the deck doesn't see the unhandled-opcode
// fallback. Replace per-opcode dispatch with real handlers once we
// know what shape to respond with.
func (h *Handler) handleUndecodedStub(msg *proto.DBMessage) []*proto.DBMessage {
	argParts := make([]string, len(msg.Args))
	for i, a := range msg.Args {
		v := a.Int()
		ascii := decodeASCIIArg(v)
		if ascii != "" {
			argParts[i] = fmt.Sprintf("0x%08x(\"%s\")", v, ascii)
		} else {
			argParts[i] = fmt.Sprintf("0x%08x(%d)", v, v)
		}
	}
	// Send NO response. For this opcode the deck sends 0x2005 and
	// expects NOTHING back; it simply proceeds to its next
	// request. Our previous bogus "success" reply was an unexpected extra
	// message that corrupted the deck's load state (0x1c rejections after a
	// couple of loads). Returning nil → the dispatch writes nothing.
	log.Printf("dbserver: ignoring 0x%04x (no response, matching rekordbox) args=[%s]", msg.Type, strings.Join(argParts, " "))
	return nil
}

// decodeASCIIArg returns the ASCII rendering of a 4-byte BE u32 when
// every byte is a printable ASCII char (0x20-0x7e), and "" otherwise.
// Used to surface 4-char tags like "PVB2" or "EXTI" embedded in
// opcode args so they're readable in the log.
func decodeASCIIArg(v uint32) string {
	b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return ""
		}
	}
	return string(b[:])
}

// handleGetHistory handles opcode 0x1016 — the deck navigating into
// the HISTORY root-menu category. We surface our auto-managed History
// folder's children (one playlist per day, e.g. "History · 2026-05-21")
// as the list. Selecting one sends 0x1116 with the playlist ID.
//
// Note: the 0x1016 opcode assignment is a best guess from the 0x10NN
// convention (HISTORY's category ID is 22 = 0x16). If the deck sends
// something different the user will see "unhandled type 0x...." in
// the log and we'll know what to rename this case to.
func (h *Handler) handleSearchSelect(msg *proto.DBMessage) []*proto.DBMessage {
	id := uint32(0)
	if len(msg.Args) >= 3 {
		id = msg.Args[2].Int()
	}
	log.Printf("dbserver: 0x1200 search-select id=%d", id)

	var items []*menuItem
	if h.lib != nil && id != 0 {
		if t := h.lib.Track(id); t != nil {
			items = []*menuItem{h.searchTrackItem(t)}
		}
	}
	h.setPendingAll(msg, items)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(uint32(len(items)))},
	}}
}

// containsFold reports whether s contains the lowercased substring q.
// Caller passes q already lowercased so we don't re-allocate per row;
// for s we lowercase here. Used for case-insensitive substring search
// against title/artist/album fields: a query of "PROB" matches tracks
// with "Probspot" anywhere in the title, not just at the start.
func containsFold(s, q string) bool {
	if q == "" {
		return false
	}
	return strings.Contains(strings.ToLower(s), q)
}

// getArtistsForLabelItems returns [ALL] + artists for a label.
func (h *Handler) getArtistsForLabelItems(labelID uint32) []*menuItem {
	artistSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label != "" && library.HashID("label", t.Label) == labelID && t.Artist != "" {
				artistSet[t.Artist] = true
			}
		}
	}
	var items []*menuItem
	items = append(items, &menuItem{
		ID: 0xffffffff, Label1: "ALL", ItemType: 0x00a0,
	})
	for name := range artistSet {
		items = append(items, &menuItem{
			ID: library.HashID("artist", name), Label1: name, ItemType: 0x07,
		})
	}
	sortItems(items[1:], sortTitle)
	return items
}

// getAlbumsForLabelArtist returns [ALL] + albums for a label + artist.
func (h *Handler) getAlbumsForLabelArtist(labelID, artistID uint32) []*menuItem {
	albumSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label == "" || library.HashID("label", t.Label) != labelID {
				continue
			}
			if artistID != 0xffffffff && library.HashID("artist", t.Artist) != artistID {
				continue
			}
			if t.Album != "" {
				albumSet[t.Album] = true
			}
		}
	}
	var items []*menuItem
	items = append(items, &menuItem{
		ID: 0xffffffff, Label1: "ALL", ItemType: 0x00a0,
	})
	for name := range albumSet {
		items = append(items, &menuItem{
			ID: library.HashID("album", name), Label1: name, ItemType: 0x02,
		})
	}
	sortItems(items[1:], sortTitle)
	return items
}

// getTracksForLabelArtistAlbum returns tracks matching label + artist + album.
func (h *Handler) getTracksForLabelArtistAlbum(labelID, artistID, albumID uint32) []*menuItem {
	var tracks []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label == "" || library.HashID("label", t.Label) != labelID {
				continue
			}
			if artistID != 0xffffffff && library.HashID("artist", t.Artist) != artistID {
				continue
			}
			if albumID != 0xffffffff && library.HashID("album", t.Album) != albumID {
				continue
			}
			tracks = append(tracks, t)
		}
	}
	return h.tracksToStdItems(tracks)
}

func (h *Handler) handleGetArtistsForLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x110a: returns [ALL] + artists for a label.
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	labelID := msg.Args[2].Int()

	// Find all artists that have tracks with this label.
	artistSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label != "" && library.HashID("label", t.Label) == labelID && t.Artist != "" {
				artistSet[t.Artist] = true
			}
		}
	}

	// First item: [ALL] (shows all tracks for this label).
	var items []*menuItem
	items = append(items, &menuItem{
		ID:       0xffffffff,
		Label1:   "ALL",
		ItemType: 0x00a0,
	})

	// Then artist items.
	for name := range artistSet {
		items = append(items, &menuItem{
			ID:       library.HashID("artist", name),
			Label1:   name,
			ItemType: 0x07, // artist
		})
	}
	sortItems(items[1:], sortTitle) // sort artists, keep [ALL] first

	h.setPendingAll(msg, items)
	log.Printf("dbserver: artists for label 0x%08x returning %d items", labelID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetAlbumsForLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x120a: albums for label + artist.
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	labelID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()

	items := h.getAlbumsForLabelArtist(labelID, artistID)
	h.setPendingAll(msg, items)
	log.Printf("dbserver: albums for label 0x%08x artist 0x%08x returning %d items", labelID, artistID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x130a: tracks for label + artist + album.
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	labelID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()
	albumID := uint32(0xffffffff)
	if len(msg.Args) >= 5 {
		albumID = msg.Args[4].Int()
	}

	var tracks []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label == "" || library.HashID("label", t.Label) != labelID {
				continue
			}
			if artistID != 0xffffffff && library.HashID("artist", t.Artist) != artistID {
				continue
			}
			if albumID != 0xffffffff && library.HashID("album", t.Album) != albumID {
				continue
			}
			tracks = append(tracks, t)
		}
	}
	items := h.tracksToStdItems(tracks)
	h.setPendingAll(msg, items)
	log.Printf("dbserver: tracks for label 0x%08x artist 0x%08x returning %d items", labelID, artistID, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetKeyDistances(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1114: returns 3 key distance groups for the selected key.
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	keyID := msg.Args[2].Int()

	// Find the selected key name.
	keyName := h.resolveKeyName(keyID)

	// Build 3 distance groups with track counts.
	// Label1 = key names (displayed by CDJ), Label2 = empty.
	// ID = count (CDJ shows this as the track count).
	groups := camelotDistanceGroups(keyName)
	var items []*menuItem
	for dist, groupKeys := range groups {
		count := h.countTracksForKeys(groupKeys)
		label := strings.Join(groupKeys, ", ")
		items = append(items, &menuItem{
			ID:       uint32(count),
			ParentID: uint32(dist),
			Label1:   label,
			ItemType: 0x0f, // key item
		})
	}
	h.setPendingAll(msg, items)
	log.Printf("dbserver: key distances for %q returning %d groups", keyName, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByKey(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1214: returns tracks matching key + distance group.
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	keyID := msg.Args[2].Int()
	distance := int(msg.Args[3].Int())

	keyName := h.resolveKeyName(keyID)
	groups := camelotDistanceGroups(keyName)
	if distance >= len(groups) {
		distance = 0
	}
	matchKeys := groups[distance]

	// Build key set for matching.
	keySet := make(map[string]bool)
	for _, k := range matchKeys {
		keySet[k] = true
	}

	var tracks []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if keySet[t.Key] {
				tracks = append(tracks, t)
			}
		}
	}
	items := h.tracksToStdItems(tracks)
	h.setPendingAll(msg, items)
	log.Printf("dbserver: tracks for key %q dist=%d returning %d tracks", keyName, distance, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

// resolveKeyName finds the Camelot key name for a key ID.
func (h *Handler) resolveKeyName(keyID uint32) string {
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Key != "" && library.HashID("key", t.Key) == keyID {
				return t.Key
			}
		}
	}
	if h.pdb != nil {
		if name, ok := h.pdb.Keys[keyID]; ok {
			return name
		}
	}
	return ""
}

// countTracksForKeys counts how many tracks match any of the given keys.
func (h *Handler) countTracksForKeys(keys []string) int {
	keySet := make(map[string]bool)
	for _, k := range keys {
		keySet[k] = true
	}
	count := 0
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if keySet[t.Key] {
				count++
			}
		}
	}
	return count
}

// camelotDistanceGroups returns 3 groups of Camelot keys at increasing distances.
// Distance 0: exact key
// Distance 1: +/-1 on Camelot wheel (adjacent keys)
// Distance 2: +/-2 on Camelot wheel
func camelotDistanceGroups(key string) [][]string {
	if key == "" {
		return [][]string{{}, {}, {}}
	}

	// Parse Camelot notation: number (1-12) + letter (A/B).
	num := 0
	mode := ""
	for i, c := range key {
		if c == 'A' || c == 'B' {
			fmt.Sscanf(key[:i], "%d", &num)
			mode = string(c)
			break
		}
	}
	if num == 0 || mode == "" {
		return [][]string{{key}, {key}, {key}}
	}

	wrap := func(n int) int {
		n = ((n - 1) % 12) + 1
		if n <= 0 {
			n += 12
		}
		return n
	}

	exact := []string{key}

	// +/-1: same number other mode, +1 same mode, -1 same mode
	near := []string{key}
	otherMode := "A"
	if mode == "A" {
		otherMode = "B"
	}
	near = append(near, fmt.Sprintf("%d%s", num, otherMode))
	for _, keys := range near {
		_ = keys
	}
	// Actually: Camelot wheel neighbors are: same number other mode, +1 same mode, -1 same mode
	near = []string{
		key,
		fmt.Sprintf("%d%s", num, otherMode),
	}

	// +/-2: add +1, -1, +2, -2 same mode
	further := make([]string, len(near))
	copy(further, near)
	further = append(further,
		fmt.Sprintf("%d%s", wrap(num-1), mode),
		fmt.Sprintf("%d%s", wrap(num+1), mode),
	)

	return [][]string{exact, near, further}
}

func (h *Handler) handleGetByArtist(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1102: returns ALBUMS for the selected artist.
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	artistID := msg.Args[2].Int()

	if h.pdb != nil {
		// Find albums for this artist from PDB tracks.
		albumSet := make(map[uint32]bool)
		var items []*menuItem
		for _, t := range h.pdb.Tracks {
			if t.ArtistID == artistID && t.AlbumID > 0 && !albumSet[t.AlbumID] {
				albumSet[t.AlbumID] = true
				items = append(items, &menuItem{
					ID:       t.AlbumID,
					Label1:   t.Album,
					ItemType: 0x02,
				})
			}
		}
		h.pendingItems = items
		log.Printf("dbserver: get by artist %d returning %d albums (PDB)", artistID, len(items))
		sortItems(items, sortTitle)
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	// Find artist name by hash ID.
	artistName := ""
	for _, name := range h.lib.Artists() {
		if library.HashID("artist", name) == artistID {
			artistName = name
			break
		}
	}
	if artistName == "" {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	tracks := h.lib.TracksByArtist(artistName)
	albumSet := make(map[string]bool)
	var items []*menuItem
	for _, t := range tracks {
		album := t.Album
		if album == "" {
			album = "Unknown Album"
		}
		if !albumSet[album] {
			albumSet[album] = true
			items = append(items, &menuItem{
				ID:       library.HashID("album", album),
				Label1:   album,
				ItemType: 0x02,
			})
		}
	}
	h.pendingItems = items
	log.Printf("dbserver: get by artist %q returning %d albums", artistName, len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetTracksByAlbum(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1202: returns TRACKS within an album (artist→album→track drill-down).
	// Args: [DMST, sort, artistID, albumID]
	if len(msg.Args) < 4 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	artistID := msg.Args[2].Int()
	albumID := msg.Args[3].Int()

	if h.pdb != nil {
		var items []*menuItem
		for _, t := range h.pdb.Tracks {
			match := false
			if albumID > 0 && t.AlbumID == albumID {
				match = true
			}
			if artistID > 0 && t.ArtistID != artistID {
				match = false
			}
			if match {
				items = append(items, h.pdbTrackToStdItem(t))
			}
		}
		sortItems(items, getSortOrder(msg))
		h.pendingItems = items
		log.Printf("dbserver: get tracks by album %d (PDB) returning %d tracks", albumID, len(items))
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	// Fallback to library: find album name by hash ID.
	albumName := ""
	for _, name := range h.lib.Albums() {
		if library.HashID("album", name) == albumID {
			albumName = name
			break
		}
	}
	var matchTracks []*library.Track
	if albumName != "" {
		matchTracks = h.lib.TracksByAlbum(albumName)
	}
	h.pendingItems = h.tracksToStdItems(matchTracks)
	log.Printf("dbserver: get tracks by album (0x1202) returning %d tracks", len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetByAlbum(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1103: returns TRACKS for the selected album.
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	albumID := msg.Args[2].Int()

	if h.pdb != nil {
		var items []*menuItem
		for _, t := range h.pdb.Tracks {
			if t.AlbumID == albumID {
				items = append(items, h.pdbTrackToStdItem(t))
			}
		}
		sortItems(items, getSortOrder(msg))
		h.pendingItems = items
		log.Printf("dbserver: get by album %d (PDB) returning %d tracks", albumID, len(items))
		return []*proto.DBMessage{h.successWithCount(msg)}
	}

	albumName := ""
	for _, name := range h.lib.Albums() {
		if library.HashID("album", name) == albumID {
			albumName = name
			break
		}
	}
	if albumName == "" {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	tracks := h.lib.TracksByAlbum(albumName)
	h.pendingItems = h.tracksToStdItems(tracks)
	log.Printf("dbserver: get by album %q returning %d tracks", albumName, len(h.pendingItems))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

func (h *Handler) handleGetByGenre(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) < 3 {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	genreID := msg.Args[2].Int()
	genreName := ""
	for _, name := range h.lib.Genres() {
		if library.HashID("genre", name) == genreID {
			genreName = name
			break
		}
	}
	if genreName == "" {
		h.pendingItems = nil
		return []*proto.DBMessage{h.successWithCount(msg)}
	}
	tracks := h.lib.TracksByGenre(genreName)
	items := h.tracksToStdItems(tracks)
	h.setPendingAll(msg, items)
	log.Printf("dbserver: get by genre %q returning %d tracks", genreName, len(items))
	return []*proto.DBMessage{h.successWithCount(msg)}
}

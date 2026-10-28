// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
	"github.com/vynulldev/vynull/related"
)

// drilldown.go contains the multi-level drill handlers — anything the
// deck issues *after* a category has been opened. By-artist /
// by-album / by-genre walks, the SEARCH handler (0x1300 query +
// 0x1200 select), the NXS2 menu-load / drill setup, key compatibility
// with camelot distance grouping, label drill (artist → album →
// tracks), colour drill, and the 0x2005/0x2805 placeholder stubs.

func (h *Handler) handleNXS2MenuLoad(msg *proto.DBMessage) []*proto.DBMessage {
	menu := dmstMenu(msg)
	dlog.Debugf("dbserver: NXS2 menu load 0x1010 menu=%d args=%d", menu, len(msg.Args))

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
	dlog.Debugf("dbserver: NXS2 drill-down 0x%04x menu=%d args=%d", msg.Type, menu, len(msg.Args))
	for i, a := range msg.Args {
		dlog.Tracef("dbserver: NXS2 drill arg[%d] = 0x%08x (%d)", i, a.Int(), a.Int())
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

	return h.countReply(msg, items)
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
		// Go string; decode to a normal string. Trailing NULs (the CDJ
		// sends "T\0") get trimmed by DecodeUTF16BE.
		query = proto.DecodeUTF16BE([]byte(msg.Args[3].Str))
	}
	tokens := searchTokens(query)
	dlog.Debugf("dbserver: SEARCH query=%q tokens=%v", query, tokens)

	var artists, albums, tracks []*menuItem
	if len(tokens) > 0 {
		c := h.trackRows()
		texts := c.searchTexts()
		// Artist domain spans every djmdArtist name: the track artist, its
		// remixer, and its original artist. rekordbox lists matching artists
		// and albums ahead of matching content rows.
		seenArtist := make(map[uint32]bool)
		addArtist := func(name, folded string, id uint32) {
			if name == "" || !searchMatch(folded, tokens) || seenArtist[id] {
				return
			}
			seenArtist[id] = true
			artists = append(artists, &menuItem{ID: id, Label1: name, ItemType: 0x07})
		}
		seenAlbum := make(map[uint32]bool)
		var matches []int32
		var labels []string
		for i, t := range c.tracks {
			text, ids := &texts[i], &c.ids[i]
			addArtist(t.Artist, text.artist, ids.artist)
			addArtist(t.Remixer, text.remixer, ids.remixer)
			addArtist(t.OriginalArtist, text.origArtist, ids.origArtist)
			if t.Album != "" && searchMatch(text.album, tokens) && !seenAlbum[ids.album] {
				seenAlbum[ids.album] = true
				albums = append(albums, &menuItem{ID: ids.album, Label1: t.Album, ItemType: 0x02})
			}
			// A content row matches on either Title or file name, and is
			// labelled with whichever matched (title takes precedence).
			if searchMatch(text.title, tokens) {
				matches, labels = append(matches, int32(i)), append(labels, t.Title)
			} else if searchMatch(text.file, tokens) {
				matches, labels = append(matches, int32(i)), append(labels, baseName(t.FilePath))
			}
		}
		// A content row is a standard track row with the Key secondary
		// column showing the key alone, not the "key - bpm" composite.
		tracks = c.copyRows(matches)
		for i, m := range tracks {
			m.ItemType = 0x0f04
			m.ParentID = m.keyID
			m.Label1 = labels[i]
			m.Label2 = c.tracks[matches[i]].Key
		}
		sortByID := func(s []*menuItem) {
			sort.Slice(s, func(i, j int) bool { return s[i].ID < s[j].ID })
		}
		sortByID(artists)
		sortByID(albums)
	}

	items := make([]*menuItem, 0, len(artists)+len(albums)+len(tracks))
	items = append(items, artists...)
	items = append(items, albums...)
	items = append(items, tracks...)

	return h.countReply(msg, items)
}

// searchTokens splits a query the way rekordbox does: on ASCII spaces, with
// ASCII a-z upper-cased and all other code units left as-is (no Unicode case
// folding). Empty tokens are dropped.
func searchTokens(query string) []string {
	var tokens []string
	for _, f := range strings.Fields(query) {
		tokens = append(tokens, asciiUpper(f))
	}
	return tokens
}

// searchMatch reports whether every token occurs as a substring of a value
// folded by asciiUpper. Token order is irrelevant.
func searchMatch(folded string, tokens []string) bool {
	for _, tok := range tokens {
		if !strings.Contains(folded, tok) {
			return false
		}
	}
	return true
}

// asciiUpper upper-cases only ASCII a-z, leaving every other byte untouched.
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 0x20
		}
	}
	return string(b)
}

// baseName returns the file-name portion of a path (after the last '/').
func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// handleGetColors handles opcode 0x100d — the deck navigating into
// the COLOR root-menu category. Returns the 8 standard rekordbox
// track colours (Pink, Red, …, Purple) plus a "no colour" row at
// the top. Each row's ID is the track-colour ID so the deck's
// follow-up "give me tracks of colour N" drill (0x110d) can route
// to handleGetTracksByColor.
func (h *Handler) handleGetColors(msg *proto.DBMessage) []*proto.DBMessage {
	dlog.Debugf("dbserver: COLOR list (0x100d)")
	// rekordbox lists the eight named colours (Pink … Purple), title-cased,
	// with no "no colour" row; each row type is 0x13 + the colour ID.
	items := make([]*menuItem, 0, 8)
	for id := uint8(1); id <= 8; id++ {
		items = append(items, &menuItem{
			ID:       uint32(id),
			Label1:   library.TrackColorName(id),
			ItemType: uint32(0x13 + id),
		})
	}
	return h.countReply(msg, items)
}

// handleGetTracksByColor handles opcode 0x110d — the deck drilling
// into a specific colour's tracks. arg[2] = the colour ID we returned
// in handleGetColors.
func (h *Handler) handleGetTracksByColor(msg *proto.DBMessage) []*proto.DBMessage {
	colorID := msgArg(msg, 2)
	dlog.Debugf("dbserver: COLOR drill (0x110d) color=%d", colorID)

	var matches []*library.Track
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if uint32(t.ColorID) == colorID {
				matches = append(matches, t)
			}
		}
	}
	items := h.tracksToStdItems(matches)
	return h.countReply(msg, items)
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
	dlog.Debugf("dbserver: ignoring 0x%04x (no response, matching rekordbox) args=[%s]", msg.Type, strings.Join(argParts, " "))
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

// handleSearchSelect handles opcode 0x1200, the drill-in the deck
// sends when the user picks a row from the SEARCH-result list. The
// selected item's arg1 (a library track ID, in our case) arrives as
// arg[2] of the request. We resolve it back to the actual track and
// stash a 1-item pending list — the deck's follow-up 0x3000 render
// then displays the track and lets the user press LOAD normally.
//
// Without this handler the request hits the unhandled-opcode fallback
// (returns Success with 0 items), the deck has nothing to render, and
// the LOAD eventually falls back to whatever track was last loaded —
// which looked like "always loads track 1" from the user's POV.
func (h *Handler) handleSearchSelect(msg *proto.DBMessage) []*proto.DBMessage {
	id := msgArg(msg, 2)
	dlog.Debugf("dbserver: 0x1200 search-select id=%d", id)

	var items []*menuItem
	if h.lib != nil && id != 0 {
		if t := h.lib.Track(id); t != nil {
			items = []*menuItem{h.searchTrackItem(t)}
		}
	}
	return h.countReply(msg, items)
}

// searchTrackItem builds a track menu item for a selected SEARCH result
// (0x1200). Forces the ItemType to the "BPM detail" shape (0x0d04, low byte
// 0x04 = loadable track) so the deck always treats the row as a directly
// loadable track on selection, regardless of the configured detail field.
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

// getArtistsForLabelItems returns [ALL] + artists for a label.
func (h *Handler) getArtistsForLabelItems(labelID uint32) []*menuItem {
	c := h.trackRows()
	return c.namesWithAll(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Label != "" && ids.label == labelID
	}), artistName, 0x07)
}

// getAlbumsForLabelArtist returns [ALL] + albums for a label + artist.
func (h *Handler) getAlbumsForLabelArtist(labelID, artistID uint32) []*menuItem {
	c := h.trackRows()
	return c.namesWithAll(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Label != "" && ids.label == labelID && matchesID(artistID, ids.artist)
	}), albumName, 0x02)
}

// getTracksForLabelArtistAlbum returns tracks matching label + artist + album.
func (h *Handler) getTracksForLabelArtistAlbum(labelID, artistID, albumID uint32) []*menuItem {
	c := h.trackRows()
	items := c.copyRows(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Label != "" && ids.label == labelID && matchesID(artistID, ids.artist) && matchesID(albumID, ids.album)
	}))
	h.orderByTitle(items)
	markContextType(items, h.ctxTrackType)
	return items
}

func (h *Handler) handleGetArtistsForLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x110a: returns [ALL] + artists for a label.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	labelID := msg.Args[2].Int()
	items := h.getArtistsForLabelItems(labelID)
	dlog.Debugf("dbserver: artists for label 0x%08x returning %d items", labelID, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetAlbumsForLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x120a: albums for label + artist.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	labelID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()

	items := h.getAlbumsForLabelArtist(labelID, artistID)
	dlog.Debugf("dbserver: albums for label 0x%08x artist 0x%08x returning %d items", labelID, artistID, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByLabel(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x130a: tracks for label + artist + album.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	labelID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()
	albumID := uint32(0xffffffff)
	if len(msg.Args) >= 5 {
		albumID = msg.Args[4].Int()
	}

	items := h.getTracksForLabelArtistAlbum(labelID, artistID, albumID)
	dlog.Debugf("dbserver: tracks for label 0x%08x artist 0x%08x returning %d items", labelID, artistID, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetKeyDistances(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1114: returns 3 key distance groups for the selected key.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	keyIndex := msg.Args[2].Int()

	// The KEY menu is keyed by the 1-24 harmonic index; derive the Camelot
	// position to compute the compatibility rings, then label them in the
	// configured (Classic) notation. arg 1 is the selected index, arg 0 the
	// distance (0 exact, 1 +relative, 2 +wheel neighbours).
	key, _ := analysis.KeyAtIndex(int(keyIndex))
	var items []*menuItem
	for dist, groupKeys := range keyDistanceGroups(key) {
		names := make([]string, len(groupKeys))
		for i, k := range groupKeys {
			names[i] = classicKeyNames[k.Index()-1]
		}
		items = append(items, &menuItem{
			ID:       keyIndex,
			ParentID: uint32(dist),
			Label1:   strings.Join(names, ", "),
			ItemType: 0x0f,
		})
	}
	dlog.Debugf("dbserver: key distances for index %d returning %d groups", keyIndex, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByKey(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1214: returns tracks matching key index + distance group.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	keyIndex := msg.Args[2].Int()
	distance := int(msg.Args[3].Int())

	// rekordbox lists the tracks whose key is in the selected key's distance
	// group, the keys its 0x1114 row names, title-sorted, as a scalar leaf
	// (no context type in arg 7). On a CDJ-2000NXS2, 1B listed 17 tracks, 1B
	// and 1A 101, and with 12B and 2B as well 143. When no track's key is in
	// the group it lists the whole collection: 1A on a library of Am and C
	// tracks listed them all (link-spec full suite, rekordbox 7.2.19).
	c := h.trackRows()
	idx := c.libraryOrder()
	if group := keyGroupIndexes(keyIndex, distance); len(group) > 0 {
		var inGroup []int32
		for i := range c.rows {
			if group[c.rows[i].newKeyID] {
				inGroup = append(inGroup, int32(i))
			}
		}
		if len(inGroup) > 0 {
			idx = inGroup
		}
	}
	items := c.copyRows(idx)
	h.orderByTitle(items)
	dlog.Debugf("dbserver: tracks for key index %d dist=%d returning %d tracks", keyIndex, distance, len(items))
	return h.countReply(msg, items)
}

// keyGroupIndexes returns the 1-24 key indexes in a key's distance group
// (keyDistanceGroups; a distance past 2 is the widest group), or nil for an
// index that names no key.
func keyGroupIndexes(keyIndex uint32, distance int) map[uint32]bool {
	k, ok := analysis.KeyAtIndex(int(keyIndex))
	if !ok {
		return nil
	}
	groups := keyDistanceGroups(k)
	group := groups[min(max(distance, 0), len(groups)-1)]
	out := make(map[uint32]bool, len(group))
	for _, g := range group {
		out[uint32(g.Index())] = true
	}
	return out
}

// keyDistanceGroups returns a key's three distance groups, as the KEY menu
// shows them: distance 0 is the key, 1 adds its relative key, and 2 adds its
// neighbours one step round the wheel. No key has three empty groups.
func keyDistanceGroups(k analysis.Key) [][]analysis.Key {
	if k == (analysis.Key{}) {
		return [][]analysis.Key{{}, {}, {}}
	}
	return [][]analysis.Key{
		{k},
		{k, k.Relative()},
		{k, k.Relative(), k.Step(-1), k.Step(1)},
	}
}

func (h *Handler) handleGetByArtist(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1102: returns ALBUMS for the selected artist.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	artistID := msg.Args[2].Int()

	artistName := h.trackRows().categoryName("artist", artistID)
	if artistName == "" {
		return h.countReply(msg, nil)
	}
	tracks := h.lib.TracksByArtist(artistName)
	albumSet := make(map[string]bool)
	items := []*menuItem{{ID: 0xffffffff, Label1: "ALL", ItemType: 0x00a0}}
	for _, t := range tracks {
		album := t.Album
		if album == "" {
			album = "Unknown Album"
		}
		if !albumSet[album] {
			albumSet[album] = true
			items = append(items, &menuItem{
				ID:       h.catID("album", album),
				Label1:   album,
				ItemType: 0x02,
			})
		}
	}
	orderByName(items[1:])
	dlog.Debugf("dbserver: get by artist %q returning %d albums", artistName, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByAlbum(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1202: returns TRACKS within an album (artist→album→track drill-down).
	// Args: [DMST, sort, artistID, albumID]
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	artistID := msg.Args[2].Int()
	albumID := msg.Args[3].Int()

	// Filter by artist and album, honouring the 0xffffffff "ALL"
	// sentinel at either level (e.g. an artist's tracks across all albums).
	c := h.trackRows()
	idx := c.filter(func(_ *library.Track, ids *catIDs) bool {
		return matchesID(artistID, ids.artist) && matchesID(albumID, ids.album)
	})
	items := c.copyRows(idx)
	// A specific album is album scope: track number in arg 9, and tracks in
	// track-number order, as rekordbox lists an album. Spanning all albums is
	// artist scope (arg 9 stays 0, title order).
	if albumID != 0xffffffff {
		for i, p := range idx {
			items[i].trackPos = uint32(c.tracks[p].TrackNum)
		}
		h.orderByTrackNumber(items)
	} else {
		h.orderByTitle(items)
	}
	markContextType(items, h.ctxTrackType)
	dlog.Debugf("dbserver: get tracks by album (0x1202) returning %d tracks", len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetByAlbum(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1103: returns TRACKS for the selected album.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	albumID := msg.Args[2].Int()

	albumName := h.trackRows().categoryName("album", albumID)
	if albumName == "" {
		return h.countReply(msg, nil)
	}
	tracks := h.lib.TracksByAlbum(albumName)
	items := h.tracksToStdItems(tracks)
	// Album scope: the track number in arg 9, and tracks in track-number
	// order, as rekordbox lists an album (checked against a real rekordbox's
	// ALBUM lists).
	for i, t := range tracks {
		items[i].trackPos = uint32(t.TrackNum)
	}
	h.orderByTrackNumber(items)
	markContextType(items, h.ctxTrackType)
	dlog.Debugf("dbserver: get by album %q returning %d tracks", albumName, len(items))
	return h.countReply(msg, items)
}

// The GENRE category is a 3-level drill like LABEL: genre -> artist -> album
// -> track, with an [ALL] row at each level. rekordbox serves these as
// 0x1101 (artists for genre), 0x1201 (albums for genre + artist), and 0x1301
// (tracks for genre + artist + album). artistID/albumID of 0xffffffff mean
// "ALL" at that level.

func (h *Handler) getArtistsForGenreItems(genreID uint32) []*menuItem {
	c := h.trackRows()
	return c.namesWithAll(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Genre != "" && ids.genre == genreID
	}), artistName, 0x07)
}

func (h *Handler) getAlbumsForGenreArtist(genreID, artistID uint32) []*menuItem {
	c := h.trackRows()
	return c.namesWithAll(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Genre != "" && ids.genre == genreID && matchesID(artistID, ids.artist)
	}), albumName, 0x02)
}

func (h *Handler) getTracksForGenreArtistAlbum(genreID, artistID, albumID uint32) []*menuItem {
	c := h.trackRows()
	items := c.copyRows(c.filter(func(t *library.Track, ids *catIDs) bool {
		return t.Genre != "" && ids.genre == genreID && matchesID(artistID, ids.artist) && matchesID(albumID, ids.album)
	}))
	h.orderByTitle(items)
	markContextType(items, h.ctxTrackType)
	return items
}

func (h *Handler) handleGetByGenre(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1101: [ALL] + artists for a genre.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	genreID := msg.Args[2].Int()
	items := h.getArtistsForGenreItems(genreID)
	dlog.Debugf("dbserver: artists for genre 0x%08x returning %d items", genreID, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetAlbumsForGenre(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1201: [ALL] + albums for a genre + artist.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	genreID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()
	items := h.getAlbumsForGenreArtist(genreID, artistID)
	dlog.Debugf("dbserver: albums for genre 0x%08x artist 0x%08x returning %d items", genreID, artistID, len(items))
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByGenre(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1301: tracks for a genre + artist + album.
	if len(msg.Args) < 5 {
		return h.countReply(msg, nil)
	}
	genreID := msg.Args[2].Int()
	artistID := msg.Args[3].Int()
	albumID := msg.Args[4].Int()
	items := h.getTracksForGenreArtistAlbum(genreID, artistID, albumID)
	dlog.Debugf("dbserver: tracks for genre 0x%08x artist 0x%08x album 0x%08x returning %d items", genreID, artistID, albumID, len(items))
	return h.countReply(msg, items)
}

// The REMIXER and ORIGINAL ARTIST categories drill like an artist does:
// entry -> album -> track, with an [ALL] row at the album level. Both the
// remixer (djmdContent.RemixerID) and original artist (OrgArtistID) are
// djmdArtist rows, matched here by the catID of the track's remixer/original
// artist name. rekordbox serves remixer as 0x1702 (albums) / 0x1802 (tracks)
// and original artist as 0x1402 / 0x1502.

// albumsForArtistLike returns [ALL] + albums whose tracks have the given
// remixer/original-artist (selected by field) matching id.
func (h *Handler) albumsForArtistLike(id uint32, field func(*library.Track, *catIDs) (string, uint32)) []*menuItem {
	c := h.trackRows()
	return c.namesWithAll(c.filter(func(t *library.Track, ids *catIDs) bool {
		name, nameID := field(t, ids)
		return name != "" && nameID == id
	}), albumName, 0x02)
}

// tracksForArtistLikeAlbum returns tracks with the given remixer/original
// artist, optionally narrowed to one album (0xffffffff = all albums). A
// specific album is album scope (track number in arg 9, track-number order);
// all albums is not (title order).
func (h *Handler) tracksForArtistLikeAlbum(id, albumID uint32, field func(*library.Track, *catIDs) (string, uint32)) []*menuItem {
	c := h.trackRows()
	idx := c.filter(func(t *library.Track, ids *catIDs) bool {
		name, nameID := field(t, ids)
		return name != "" && nameID == id && matchesID(albumID, ids.album)
	})
	items := c.copyRows(idx)
	if albumID != 0xffffffff {
		for i, p := range idx {
			items[i].trackPos = uint32(c.tracks[p].TrackNum)
		}
		h.orderByTrackNumber(items)
	} else {
		h.orderByTitle(items)
	}
	markContextType(items, h.ctxTrackType)
	return items
}

// The name fields the drill-downs list, each with its category ID.
func artistName(t *library.Track, ids *catIDs) (string, uint32) { return t.Artist, ids.artist }
func albumName(t *library.Track, ids *catIDs) (string, uint32)  { return t.Album, ids.album }
func remixerField(t *library.Track, ids *catIDs) (string, uint32) {
	return t.Remixer, ids.remixer
}
func origArtistField(t *library.Track, ids *catIDs) (string, uint32) {
	return t.OriginalArtist, ids.origArtist
}

// matchesID reports whether a category ID is the one a drill-down asks for,
// where 0xffffffff is the [ALL] row, which matches any.
func matchesID(want, id uint32) bool {
	return want == 0xffffffff || id == want
}

func (h *Handler) handleGetAlbumsForRemixer(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1702: [ALL] + albums for a remixer.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	items := h.albumsForArtistLike(msg.Args[2].Int(), remixerField)
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByRemixer(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1802: tracks for a remixer + album.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	items := h.tracksForArtistLikeAlbum(msg.Args[2].Int(), msg.Args[3].Int(), remixerField)
	return h.countReply(msg, items)
}

func (h *Handler) handleGetAlbumsForOriginalArtist(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1402: [ALL] + albums for an original artist.
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	items := h.albumsForArtistLike(msg.Args[2].Int(), origArtistField)
	return h.countReply(msg, items)
}

func (h *Handler) handleGetTracksByOriginalArtist(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x1502: tracks for an original artist + album.
	if len(msg.Args) < 4 {
		return h.countReply(msg, nil)
	}
	items := h.tracksForArtistLikeAlbum(msg.Args[2].Int(), msg.Args[3].Int(), origArtistField)
	return h.countReply(msg, items)
}

// matchingFallbackLimit caps the computed MATCHING list for tracks without
// rekordbox recommendations: the best-scoring suggestions, not the library.
const matchingFallbackLimit = 50

// handleGetMatching serves the MATCHING browse (0x1017). A track with
// rekordbox recommendations (djmdRecommendLike, both directions) gets exactly
// those, as rekordbox serves them (link-spec conformance case
// matching-forward-reverse, rekordbox 7.2.19). A track with none, where
// rekordbox would show an empty list, gets Vynull's computed suggestions
// (package related) instead, up to matchingFallbackLimit, best match first.
// A scalar leaf (no context type in arg 7); album-style track number in arg 9;
// rekordbox's pairs are title-sorted.
func (h *Handler) handleGetMatching(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) < 3 {
		return h.countReply(msg, nil)
	}
	srcID := msg.Args[2].Int()
	var tracks []*library.Track
	computed := false
	if h.lib != nil {
		for _, id := range h.lib.Related(srcID) {
			if t := h.lib.Track(id); t != nil {
				tracks = append(tracks, t)
			}
		}
		if len(tracks) == 0 {
			for _, m := range related.Find(h.lib, srcID, related.Options{Limit: matchingFallbackLimit}) {
				tracks = append(tracks, m.Track)
			}
			computed = true
		}
	}
	items := h.tracksToStdItems(tracks)
	for i, t := range tracks {
		items[i].trackPos = uint32(t.TrackNum)
	}
	if !computed {
		h.orderByTitle(items) // rekordbox's pairs, title-sorted as rekordbox serves them
	} // computed suggestions stay best-first
	dlog.Debugf("dbserver: matching for track %d returning %d tracks", srcID, len(items))
	return h.countReply(msg, items)
}

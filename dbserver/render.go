// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"github.com/vynulldev/vynull/internal/dlog"
	"sort"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// render.go contains the 0x3000 Render dispatcher and everything that
// supports it: the pending-state maps (per-txid and per-menu) every
// category handler stashes its results into, the standard success
// acks, and the populate* preview seeders for when the deck asks the
// root menu for a category preview before tapping in.

func (h *Handler) handleRenderMenu(msg *proto.DBMessage) []*proto.DBMessage {
	argVals := make([]string, len(msg.Args))
	for i, a := range msg.Args {
		argVals[i] = fmt.Sprintf("0x%08x", a.Int())
	}
	// Look up pending items: try txid first, then menu key, then on-demand population.
	menu := dmstMenu(msg)
	// Disambiguate menu byte 1 renders. The deck uses menu=1 for two
	// different things and there's no clean opcode/menu distinction:
	//   - "render the root list" (deck scrolling the root menu) →
	//     args[4] (expected total) equals len(rootMenuItems).
	//   - "render the drill-in content for the category we just
	//     queried" (deck displaying a category list after the user
	//     tapped into it from the root menu) → args[4] is the
	//     category's actual count and args[1] is a paging offset
	//     into THAT list (not the root menu's offset).
	// Use args[4] as the discriminator: rootMenuItems' size → root
	// list; otherwise serve lastCategoryItems (the response from the
	// most recent category-list query, e.g. handleGetColors). Falls
	// through to the normal pending lookup when neither matches.
	var pending []*menuItem
	// Menu bytes 1 and 7 both indicate a root-menu render. The deck
	// uses scope=7 for the initial "give me the full root menu" call
	// right after connect, and scope=1 once the user starts paging
	// through it. Treating only menu=1 as root-menu leaves the initial
	// render empty (header+footer with zero items), forcing the deck
	// to re-fetch with scope=1 — and breaks any deck UI that gates on
	// the first render returning the whole list.
	if (menu == 1 || menu == 7) && len(msg.Args) >= 6 {
		expected := int(msg.Args[4].Int())
		root := h.rootMenu()
		// Prefer lastCategoryItems whenever its size matches the deck's
		// expected count — it's strictly more specific than rootMenu
		// (every category query touches it, including 0x2002 metadata).
		// Without this priority, a category that happens to have the
		// same item count as the root menu (e.g. 0x2002 metadata also
		// returns 16 items) would render rootMenu rows in place of the
		// actual category content. Fall back to rootMenu only when no
		// recent category result matches.
		switch {
		case len(h.lastCategoryItems) > 0 && expected == len(h.lastCategoryItems):
			pending = h.lastCategoryItems
		case expected == len(root) && len(root) > 0:
			pending = root
		}
	}
	if len(pending) == 0 {
		pending = h.getPendingTxID(msg.TxID)
	}
	if len(pending) == 0 {
		pending = h.getPending(menu)
	}
	// Last resort: use pendingItems only for sub-menu drill-downs (menu > 12).
	if len(pending) == 0 && menu > 12 && len(h.pendingItems) > 0 {
		pending = h.pendingItems
	}
	dlog.Debugf("dbserver: render menu=%d args=%v pendingItems=%d", menu, argVals, len(pending))

	offset := 0
	limit := len(pending)
	if len(msg.Args) >= 6 {
		// Render menu format: [spec, offset, count, ???, total, ???]
		// args[1] = offset (start position), args[2] = count (items requested)
		offset = int(msg.Args[1].Int())
		limit = int(msg.Args[2].Int())
	} else if len(msg.Args) >= 4 {
		offset = int(msg.Args[1].Int())
		limit = int(msg.Args[2].Int())
	}

	items := pending
	if offset >= len(items) {
		items = nil
	} else {
		end := offset + limit
		if end > len(items) {
			end = len(items)
		}
		items = items[offset:end]
	}

	var responses []*proto.DBMessage

	// Menu header. rekordbox sends arg[0]=1 (constant), not item count.
	responses = append(responses, &proto.DBMessage{
		TxID: msg.TxID,
		Type: proto.DBMsgMenuHeader,
		Args: []proto.DBArg{
			proto.ArgI32(1),
			proto.ArgI32(0),
		},
	})

	// Menu items: 12 args.
	// Category items (type >= 0x80) use fffa/fffb wrapped strings.
	// Track/data items use plain strings.
	for _, item := range items {
		label1 := item.Label1
		label2 := item.Label2
		isCategory := item.ItemType >= 0x80 && item.ItemType&0xff != 0x04

		var strFunc func(string) proto.DBArg
		var labelLen func(string) uint32
		if isCategory {
			strFunc = proto.ArgStrW
			labelLen = func(s string) uint32 {
				if s == "" {
					return 2
				}
				return uint32((len([]rune(s)) + 3) * 2) // fffa + text + fffb + NUL
			}
		} else {
			strFunc = proto.ArgStr
			labelLen = func(s string) uint32 {
				return uint32((len([]rune(s)) + 1) * 2) // text + NUL
			}
		}

		// Title items (0x0004) carry flag args, but with two distinct shapes:
		//
		//   - Browse/metadata title (0x2002): [0x01000000, artID, 2, marker, 0]
		//     where marker is 0x00000100 (compressed) or 0x00000101 (lossless).
		//   - Track-info title (0x2102, TrackInfo): the decoder ID lives in
		//     arg[1] and EVERY flag arg is zero. The M4A track-info
		//     title row is [parent=0, id=4, …, itemType=4, 0,0,0,0,0]. We were
		//     sending the metadata flags here too (esp. flags10=0x00000100),
		//     which — together with the wrong decoder ID — kept AAC from
		//     loading. The field comment ("no special flags") was only being
		//     half-honoured.
		flags7 := uint32(0)
		flags8 := item.ArtID
		flags9 := uint32(0)
		flags10 := uint32(0)
		// rekordbox sends flags11 = 0 for every menu item, not the
		// track colour as we previously assumed — colour is encoded in
		// the ItemType high byte (see applyTrackDetail "color" case).
		flags11 := uint32(0)
		if item.ItemType&0xff == 0x04 {
			if item.TrackInfo {
				flags8 = 0 // track-info title: decoder ID only, no flags
			} else {
				flags7 = 0x01000000
				flags8 = item.ArtID // artwork ID for CDJ to request via 0x2003 (0 = no art)
				flags9 = 2
				switch item.FileType {
				case "flac", "wav", "aiff":
					flags10 = 0x00000101
				default:
					flags10 = 0x00000100
				}
			}
		}

		responses = append(responses, &proto.DBMessage{
			TxID: msg.TxID,
			Type: proto.DBMsgMenuItem,
			Args: []proto.DBArg{
				proto.ArgI32(item.ParentID),         // 0: parent ID
				proto.ArgI32(item.ID),               // 1: main ID
				proto.ArgI32(labelLen(label1)),      // 2: label 1 byte length
				strFunc(label1),                     // 3: label 1 text
				proto.ArgI32(labelLen(label2)),      // 4: label 2 byte length
				strFunc(label2),                     // 5: label 2 text
				proto.ArgI32(uint32(item.ItemType)), // 6: item type
				proto.ArgI32(flags7),                // 7: flags
				proto.ArgI32(flags8),                // 8: artwork ID
				proto.ArgI32(flags9),                // 9: unknown
				proto.ArgI32(flags10),               // 10: unknown
				proto.ArgI32(flags11),               // 11: unknown
			},
		})
	}

	// Menu footer.
	responses = append(responses, &proto.DBMessage{
		TxID: msg.TxID,
		Type: proto.DBMsgMenuFooter,
		Args: []proto.DBArg{},
	})

	return responses
}

// dmstMenu extracts the menu byte from a DMST arg.
func dmstMenu(msg *proto.DBMessage) uint8 {
	if len(msg.Args) > 0 {
		return uint8((msg.Args[0].Int() >> 16) & 0xff)
	}
	return 0
}

// setPendingAll stores pending items by both menu key and transaction ID.
// The txid lookup is primary (immune to root menu polling overwriting).
func (h *Handler) setPendingAll(msg *proto.DBMessage, items []*menuItem) {
	menu := dmstMenu(msg)
	h.setPending(menu, items)
	h.setPendingTxID(msg.TxID, items)
	h.pendingItems = items
	// Also stash as the "most recent category-query result" so the
	// menu=1 render dispatch in handleRenderMenu can prefer this over
	// rootMenu when both have the same item count (e.g. 0x2002
	// metadata returns 16 items and so does the root menu — without
	// this update the metadata render would serve root-menu rows).
	h.lastCategoryItems = items
}

func (h *Handler) setPending(menu uint8, items []*menuItem) {
	if h.pendingByMenu == nil {
		h.pendingByMenu = make(map[uint8][]*menuItem)
	}
	h.pendingByMenu[menu] = items
}

func (h *Handler) setPendingTxID(txid uint32, items []*menuItem) {
	if h.pendingByTxID == nil {
		h.pendingByTxID = make(map[uint32][]*menuItem)
	}
	h.pendingByTxID[txid] = items
}

func (h *Handler) getPending(menu uint8) []*menuItem {
	if h.pendingByMenu == nil {
		return nil
	}
	return h.pendingByMenu[menu]
}

func (h *Handler) getPendingTxID(txid uint32) []*menuItem {
	if h.pendingByTxID == nil {
		return nil
	}
	items := h.pendingByTxID[txid]
	if items != nil {
		delete(h.pendingByTxID, txid) // consume once rendered
	}
	return items
}

func (h *Handler) success(msg *proto.DBMessage) *proto.DBMessage {
	return &proto.DBMessage{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(1)},
	}
}

func (h *Handler) successWithCount(msg *proto.DBMessage) *proto.DBMessage {
	// Store pending items by both menu key and txid for later render lookup.
	h.setPendingAll(msg, h.pendingItems)
	return &proto.DBMessage{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(uint32(len(h.pendingItems))),
		},
	}
}

func (h *Handler) populateGenres(menu uint8) {
	genres := h.lib.Genres()
	items := make([]*menuItem, len(genres))
	for i, name := range genres {
		items[i] = &menuItem{ID: library.HashID("genre", name), Label1: name, ItemType: 0x06}
	}
	h.setPending(menu, items)
}

func (h *Handler) populateBPM(menu uint8) {
	bpmSet := make(map[int]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.BPM > 0 {
				bpmSet[int(t.BPM+0.5)] = true
			}
		}
	}
	var items []*menuItem
	for bpm := range bpmSet {
		items = append(items, &menuItem{ID: uint32(bpm * 100), Label1: fmt.Sprintf("%d", bpm), ItemType: 0x0d})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	h.setPending(menu, items)
}

func (h *Handler) populateYears(menu uint8) {
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
		items = append(items, &menuItem{ID: uint32(decade), Label1: fmt.Sprintf("%ds", decade), ItemType: 0x11})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	h.setPending(menu, items)
}

func (h *Handler) populateLabels(menu uint8) {
	labelSet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Label != "" {
				labelSet[t.Label] = true
			}
		}
	}
	var items []*menuItem
	for name := range labelSet {
		items = append(items, &menuItem{ID: library.HashID("label", name), Label1: name, ItemType: 0x0e})
	}
	sortItems(items, sortTitle)
	h.setPending(menu, items)
}

func (h *Handler) populateKeys(menu uint8) {
	keySet := make(map[string]bool)
	if h.lib != nil {
		for _, t := range h.lib.Tracks() {
			if t.Key != "" {
				keySet[t.Key] = true
			}
		}
	}
	var items []*menuItem
	for name := range keySet {
		items = append(items, &menuItem{ID: library.HashID("key", name), Label1: name, ItemType: 0x0f})
	}
	sortItems(items, sortTitle)
	h.setPending(menu, items)
}

func (h *Handler) populateArtists(menu uint8) {
	artists := h.lib.Artists()
	items := make([]*menuItem, len(artists))
	for i, name := range artists {
		items[i] = &menuItem{ID: uint32(i + 1), Label1: name, ItemType: 0x07}
	}
	h.setPending(menu, items)
}

func (h *Handler) populateAlbums(menu uint8) {
	albums := h.lib.Albums()
	items := make([]*menuItem, len(albums))
	for i, name := range albums {
		items[i] = &menuItem{ID: uint32(i + 1), Label1: name, ItemType: 0x02}
	}
	h.setPending(menu, items)
}

func (h *Handler) populateTracks(menu uint8) {
	tracks := h.lib.Tracks()
	items := h.tracksToStdItems(tracks)
	h.setPending(menu, items)
}

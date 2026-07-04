// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

// Handler processes individual dbserver messages and returns responses.
type Handler struct {
	lib          *library.Library
	pdb          *pdb.Database
	deviceNumber uint8
	exportRoot   string
	analysis     *analysis.Store
	folders      *pdb.FolderLookup
	playlists    PlaylistSource
	menu         MenuSource
	cues         *CueStore
	settings     *device.CDJSettings

	// Pending items keyed by transaction ID.
	// The CDJ sends a query (which sets pending items), then a render
	// request with the same txid to fetch the items.
	pendingByTxID map[uint32][]*menuItem
	pendingByMenu map[uint8][]*menuItem // legacy fallback
	pendingItems  []*menuItem           // legacy fallback

	// lastCategoryItems is the items list returned by the most recent
	// category-list query (0x100d COLOR list, etc.). The deck uses
	// menu=1 renders for BOTH the root menu and category drill-ins
	// post-tap; args[4] (expected count) is the only discriminator,
	// and when it doesn't match rootMenuItems' size we serve this
	// instead so the deck's "drilled into colour list" view shows
	// the right rows.
	lastCategoryItems []*menuItem

	// Context from the last 0x2705 cue write request.
	lastCueTrackID uint32
	lastCueTxID    uint32
}

// lazyAnalyze returns analysis data for a track, running on-demand analysis
// if the data hasn't been computed yet. Never blocks — returns nil if
// analysis is still in progress.
func (h *Handler) lazyAnalyze(trackID uint32) *analysis.Result {
	if h.analysis == nil {
		return nil
	}

	// Fast path: already in memory.
	if r := h.analysis.Get(trackID); r != nil {
		return analysis.ApplyOverrides(r)
	}

	// Find the track's file path.
	filePath := h.resolveTrackPath(trackID)
	if filePath == "" {
		return nil
	}

	// Register path and check disk cache.
	h.analysis.SetPath(trackID, filePath)
	if r := h.analysis.Get(trackID); r != nil {
		return analysis.ApplyOverrides(r)
	}

	// Start background analysis (deduplicated globally via the Store).
	h.analysis.AnalyzeInBackground(trackID, filePath, func(r *analysis.Result) {
		h.storeAnalysisResult(trackID, r)
	})

	return nil
}

// resolveFileType returns the file type (e.g. "mp3", "flac") for a track.
func (h *Handler) resolveFileType(trackID uint32) string {
	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil && t.FileType != "" {
			return t.FileType
		}
	}
	// Derive from file extension.
	path := h.resolveTrackPath(trackID)
	if path != "" {
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".mp3":
			return "mp3"
		case ".m4a":
			return "m4a"
		case ".flac":
			return "flac"
		case ".wav":
			return "wav"
		case ".aiff", ".aif":
			return "aiff"
		}
	}
	return ""
}

func (h *Handler) resolveTrackPath(trackID uint32) string {
	if h.pdb != nil {
		if t := h.pdb.TrackByID(trackID); t != nil {
			filePath := t.FilePath
			if h.exportRoot != "" && !strings.HasPrefix(filePath, "/") {
				return h.exportRoot + "/" + filePath
			}
			return filePath
		}
	}
	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			return t.FilePath
		}
	}
	return ""
}

func (h *Handler) storeAnalysisResult(trackID uint32, r *analysis.Result) {
	// Update track metadata from analysis — PDB tracks
	if h.pdb != nil {
		if t := h.pdb.TrackByID(trackID); t != nil {
			if t.Tempo == 0 && r.BPM > 0 {
				t.Tempo = uint32(r.BPM * 100)
			}
			if t.Duration == 0 && r.Duration > 0 {
				t.Duration = r.Duration
			}
			if t.Key == "" && r.KeyCamelot != "" {
				t.Key = r.KeyCamelot
			}
		}
	}

	// Update track metadata from analysis — library tracks
	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			if t.BPM == 0 && r.BPM > 0 {
				t.BPM = r.BPM
			}
			if t.Duration == 0 && r.Duration > 0 {
				t.Duration = library.DurationSec(time.Duration(r.Duration) * time.Second)
			}
			if t.Key == "" && r.KeyCamelot != "" {
				t.Key = r.KeyCamelot
			}
		}
	}

	// Cache result and artwork
	h.analysis.Set(trackID, r)
	if r.Artwork != nil {
		h.lib.Artwork.AddWithID(trackID, "image/jpeg", r.Artwork)
	}

	log.Printf("lazy-analysis: track %d done (BPM=%.1f key=%s dur=%ds)",
		trackID, r.BPM, r.KeyCamelot, r.Duration)
}

// Handle dispatches a message to the appropriate handler and returns
// zero or more response messages.
func (h *Handler) Handle(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver msg type=0x%04x txid=%08x args=%d", msg.Type, msg.TxID, len(msg.Args))

	switch msg.Type {
	case proto.DBMsgSetup:
		return h.handleSetup(msg)
	case 0x3007: // mount/media info
		return h.handleMediaInfo(msg)
	case proto.DBMsgRootMenu: // 0x1000
		return h.handleRootMenu(msg)
	case 0x3e03: // NXS2 extension
		return h.handleNXS2Extension(msg)
	case proto.DBMsgRenderMenu: // 0x3000
		return h.handleRenderMenu(msg)
	case 0x1010: // overloaded: TIME list (2 args) OR NXS2 menu load
		if len(msg.Args) <= 2 {
			return h.handleGetTime(msg)
		}
		return h.handleNXS2MenuLoad(msg)
	case 0x1011: // overloaded: BITRATE list (2 args) OR NXS2 drill level 1 (3+ args)
		if len(msg.Args) <= 2 {
			return h.handleGetBitrate(msg)
		}
		return h.handleNXS2DrillDown(msg)
	case 0x1110: // tracks for a TIME bucket (drill from 0x1010 list)
		return h.handleGetTracksByTime(msg)
	case 0x1111: // tracks for a BITRATE bucket (drill from 0x1011 list)
		return h.handleGetTracksByBitrate(msg)
	case 0x1012: // overloaded: NXS2 drill level 2 OR SEARCH category setup
		return h.handleNXS2DrillOrSearchSetup(msg)
	case 0x1013: // overloaded: FILENAME list (2 args) OR NXS2 drill level 3 (5 args all I32) OR SEARCH query (5 args, arg[3]=string)
		if len(msg.Args) <= 2 {
			return h.handleGetFilename(msg)
		}
		return h.handleNXS2DrillDown(msg)
	case 0x2001: // HOT CUE BANK list
		return h.handleGetHotCueBank(msg)
	case 0x1300: // SEARCH category: per-keystroke search query
		return h.handleSearch(msg)
	case 0x1200: // SEARCH-result drill-in: resolve the selected item's ID
		return h.handleSearchSelect(msg)
	case 0x1602: // session preflight (rekordbox-style ACK; semantics TBD)
		// Respond with [0x1602, 0]. Leaving it
		// unhandled appears to make the deck stall before entering
		// some categories (SEARCH keyboard not opening in particular).
		log.Printf("dbserver: 0x1602 preflight ack")
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	case 0x3001: // metadata notification (CDJ sends ~1min after loading); no response expected
		log.Printf("dbserver: 0x3001 notification (no response)")
		return nil
	case proto.DBMsgGetTracks:
		return h.handleGetTracks(msg)
	case proto.DBMsgGetArtists:
		return h.handleGetArtists(msg)
	case proto.DBMsgGetAlbums:
		return h.handleGetAlbums(msg)
	case 0x1001: // genre list
		return h.handleGetGenres(msg)
	case proto.DBMsgGetBPM: // 0x1006
		return h.handleGetBPM(msg)
	case proto.DBMsgGetByArtist: // 0x1102
		return h.handleGetByArtist(msg)
	case proto.DBMsgGetByAlbum: // 0x1103
		return h.handleGetByAlbum(msg)
	case 0x1202: // get tracks by album (artist→album→track drill-down)
		return h.handleGetTracksByAlbum(msg)
	case 0x1101: // artists for genre
		return h.handleGetByGenre(msg)
	case proto.DBMsgGetByBPM: // 0x1106 — BPM percentage ranges
		return h.handleGetBPMRanges(msg)
	case 0x1014: // key list
		return h.handleGetKeys(msg)
	case 0x1016: // HISTORY list (sessions = daily playlists in "History" folder)
		return h.handleGetHistory(msg)
	case 0x1116: // HISTORY drill-in: tracks in a session
		return h.handleGetHistoryTracks(msg)
	case 0x100d: // COLOR list — the 8 named track colours + "no colour"
		return h.handleGetColors(msg)
	case 0x110d: // COLOR drill-in: tracks of a given colour
		return h.handleGetTracksByColor(msg)
	case 0x2005:
		// Stub: analysis section by track — args [spec, slot?, track_id, 0,
		// range, 0]. This opcode expects NO response;
		// replying corrupts the deck's load state.
		return h.handleUndecodedStub(msg)
	case 0x2805:
		// PVB2 write: the deck uploads a seek index it computed itself (arg[6]
		// is a full PVB2 section) after rejecting the PVB2 we served. This is a
		// WRITE, analogous to 0x2705 cue-write — it MUST be acknowledged or the
		// deck deadlocks its dbserver channel (blank details, hung browse)
		// while NFS/audio keep working.
		return h.handleWritePVB2(msg)
	case 0x1114: // key distances (3 groups: exact, +/-1, +/-2)
		return h.handleGetKeyDistances(msg)
	case 0x1214: // tracks near key (key_id + distance)
		return h.handleGetTracksByKey(msg)
	case 0x1009: // remixer list
		return h.handleGetRemixers(msg)
	case 0x100a: // label list
		return h.handleGetLabels(msg)
	case 0x100b: // original artist list
		return h.handleGetOriginalArtists(msg)
	case 0x110a: // artists for label (drill-down)
		return h.handleGetArtistsForLabel(msg)
	case 0x120a: // albums for label + artist
		return h.handleGetAlbumsForLabel(msg)
	case 0x130a: // tracks for label + artist + album
		return h.handleGetTracksByLabel(msg)
	case 0x1108: // years within decade (drill-down)
		return h.handleGetYearsForDecade(msg)
	case 0x1208: // tracks for decade + year
		return h.handleGetTracksByYear(msg)
	case 0x1206: // tracks for BPM +/- %
		return h.handleGetTracksByBPM(msg)
	case 0x1005, 0x1105: // PLAYLIST root (0x1005, 2 args) + drill-down (0x1105, 4 args)
		return h.handleGetPlaylist(msg)
	case 0x2006, 0x100f: // folder menu (0x100f = root listing, 0x2006 = drill-down by folder ID)
		return h.handleGetFolder(msg)
	case proto.DBMsgGetMetadata:
		return h.handleGetMetadata(msg)
	case proto.DBMsgGetArtwork:
		return h.handleGetArtwork(msg)
	case proto.DBMsgGetWavePreview: // 0x2004
		return h.handleGetWavePreview(msg)
	case proto.DBMsgGetWaveDetail: // 0x2904
		return h.handleGetWaveDetail(msg)
	case proto.DBMsgGetWaveColor: // 0x2c04
		return h.handleGetExtAnalysis(msg)
	case proto.DBMsgGetBeatGrid: // 0x2204
		return h.handleGetBeatGrid(msg)
	case proto.DBMsgGetCuePoints: // 0x2104
		return h.handleGetCuePoints(msg)
	case 0x2102: // track file info (path, duration, BPM, key, comment)
		return h.handleGetTrackInfo(msg)
	case 0x2103: // alternate artwork request (by track ID)
		return h.handleGetArtwork(msg)
	case 0x2107: // rating update from CDJ
		return h.handleSetRating(msg)
	case 0x3100: // mount/ANLZ notification
		return h.handleMountInfo(msg)
	case 0x2b04: // NXS2 cue/loop points (extended format)
		return h.handleGetNXS2CuePoints(msg)
	case 0x2504: // song structure / phrase analysis
		return h.handleGetSongStructure(msg)
	case 0x3d03: // NXS2 cue/loop data
		return h.handleGetNXS2Cues(msg)
	case 0x1007: // rating list
		return h.handleGetRating(msg)
	case 0x1107: // tracks for a given rating
		return h.handleGetTracksByRating(msg)
	case 0x1008: // year list
		return h.handleGetYears(msg)
	case 0x0001: // cancel/abort — no response needed
		return nil
	case 0x0100: // teardown — no response needed
		return nil
	case 0x2705: // cue point write — blob is the 5th arg (binary)
		trackID := uint32(0)
		if len(msg.Args) >= 2 {
			trackID = msg.Args[1].Int()
		}
		log.Printf("dbserver: cue write 0x2705 track=%d args=%d", trackID, len(msg.Args))

		// The 5th arg (index 4) is the binary blob with cue data.
		var blob []byte
		if len(msg.Args) >= 5 && len(msg.Args[4].Bytes) > 0 {
			blob = msg.Args[4].Bytes
			if trackID > 0 && h.cues != nil {
				cue, err := ParseCueBlob(blob, trackID)
				if err != nil {
					log.Printf("dbserver: cue parse error: %v", err)
				} else {
					h.cues.SaveCue(trackID, cue, blob)
				}
			}
		}

		// The response is 0x4e02 containing ALL cues concatenated.
		allBlob := blob
		if h.cues != nil {
			if combined := h.cues.GetCombinedBlob(trackID); len(combined) > 0 {
				allBlob = combined
			}
		}
		cueCount := uint32(0)
		if h.cues != nil {
			cueCount = uint32(len(h.cues.GetCues(trackID)))
		}
		return []*proto.DBMessage{{
			TxID: msg.TxID,
			Type: 0x4e02,
			Args: []proto.DBArg{
				proto.ArgI32(uint32(msg.Type)),
				proto.ArgI32(0),
				proto.ArgI32(uint32(len(allBlob))),
				proto.ArgBlob(allBlob),
				proto.ArgI32(cueCount),
			},
		}}
	case 0xffff: // leftover binary blob (shouldn't happen now)
		log.Printf("dbserver: unexpected binary blob frame")
		return nil
	default:
		argVals := make([]string, len(msg.Args))
		for i, a := range msg.Args {
			argVals[i] = fmt.Sprintf("0x%08x(%d)", a.Int(), a.Int())
		}
		log.Printf("dbserver: unhandled type 0x%04x args=%v", msg.Type, argVals)
		// Default: echo type in success response (matches CDJ behavior)
		return []*proto.DBMessage{{
			TxID: msg.TxID,
			Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
}

func (h *Handler) handleSetup(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) > 0 {
		log.Printf("dbserver setup: client player number = %d", msg.Args[0].Int())
	}
	// Response must have TWO int32 args: [0, server_player_number]
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(0),
			proto.ArgI32(uint32(h.deviceNumber)),
		},
	}}
}

func (h *Handler) handleMediaInfo(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver: media info (0x3007)")
	// Respond: success with [echo_type, 0] — matches the CDJ
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(0x3007), proto.ArgI32(0)},
	}}
}

func (h *Handler) handleNXS2Extension(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver: NXS2 extension (0x3e03)")
	// Respond: type 0x4b02 with [echo_type, 0, 2, ""] — matches the CDJ
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: 0x4b02,
		Args: []proto.DBArg{
			proto.ArgI32(0x3e03),
			proto.ArgI32(0),
			proto.ArgI32(2),
			proto.ArgStr(""),
		},
	}}
}

// rootMenu returns the current root-menu category list. Source of
// truth: MenuSource.RootMenu() when wired, else a built-in default
// matching python-prodj-link's category IDs. Computed fresh each
// call — there's no per-Handler cache because the CDJ aggressively
// opens parallel TCP connections (each with its own Handler) for
// analysis fetches; caching here would break renders on those new
// connections until they happen to receive their own 0x1000 query.
func (h *Handler) rootMenu() []*menuItem {
	if h.menu != nil {
		entries := h.menu.RootMenu()
		items := make([]*menuItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, &menuItem{ID: e.ID, Label1: e.Label, ItemType: e.ItemType})
		}
		return items
	}
	return []*menuItem{
		{ID: 2, Label1: "ARTIST", ItemType: 0x81},
		{ID: 3, Label1: "ALBUM", ItemType: 0x82},
		{ID: 4, Label1: "TRACK", ItemType: 0x83},
		{ID: 5, Label1: "PLAYLIST", ItemType: 0x84},
		{ID: 6, Label1: "BPM", ItemType: 0x85},
		{ID: 7, Label1: "RATING", ItemType: 0x86},
		{ID: 9, Label1: "REMIXER", ItemType: 0x88},
		{ID: 10, Label1: "LABEL", ItemType: 0x89},
		{ID: 11, Label1: "ORIGINAL ARTIST", ItemType: 0x8a},
		{ID: 12, Label1: "KEY", ItemType: 0x8b},
		{ID: 13, Label1: "FOLDER", ItemType: 0x8d},
		{ID: 1, Label1: "GENRE", ItemType: 0x80},
		{ID: 8, Label1: "YEAR", ItemType: 0x87},
	}
}

func (h *Handler) handleRootMenu(msg *proto.DBMessage) []*proto.DBMessage {
	log.Printf("dbserver: root menu (0x1000)")
	// The deck is back at the root menu, so any "most recent category/detail
	// list" context is stale. Clear it — otherwise the follow-up root render
	// (menu=1/7) can match lastCategoryItems by count and show that stale
	// content instead of the root menu. This is exactly the INFO bug: the
	// track-info detail list has 16 rows, same as the root's 16 categories,
	// so browsing the menu after INFO rendered the track info.
	h.lastCategoryItems = nil
	items := h.rootMenu()
	log.Printf("dbserver: root menu returning %d categories", len(items))
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(0x1000),
			proto.ArgI32(uint32(len(items))),
		},
	}}
}

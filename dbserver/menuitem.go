// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/internal/filetype"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// menuitem.go holds the menuItem type, sort constants/dispatch, the
// track→item conversion helpers (library + PDB variants), the
// per-detail-column ItemType/ParentID/Label2 application logic, and a
// few small format helpers used across handlers.

type menuItem struct {
	ID        uint32
	ParentID  uint32
	Label1    string
	Label2    string
	ArtID     uint32
	ItemType  uint32 // e.g. track=1, artist=2, album=3, genre=6
	FileType  string // "mp3", "m4a", "flac", "wav", "aiff" — for title items
	TrackInfo bool   // true for track-info title items (no special flags)
	ColorID   uint32 // track color (1=pink, 2=red, ..., 8=purple) for arg[11]
	rating    uint32 // track rating (0-5 stars), for the TRACK FILTER

	// Sort metadata (for tracks).
	sortArtist string
	sortAlbum  string
	sortBPM    uint32 // BPM * 100
	sortKey    string
	keyID      uint32      // djmdContent.KeyID — extended-row arg 12
	sv         *sortValues // the fields the deck's sorts read (sortmenu.go)

	// Track-row wire-flag inputs (see render.go's flag assembly).
	contextTrackType uint32 // packed context track type — arg 7 high byte (TT<<24) on hierarchy track leaves
	noArtHandle      bool   // album row in a song-info list: arg 8 stays 0
	// songInfoTitle, on a Display/Delivery title row, picks its second
	// column at render time (songinfo_title.go).
	songInfoTitle  *songInfoTitle
	trackPos       uint32 // arg 9: scope position / track number (album scope uses TrackNo; 0 elsewhere)
	hasSongTag     bool   // track carries a MyTag — arg 7 bit 0x1
	autoLoadHotCue bool   // deck auto-loads hot cues — arg 10 bit 0x100
	fileTypeID     int    // rekordbox FileType enum — arg 10 compatibility bit
	sampleRate     int    // Hz — arg 10 compatibility bit for wav/aiff
	newKeyID       uint32 // converted (Camelot 1-24) key index — arg 11
}

// Sort order constants from CDJ protocol.
const (
	sortDefault = 0x00
	sortTitle   = 0x01
	sortArtist  = 0x02
	sortAlbum   = 0x03
	sortBPM     = 0x04
	sortRating  = 0x05
	sortKey     = 0x0c
)

// sortItems sorts menu items by the given sort order.
func sortItems(items []*menuItem, order uint32) {
	switch order {
	case sortTitle:
		sort.Slice(items, func(i, j int) bool {
			return strings.ToLower(items[i].Label1) < strings.ToLower(items[j].Label1)
		})
	case sortArtist:
		sort.Slice(items, func(i, j int) bool {
			return strings.ToLower(items[i].sortArtist) < strings.ToLower(items[j].sortArtist)
		})
	case sortAlbum:
		sort.Slice(items, func(i, j int) bool {
			return strings.ToLower(items[i].sortAlbum) < strings.ToLower(items[j].sortAlbum)
		})
	case sortBPM:
		sort.Slice(items, func(i, j int) bool {
			return items[i].sortBPM < items[j].sortBPM
		})
	case sortKey:
		sort.Slice(items, func(i, j int) bool {
			return items[i].sortKey < items[j].sortKey
		})
	default:
		// Default: preserve PDB/database order (matches CDJ behavior).
	}
}

// getSortOrder extracts the sort order from msg args (typically arg[1]).
func getSortOrder(msg *proto.DBMessage) uint32 {
	if len(msg.Args) >= 2 {
		return msg.Args[1].Int()
	}
	return 0
}

// catID returns the rekordbox lookup-table ID for a category value when the
// library carries one (recorded during a master.db import), falling back to
// the stable name hash otherwise. Both category-row generation and drill-in
// matching route through this helper, so the IDs we serve and the IDs decks
// send back when drilling in stay in lockstep.
func (h *Handler) catID(kind, name string) uint32 {
	return categoryID(h.lib, kind, name)
}

// categoryID is catID for a library, which may be nil.
func categoryID(lib *library.Library, kind, name string) uint32 {
	if lib != nil {
		if id := lib.CategoryID(kind, name); id != 0 {
			return id
		}
	}
	return library.HashID(kind, name)
}

// trackToStdItem creates a standard track menu item in the standard format.
// Detail column is governed by h.menu.TrackDetail() (default "bpm"),
// producing "BPM - Key" in Label2 + BPM*100 in ParentID + ItemType
// 0x0d04 for that classic look. It builds the row on its own; a list of
// tracks copies the library's prebuilt rows (tracksToStdItems).
func (h *Handler) trackToStdItem(t *library.Track) *menuItem {
	m := new(menuItem)
	h.newTrackRow(m, t)
	return m
}

// newTrackRow fills m with a track's row, built on its own rather than
// copied from the library's rows (trackrows.go).
func (h *Handler) newTrackRow(m *menuItem, t *library.Track) {
	ids := trackCatIDs(t, h.catID)
	sv := libSortValues(t)
	h.fillTrackRow(m, t, &ids, &sv)
}

// fillTrackRow fills m with a track's row, given its category IDs and sort
// values (which the row keeps).
func (h *Handler) fillTrackRow(m *menuItem, t *library.Track, ids *catIDs, sv *sortValues) {
	*m = menuItem{
		ID:             t.ID,
		Label1:         t.Title,
		ArtID:          t.ArtID,
		FileType:       h.servedFileType(t.FileType),
		ColorID:        uint32(t.ColorID),
		rating:         uint32(t.Rating),
		sortArtist:     t.Artist,
		sortAlbum:      t.Album,
		sortBPM:        uint32(math.Round(t.BPM * 100)), // exact for range checks (TRACK FILTER)
		sortKey:        t.Key,
		hasSongTag:     h.lib != nil && h.lib.HasSongTag(t.ID),
		autoLoadHotCue: t.AutoLoadHotCue,
		fileTypeID:     t.FileTypeID,
		sampleRate:     t.SampleRate,
		newKeyID:       newKeyID(t.Key),
		sv:             sv,
	}
	if h.transcoded(t.FileType) {
		// rekordbox's file type is the original file's; the deck reads an
		// MP3, so the compatibility bit comes from the "mp3" fallback.
		m.fileTypeID = 0
	}
	// keyID feeds the extended-row key argument; leave it 0 for keyless
	// tracks rather than hashing an empty string.
	if t.Key != "" {
		m.keyID = ids.key
	}
	h.applyTrackDetail(m, t, ids)
}

// newKeyID computes rekordbox's 1-24 "new key" index for a track's key. The
// index is derived from the musical key (via its Camelot position), not the
// configured display notation, so a track shown as "C" (Camelot 8B) still maps
// to 16. Keys interleave A then B per wheel position: 1A=1, 1B=2, ... 8A=15,
// 8B=16, ... 12A=23, 12B=24. Returns 0 for an unrecognized key.
func newKeyID(key string) uint32 {
	k, ok := analysis.KeyByName(key)
	if !ok {
		// rekordbox reads a key name loosely: a leading root note A-G
		// (fullwidth too), an optional sharp or flat, then "m" for minor;
		// anything after is ignored ("Alpha" is A major, "Beta" B major), and
		// a name that doesn't start with a root note has no index.
		if loose := looseKeyName(key); loose != "" && loose != key {
			return newKeyID(loose)
		}
		return 0
	}
	return uint32(k.Index())
}

// newCDJIncompatible reports whether a track is rejected by rekordbox's
// new-CDJ decode-compatibility path (argument 10 bit 0). FLAC (type 5) and
// type 6 are always rejected; WAV (11) and AIFF (12) are rejected unless the
// sample rate is 44100 or 48000 Hz. The rekordbox FileType enum is
// authoritative; the codec string is a fallback when no enum was imported.
func newCDJIncompatible(fileTypeID int, fileType string, sampleRate int) bool {
	id := fileTypeID
	if id == 0 {
		ft, _ := filetype.ByName(fileType)
		id = ft.RekordboxID
	}
	switch id {
	case 5, 6:
		return true
	case 11, 12:
		return sampleRate != 44100 && sampleRate != 48000
	}
	return false
}

// trackDetailKey returns the configured detail-field key, falling back
// to "bpm" when no MenuSource is wired (legacy tests / setups).
func (h *Handler) trackDetailKey() string {
	if h.menu == nil {
		return "bpm"
	}
	if k := h.menu.TrackDetail(); k != "" {
		return k
	}
	return "bpm"
}

// TrackDetailField is a track-detail column the CDJ knows how to render in
// the second column of a track list. It maps a stable key (used in the API +
// persistence) to the on-the-wire ItemType high byte and a human label for
// the UI dropdown.
//
// Wire-format mapping:
// ItemType = (highByte << 8) | 0x04, ParentID carries the
// raw value (hash, count, ID, BPM*100), Label2 carries the display
// string (CDJ renders some fields from raw value alone).
type TrackDetailField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	HighByte uint8  `json:"high_byte"`
}

// TrackDetailFields lists every track-detail column, in the order the web
// UI offers them. applyTrackDetail serves the configured one.
var TrackDetailFields = []TrackDetailField{
	{Key: "bpm", Label: "BPM", HighByte: 0x0d},
	{Key: "artist", Label: "Artist", HighByte: 0x07},
	{Key: "album", Label: "Album", HighByte: 0x02},
	{Key: "genre", Label: "Genre", HighByte: 0x06},
	{Key: "key", Label: "Key", HighByte: 0x0f},
	{Key: "rating", Label: "Rating", HighByte: 0x0a},
	{Key: "time", Label: "Time", HighByte: 0x0b},
	{Key: "label", Label: "Record label", HighByte: 0x0e},
	{Key: "bitrate", Label: "Bitrate", HighByte: 0x10},
	{Key: "color", Label: "Track colour", HighByte: 0x17},
	{Key: "comments", Label: "Comment", HighByte: 0x23},
	{Key: "original_artist", Label: "Original artist", HighByte: 0x28},
	{Key: "remixer", Label: "Remixer", HighByte: 0x29},
	{Key: "dj_play_count", Label: "Play count", HighByte: 0x2a},
	{Key: "date_added", Label: "Date added", HighByte: 0x2e},
	{Key: "not_specified", Label: "(none)", HighByte: 0x00},
}

// trackDetailHighBytes maps each TrackDetailFields key to its high byte.
var trackDetailHighBytes = func() map[string]uint8 {
	m := make(map[string]uint8, len(TrackDetailFields))
	for _, f := range TrackDetailFields {
		m[f.Key] = f.HighByte
	}
	return m
}()

// TrackDetailHighByte returns a detail column's ItemType high byte, and
// whether key is one of TrackDetailFields.
func TrackDetailHighByte(key string) (uint8, bool) {
	hi, ok := trackDetailHighBytes[key]
	return hi, ok
}

// applyTrackDetail sets ItemType / ParentID / Label2 on m based on the
// configured detail column. Refer to track_detail_fields.md in memory
// for the exact per-field wire shape.
func (h *Handler) applyTrackDetail(m *menuItem, t *library.Track, ids *catIDs) {
	key := h.trackDetailKey()
	hi, _ := TrackDetailHighByte(key)
	m.ItemType = (uint32(hi) << 8) | 0x04
	tempo := uint32(t.BPM * 100)
	switch key {
	case "bpm":
		m.ParentID = tempo
		m.Label2 = fmt.Sprintf("%.1f bpm", t.BPM)
		if t.Key != "" {
			m.Label2 += " - " + t.Key
		}
	case "key":
		// rekordbox composes "<key> - <bpm> bpm" even for an empty key, but
		// shows the key alone when the tempo is zero, and a stored negative
		// tempo by its magnitude (link-spec smart-numeric-boundaries,
		// rekordbox 7.2.19).
		m.ParentID = trackKeyID(t, ids)
		m.Label2 = t.Key
		if raw := math.Round(t.BPM * 100); raw != 0 {
			m.Label2 = fmt.Sprintf("%s - %.1f bpm", t.Key, math.Abs(raw)/100)
		}
	case "artist":
		m.ParentID = ids.artist
		m.Label2 = t.Artist
	case "album":
		m.ParentID = ids.album
		m.Label2 = t.Album
	case "genre":
		m.ParentID = ids.genre
		m.Label2 = t.Genre
	case "label":
		m.ParentID = ids.label
		m.Label2 = t.Label
	case "remixer":
		m.ParentID = ids.remixer
		m.Label2 = t.Remixer
	case "original_artist":
		m.ParentID = ids.origArtist // the ORIGINAL ARTIST menu's IDs
		m.Label2 = t.OriginalArtist
	case "time":
		m.ParentID = uint32(t.Duration.Seconds())
	case "bitrate":
		m.ParentID = uint32(t.Bitrate)
	case "rating":
		m.ParentID = uint32(t.Rating)
	case "dj_play_count":
		m.ParentID = uint32(t.PlayCount)
	case "color":
		// Color encoding (all 8 colours + no-colour tracks):
		//   ItemType high byte = 0x13 + ColorID (0x13 = none, 0x14 = Pink
		//     … 0x1b = Purple); low byte stays 0x04 (track row).
		//   ParentID = ColorID for coloured tracks, 0x7fffffff sentinel
		//     for no-colour.
		//   Label2 = colour name string, empty for no-colour.
		if t.ColorID >= 1 && t.ColorID <= 8 {
			m.ParentID = uint32(t.ColorID)
			m.Label2 = library.TrackColorName(t.ColorID)
			m.ItemType = (uint32(0x13+t.ColorID) << 8) | 0x04
		} else {
			m.ParentID = 0x7fffffff
			m.Label2 = ""
			m.ItemType = 0x1304
		}
	case "comments":
		m.ParentID = t.ID
		m.Label2 = t.Comment
	case "date_added":
		m.ParentID = t.ID
		if !t.DateAdded.IsZero() {
			m.Label2 = t.DateAdded.Format("2006-01-02")
		}
	}
}

// markContextType stamps the packed context track type (TT) on every item, so
// their served rows carry TT<<24 in argument 7. Applied to hierarchy track
// leaves (genre/artist/album/label/original-artist/remixer), which rekordbox
// tags with the request's own context track type (h.ctxTrackType); the direct
// track list and scalar filters leave it 0.
func markContextType(items []*menuItem, tt uint32) {
	for _, m := range items {
		m.contextTrackType = tt
	}
}

// tracksToStdItems returns the tracks' rows (0x0d04 format by default):
// copies of the library's prebuilt rows, in one allocation, and a row built
// on its own for a track they don't hold.
func (h *Handler) tracksToStdItems(tracks []*library.Track) []*menuItem {
	c := h.trackRows()
	rows := make([]menuItem, len(tracks))
	items := make([]*menuItem, len(tracks))
	for i, t := range tracks {
		if p, ok := c.position(t); ok {
			rows[i] = c.rows[p]
		} else {
			h.newTrackRow(&rows[i], t)
		}
		items[i] = &rows[i]
	}
	return items
}

// trackInfoTitleID returns the decoder ID the CDJ uses to pick its audio
// decoder for a loaded track. It is the rekordbox FileType enum value — the
// same number written to the PDB track row at offset 0x5A:
//
//	mp3 = 1, m4a/AAC = 4, flac = 5, wav = 0x0b, aiff = 0x0c
//
// For AAC, the 0x2102 track-info title row carries id=4 for
// .m4a tracks. The previous "1 = MP3/AAC" assumption handed AAC tracks the
// MP3 decoder ID; the deck read the whole file, failed to decode it as MP3,
// and bailed to play-state 0x0e. AIFF is corrected to 0x0c by the same rule
// (was 0x0b/WAV) — inferred from the enum, not yet hardware-verified.
func trackInfoTitleID(fileType string) uint32 {
	if ft, ok := filetype.ByName(fileType); ok {
		return uint32(ft.RekordboxID)
	}
	return 1 // as MP3
}

// trackKeyID is a track's key ID as rekordbox serves it: the imported key row
// ID (which an empty-named key also has), else the KEY menu's ID for the key's
// name, and 0 for a track with no key.
func trackKeyID(t *library.Track, ids *catIDs) uint32 {
	if t.KeyID != 0 {
		return t.KeyID
	}
	if t.Key == "" {
		return 0
	}
	return ids.key
}

// looseKeyName reduces a key name to the part rekordbox reads (see
// newKeyID): root note, accidental and minor flag, or "" when it doesn't
// start with a root note.
func looseKeyName(key string) string {
	r := []rune(key)
	for i, c := range r {
		if c >= 0xff01 && c <= 0xff5e { // fullwidth ASCII
			r[i] = c - 0xfee0
		}
	}
	if len(r) == 0 || r[0] < 'A' || r[0] > 'G' {
		return ""
	}
	name := string(r[0])
	rest := r[1:]
	if len(rest) > 0 {
		switch rest[0] {
		case '#', '♯':
			name += "#"
			rest = rest[1:]
		case 'b', '♭':
			name += "b"
			rest = rest[1:]
		}
	}
	if len(rest) > 0 && rest[0] == 'm' {
		name += "m"
	}
	return name
}

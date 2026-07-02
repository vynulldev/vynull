// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
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

	// Sort metadata (for tracks).
	sortArtist string
	sortAlbum  string
	sortBPM    uint32 // BPM * 100
	sortKey    string
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
		// Default: preserve PDB/database order (matches real CDJ behavior).
	}
}

// getSortOrder extracts the sort order from msg args (typically arg[1]).
func getSortOrder(msg *proto.DBMessage) uint32 {
	if len(msg.Args) >= 2 {
		return msg.Args[1].Int()
	}
	return 0
}

func (h *Handler) pdbTracksToItems(tracks []*pdb.Track) []*menuItem {
	items := make([]*menuItem, len(tracks))
	for i, t := range tracks {
		m := h.pdbTrackToStdItem(t)
		m.sortArtist = t.Artist
		m.sortAlbum = t.Album
		m.sortBPM = t.Tempo
		m.sortKey = t.Key
		items[i] = m
	}
	return items
}

// pdbTrackToStdItem creates a standard track menu item from a PDB track.
func (h *Handler) pdbTrackToStdItem(t *pdb.Track) *menuItem {
	m := &menuItem{
		ID:       t.ID,
		Label1:   t.Title,
		ArtID:    t.ArtworkID,
		FileType: h.resolveFileType(t.ID),
		ColorID:  uint32(t.ColorID),
	}
	h.applyTrackDetailFromPDB(m, t)
	return m
}

// trackToStdItem creates a standard track menu item matching rekordbox format.
// Detail column is governed by h.menu.TrackDetail() (default "bpm"),
// producing "BPM - Key" in Label2 + BPM*100 in ParentID + ItemType
// 0x0d04 for that classic look.
func (h *Handler) trackToStdItem(t *library.Track) *menuItem {
	m := &menuItem{
		ID:       t.ID,
		Label1:   t.Title,
		ArtID:    t.ArtID,
		FileType: t.FileType,
		ColorID:  uint32(t.ColorID),
	}
	h.applyTrackDetail(m, t)
	return m
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

// trackDetailHighByte maps a detail key to its ItemType high byte.
// Inlined here rather than imported from api/ to keep dbserver
// independent of the api package; the map mirrors api.TrackDetailFields.
var trackDetailHighByte = map[string]uint8{
	"bpm":             0x0d,
	"artist":          0x07,
	"album":           0x02,
	"genre":           0x06,
	"key":             0x0f,
	"rating":          0x0a,
	"time":            0x0b,
	"label":           0x0e,
	"bitrate":         0x10,
	"color":           0x17,
	"comments":        0x23,
	"original_artist": 0x28,
	"remixer":         0x29,
	"dj_play_count":   0x2a,
	"date_added":      0x2e,
	"not_specified":   0x00,
}

// applyTrackDetail sets ItemType / ParentID / Label2 on m based on the
// configured detail column. Refer to track_detail_fields.md in memory
// for the exact per-field wire shape.
func (h *Handler) applyTrackDetail(m *menuItem, t *library.Track) {
	key := h.trackDetailKey()
	hi := trackDetailHighByte[key]
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
		m.ParentID = library.HashID("key", t.Key)
		if t.Key != "" {
			m.Label2 = fmt.Sprintf("%s - %.1f bpm", t.Key, t.BPM)
		}
	case "artist":
		m.ParentID = library.HashID("artist", t.Artist)
		m.Label2 = t.Artist
	case "album":
		m.ParentID = library.HashID("album", t.Album)
		m.Label2 = t.Album
	case "genre":
		m.ParentID = library.HashID("genre", t.Genre)
		m.Label2 = t.Genre
	case "label":
		m.ParentID = library.HashID("label", t.Label)
		m.Label2 = t.Label
	case "remixer":
		m.ParentID = library.HashID("remixer", t.Remixer)
		m.Label2 = t.Remixer
	case "original_artist":
		m.ParentID = library.HashID("original_artist", t.OriginalArtist)
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
		// rekordbox encoding (verified against rb-menu-colors-playlist
		// -classics.pcap with all 8 colours + no-colour tracks):
		//   ItemType high byte = 0x13 + ColorID (0x13 = none, 0x14 = Pink
		//     … 0x1b = Purple); low byte stays 0x04 (track row).
		//   ParentID = ColorID for coloured tracks, 0x7fffffff sentinel
		//     for no-colour.
		//   Label2 = colour name string, empty for no-colour.
		if t.ColorID >= 1 && t.ColorID <= 8 {
			m.ParentID = uint32(t.ColorID)
			m.Label2 = trackColorName(t.ColorID)
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

// applyTrackDetailFromPDB is the PDB-track equivalent — same field
// dispatch but reads from the pdb.Track shape (no DateAdded, Comment,
// etc. always populated). Falls back to bpm rendering for fields that
// the PDB track doesn't carry.
func (h *Handler) applyTrackDetailFromPDB(m *menuItem, t *pdb.Track) {
	key := h.trackDetailKey()
	hi := trackDetailHighByte[key]
	m.ItemType = (uint32(hi) << 8) | 0x04
	bpm := float64(t.Tempo) / 100
	switch key {
	case "bpm":
		m.ParentID = t.Tempo
		m.Label2 = fmt.Sprintf("%.1f bpm", bpm)
		if t.Key != "" {
			m.Label2 += " - " + t.Key
		}
	case "key":
		m.ParentID = library.HashID("key", t.Key)
		if t.Key != "" {
			m.Label2 = fmt.Sprintf("%s - %.1f bpm", t.Key, bpm)
		}
	case "artist":
		m.ParentID = library.HashID("artist", t.Artist)
		m.Label2 = t.Artist
	case "album":
		m.ParentID = library.HashID("album", t.Album)
		m.Label2 = t.Album
	case "genre":
		m.ParentID = library.HashID("genre", t.Genre)
		m.Label2 = t.Genre
	case "time":
		m.ParentID = uint32(t.Duration)
	case "bitrate":
		m.ParentID = t.Bitrate
	case "rating":
		m.ParentID = uint32(t.Rating)
	case "color":
		if t.ColorID >= 1 && t.ColorID <= 8 {
			m.ParentID = uint32(t.ColorID)
			m.Label2 = trackColorName(t.ColorID)
			m.ItemType = (uint32(0x13+uint32(t.ColorID)) << 8) | 0x04
		} else {
			m.ParentID = 0x7fffffff
			m.Label2 = ""
			m.ItemType = 0x1304
		}
	case "comments":
		m.ParentID = t.ID
		m.Label2 = t.Comment
	default:
		// PDB track doesn't carry remixer/original_artist/date_added/
		// play_count strings — fall through to BPM rendering so the
		// row still reads naturally.
		m.ItemType = 0x0d04
		m.ParentID = t.Tempo
		m.Label2 = fmt.Sprintf("%.1f bpm", bpm)
		if t.Key != "" {
			m.Label2 += " - " + t.Key
		}
	}
}

// trackColorName mirrors the 8-entry rekordbox palette name list used
// in the api package. Defined here to avoid cross-package import.
func trackColorName(id uint8) string {
	switch id {
	case 1:
		return "Pink"
	case 2:
		return "Red"
	case 3:
		return "Orange"
	case 4:
		return "Yellow"
	case 5:
		return "Green"
	case 6:
		return "Aqua"
	case 7:
		return "Blue"
	case 8:
		return "Purple"
	}
	return ""
}

// tracksToStdItems converts library tracks to standard menu items (0x0d04 format).
func (h *Handler) tracksToStdItems(tracks []*library.Track) []*menuItem {
	items := make([]*menuItem, len(tracks))
	for i, t := range tracks {
		items[i] = h.trackToStdItem(t)
	}
	return items
}

// trackInfoTitleID returns the decoder ID the CDJ uses to pick its audio
// decoder for a loaded track. It is the rekordbox FileType enum value — the
// same number written to the PDB track row at offset 0x5A:
//
//	mp3 = 1, m4a/AAC = 4, flac = 5, wav = 0x0b, aiff = 0x0c
//
// The AAC value is confirmed against a rekordbox capture
// (a capture): the 0x2102 track-info title row carries id=4 for
// .m4a tracks. The previous "1 = MP3/AAC" assumption handed AAC tracks the
// MP3 decoder ID; the deck read the whole file, failed to decode it as MP3,
// and bailed to play-state 0x0e. AIFF is corrected to 0x0c by the same rule
// (was 0x0b/WAV) — inferred from the enum, not yet hardware-verified.
func trackInfoTitleID(fileType string) uint32 {
	switch fileType {
	case "m4a":
		return 4
	case "flac":
		return 5
	case "wav":
		return 0x0b
	case "aiff":
		return 0x0c
	default: // mp3
		return 1
	}
}

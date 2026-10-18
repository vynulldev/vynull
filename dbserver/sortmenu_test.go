// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// fixtureLibrary is link-spec's eight-track "full" fixture (build_fixture.py
// full_tracks), the library rekordbox sorted for the sort-ids and
// sort-secondary-render-6 goldens.
func fixtureLibrary() *library.Library {
	titles := []string{"Alpha One", "Alpha Two", "Beta One", "Boundary Fifty Nine",
		"Boundary Sixty", "Unicode Ω Search", "Unknown Album", "Maximum Ordinary"}
	lengths := []int{59, 60, 61, 599, 600, 601, 10799, 10800}
	bitrates := []int{0, 32, 128, 160, 192, 256, 320, 1411}
	lib := library.New()
	for i, title := range titles {
		n := i + 1
		odd := n%2 == 1
		t := &library.Track{
			ID: uint32(10000 + n), Title: title,
			Artist: map[bool]string{true: "Alpha Artist", false: "Beta Artist"}[n <= 5],
			Genre:  map[bool]string{true: "Fixture House", false: "Fixture Techno"}[odd],
			Label:  map[bool]string{true: "Fixture Label One", false: "Fixture Label Two"}[odd],
			Key:    map[bool]string{true: "Am", false: "C"}[odd],
			BPM:    float64(11950+n*50) / 100, Duration: library.DurationSec(time.Duration(lengths[i]) * time.Second),
			Bitrate: bitrates[i], Comment: fmt.Sprintf("comment-%d", n), Rating: uint8((n - 1) % 6),
			Remixer: "Fixture Remixer", OriginalArtist: "Fixture Original", ColorID: uint8(n), PlayCount: n - 1,
			DateAdded: time.Date(2020+n%5, time.Month(n%9+1), n%9+1, 0, 0, 0, 0, time.UTC),
		}
		switch n {
		case 1, 3, 4:
			t.Album = "Album One"
		case 2, 5:
			t.Album = "Album Two"
		case 6, 8:
			t.Album = "Album Three"
		}
		lib.AddTrack(t)
	}
	return lib
}

// Each sort ID orders the fixture as rekordbox did (link-spec sort-ids
// golden, rekordbox 7.2.19): text ascending with empty last, BPM and colour
// ascending, rating, time, bitrate, play count and date added highest first,
// key by key index, and title order on ties and for 0, 1 and 14.
func TestSortTracksMatchesRekordboxOrder(t *testing.T) {
	want := map[uint32][]int{
		0: {1, 2, 3, 4, 5, 8, 6, 7}, 1: {1, 2, 3, 4, 5, 8, 6, 7}, 2: {1, 2, 3, 4, 5, 8, 6, 7},
		3: {1, 3, 4, 8, 6, 2, 5, 7}, 4: {1, 2, 3, 4, 5, 6, 7, 8}, 5: {6, 5, 4, 3, 2, 8, 1, 7},
		6: {1, 3, 5, 7, 2, 4, 8, 6}, 7: {1, 2, 3, 4, 5, 6, 7, 8}, 8: {8, 7, 6, 5, 4, 3, 2, 1},
		9: {1, 2, 3, 4, 5, 8, 6, 7}, 10: {1, 3, 5, 7, 2, 4, 8, 6}, 11: {1, 2, 3, 4, 5, 8, 6, 7},
		12: {1, 3, 5, 7, 2, 4, 8, 6}, 13: {8, 7, 6, 5, 4, 3, 2, 1}, 14: {1, 2, 3, 4, 5, 8, 6, 7},
		15: {1, 2, 3, 4, 5, 6, 7, 8}, 16: {8, 7, 6, 5, 4, 3, 2, 1}, 17: {4, 8, 3, 7, 2, 6, 1, 5},
	}
	h := &Handler{lib: fixtureLibrary()}
	for order, ids := range want {
		h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(order)}})
		var got []int
		for _, m := range h.pendingItems {
			got = append(got, int(m.ID)-10000)
		}
		if !slices.Equal(got, ids) {
			t.Errorf("sort %d: %v, want %v", order, got, ids)
		}
	}
}

// A sorted list's rows show the sort's field as their second column
// (link-spec sort-secondary-render-6 golden).
func TestSortTracksSecondColumn(t *testing.T) {
	h := &Handler{lib: fixtureLibrary()}
	first := func(order uint32) *menuItem {
		h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(0x01010401), proto.ArgI32(order)}})
		return h.pendingItems[0]
	}
	for _, tc := range []struct {
		order    uint32
		parent   uint32
		label    string
		itemType uint32
	}{
		{1, 0, "Alpha One", 0x0404},
		{2, 0, "Alpha Artist", 0x0704},
		{3, 0, "Album One", 0x0204},
		{4, 12000, "120.0 bpm - Am", 0x0d04},
		{5, 5, "", 0x0a04},
		{6, 0, "Fixture House", 0x0604},
		{12, 14, "Am - 120.0 bpm", 0x0f04},
		{16, 7, "", 0x2a04},
		{17, 0, "2024-05-05", 0x2e04},
	} {
		m := first(tc.order)
		if m.ParentID != tc.parent || m.Label2 != tc.label || m.ItemType != tc.itemType || m.contextTrackType != 0x01 {
			t.Errorf("sort %d: first row %d %q %#x tt %d, want %d %q %#x tt 1",
				tc.order, m.ParentID, m.Label2, m.ItemType, m.contextTrackType, tc.parent, tc.label, tc.itemType)
		}
	}
	if m := first(0); m.contextTrackType != 0 {
		t.Errorf("default sort: context type %d, want none", m.contextTrackType)
	}
}

// The SORT menu (0x1400) lists the configured sorts, rekordbox's default
// ones when none are configured.
func TestSortMenuRows(t *testing.T) {
	h := &Handler{}
	h.Handle(&proto.DBMessage{TxID: 1, Type: 0x1400, Args: []proto.DBArg{proto.ArgI32(0x01050401), proto.ArgI32(0), proto.ArgI32(0x1004)}})
	var got []string
	for _, m := range h.pendingItems {
		got = append(got, fmt.Sprintf("%d %s %#x", m.ID, m.Label1, m.ItemType))
	}
	want := []string{"0 DEFAULT 0xa1", "1 ALPHABET 0xa2", "2 ARTIST 0x81", "3 ALBUM 0x82", "4 BPM 0x85",
		"5 RATING 0x86", "12 KEY 0x8b", "10 LABEL 0x89", "6 GENRE 0x80", "17 DATE ADDED 0x8c", "16 DJ PLAY COUNT 0x97"}
	if !slices.Equal(got, want) {
		t.Errorf("sort menu:\n got %v\nwant %v", got, want)
	}
}

// Text sorts ignore case, accents, spaces and punctuation, and put digits
// after letters; text not starting with a letter (a digit, a symbol or a
// space) sorts after all text that does, as rekordbox's do.
func TestSortCollationKey(t *testing.T) {
	ordered := []string{"ABC", "About Damn Time", "A380 (Extended)", "Café", "cafe z", "Zebra", " HTE Recordings", "(What's the Story)", "2mysoul"}
	for i := 1; i < len(ordered); i++ {
		a, b := sortCollationKey(ordered[i-1]), sortCollationKey(ordered[i])
		if a > b {
			t.Errorf("%q sorts after %q", ordered[i-1], ordered[i])
		}
	}
}

// In a playlist, tracks a sort ranks equal keep the playlist's order (link-
// spec smart-serving-crosses sort-12), and ALPHABET orders by title.
func TestSortTracksKeepsListOrderOnTies(t *testing.T) {
	h := &Handler{lib: fixtureLibrary()}
	order := func(sortID uint32, ids ...uint32) []int {
		items := h.trackIDsToMenuItems(ids)
		h.applyPlaylistTrackScope(items, sortID, false)
		var got []int
		for _, m := range items {
			got = append(got, int(m.ID)-10000)
		}
		return got
	}
	// Playlist order 8, 6, 2, 4 (all key C): key sort leaves them as they are.
	if got := order(12, 10008, 10006, 10002, 10004); !slices.Equal(got, []int{8, 6, 2, 4}) {
		t.Errorf("key sort: %v, want playlist order [8 6 2 4]", got)
	}
	if got := order(1, 10008, 10006, 10002, 10004); !slices.Equal(got, []int{2, 4, 8, 6}) {
		t.Errorf("ALPHABET: %v, want title order [2 4 8 6]", got)
	}
}

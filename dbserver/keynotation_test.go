// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"strings"
	"testing"

	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// keySetting is a KeyDisplaySource whose notation can be changed.
type keySetting struct{ notation *string }

func (k keySetting) KeyDisplay() string { return *k.notation }

// A key is named by its place on the wheel, however it's spelled; a name
// that isn't a key is left as it is.
func TestKeyName(t *testing.T) {
	for _, c := range []struct{ key, classic, alpha string }{
		{"08A", "Am", "8A"}, {"8B", "C", "8B"}, {"Am", "Am", "8A"}, {"G#m", "Abm", "1A"}, {"12b", "E", "12B"},
		{"Alpha", "Alpha", "Alpha"}, {"13A", "13A", "13A"}, {"", "", ""},
	} {
		if got := keyName(c.key, true); got != c.classic {
			t.Errorf("keyName(%q) in Classic = %q, want %q", c.key, got, c.classic)
		}
		if got := keyName(c.key, false); got != c.alpha {
			t.Errorf("keyName(%q) in Alphanumeric = %q, want %q", c.key, got, c.alpha)
		}
	}
}

// The KEY menu and its distances follow the device setting, Classic when
// there is none.
func TestKeyMenusFollowNotation(t *testing.T) {
	labels := func(h *Handler, kind uint16, args ...uint32) string {
		a := []proto.DBArg{proto.ArgI32(rootCtx)}
		for _, v := range args {
			a = append(a, proto.ArgI32(v))
		}
		h.Handle(&proto.DBMessage{TxID: 1, Type: kind, Args: a})
		var out []string
		for _, m := range h.pendingItems {
			out = append(out, m.Label1)
		}
		return strings.Join(out, "|")
	}
	notation := "alphanumeric"
	for _, c := range []struct {
		h                 *Handler
		first, last, dist string
	}{
		{&Handler{lib: fixtureLibrary()}, "Abm", "E", "Am|Am, C|Am, C, Dm, Em"},
		{&Handler{lib: fixtureLibrary(), keys: keySetting{&notation}}, "1A", "12B", "8A|8A, 8B|8A, 8B, 7A, 9A"},
	} {
		root := strings.Split(labels(c.h, 0x1014, 0), "|")
		if len(root) != 24 || root[0] != c.first || root[23] != c.last {
			t.Errorf("KEY menu %v, want 24 keys from %s to %s", root, c.first, c.last)
		}
		if got := labels(c.h, 0x1114, 0, 15); got != c.dist {
			t.Errorf("distances from 8A: %q, want %q", got, c.dist)
		}
	}
}

// Track rows name the key in the device setting's notation, in the second
// column and the key argument, and rebuild when the setting changes; a
// zero-padded key still has its index and KEY sort place.
func TestTrackRowsKeyNotation(t *testing.T) {
	lib := library.NewLibrary([]*library.Track{
		{ID: 1, Title: "One", Key: "08B", BPM: 121, FilePath: "/m/1.wav"},
		{ID: 2, Title: "Two", Key: "Alpha", BPM: 122, FilePath: "/m/2.wav"},
		{ID: 3, Title: "Three", Key: "08A", BPM: 120, FilePath: "/m/3.wav"},
	}, nil)
	detail, notation := "key", "classic"
	h := &Handler{lib: lib, menu: detailMenu{&detail}, keys: keySetting{&notation}}
	check := func(id uint32, label2, key string) {
		t.Helper()
		m := rowFor(trackList(h, 0), id)
		if m.Label2 != label2 || m.sortKey != key {
			t.Errorf("%s, %s column: row %d %q, key %q; want %q, %q", notation, detail, id, m.Label2, m.sortKey, label2, key)
		}
	}
	check(3, "Am - 120.0 bpm", "Am")
	check(2, "Alpha - 122.0 bpm", "Alpha")
	notation = "alphanumeric"
	check(3, "8A - 120.0 bpm", "8A")
	detail = "bpm"
	check(1, "121.0 bpm - 8B", "8B")

	var order []uint32
	for _, m := range trackList(h, 12) { // KEY
		order = append(order, m.ID)
	}
	if first := rowFor(trackList(h, 12), 3); first.newKeyID != 15 || first.ParentID != 14 || len(order) != 3 || order[0] != 3 || order[1] != 1 {
		t.Errorf("KEY sort: order %v, 08A's index %d and column %d; want [3 1 2], 15 and 14", order, first.newKeyID, first.ParentID)
	}

	h.Handle(&proto.DBMessage{TxID: 2, Type: proto.DBMsgGetMetadata, Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(3)}})
	if got := h.pendingItems[5]; got.ItemType != 0x0f || got.Label1 != "8A" {
		t.Errorf("metadata key row %#x %q, want the key 8A", got.ItemType, got.Label1)
	}
}

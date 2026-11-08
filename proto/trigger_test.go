// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The type 0x1b triggers, against rekordbox's own: a TAG LIST change
// (device 17, 2339 tracks; rekordbox had 40 playlists where we always
// advertise 35) and a rating change, whose bytes are unchanged.
func TestLibraryChangeTriggers(t *testing.T) {
	const header = "5173707431576d4a4f4c1b72656b6f7264626f780000000000000000000000010111000c11"
	captured, _ := hex.DecodeString(header + "0800000000092300000028")
	got := MarshalListRefreshTrigger("rekordbox", 0x11, true, 2339)
	if !bytes.Equal(got[:0x2c], captured[:0x2c]) {
		t.Errorf("tag list trigger\n got %x\nwant %x", got, captured)
	}
	if got[0x2f] != mediaPlaylistCount || !bytes.Equal(got[0x2c:0x2f], []byte{0, 0, 0}) {
		t.Errorf("tag list trigger playlist count %x, want %d", got[0x2c:], mediaPlaylistCount)
	}
	if p := MarshalListRefreshTrigger("rekordbox", 0x11, false, 2339); p[0x25] != 0x22 {
		t.Errorf("playlists trigger byte 0x25 = %#x, want 0x22", p[0x25])
	}
	rating, _ := hex.DecodeString(header + "0100000000002a00000023")
	if got := MarshalRatingRefreshTrigger("rekordbox", 0x11, 42); !bytes.Equal(got, rating) {
		t.Errorf("rating trigger\n got %x\nwant %x", got, rating)
	}
}

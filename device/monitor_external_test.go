// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"testing"

	"github.com/vynulldev/vynull/proto"
)

func TestExternalSource(t *testing.T) {
	st := func(id uint32, dev uint8) *proto.CDJStatus {
		return &proto.CDJStatus{TrackID: id, TrackDevice: dev, TrackSlot: proto.SlotUSB}
	}
	cases := []struct {
		name   string
		status *proto.CDJStatus
		self   uint8
		want   bool
	}{
		{"loaded from us", st(5, 3), 3, false},
		{"USB on another deck", st(5, 2), 3, true},
		{"self not yet claimed", st(5, 2), 0, false},
		{"no track loaded", st(0, 2), 3, false},
		{"no source info", st(5, 0), 3, false},
	}
	for _, c := range cases {
		if got := externalSource(c.status, c.self); got != c.want {
			t.Errorf("%s: externalSource = %v, want %v", c.name, got, c.want)
		}
	}
}

// LoadedTracks lists our tracks on the decks, not a USB track whose ID only
// collides with ours, nor an empty deck.
func TestLoadedTracks(t *testing.T) {
	m := NewPlayerMonitor(nil, nil)
	m.SelfDevice = func() uint8 { return 17 }
	m.Update(&proto.CDJStatus{DeviceNumber: 1, TrackID: 5, TrackDevice: 17, TrackSlot: proto.SlotRekordbox})
	m.Update(&proto.CDJStatus{DeviceNumber: 2, TrackID: 6, TrackDevice: 2, TrackSlot: proto.SlotUSB})
	m.Update(&proto.CDJStatus{DeviceNumber: 3})
	if got := m.LoadedTracks(); len(got) != 1 || !got[5] {
		t.Errorf("LoadedTracks = %v, want only track 5", got)
	}
}

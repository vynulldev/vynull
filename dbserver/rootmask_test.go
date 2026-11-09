// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// fakeMenu is a MenuSource with a given root menu and detail column.
type fakeMenu struct {
	root   []RootMenuEntry
	detail string
}

func (f fakeMenu) RootMenu() []RootMenuEntry {
	if f.root == nil {
		return DefaultRootMenu()
	}
	return f.root
}
func (f fakeMenu) TrackDetail() string { return f.detail }

// rootLabels asks for the root menu with the given request arguments after
// the context, and renders it.
func rootLabels(t *testing.T, h *Handler, args ...uint32) []string {
	t.Helper()
	msg := &proto.DBMessage{TxID: 1, Type: 0x1000, Args: []proto.DBArg{proto.ArgI32(rootCtx)}}
	for _, a := range args {
		msg.Args = append(msg.Args, proto.ArgI32(a))
	}
	total := h.Handle(msg)[0].Args[1].Int()
	page := h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(rootCtx), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0), proto.ArgI32(total), proto.ArgI32(0)}})
	var out []string
	for _, m := range page[1 : len(page)-1] {
		out = append(out, strings.Trim(m.Args[3].Str, "\ufffa\ufffb"))
	}
	return out
}

// The root menu keeps the categories the deck's capability mask admits: bit
// (menu item - 1), DATE ADDED bit 24. A CDJ-2000NXS2's mask drops DATE ADDED
// but keeps DJ PLAY COUNT; the old 0x00ffffff mask brings HOT CUE BANK back;
// a request without a mask keeps everything.
func TestRootCapabilityMask(t *testing.T) {
	root := []RootMenuEntry{
		{ID: 4, Label: "TRACK", ItemType: 0x83},
		{ID: 24, Label: "DATE ADDED", ItemType: 0x8c},
		{ID: 25, Label: "DJ PLAY COUNT", ItemType: 0x97},
		{ID: 26, Label: "MATCHING", ItemType: 0xaa},
	}
	h := &Handler{menu: fakeMenu{root: root}}
	for _, c := range []struct {
		name string
		args []uint32
		want []string
	}{
		{"CDJ-2000NXS2", []uint32{0, 0x04ffffff}, []string{"TRACK", "DJ PLAY COUNT", "MATCHING"}},
		{"XDJ-RX3", []uint32{0, 0x05fdffff}, []string{"TRACK", "DATE ADDED", "DJ PLAY COUNT", "MATCHING"}},
		{"TRACK only", []uint32{0, 0x00000008}, []string{"TRACK"}},
		{"nothing", []uint32{0, 0}, nil},
		{"legacy", []uint32{0, legacyRootMask}, []string{"TRACK", "DJ PLAY COUNT", "HOT CUE BANK"}},
		{"no mask", []uint32{0}, []string{"TRACK", "DATE ADDED", "DJ PLAY COUNT", "MATCHING"}},
	} {
		if got := rootLabels(t, h, c.args...); !slices.Equal(got, c.want) {
			t.Errorf("%s: root %q, want %q", c.name, got, c.want)
		}
	}
}

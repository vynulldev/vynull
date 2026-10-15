// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"path/filepath"
	"testing"
)

// A master.db dump's Hot Cue Banks are imported with the top-level "root"
// parent mapped to 0 and ContentIDs resolved to library tracks, and they
// survive a reload from disk.
func TestImportHotCueBanks(t *testing.T) {
	dir := t.TempDir()
	lib := New()
	lib.SetDBPath(filepath.Join(dir, "library.json"))
	dump := &MasterDBDump{
		Tracks: []masterDBTrack{{ID: "10001", Title: "One", FilePath: "/music/one.mp3"}},
		HotCueBanks: []masterDBHotCueBank{
			{ID: "9001", ParentID: "root", Seq: 1, Name: "Folder", Folder: true},
			{ID: "9012", ParentID: "9001", Seq: 1, Name: "Bank"},
		},
		HotCueBankMembers: []masterDBHotCueBankMember{
			{BankID: "9012", ContentID: "10001", TrackNo: 1, InMsec: 1500, OutMsec: -1, InMpegAbs: -1, ColorTableIndex: 0x24},
			{BankID: "9012", ContentID: "999999", TrackNo: 2},
		},
	}
	if _, err := ImportMasterDBDump(lib, dump); err != nil {
		t.Fatal(err)
	}

	check := func(lib *Library) {
		t.Helper()
		if top := lib.HotCueBankChildren(0); len(top) != 1 || top[0].ID != 9001 || !top[0].Folder {
			t.Fatalf("top level = %+v, want Folder 9001", top)
		}
		members := lib.HotCueBankMembers(9012)
		if len(members) != 2 {
			t.Fatalf("members = %+v, want 2", members)
		}
		one := lib.TrackByPath("/music/one.mp3")
		if members[0].TrackID != one.ID || members[0].OutMsec != -1 || members[0].InMpegAbs != 0xffffffff || members[0].ColorTableIndex != 0x24 {
			t.Errorf("member 1 = %+v, want track %d, OutMsec -1, InMpegAbs 0xffffffff, ColorTableIndex 0x24", members[0], one.ID)
		}
		if members[1].TrackID != 0 || members[1].ContentID != 999999 {
			t.Errorf("dangling member = %+v, want TrackID 0, ContentID 999999", members[1])
		}
	}
	check(lib)

	reloaded := New()
	reloaded.SetDBPath(filepath.Join(dir, "library.json"))
	check(reloaded)
}

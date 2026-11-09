// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"testing"
	"time"

	"github.com/vynulldev/vynull/api"
	"github.com/vynulldev/vynull/pdb"
)

// TestHistoryPlaylistAppend verifies the auto-history hook:
// first append creates the History folder + today's playlist and
// adds the track; second append for the same track is a no-op
// (consecutive-dup guard); a different track appends; and a
// different date opens a new playlist under the same folder.
func TestHistoryPlaylistAppend(t *testing.T) {
	dir := t.TempDir()
	ps := api.NewPlaylistStore(dir)

	day1 := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)

	appendToHistoryPlaylist(ps, 42, day1)

	// History folder created.
	all := ps.All()
	var folder *api.PlaylistInfo
	for _, p := range all {
		if p.IsFolder && p.Name == historyFolderName && p.ParentID == 0 {
			folder = p
			break
		}
	}
	if folder == nil {
		t.Fatal("History folder not created")
	}

	// Today's playlist created under it.
	wantName := "History · 2026-05-20"
	var pl *api.PlaylistInfo
	for _, p := range all {
		if p.ParentID == folder.ID && p.Name == wantName {
			pl = p
			break
		}
	}
	if pl == nil {
		t.Fatalf("playlist %q not created", wantName)
	}
	if got := ps.Tracks(pl.ID); len(got) != 1 || got[0] != 42 {
		t.Fatalf("first append wrong tracks: %v", got)
	}

	// Same track again — skipped (consecutive dup).
	appendToHistoryPlaylist(ps, 42, day1)
	if got := ps.Tracks(pl.ID); len(got) != 1 {
		t.Fatalf("dup not skipped, got %v", got)
	}

	// Different track — appended.
	appendToHistoryPlaylist(ps, 99, day1)
	if got := ps.Tracks(pl.ID); len(got) != 2 || got[1] != 99 {
		t.Fatalf("second track wrong: %v", got)
	}

	// Same track now allowed (not consecutive anymore).
	appendToHistoryPlaylist(ps, 42, day1)
	if got := ps.Tracks(pl.ID); len(got) != 3 || got[2] != 42 {
		t.Fatalf("non-consecutive 42 should append: %v", got)
	}

	// New day → new playlist under the same folder.
	appendToHistoryPlaylist(ps, 7, day2)
	all = ps.All()
	day2Count := 0
	for _, p := range all {
		if p.ParentID == folder.ID && p.Name == "History · 2026-05-21" {
			day2Count++
		}
	}
	if day2Count != 1 {
		t.Fatalf("want 1 day-2 playlist, got %d", day2Count)
	}
}

// A deck's "make a playlist of the TAG LIST" lands in the playlist store as a
// regular playlist of the list's tracks, in order.
func TestPlaylistSourceCreatePlaylist(t *testing.T) {
	ps := api.NewPlaylistStore(t.TempDir())
	src := playlistSource{ps: ps}
	if err := src.CreatePlaylist("TAG LIST", 0, []uint32{3, 1, 2}); err != nil {
		t.Fatal(err)
	}
	all := ps.All()
	if len(all) != 1 || all[0].Name != "TAG LIST" || all[0].IsFolder || all[0].IsSmart || all[0].ParentID != 0 {
		t.Fatalf("playlists %+v, want one top-level TAG LIST playlist", all)
	}
	if got := ps.Tracks(all[0].ID); !slices.Equal(got, []uint32{3, 1, 2}) {
		t.Errorf("tracks %v, want [3 1 2]", got)
	}
	if err := src.CreatePlaylist("x", 999, nil); err == nil {
		t.Error("a missing parent folder made a playlist")
	}
}

// FOLDER stays in a USB export's menu, but isn't served in the root menu
// over the link: a deck opens that category with the TAG LIST's request.
func TestFolderIsExportOnly(t *testing.T) {
	ms := api.NewMenuStore(t.TempDir())
	for _, e := range (menuSource{ms: ms}).RootMenu() {
		if e.Label == "FOLDER" {
			t.Fatalf("root menu over the link has FOLDER: %+v", e)
		}
	}
	if cfg := ms.PDBMenuConfig(); !slices.Contains(cfg.Visible, pdb.MenuCategoryByKey["folder"]) {
		t.Errorf("USB export menu %+v lost FOLDER", cfg)
	}
}

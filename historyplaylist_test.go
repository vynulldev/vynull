// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"
	"time"

	"vynull/api"
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

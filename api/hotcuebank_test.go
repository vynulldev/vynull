// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/library"
)

// GET /api/hotcuebanks lists the live tree with each bank's track count, and
// GET /api/hotcuebanks/{id}/tracks lists a bank's tracks as decks see them:
// membership order, duplicates kept, unresolved content left out, loop end
// only for loops.
func TestHotCueBankAPI(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, Title: "One", Artist: "A"})
	lib.AddTrack(&library.Track{ID: 2, Title: "Two", Artist: "B"})
	lib.AddTrack(&library.Track{ID: 3, Title: "Gone", FileMissing: true})
	lib.SetHotCueBanks([]library.HotCueBank{
		{ID: 10, Seq: 1, Name: "Folder", Folder: true},
		{ID: 11, ParentID: 10, Seq: 1, Name: "Bank"},
		{ID: 12, ParentID: 10, Seq: 2, Name: "Gone", Deleted: true},
	}, []library.HotCueBankMember{
		{BankID: 11, TrackID: 2, TrackNo: 2, InMsec: 2000, OutMsec: 4000, ColorTableIndex: 0x2a},
		{BankID: 11, TrackID: 1, TrackNo: 1, InMsec: 1000, OutMsec: -1, Comment: "drop"},
		{BankID: 11, ContentID: 999, TrackNo: 3},
		{BankID: 11, TrackID: 1, TrackNo: 4, OutMsec: -1},
		{BankID: 11, TrackID: 3, TrackNo: 5, OutMsec: -1}, // file missing: listed, not counted
	})
	h := (&Server{Library: lib}).Handler()
	get := func(path string, v any) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		return w.Code
	}

	var nodes []HotCueBankNode
	if code := get("/api/hotcuebanks", &nodes); code != 200 || len(nodes) != 2 {
		t.Fatalf("tree: %d %+v, want the folder and the live bank", code, nodes)
	}
	for _, n := range nodes {
		if n.ID == 11 && n.TrackCount != 3 {
			t.Errorf("bank track_count = %d, want 3 (the missing file isn't sent to decks)", n.TrackCount)
		}
	}

	var entries []HotCueBankEntry
	if code := get("/api/hotcuebanks/11/tracks", &entries); code != 200 {
		t.Fatalf("tracks: %d", code)
	}
	var got []int32
	for _, e := range entries {
		got = append(got, e.Position)
	}
	if len(entries) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 4 || got[3] != 5 {
		t.Fatalf("positions = %v, want [1 2 4 5]", got)
	}
	if !entries[3].Track.FileMissing {
		t.Errorf("entry 5 should be flagged file_missing")
	}
	if entries[0].Track.Title != "One" || entries[0].OutMs != nil || entries[0].Comment != "drop" {
		t.Errorf("entry 1 = %+v, want One, no loop, comment drop", entries[0])
	}
	if entries[1].OutMs == nil || *entries[1].OutMs != 4000 {
		t.Errorf("entry 2 loop end = %v, want 4000", entries[1].OutMs)
	}
	if entries[1].ColorID != 0x2a || entries[0].ColorID != 0 {
		t.Errorf("color_id = %#x, %#x, want 0x2a and 0 (unset)", entries[1].ColorID, entries[0].ColorID)
	}

	if code := get("/api/hotcuebanks/10/tracks", &entries); code != 404 {
		t.Errorf("folder tracks: %d, want 404", code)
	}
	if code := get("/api/hotcuebanks/99/tracks", &entries); code != 404 {
		t.Errorf("unknown bank: %d, want 404", code)
	}
}

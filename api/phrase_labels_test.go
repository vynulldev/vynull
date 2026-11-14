// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// The phrases API names a phrase by rekordbox's mood and variant, and gives
// the section UIs colour it by; Vynull's own phrases keep the plain names.
func TestPhrasesNameRekordboxLabels(t *testing.T) {
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, FilePath: "/music/one.flac"})
	lib.AddTrack(&library.Track{ID: 2, FilePath: "/music/two.flac"})
	store := analysis.NewStore()
	store.Set(1, &analysis.Result{BPM: 120, Imported: true, Phrases: []analysis.Phrase{
		{StartBeat: 1, EndBeat: 33, Kind: 1, Mood: 2, EndMs: 16000},
		{StartBeat: 33, EndBeat: 65, Kind: 3, Mood: 2, StartMs: 16000, EndMs: 32000},
		{StartBeat: 65, EndBeat: 97, Kind: 8, Mood: 2, StartMs: 32000, EndMs: 48000},
	}})
	store.Set(2, &analysis.Result{BPM: 120, Phrases: []analysis.Phrase{
		{StartBeat: 1, EndBeat: 33, Kind: 5, EndMs: 16000},
	}})
	s := &Server{Library: lib, Analysis: store}
	for id, want := range map[int]string{
		1: "INTRO intro 2, VERSE 2 verse 2, BRIDGE bridge 2",
		2: "CHORUS chorus 0",
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/api/tracks/%d/phrases", id), nil))
		var phrases []struct {
			Name, Section string
			Mood          int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &phrases); err != nil {
			t.Fatalf("track %d: %v %s", id, err, w.Body)
		}
		got := ""
		for i, p := range phrases {
			if i > 0 {
				got += ", "
			}
			got += fmt.Sprintf("%s %s %d", p.Name, p.Section, p.Mood)
		}
		if got != want {
			t.Errorf("track %d: %s, want %s", id, got, want)
		}
	}
}

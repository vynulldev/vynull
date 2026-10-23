// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// A beat-grid edit stores an edited copy: a result already handed to a reader
// (a deck being served its grid) keeps the grid it had, rather than changing
// under it part-way through the edit.
func TestGridEditLeavesTheServedResultAlone(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"shift", `{"track_id":1,"offset_ms":10}`},
		{"bpm", `{"track_id":1,"bpm":125}`},
		{"downbeat", `{"track_id":1,"set_downbeat_ms":1100}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib := library.New()
			lib.AddTrack(&library.Track{ID: 1, Title: "One", FilePath: "/music/one.flac"})
			store := analysis.NewStore()
			beats := []float64{100, 600, 1100, 1600, 2100, 2600}
			store.Set(1, &analysis.Result{BPM: 120, Duration: 3, Beats: slices.Clone(beats),
				Phrases: []analysis.Phrase{{StartBeat: 1, EndBeat: 4}}})
			srv := &Server{Library: lib, Analysis: store}

			served := store.Get(1) // what a deck is reading
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/analysis/beatgrid/adjust", strings.NewReader(tc.body)))
			if w.Code != http.StatusOK {
				t.Fatalf("adjust: %d %s", w.Code, w.Body)
			}
			if !slices.Equal(served.Beats, beats) || served.BPM != 120 || served.GridEdited || served.Phrases[0].EndMs != 0 {
				t.Errorf("the served result changed: BPM %v, beats %v, edited %v, phrase %+v",
					served.BPM, served.Beats, served.GridEdited, served.Phrases[0])
			}
			got := store.Get(1)
			if got == served || !got.GridEdited {
				t.Fatalf("the edit wasn't stored: %+v", got)
			}
			if tc.name == "downbeat" && got.DownbeatIndex != 2 {
				t.Errorf("downbeat index %d, want 2 (the beat at 1100 ms)", got.DownbeatIndex)
			}
		})
	}
}

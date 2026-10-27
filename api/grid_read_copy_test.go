// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

func TestReadingTempoOverrideDoesNotChangeStoredGrid(t *testing.T) {
	original := []float64{125, 625, 1135, 1660, 2185, 2715}
	stored := &analysis.Result{BPM: 120, Duration: 3, Beats: append([]float64(nil), original...)}
	lib := library.New()
	lib.AddTrack(&library.Track{ID: 1, BPM: 60, DetectedBPM: 120})
	store := analysis.NewStore()
	store.Set(1, stored)
	server := &Server{Library: lib, Analysis: store}
	for range 3 {
		response := httptest.NewRecorder()
		server.handleAnalysis(response, httptest.NewRequest("GET", "/api/analysis/1", nil))
		var got struct {
			BPM   float64
			Beats []struct {
				Time float64 `json:"time_ms"`
			}
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &got) != nil {
			t.Fatalf("response: %d %s", response.Code, response.Body)
		}
		if got.BPM != 60 || len(got.Beats) != 3 || got.Beats[1].Time != 1125 {
			t.Fatalf("override: %+v", got)
		}
		if !reflect.DeepEqual(stored.Beats, original) || !reflect.DeepEqual(store.Get(1).Beats, original) {
			t.Fatalf("read mutated cached grid: %v", stored.Beats)
		}
	}
}

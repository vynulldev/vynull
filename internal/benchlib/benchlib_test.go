// SPDX-License-Identifier: GPL-3.0-or-later

package benchlib

import (
	"reflect"
	"testing"
)

// Benchmarks compare runs, so the library must be the same every time, and
// it must have the spread the benchmarks rely on.
func TestTracksDeterministicWithSpread(t *testing.T) {
	a, b := Tracks(2000), Tracks(2000)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Tracks differs between calls")
	}
	artists, types, cues := map[string]bool{}, map[string]bool{}, 0
	for i, tr := range a {
		if tr.ID != uint32(i+1) {
			t.Fatalf("track %d has ID %d", i, tr.ID)
		}
		artists[tr.Artist] = true
		types[tr.FileType] = true
		if !reflect.DeepEqual(Cues(tr), Cues(tr)) {
			t.Fatalf("Cues(%d) differs between calls", tr.ID)
		}
		cues += len(Cues(tr))
	}
	if len(artists) < 150 || len(types) != len(fileTypes) {
		t.Errorf("%d artists, %d file types; want a spread", len(artists), len(types))
	}
	if avg := float64(cues) / float64(len(a)); avg < 4 || avg > 8 {
		t.Errorf("%.1f cues per track, want about six", avg)
	}
}

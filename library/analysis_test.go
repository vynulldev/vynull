// SPDX-License-Identifier: GPL-3.0-or-later

package library_test

import (
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// ApplyAnalysis fills empty fields, lets BPM follow a re-analysis unless the
// user set it, estimates the bitrate, and saves only when something changed.
func TestApplyAnalysis(t *testing.T) {
	lib := newLib(
		&library.Track{ID: 1, FileSize: 8_000_000},
		&library.Track{ID: 2, BPM: 120, Key: "8A", Duration: library.DurationSec(200 * time.Second)},
		&library.Track{ID: 3, BPM: 126, DetectedBPM: 120},
		&library.Track{ID: 4, FileSize: 1000, Duration: library.DurationSec(500 * time.Millisecond)},
	)
	facts := library.AnalysisFacts{BPM: 121, Duration: 320, KeyCamelot: "11A"}

	rev := lib.Rev()
	if !lib.ApplyAnalysis(1, facts) || lib.Rev() == rev {
		t.Fatal("filling an empty track didn't change and save it")
	}
	if tr := lib.Track(1); tr.BPM != 121 || tr.Duration.Duration() != 320*time.Second || tr.Key != "11A" || tr.Bitrate != 200 {
		t.Errorf("track 1: BPM %v duration %v key %q bitrate %d; want 121, 5m20s, 11A, 200", tr.BPM, tr.Duration.Duration(), tr.Key, tr.Bitrate)
	}

	lib.ApplyAnalysis(2, facts)
	if tr := lib.Track(2); tr.BPM != 121 || tr.Key != "8A" || tr.Duration.Duration() != 200*time.Second {
		t.Errorf("track 2: BPM %v key %q duration %v; want the BPM to follow the analysis, key and duration kept", tr.BPM, tr.Key, tr.Duration.Duration())
	}
	lib.ApplyAnalysis(3, facts)
	if lib.Track(3).BPM != 126 {
		t.Errorf("track 3: a user-set BPM was replaced with %v", lib.Track(3).BPM)
	}

	rev = lib.Rev()
	if lib.ApplyAnalysis(1, facts) || lib.Rev() != rev {
		t.Error("applying the same analysis again changed or saved the track")
	}
	if lib.ApplyAnalysis(4, library.AnalysisFacts{}) || lib.Track(4).Bitrate != 0 {
		t.Error("a track under a second long got a bitrate estimate")
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Real-audio diagnostic harnesses for beat/phrase detection, env-gated so
// machines without (private, copyrighted) audio still pass. Moved here from
// link/prolink, where the part-3c encoder relocation had stranded them —
// their subject is the detector, not the wire encoders (those dumps live in
// link/prolink/encoders_realaudio_test.go).

// TestDetectBeats runs beat detection across every audio file in a
// directory and sanity-checks that a BPM comes out.
//
//	TEST_MUSIC_DIR=/path/to/tracks go test ./analysis/ -run TestDetectBeats -v
func TestDetectBeats(t *testing.T) {
	dir := os.Getenv("TEST_MUSIC_DIR")
	if dir == "" {
		t.Skip("Set TEST_MUSIC_DIR to run BPM detection tests")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(e.Name()) {
		case ".mp3", ".m4a", ".flac", ".wav", ".aiff", ".aif":
		default:
			continue
		}

		path := filepath.Join(dir, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			samples, err := DecodePCM(path, AnalysisRate)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			result := DetectBeatsWithEncoderDelay(samples, AnalysisRate, EncoderDelayMs(path))
			dur := float64(len(samples)) / float64(AnalysisRate)

			t.Logf("BPM=%.2f  beats=%d  downbeat=%.1fms  duration=%.1fs",
				result.BPM, len(result.Beats), result.Downbeat, dur)

			if result.BPM == 0 {
				t.Error("BPM detection failed (returned 0)")
			}
		})
	}
}

// TestDetectBeatsSingle analyzes one file (TEST_AUDIO_FILE) and prints the
// detection + phrase breakdown. Uses the format-aware encoder delay — the
// old harness called DetectBeats (the lossy default), silently shifting
// lossless files' grids by the 25ms MP3 encoder-delay compensation.
func TestDetectBeatsSingle(t *testing.T) {
	path := os.Getenv("TEST_AUDIO_FILE")
	if path == "" {
		t.Skip("Set TEST_AUDIO_FILE to run single-file BPM test")
	}

	samples, err := DecodePCM(path, AnalysisRate)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	result := DetectBeatsWithEncoderDelay(samples, AnalysisRate, EncoderDelayMs(path))
	dur := float64(len(samples)) / float64(AnalysisRate)

	fmt.Printf("File: %s\n", filepath.Base(path))
	fmt.Printf("Duration: %.1fs\n", dur)
	fmt.Printf("BPM: %.2f\n", result.BPM)
	fmt.Printf("Beats: %d\n", len(result.Beats))
	fmt.Printf("Downbeat: %.1fms\n", result.Downbeat)

	if len(result.Beats) > 8 {
		fmt.Printf("First 8 beats (ms): ")
		for i := 0; i < 8; i++ {
			fmt.Printf("%.1f ", result.Beats[i])
		}
		fmt.Println()

		fmt.Printf("IBIs (ms): ")
		for i := 1; i < 8 && i < len(result.Beats); i++ {
			fmt.Printf("%.1f ", result.Beats[i]-result.Beats[i-1])
		}
		fmt.Println()
	}

	phrases := DetectPhrases(samples, AnalysisRate, result.BPM, result.Downbeat)
	fmt.Printf("\nPhrases: %d\n", len(phrases))
	phraseNames := map[uint16]string{
		1: "Intro", 2: "Up", 3: "Down", 5: "Chorus", 6: "Outro",
	}
	for i, p := range phrases {
		name := phraseNames[p.Kind]
		if name == "" {
			name = fmt.Sprintf("Unknown(%d)", p.Kind)
		}
		startSec := float64(p.StartBeat-1) * 60.0 / result.BPM
		endSec := float64(p.EndBeat) * 60.0 / result.BPM
		fmt.Printf("  %2d. %-8s  beats %4d-%4d  (%.1fs - %.1fs)  energy=%.3f\n",
			i+1, name, p.StartBeat, p.EndBeat, startSec, endSec, p.Energy)
	}

	if result.BPM == 0 {
		t.Error("BPM detection failed")
	}
}

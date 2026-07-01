// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectBeats(t *testing.T) {
	// Scan for audio files in a test directory or use env var.
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
		ext := filepath.Ext(e.Name())
		switch ext {
		case ".mp3", ".m4a", ".flac", ".wav", ".aiff", ".aif":
		default:
			continue
		}

		path := filepath.Join(dir, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			samples, err := DecodePCM(path, analysisRate)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			result := DetectBeats(samples, analysisRate)
			dur := float64(len(samples)) / float64(analysisRate)

			t.Logf("BPM=%.2f  beats=%d  downbeat=%.1fms  duration=%.1fs",
				result.BPM, len(result.Beats), result.Downbeat, dur)

			if result.BPM == 0 {
				t.Error("BPM detection failed (returned 0)")
			}
		})
	}
}

// TestDetectBeatsSingle analyzes a single file specified by TEST_AUDIO_FILE.
func TestDetectBeatsSingle(t *testing.T) {
	path := os.Getenv("TEST_AUDIO_FILE")
	if path == "" {
		t.Skip("Set TEST_AUDIO_FILE to run single-file BPM test")
	}

	samples, err := DecodePCM(path, analysisRate)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	result := DetectBeats(samples, analysisRate)
	dur := float64(len(samples)) / float64(analysisRate)

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

		// Show inter-beat intervals for first 8 beats.
		fmt.Printf("IBIs (ms): ")
		for i := 1; i < 8 && i < len(result.Beats); i++ {
			fmt.Printf("%.1f ", result.Beats[i]-result.Beats[i-1])
		}
		fmt.Println()
	}

	// Phrase detection.
	phrases := DetectPhrases(samples, analysisRate, result.BPM, result.Downbeat)
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

	// Generate PSSI and show raw size.
	pssi := GeneratePSSI(phrases, result.BPM)
	if pssi != nil {
		fmt.Printf("\nPSSI blob: %d bytes\n", len(pssi))
	} else {
		fmt.Printf("\nPSSI: nil (no phrases detected)\n")
	}

	// Beat grid debug output.
	if len(result.Beats) > 0 {
		beatGrid := GenerateBeatGridFromBeats(result)
		fmt.Printf("\nBeat Grid (0x2204 format): %d bytes\n", len(beatGrid))
		if len(beatGrid) >= 20 {
			numBeats := int(binary.LittleEndian.Uint32(beatGrid[4:8]))
			fmt.Printf("  Preamble: numBeats=%d\n", numBeats)
			fmt.Printf("  Beat  Bar  BPM      Time(ms)   Time(s)\n")
			shown := 0
			for i := 0; i < numBeats && shown < 20; i++ {
				off := 20 + i*16
				if off+8 > len(beatGrid) {
					break
				}
				beatNum := binary.LittleEndian.Uint16(beatGrid[off:])
				tempo := binary.LittleEndian.Uint16(beatGrid[off+2:])
				timeMs := binary.LittleEndian.Uint32(beatGrid[off+4:])
				fmt.Printf("  %4d  %d    %6.2f   %8d   %6.2f\n",
					i+1, beatNum, float64(tempo)/100, timeMs, float64(timeMs)/1000)
				shown++
			}
			if numBeats > 20 {
				fmt.Printf("  ... (%d more beats)\n", numBeats-20)
			}
		}

		pqt2 := GeneratePQT2(result.BPM, result.Beats, 0)
		if pqt2 != nil {
			fmt.Printf("\nPQT2 blob: %d bytes\n", len(pqt2))
			// Decode PQT2 header
			if len(pqt2) >= 60 {
				off := 4 // skip LE prefix
				hdrLen := binary.BigEndian.Uint32(pqt2[off+4:])
				tagLen := binary.BigEndian.Uint32(pqt2[off+8:])
				entryCount := binary.BigEndian.Uint32(pqt2[off+40:])
				firstBeat := binary.BigEndian.Uint16(pqt2[off+24:])
				firstTempo := binary.BigEndian.Uint16(pqt2[off+26:])
				firstTime := binary.BigEndian.Uint32(pqt2[off+28:])
				lastBeat := binary.BigEndian.Uint16(pqt2[off+32:])
				lastTempo := binary.BigEndian.Uint16(pqt2[off+34:])
				lastTime := binary.BigEndian.Uint32(pqt2[off+36:])
				fmt.Printf("  Header: hdr=%d tag=%d entries=%d\n", hdrLen, tagLen, entryCount)
				fmt.Printf("  First beat: num=%d bpm=%.2f time=%dms\n",
					firstBeat, float64(firstTempo)/100, firstTime)
				fmt.Printf("  Last beat:  num=%d bpm=%.2f time=%dms (%.1fs)\n",
					lastBeat, float64(lastTempo)/100, lastTime, float64(lastTime)/1000)
				// Show first 10 entries
				dataOff := off + int(hdrLen)
				fmt.Printf("  Entries (beat_time_ms %% 1000):\n  ")
				for i := 0; i < int(entryCount) && i < 20; i++ {
					if dataOff+i*2+2 > len(pqt2) {
						break
					}
					val := binary.BigEndian.Uint16(pqt2[dataOff+i*2:])
					fmt.Printf("%4d ", val)
					if (i+1)%10 == 0 {
						fmt.Printf("\n  ")
					}
				}
				if entryCount > 20 {
					fmt.Printf("... (%d more)", entryCount-20)
				}
				fmt.Println()
			}
		}
	}

	if result.BPM == 0 {
		t.Error("BPM detection failed")
	}
}

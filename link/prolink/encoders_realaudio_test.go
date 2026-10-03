// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// TestEncodersRealAudio decodes one real file (TEST_AUDIO_FILE), runs
// detection, and dumps the wire encoders' output — PSSI, the 0x2204 beat
// grid, and PQT2 — with decoded headers/entries for eyeballing against
// captures. The detection-side diagnostics live in
// analysis/realaudio_test.go; this file is only about the encoder bytes.
func TestEncodersRealAudio(t *testing.T) {
	path := os.Getenv("TEST_AUDIO_FILE")
	if path == "" {
		t.Skip("Set TEST_AUDIO_FILE to run the encoder dump")
	}

	samples, err := analysis.DecodePCM(path, analysis.AnalysisRate)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	result := analysis.DetectBeatsWithEncoderDelay(samples, analysis.AnalysisRate, analysis.EncoderDelayMs(path))
	phrases := analysis.DetectPhrases(samples, analysis.AnalysisRate, result.BPM, result.Downbeat)

	pssi := GeneratePSSI(phrases, result.BPM)
	if pssi != nil {
		fmt.Printf("PSSI blob: %d bytes\n", len(pssi))
	} else {
		fmt.Printf("PSSI: nil (no phrases detected)\n")
	}

	if len(result.Beats) == 0 {
		t.Fatal("no beats detected")
	}

	beatGrid := analysis.GenerateBeatGridFromBeats(result)
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

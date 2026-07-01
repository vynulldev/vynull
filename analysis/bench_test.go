// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAnalysisTiming(t *testing.T) {
	path := os.Getenv("TEST_AUDIO_FILE")
	if path == "" {
		t.Skip("Set TEST_AUDIO_FILE to run analysis timing")
	}

	if os.Getenv("TEST_GPU") == "1" {
		EnableGPU(true)
		t.Logf("GPU mode: %s (available=%v, enabled=%v)", gpuInfo(), HasGPU(), UseGPU())
	} else {
		t.Logf("CPU mode (set TEST_GPU=1 to test GPU)")
	}

	t0 := time.Now()
	samples, err := DecodePCM(path, analysisRate)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decodeTime := time.Since(t0)
	dur := float64(len(samples)) / float64(analysisRate)

	fmt.Printf("File: %s (%.1fs audio, %d samples)\n\n", path, dur, len(samples))
	fmt.Printf("%-25s %10s\n", "Stage", "Time")
	fmt.Printf("%-25s %10s\n", "-----", "----")
	fmt.Printf("%-25s %10s\n", "Decode PCM", decodeTime.Round(time.Millisecond))

	t0 = time.Now()
	DetectBeats(samples, analysisRate)
	bpmTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "BPM/Beat Detection", bpmTime.Round(time.Millisecond))

	t0 = time.Now()
	DetectKey(samples, analysisRate)
	keyTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Key Detection", keyTime.Round(time.Millisecond))

	t0 = time.Now()
	GeneratePreview(samples, analysisRate)
	previewTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Preview (PWAV)", previewTime.Round(time.Millisecond))

	t0 = time.Now()
	GeneratePreviewANLZ(samples, analysisRate)
	previewANLZTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Preview ANLZ", previewANLZTime.Round(time.Millisecond))

	t0 = time.Now()
	GenerateColorPreview(samples, analysisRate)
	colorPreviewTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Color Preview (PWV4)", colorPreviewTime.Round(time.Millisecond))

	t0 = time.Now()
	detail := GenerateDetail(samples, analysisRate)
	detailTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Detail Waveform (PWV5)", detailTime.Round(time.Millisecond))

	t0 = time.Now()
	GenerateDetailMono(detail)
	monoTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Detail Mono", monoTime.Round(time.Millisecond))

	t0 = time.Now()
	ExtractArtwork(path)
	artTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Artwork Extract", artTime.Round(time.Millisecond))

	total := decodeTime + bpmTime + keyTime + previewTime + previewANLZTime + colorPreviewTime + detailTime + monoTime + artTime
	fmt.Printf("%-25s %10s\n", "-----", "----")
	fmt.Printf("%-25s %10s\n", "TOTAL", total.Round(time.Millisecond))

	fmt.Printf("\nBreakdown:\n")
	stages := []struct {
		name string
		dur  time.Duration
	}{
		{"Decode", decodeTime},
		{"BPM/Beats", bpmTime},
		{"Key", keyTime},
		{"PWV4", colorPreviewTime},
		{"PWV5", detailTime},
		{"Other", previewTime + previewANLZTime + monoTime + artTime},
	}
	for _, s := range stages {
		pct := float64(s.dur) / float64(total) * 100
		fmt.Printf("  %-15s %8s  %5.1f%%\n", s.name, s.dur.Round(time.Millisecond), pct)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vynulldev/vynull/analysis"
)

func TestAnalysisTiming(t *testing.T) {
	path := os.Getenv("TEST_AUDIO_FILE")
	if path == "" {
		t.Skip("Set TEST_AUDIO_FILE to run analysis timing")
	}

	t0 := time.Now()
	samples, err := analysis.DecodePCM(path, analysis.AnalysisRate)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decodeTime := time.Since(t0)
	dur := float64(len(samples)) / float64(analysis.AnalysisRate)

	fmt.Printf("File: %s (%.1fs audio, %d samples)\n\n", path, dur, len(samples))
	fmt.Printf("%-25s %10s\n", "Stage", "Time")
	fmt.Printf("%-25s %10s\n", "-----", "----")
	fmt.Printf("%-25s %10s\n", "Decode PCM", decodeTime.Round(time.Millisecond))

	t0 = time.Now()
	analysis.DetectBeats(samples, analysis.AnalysisRate)
	bpmTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "BPM/Beat Detection", bpmTime.Round(time.Millisecond))

	t0 = time.Now()
	analysis.DetectKey(samples, analysis.AnalysisRate)
	keyTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Key Detection", keyTime.Round(time.Millisecond))

	t0 = time.Now()
	GeneratePreview(samples, analysis.AnalysisRate)
	previewTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Preview (PWAV)", previewTime.Round(time.Millisecond))

	t0 = time.Now()
	GeneratePreviewANLZ(samples, analysis.AnalysisRate)
	previewANLZTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Preview ANLZ", previewANLZTime.Round(time.Millisecond))

	t0 = time.Now()
	GenerateColorPreview(samples, analysis.AnalysisRate)
	colorPreviewTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Color Preview (PWV4)", colorPreviewTime.Round(time.Millisecond))

	t0 = time.Now()
	detail := GenerateDetail(samples, analysis.AnalysisRate)
	detailTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Detail Waveform (PWV5)", detailTime.Round(time.Millisecond))

	t0 = time.Now()
	GenerateDetailMono(detail)
	monoTime := time.Since(t0)
	fmt.Printf("%-25s %10s\n", "Detail Mono", monoTime.Round(time.Millisecond))

	t0 = time.Now()
	analysis.ExtractArtwork(path)
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

// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"vynull/analysis"
)

func main() {
	outDir := flag.String("out", ".", "output directory for PNG files")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: waveform [--out dir] <audio-file> [audio-file...]\n")
		os.Exit(1)
	}

	for _, file := range flag.Args() {
		base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		log.Printf("analyzing %s...", filepath.Base(file))

		result, err := analysis.AnalyzeTrack(file)
		if err != nil {
			log.Printf("error: %v", err)
			continue
		}

		log.Printf("  BPM: %.1f", result.BPM)
		log.Printf("  preview: %d bytes, color: %d bytes, detail: %d bytes",
			len(result.WavePreview), len(result.WaveColorPreview), len(result.WaveDetail))

		if err := analysis.RenderPreviewPNG(result.WavePreview,
			filepath.Join(*outDir, base+"-preview.png")); err != nil {
			log.Printf("  preview png: %v", err)
		} else {
			log.Printf("  wrote %s-preview.png", base)
		}

		if err := analysis.RenderColorPreviewPNG(result.WaveColorPreview,
			filepath.Join(*outDir, base+"-color.png")); err != nil {
			log.Printf("  color png: %v", err)
		} else {
			log.Printf("  wrote %s-color.png", base)
		}

		if err := analysis.RenderDetailPNG(result.WaveDetail,
			filepath.Join(*outDir, base+"-detail.png")); err != nil {
			log.Printf("  detail png: %v", err)
		} else {
			log.Printf("  wrote %s-detail.png", base)
		}
	}
}

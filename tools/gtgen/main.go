// SPDX-License-Identifier: GPL-3.0-or-later

// Command gtgen builds a beat-grid reference manifest from a rekordbox library
// export, for validating our beat detector against rekordbox's own grids
// (analysis.TestBeatGridAccuracy, VYNULL_BEAT_REF).
//
// It joins a master.db dump (from tools/rekordbox_dump.py: each track's
// file_path, bpm, analyze_path) with the export's ANLZ .DAT (the PQTZ beat
// grid). For each track whose audio exists locally (after remapping the stored
// path prefix), it emits {file, bpm, first_beat_ms, title}.
//
// Usage:
//
//	python3 tools/rekordbox_dump.py master.db <key> > dump.json
//	go run ./tools/gtgen -dump dump.json -zip export.zip \
//	  -from "Z:/Music/" -to "/run/media/tj/Data/Music/" -out beat_gt.json
package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vynulldev/vynull/analysis"
)

type dumpFile struct {
	Tracks []struct {
		Title       string  `json:"title"`
		FilePath    string  `json:"file_path"`
		FileName    string  `json:"file_name"`
		BPM         float64 `json:"bpm"`
		AnalyzePath string  `json:"analyze_path"`
	} `json:"tracks"`
}

type gtEntry struct {
	File        string    `json:"file"`
	BPM         float64   `json:"bpm"`
	FirstBeatMs float64   `json:"first_beat_ms"`
	Title       string    `json:"title"`
	RBBeats     []float64 `json:"rb_beats,omitempty"` // full rekordbox PQTZ grid (with -beats)
}

func main() {
	dumpPath := flag.String("dump", "", "master.db dump JSON (from rekordbox_dump.py)")
	zipPath := flag.String("zip", "", "rekordbox library export .zip (for ANLZ grids)")
	from := flag.String("from", "Z:/Music/", "stored path prefix to replace")
	to := flag.String("to", "/run/media/tj/Data/Music/", "local audio prefix")
	out := flag.String("out", "beat_gt.json", "output manifest path")
	withBeats := flag.Bool("beats", false, "include the full rekordbox PQTZ beat array per track")
	flag.Parse()
	if *dumpPath == "" || *zipPath == "" {
		fmt.Fprintln(os.Stderr, "usage: gtgen -dump dump.json -zip export.zip [-from -to -out]")
		os.Exit(2)
	}

	var dump dumpFile
	if b, err := os.ReadFile(*dumpPath); err != nil {
		fatal(err)
	} else if err := json.Unmarshal(b, &dump); err != nil {
		fatal(err)
	}

	r, err := zip.OpenReader(*zipPath)
	if err != nil {
		fatal(err)
	}
	defer r.Close()
	anlz := make(map[string]*zip.File, len(r.File)) // normalized ".../ANLZ0000.DAT" -> entry
	for _, f := range r.File {
		u := "/" + strings.ToUpper(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.HasSuffix(u, "ANLZ0000.DAT") {
			anlz[u] = f
		}
	}

	var items []gtEntry
	var total, audioPresent, hasAnlz, hasGrid int
	for _, t := range dump.Tracks {
		total++
		if t.BPM <= 0 || t.AnalyzePath == "" {
			continue
		}
		local := remap(t.FilePath, *from, *to)
		if fi, err := os.Stat(local); err != nil || fi.IsDir() {
			continue
		}
		audioPresent++
		// The zip stores the ANLZ under share/ + the db's analyze_path.
		key := "/" + strings.ToUpper(strings.TrimPrefix(strings.ReplaceAll(t.AnalyzePath, "\\", "/"), "/"))
		f := anlz[key]
		if f == nil {
			f = anlz["/SHARE"+key] // analyze_path is "/PIONEER/..."; zip entry is "share/PIONEER/..."
		}
		if f == nil {
			continue
		}
		hasAnlz++
		dat, err := readZipFile(f)
		if err != nil {
			continue
		}
		res := analysis.ParseANLZBytes(dat, nil, nil, t.BPM, 0)
		if res == nil || len(res.Beats) == 0 {
			continue
		}
		hasGrid++
		title := t.Title
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(local), filepath.Ext(local))
		}
		e := gtEntry{File: local, BPM: t.BPM, FirstBeatMs: res.Beats[0], Title: title}
		if *withBeats {
			e.RBBeats = res.Beats
		}
		items = append(items, e)
	}

	b, _ := json.MarshalIndent(items, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "tracks=%d  audio-present=%d  anlz-found=%d  grid-ok=%d  written=%d -> %s\n",
		total, audioPresent, hasAnlz, hasGrid, len(items), *out)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func remap(p, from, to string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, from) {
		return to + p[len(from):]
	}
	return p
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gtgen:", err)
	os.Exit(1)
}

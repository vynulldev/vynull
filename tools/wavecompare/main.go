// SPDX-License-Identifier: GPL-3.0-or-later

// Command wavecompare scores our PWV5 color-detail waveform against
// rekordbox's. It decodes the audio, runs analysis.GenerateDetail, and compares
// the per-entry R/G/B (bass/mid/treble → red/green/blue) against rekordbox's
// imported PWV5 (read from the analysis .gob cache). The aggregate per-channel
// colour balance shows which frequency band is over/under-weighted vs rekordbox
// — the knob to turn is the band cutoff frequencies in splitBandsAndPeaks.
//
// Usage: go run ./tools/wavecompare -lib data-dir-test13/library.json -n 40 [-v]
package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"vynull/analysis"
)

type track struct {
	FilePath string  `json:"file_path"`
	BPM      float64 `json:"bpm"`
}

// col is a normalized per-entry colour sample: R/G/B = bass/mid/treble channel
// values, sig = the entry carries real signal (not silence/padding).
type col struct {
	R, G, B float64
	sig     bool
}

// decodePWV5 -> cols. R/G/B are the 3-bit band proportions; sig when height>0.
func decodePWV5(buf []byte) []col {
	n := len(buf) / 2
	out := make([]col, n)
	for i := 0; i < n; i++ {
		w := uint16(buf[i*2])<<8 | uint16(buf[i*2+1])
		out[i] = col{
			R:   float64(int(w>>13) & 7),
			G:   float64(int(w>>10) & 7),
			B:   float64(int(w>>7) & 7),
			sig: (int(w>>2) & 0x1f) > 0,
		}
	}
	return out
}

// decodePWV6 -> cols. Each entry is 3 bytes: bass/mid/treble (0-255), each
// normalized to its own band max. sig when any band carries level.
func decodePWV6(buf []byte) []col {
	n := len(buf) / 3
	out := make([]col, n)
	for i := 0; i < n; i++ {
		R, G, B := float64(buf[i*3]), float64(buf[i*3+1]), float64(buf[i*3+2])
		mx := R
		if G > mx {
			mx = G
		}
		if B > mx {
			mx = B
		}
		out[i] = col{R: R, G: G, B: B, sig: mx > 20}
	}
	return out
}

// decodePWV4 -> cols. Each entry is 6 bytes; d3/d4/d5 = bass/mid/treble (0-255),
// d1 = luminance envelope (sig when bright enough to render colour).
func decodePWV4(buf []byte) []col {
	n := len(buf) / 6
	out := make([]col, n)
	for i := 0; i < n; i++ {
		d1 := buf[i*6+1]
		out[i] = col{
			R:   float64(buf[i*6+3]),
			G:   float64(buf[i*6+4]),
			B:   float64(buf[i*6+5]),
			sig: d1 > 20,
		}
	}
	return out
}

func main() {
	lib := flag.String("lib", "data-dir-test13/library.json", "library.json")
	cacheDir := flag.String("cache", "data-dir-test13/analysis", "analysis cache dir holding rekordbox's imported PWV5")
	n := flag.Int("n", 40, "tracks to sample (0 = all eligible)")
	workers := flag.Int("workers", 3, "concurrent workers")
	verbose := flag.Bool("v", false, "per-track output")
	crossover := flag.Float64("crossover", 0, "override mid/treble crossover Hz (0 = use default)")
	bassMid := flag.Float64("bassmid", 0, "override bass/mid crossover Hz (0 = use default)")
	pwv4 := flag.Bool("pwv4", false, "compare PWV4 (overview) instead of PWV5 (detail)")
	pwv6 := flag.Bool("pwv6", false, "compare PWV6 (3-band overview)")
	pwv7 := flag.Bool("pwv7", false, "compare PWV7 (3-band detail)")
	pwv4treble := flag.Float64("pwv4treble", 0, "override PWV4 treble HP cutoff Hz (0 = default)")
	bs := flag.Float64("bscale", 0, "override PWV4 bass scale (0 = default)")
	ms := flag.Float64("mscale", 0, "override PWV4 mid scale (0 = default)")
	ts := flag.Float64("tscale", 0, "override PWV4 treble scale (0 = default)")
	d3b := flag.Float64("d3bass", 0, "override PWV7 bass scale (0 = default)")
	d3m := flag.Float64("d3mid", 0, "override PWV7 mid scale (0 = default)")
	d3t := flag.Float64("d3treble", 0, "override PWV7 treble scale (0 = default)")
	p3b := flag.Float64("p3bass", 0, "override PWV6 bass scale (0 = default)")
	p3m := flag.Float64("p3mid", 0, "override PWV6 mid scale (0 = default)")
	p3t := flag.Float64("p3treble", 0, "override PWV6 treble scale (0 = default)")
	flag.Parse()
	if *d3b > 0 {
		analysis.Detail3BassScale = *d3b
	}
	if *d3m > 0 {
		analysis.Detail3MidScale = *d3m
	}
	if *d3t > 0 {
		analysis.Detail3TrebleScale = *d3t
	}
	if *p3b > 0 {
		analysis.Preview3BassScale = *p3b
	}
	if *p3m > 0 {
		analysis.Preview3MidScale = *p3m
	}
	if *p3t > 0 {
		analysis.Preview3TrebleScale = *p3t
	}
	if *pwv4treble > 0 {
		analysis.PreviewTrebleHz = *pwv4treble
	}
	if *bs > 0 {
		analysis.PreviewBassScale = *bs
	}
	if *ms > 0 {
		analysis.PreviewMidScale = *ms
	}
	if *ts > 0 {
		analysis.PreviewTrebleScale = *ts
	}
	if *crossover > 0 {
		analysis.BandMidTrebleHz = *crossover
	}
	if *bassMid > 0 {
		analysis.BandBassMidHz = *bassMid
	}
	fmt.Printf("band crossovers: bass/mid=%.0fHz  mid/treble=%.0fHz\n", analysis.BandBassMidHz, analysis.BandMidTrebleHz)

	raw, err := os.ReadFile(*lib)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var tracks []track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Eligible = audio present AND a cached rekordbox PWV5 exists.
	var elig []track
	for _, t := range tracks {
		if t.FilePath == "" {
			continue
		}
		if fi, err := os.Stat(t.FilePath); err != nil || fi.IsDir() {
			continue
		}
		if len(loadRB(*cacheDir, t.FilePath, *pwv4, *pwv6, *pwv7)) == 0 {
			continue
		}
		elig = append(elig, t)
	}
	sort.Slice(elig, func(i, j int) bool { return elig[i].FilePath < elig[j].FilePath })
	fmt.Printf("eligible (audio + rekordbox PWV5 in cache): %d\n", len(elig))

	sample := elig
	if *n > 0 && *n < len(elig) {
		sample = make([]track, 0, *n)
		step := float64(len(elig)) / float64(*n)
		for i := 0; i < *n; i++ {
			sample = append(sample, elig[int(float64(i)*step)])
		}
	}
	fmt.Printf("sampling %d with %d workers…\n\n", len(sample), *workers)

	type res struct {
		t                      track
		ok                     bool
		ourR, ourG, ourB       float64 // mean channel value over compared entries
		rbR, rbG, rbB          float64
		nEntries               int
	}
	results := make([]res, len(sample))
	var done int64
	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	for i, t := range sample {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t track) {
			defer wg.Done()
			defer func() { <-sem }()
			r := res{t: t}
			rbBytes := loadRB(*cacheDir, t.FilePath, *pwv4, *pwv6, *pwv7)
			samples, err := analysis.DecodePCM(t.FilePath, 44100)
			if err == nil && len(rbBytes) > 0 {
				var rb, ours []col
				switch {
				case *pwv7:
					rb = decodePWV6(rbBytes) // PWV7 is also 3 bytes/entry
					ours = decodePWV6(analysis.GenerateDetail3Band(samples, 44100))
				case *pwv6:
					rb = decodePWV6(rbBytes)
					ours = decodePWV6(analysis.GeneratePreview3Band(samples, 44100))
				case *pwv4:
					rb = decodePWV4(rbBytes)
					ours = decodePWV4(analysis.GenerateColorPreview(samples, 44100))
				default:
					rb = decodePWV5(rbBytes)
					ours = decodePWV5(analysis.GenerateDetail(samples, 44100))
				}
				m := len(rb)
				if len(ours) < m {
					m = len(ours)
				}
				var oR, oG, oB, rR, rG, rB float64
				cnt := 0
				for k := 0; k < m; k++ {
					// Compare only where rekordbox has real signal.
					if !rb[k].sig {
						continue
					}
					oR += ours[k].R
					oG += ours[k].G
					oB += ours[k].B
					rR += rb[k].R
					rG += rb[k].G
					rB += rb[k].B
					cnt++
				}
				if cnt > 100 {
					f := float64(cnt)
					r.ourR, r.ourG, r.ourB = oR/f, oG/f, oB/f
					r.rbR, r.rbG, r.rbB = rR/f, rG/f, rB/f
					r.nEntries = cnt
					r.ok = true
				}
			}
			results[i] = r
			c := atomic.AddInt64(&done, 1)
			fmt.Fprintf(os.Stderr, "\r%d/%d…", c, len(sample))
		}(i, t)
	}
	wg.Wait()
	fmt.Fprintln(os.Stderr)

	// Aggregate: average the per-track channel means, and the normalized colour
	// balance (each channel's share of R+G+B), ours vs rekordbox.
	var oR, oG, oB, rR, rG, rB float64
	nOK := 0
	for _, r := range results {
		if !r.ok {
			continue
		}
		nOK++
		oR += r.ourR
		oG += r.ourG
		oB += r.ourB
		rR += r.rbR
		rG += r.rbG
		rB += r.rbB
		if *verbose {
			fmt.Printf("  ours(R%.2f G%.2f B%.2f) rb(R%.2f G%.2f B%.2f)  %s\n",
				r.ourR, r.ourG, r.ourB, r.rbR, r.rbG, r.rbB, base(r.t.FilePath))
		}
	}
	if nOK == 0 {
		fmt.Println("no comparable tracks")
		return
	}
	f := float64(nOK)
	oR, oG, oB = oR/f, oG/f, oB/f
	rR, rG, rB = rR/f, rG/f, rB/f
	bal := func(a, b, c float64) (float64, float64, float64) {
		s := a + b + c
		if s == 0 {
			return 0, 0, 0
		}
		return 100 * a / s, 100 * b / s, 100 * c / s
	}
	orp, ogp, obp := bal(oR, oG, oB)
	rrp, rgp, rbp := bal(rR, rG, rB)
	fmtName := "PWV5 (detail)"
	if *pwv4 {
		fmtName = "PWV4 (overview)"
	}
	if *pwv6 {
		fmtName = "PWV6 (3-band overview)"
	}
	if *pwv7 {
		fmtName = "PWV7 (3-band detail)"
	}
	fmt.Printf("=== %s colour vs rekordbox (%d tracks) ===\n", fmtName, nOK)
	fmt.Printf("  channel mean (0-7):  R(bass)   G(mid)   B(treble)\n")
	fmt.Printf("    ours              %6.2f   %6.2f   %6.2f\n", oR, oG, oB)
	fmt.Printf("    rekordbox         %6.2f   %6.2f   %6.2f\n", rR, rG, rB)
	fmt.Printf("    Δ (ours-rb)       %+6.2f   %+6.2f   %+6.2f\n", oR-rR, oG-rG, oB-rB)
	fmt.Printf("  colour balance (%% of R+G+B):\n")
	fmt.Printf("    ours              %5.1f%%   %5.1f%%   %5.1f%%\n", orp, ogp, obp)
	fmt.Printf("    rekordbox         %5.1f%%   %5.1f%%   %5.1f%%\n", rrp, rgp, rbp)
	fmt.Printf("    Δ                 %+5.1f   %+5.1f   %+5.1f\n", orp-rrp, ogp-rgp, obp-rbp)
}

func loadRB(cacheDir, filePath string, pwv4, pwv6, pwv7 bool) []byte {
	h := sha256.Sum256([]byte(filePath))
	p := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+".gob")
	fdata, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer fdata.Close()
	var r analysis.Result
	if err := gob.NewDecoder(fdata).Decode(&r); err != nil {
		return nil
	}
	switch {
	case pwv7:
		return r.WaveDetail3Band
	case pwv6:
		return r.WavePreview3Band
	case pwv4:
		return r.WaveColorPreview
	default:
		return r.WaveDetail
	}
}

func base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

var _ = math.Abs

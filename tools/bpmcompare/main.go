// SPDX-License-Identifier: GPL-3.0-or-later

// Command bpmcompare scores our beat/BPM detection against rekordbox's
// ground-truth BPM (carried per-track in a library.json imported from a
// rekordbox backup). It decodes the audio, runs analysis.DetectBeats, and
// classifies our BPM vs rekordbox's — in particular flagging half/double-time
// (octave) errors, which are the dominant beat-grid failure mode.
//
// Usage:
//
//	go run ./tools/bpmcompare -lib data-dir-test13/library.json -n 40 [-v]
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
	"time"

	"vynull/analysis"
)

type track struct {
	FilePath string  `json:"file_path"`
	BPM      float64 `json:"bpm"`
	Key      string  `json:"key"`
}

func main() {
	lib := flag.String("lib", "data-dir-test13/library.json", "library.json with rekordbox BPM per track")
	cacheDir := flag.String("cache", "data-dir-test13/analysis", "analysis cache dir holding rekordbox's imported beat grids (.gob)")
	n := flag.Int("n", 40, "number of tracks to sample (0 = all eligible)")
	workers := flag.Int("workers", 3, "concurrent decode+analyze workers")
	verbose := flag.Bool("v", false, "print a line per track")
	dump := flag.String("dump", "", "dump our grid vs the cached rekordbox grid for a single audio file, then exit")
	flag.Parse()

	if *dump != "" {
		dumpOne(*dump, *cacheDir)
		return
	}

	raw, err := os.ReadFile(*lib)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *lib, err)
		os.Exit(1)
	}
	var tracks []track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", *lib, err)
		os.Exit(1)
	}

	// Eligible = has a rekordbox BPM and the audio file exists locally.
	var elig []track
	for _, t := range tracks {
		if t.BPM <= 0 || t.FilePath == "" {
			continue
		}
		if fi, err := os.Stat(t.FilePath); err != nil || fi.IsDir() {
			continue
		}
		elig = append(elig, t)
	}
	sort.Slice(elig, func(i, j int) bool { return elig[i].FilePath < elig[j].FilePath })
	fmt.Printf("eligible tracks (rb BPM + file present): %d\n", len(elig))

	// Evenly-spaced sample for diversity + determinism.
	sample := elig
	if *n > 0 && *n < len(elig) {
		sample = make([]track, 0, *n)
		step := float64(len(elig)) / float64(*n)
		for i := 0; i < *n; i++ {
			sample = append(sample, elig[int(float64(i)*step)])
		}
	}
	fmt.Printf("sampling %d tracks with %d workers…\n\n", len(sample), *workers)

	type result struct {
		t           track
		ours        float64
		class       string
		err         error
		ourDown     float64 // our detected downbeat (ms)
		hasGrid     bool    // rekordbox grid found + BPM matches (trusted ground truth)
		tempoOK     bool    // our BPM matches rekordbox (phase is only meaningful then)
		beatPhaseMs float64 // signed sub-beat misalignment of our grid vs rekordbox's (mid-track)
		earlyPhase  float64 // same, measured near the start (drift hasn't accumulated)
		barOff      int     // our downbeat's beat-in-bar offset from rekordbox's (0 = correct)
		idealRot    int     // diagnostic: which rotation of OUR grid would be correct
	}
	results := make([]result, len(sample))
	var done int64
	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	start := time.Now()
	for i, t := range sample {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t track) {
			defer wg.Done()
			defer func() { <-sem }()
			r := result{t: t}
			samples, err := analysis.DecodePCM(t.FilePath, 44100)
			if err != nil {
				r.err = err
			} else {
				br := analysis.DetectBeats(samples, 44100)
				r.ours = br.BPM
				r.class = classify(br.BPM, t.BPM)
				r.ourDown = br.Downbeat
				// Beat-grid phase: trust the cached rekordbox grid only when its
				// BPM matches the library (else it may be our own re-analysis),
				// and only score phase when our tempo matches (phase is undefined
				// across a tempo multiple).
				if rb := loadRBGrid(*cacheDir, t.FilePath); rb != nil && bpmMatches(rb.BPM, t.BPM) {
					r.tempoOK = math.Abs(br.BPM-t.BPM) <= t.BPM*0.01
					if r.tempoOK {
						if ph, early, bar, ideal, ok := gridPhase(br, t.BPM, rb); ok {
							r.hasGrid, r.beatPhaseMs, r.earlyPhase, r.barOff, r.idealRot = true, ph, early, bar, ideal
						}
					}
				}
			}
			results[i] = r
			c := atomic.AddInt64(&done, 1)
			fmt.Fprintf(os.Stderr, "\r%d/%d…", c, len(sample))
		}(i, t)
	}
	wg.Wait()
	fmt.Fprintln(os.Stderr)

	// Aggregate.
	counts := map[string]int{}
	var nOK int
	for _, r := range results {
		if r.err != nil {
			counts["decode-err"]++
			continue
		}
		counts[r.class]++
		nOK++
		if *verbose {
			g := fmt.Sprintf("  ourDown=%6.1fms", r.ourDown)
			if r.hasGrid {
				g += fmt.Sprintf(" phaseΔ=%+6.1fms early=%+6.1f bar=%d", r.beatPhaseMs, r.earlyPhase, r.barOff)
			}
			fmt.Printf("  %-7s rb=%7.2f ours=%7.2f  ratio=%.3f%s  %s\n",
				r.class, r.t.BPM, r.ours, r.ours/r.t.BPM, g, base(r.t.FilePath))
		}
	}

	fmt.Printf("\n=== BPM vs rekordbox (%d analyzed, %s) ===\n", nOK, time.Since(start).Round(time.Second))
	order := []string{"exact", "close", "half", "double", "1.5x", "0.75x", "1.33x", "0.67x", "OTHER", "decode-err"}
	for _, k := range order {
		if counts[k] == 0 {
			continue
		}
		pct := 100 * float64(counts[k]) / float64(nOK)
		fmt.Printf("  %-10s %4d  %5.1f%%\n", k, counts[k], pct)
	}
	correct := counts["exact"] + counts["close"]
	octave := counts["half"] + counts["double"]
	fmt.Printf("\n  tempo correct (exact+close): %d/%d (%.1f%%)\n", correct, nOK, 100*float64(correct)/float64(nOK))
	fmt.Printf("  octave errors (half+double): %d/%d (%.1f%%)\n", octave, nOK, 100*float64(octave)/float64(nOK))

	// Beat-grid phase + downbeat, over tempo-correct tracks with a trusted
	// rekordbox grid.
	var grid []result
	for _, r := range results {
		if r.hasGrid {
			grid = append(grid, r)
		}
	}
	if len(grid) == 0 {
		fmt.Printf("\n(no beat-grid ground truth found in %s — skipping phase scoring)\n", *cacheDir)
		return
	}
	var p10, p25, p50, pBad int
	bar := map[int]int{}
	barAligned := map[int]int{} // downbeat offset over phase-aligned tracks only
	var nAligned int
	var absPhases, signed []float64
	for _, r := range grid {
		a := math.Abs(r.beatPhaseMs)
		absPhases = append(absPhases, a)
		signed = append(signed, r.beatPhaseMs)
		switch {
		case a <= 10:
			p10++
		case a <= 25:
			p25++
		case a <= 50:
			p50++
		default:
			pBad++
		}
		bar[r.barOff]++
		// barOff conflates downbeat error with phase error: when the beats
		// aren't aligned, the "nearest beat" on each side can be different beats,
		// so the bar index is noise. Restrict the clean downbeat metric to tracks
		// whose beats line up (|phase| ≤ 50ms ≈ within a quarter-beat).
		if a <= 50 {
			barAligned[r.barOff]++
			nAligned++
		}
	}
	// Mean signed phase: a consistent bias = a fixable systematic offset (e.g.
	// onset-detection lag); a near-zero mean with large spread = random.
	var sum float64
	for _, s := range signed {
		sum += s
	}
	meanSigned := sum / float64(len(signed))
	ng := len(grid)
	fmt.Printf("\n=== Beat-grid alignment vs rekordbox (%d tempo-correct tracks w/ grid) ===\n", ng)
	fmt.Printf("  beat phase |Δ| ≤10ms:  %3d  %5.1f%%\n", p10, 100*float64(p10)/float64(ng))
	fmt.Printf("  beat phase |Δ| ≤25ms:  %3d  %5.1f%%  (cumulative %.1f%%)\n", p25, 100*float64(p25)/float64(ng), 100*float64(p10+p25)/float64(ng))
	fmt.Printf("  beat phase |Δ| ≤50ms:  %3d  %5.1f%%  (cumulative %.1f%%)\n", p50, 100*float64(p50)/float64(ng), 100*float64(p10+p25+p50)/float64(ng))
	fmt.Printf("  beat phase |Δ| >50ms:  %3d  %5.1f%%  (misaligned grid)\n", pBad, 100*float64(pBad)/float64(ng))
	fmt.Printf("  median |Δ|: %.1f ms   mean signed Δ: %+.1f ms (median signed %+.1f)\n",
		medianf(absPhases), meanSigned, medianf(signed))
	// Drift split: how much of the mid-track misalignment is already present near
	// the start (true grid-origin error) vs accumulated by mid (residual tempo
	// difference). If early ≪ mid, the grid is well placed and the BPM precision
	// is the problem.
	var earlyAbs, driftAbs []float64
	var earlyAligned int
	for _, r := range grid {
		earlyAbs = append(earlyAbs, math.Abs(r.earlyPhase))
		driftAbs = append(driftAbs, math.Abs(r.beatPhaseMs-r.earlyPhase))
		if math.Abs(r.earlyPhase) <= 50 {
			earlyAligned++
		}
	}
	fmt.Printf("  early-phase median |Δ|: %.1f ms   (≤50ms: %d/%d, %.1f%%)   mid-minus-early median: %.1f ms\n",
		medianf(earlyAbs), earlyAligned, ng, 100*float64(earlyAligned)/float64(ng), medianf(driftAbs))
	// Decisive split: restrict phase to tracks whose tempo is near-exact (|Δ| <
	// 0.02 BPM). On these the grid can't drift, so mid-track phase IS the true
	// origin offset. If these align well while the full set doesn't, the residual
	// "misalignment" is really sub-1% BPM imprecision (allowed by tempoOK),
	// drifting into apparent phase error — a tempo problem, not a phase one.
	var exactAbs []float64
	var nExact, exactAligned int
	for _, r := range grid {
		if math.Abs(r.ours-r.t.BPM) < 0.02 {
			nExact++
			exactAbs = append(exactAbs, math.Abs(r.beatPhaseMs))
			if math.Abs(r.beatPhaseMs) <= 50 {
				exactAligned++
			}
		}
	}
	if nExact > 0 {
		fmt.Printf("  near-exact-BPM subset (|Δbpm|<0.02): n=%d  phase median |Δ|: %.1f ms  (≤50ms: %d, %.1f%%)\n",
			nExact, medianf(exactAbs), exactAligned, 100*float64(exactAligned)/float64(nExact))
	}
	fmt.Printf("  downbeat-in-bar (all):           correct=%d (%.1f%%)  off-by-1=%d  off-by-2=%d  off-by-3=%d\n",
		bar[0], 100*float64(bar[0])/float64(ng), bar[1], bar[2], bar[3])
	if nAligned > 0 {
		fmt.Printf("  downbeat-in-bar (phase-aligned): correct=%d (%.1f%% of %d)  off-by-1=%d  off-by-2=%d  off-by-3=%d\n",
			barAligned[0], 100*float64(barAligned[0])/float64(nAligned), nAligned, barAligned[1], barAligned[2], barAligned[3])
		ideal := map[int]int{}
		for _, r := range grid {
			if math.Abs(r.beatPhaseMs) <= 50 {
				ideal[r.idealRot]++
			}
		}
		fmt.Printf("  ideal-rotation histogram (phase-aligned): rot0=%d rot1=%d rot2=%d rot3=%d\n",
			ideal[0], ideal[1], ideal[2], ideal[3])
	}
}

// loadRBGrid reads the cached analysis.Result for filePath (keyed by
// sha256(path)[:8], matching analysis.Store) — for an imported library this is
// rekordbox's beat grid. Returns nil if absent/undecodable.
func loadRBGrid(cacheDir, filePath string) *analysis.Result {
	h := sha256.Sum256([]byte(filePath))
	p := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+".gob")
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var r analysis.Result
	if err := gob.NewDecoder(f).Decode(&r); err != nil {
		return nil
	}
	return &r
}

func bpmMatches(a, b float64) bool { return b > 0 && math.Abs(a-b) <= 0.5 }

// gridPhase computes our beat grid's sub-beat phase offset and downbeat-in-bar
// offset relative to rekordbox's grid (same tempo assumed). beatPhaseMs is the
// residual after removing whole beats (in (-T/2, T/2]); barOff is how many
// beats (mod 4) our downbeat is shifted from rekordbox's.
func gridPhase(ours *analysis.BeatResult, rbBPM float64, rb *analysis.Result) (beatPhaseMs, earlyPhaseMs float64, barOff, idealRot int, ok bool) {
	if rb == nil || len(rb.Beats) == 0 || len(ours.Beats) == 0 || rbBPM <= 0 {
		return 0, 0, 0, 0, false
	}
	T := 60000.0 / rbBPM
	end := math.Min(ours.Beats[len(ours.Beats)-1], rb.Beats[len(rb.Beats)-1])
	// Folded phase offset (in (-T/2, T/2]) between the two grids at a given
	// reference time, using the nearest beat in each. Measuring at a single
	// reference localizes the comparison, but the offset itself still includes
	// any drift accumulated from the grid origin to that reference — so a tiny
	// residual tempo difference shows up as phase that grows with reference time.
	phaseAt := func(ref float64) float64 {
		oi := nearestIdx(ours.Beats, ref)
		ri := nearestIdx(rb.Beats, ref)
		raw := ours.Beats[oi] - rb.Beats[ri]
		return raw - math.Round(raw/T)*T
	}
	// Measure both near the start (≈15s in, before drift accumulates) and at
	// mid-track. If early is small but mid is large, the misalignment is residual
	// tempo drift, not a grid-origin error. The mid measurement stays the primary
	// score for continuity with the downbeat metric.
	mid := end / 2
	beatPhaseMs = phaseAt(mid)
	earlyPhaseMs = phaseAt(math.Min(15000, end*0.1))
	oi := nearestIdx(ours.Beats, mid)
	ri := nearestIdx(rb.Beats, mid)

	// Downbeat-in-bar at the same reference beat: which beat of the bar does
	// each grid assign here? 0 = same.
	ourDownIdx := nearestIdx(ours.Beats, ours.Downbeat)
	rbBar := ((ri-rb.DownbeatIndex)%4 + 4) % 4
	ourBar := ((oi-ourDownIdx)%4 + 4) % 4
	barOff = ((ourBar-rbBar)%4 + 4) % 4
	// Diagnostic: the rotation r for which using beats[r] as the downbeat would
	// be correct (barOff==0). Histogramming this over phase-aligned tracks shows
	// whether the true downbeat is a half-bar choice (only 0/2) or full (0-3).
	idealRot = ((oi - rbBar) % 4 + 4) % 4
	return beatPhaseMs, earlyPhaseMs, barOff, idealRot, true
}

func nearestIdx(beats []float64, t float64) int {
	lo, hi := 0, len(beats)-1
	for lo < hi {
		m := (lo + hi) / 2
		if beats[m] < t {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo > 0 && math.Abs(beats[lo-1]-t) <= math.Abs(beats[lo]-t) {
		return lo - 1
	}
	return lo
}

// dumpOne prints our detected grid next to the cached rekordbox grid for one
// file, to validate the phase measurement by eye.
func dumpOne(path, cacheDir string) {
	samples, err := analysis.DecodePCM(path, 44100)
	if err != nil {
		fmt.Println("decode:", err)
		return
	}
	br := analysis.DetectBeats(samples, 44100)
	rb := loadRBGrid(cacheDir, path)
	fmt.Printf("file: %s\n", base(path))
	fmt.Printf("ours: BPM=%.2f  downbeat=%.1fms  beats=%d\n", br.BPM, br.Downbeat, len(br.Beats))
	show := func(label string, beats []float64) {
		n := 6
		if len(beats) < n {
			n = len(beats)
		}
		fmt.Printf("  %s first %d:", label, n)
		for i := 0; i < n; i++ {
			d := 0.0
			if i > 0 {
				d = beats[i] - beats[i-1]
			}
			fmt.Printf(" %.1f(+%.1f)", beats[i], d)
		}
		fmt.Println()
	}
	show("ours", br.Beats)
	if rb == nil {
		fmt.Println("rb:   (no cached grid)")
		return
	}
	rbDown := 0.0
	if rb.DownbeatIndex >= 0 && rb.DownbeatIndex < len(rb.Beats) {
		rbDown = rb.Beats[rb.DownbeatIndex]
	}
	fmt.Printf("rb:   BPM=%.2f  downbeatIdx=%d (%.1fms)  beats=%d\n", rb.BPM, rb.DownbeatIndex, rbDown, len(rb.Beats))
	show("rb  ", rb.Beats)
}

func medianf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// classify buckets our BPM against rekordbox's. exact = essentially identical;
// close = within ~1%; half/double/etc = tempo-multiple (octave) errors.
func classify(ours, rb float64) string {
	if rb <= 0 {
		return "OTHER"
	}
	if math.Abs(ours-rb) <= 0.2 {
		return "exact"
	}
	r := ours / rb
	near := func(target float64) bool { return math.Abs(r-target) <= 0.03 }
	switch {
	case near(1.0):
		return "close"
	case near(0.5):
		return "half"
	case near(2.0):
		return "double"
	case near(1.5):
		return "1.5x"
	case near(0.75):
		return "0.75x"
	case near(4.0 / 3.0):
		return "1.33x"
	case near(2.0 / 3.0):
		return "0.67x"
	default:
		return "OTHER"
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

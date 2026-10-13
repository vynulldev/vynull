// SPDX-License-Identifier: GPL-3.0-or-later

// Command attackeval prototypes per-track attack refinement: instead of a
// fixed grid offset, measure where each track's kick actually starts and move
// the detected grid onto it. For each hand-corrected track (GridEdited cache
// entries) it detects the grid at offset 0, averages a sample-resolution energy
// envelope over every beat, locates the attack in that average under several
// definitions, and scores each against the user's grid next to the fixed
// offset. A constant calibration per variant is fitted leave-one-out so the
// score reflects spread, not bias.
//
//	go run ./tools/attackeval -lib ~/.vynull/library.json -cache ~/.vynull/analysis
package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/vynulldev/vynull/analysis"
)

const winMs = 100.0 // beat-synchronous window: +-winMs around each grid beat

// variant: an envelope band and an attack definition on its beat average.
type variant struct {
	name string
	band string  // "low" (<150Hz), "mid" (<1kHz), "full"
	frac float64 // threshold fraction of the rise; 0 = steepest slope; 1 = peak
}

var variants = []variant{
	{"low peak", "low", 1}, {"low slope", "low", 0}, {"low 10%", "low", 0.1}, {"low 20%", "low", 0.2}, {"low 30%", "low", 0.3}, {"low 50%", "low", 0.5},
	{"mid peak", "mid", 1}, {"mid slope", "mid", 0}, {"mid 10%", "mid", 0.1}, {"mid 20%", "mid", 0.2}, {"mid 30%", "mid", 0.3}, {"mid 50%", "mid", 0.5},
	{"full peak", "full", 1}, {"full slope", "full", 0}, {"full 10%", "full", 0.1}, {"full 20%", "full", 0.2}, {"full 30%", "full", 0.3}, {"full 50%", "full", 0.5},
}

type trackOut struct {
	title, ext string
	ok         bool
	period     float64
	base       float64   // detected grid phase minus the user's, ms (offset 0), folded
	attack     []float64 // per variant: attack position relative to the detected grid, ms
}

func main() {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".vynull") // the daemon's default --data-dir
	lib := flag.String("lib", filepath.Join(dataDir, "library.json"), "library.json")
	cache := flag.String("cache", filepath.Join(dataDir, "analysis"), "analysis cache with the corrections")
	verbose := flag.Bool("v", false, "per-track rows")
	manifest := flag.String("manifest", "", "gtgen manifest: instead of the corrections, write per-track raw attacks vs rekordbox's grid to -out")
	out := flag.String("out", "/tmp/attack_rb.csv", "CSV for -manifest")
	flag.Parse()
	if *manifest != "" {
		runManifest(*manifest, *out)
		return
	}

	var tracks []struct {
		Title    string `json:"title"`
		FilePath string `json:"file_path"`
	}
	b, err := os.ReadFile(*lib)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	json.Unmarshal(b, &tracks)
	analysis.SetGridOffset(0)

	outs := make([]trackOut, len(tracks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(2, runtime.NumCPU()/2))
	for i, t := range tracks {
		wg.Add(1)
		go func(i int, title, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outs[i] = evalTrack(*cache, title, path)
		}(i, t.Title, t.FilePath)
	}
	wg.Wait()
	var rows []trackOut
	for _, o := range outs {
		if o.ok {
			rows = append(rows, o)
		}
	}
	fmt.Printf("hand-corrected tracks scored: %d\n", len(rows))
	for _, ext := range []string{"", ".flac", ".mp3"} {
		label := "all"
		if ext != "" {
			label = ext
		}
		fmt.Printf("\n— %s —\n%-12s %7s %7s %7s %7s %9s %9s\n", label, "method", "<5ms", "<10ms", "<20ms", "<30ms", "IQR", "cal")
		// Fixed offsets: the error is base + off.
		for _, off := range []float64{0, -25} {
			report(fmt.Sprintf("fixed %+.0f", off), rows, ext, func(r trackOut) float64 { return r.base + off }, false)
		}
		report("fixed LOO", rows, ext, func(r trackOut) float64 { return r.base }, true)
		for vi, v := range variants {
			report(v.name, rows, ext, func(r trackOut) float64 { return r.base + r.attack[vi] }, true)
		}
	}
	if *verbose {
		fmt.Printf("\n%-5s %-36s %7s", "ext", "title", "base")
		for _, v := range variants {
			fmt.Printf(" %9s", v.name)
		}
		fmt.Println()
		for _, r := range rows {
			fmt.Printf("%-5s %-36.36s %+7.1f", r.ext, r.title, r.base)
			for vi := range variants {
				fmt.Printf(" %+9.1f", r.attack[vi])
			}
			fmt.Println()
		}
	}
}

// report scores err(r) per track. With calibrate, a constant is subtracted,
// fitted as the median of the OTHER tracks' errors (leave-one-out), so a
// variant is judged on how consistently it lands, not on its bias.
func report(name string, all []trackOut, ext string, err func(trackOut) float64, calibrate bool) {
	var rows []trackOut
	for _, r := range all {
		if ext == "" || r.ext == ext {
			rows = append(rows, r)
		}
	}
	if len(rows) < 3 {
		return
	}
	es := make([]float64, len(rows))
	for i, r := range rows {
		es[i] = err(r)
	}
	final := make([]float64, len(rows))
	cal := 0.0
	for i := range rows {
		c := 0.0
		if calibrate {
			var others []float64
			for j := range rows {
				if j != i {
					others = append(others, es[j])
				}
			}
			c = median(others)
		}
		final[i] = fold(es[i]-c, rows[i].period)
	}
	if calibrate {
		cal = median(es)
	}
	var t5, t10, t20, t30 int
	for _, e := range final {
		a := math.Abs(e)
		if a < 5 {
			t5++
		}
		if a < 10 {
			t10++
		}
		if a < 20 {
			t20++
		}
		if a < 30 {
			t30++
		}
	}
	s := append([]float64(nil), final...)
	sort.Float64s(s)
	iqr := s[3*len(s)/4] - s[len(s)/4]
	p := func(k int) string { return fmt.Sprintf("%5.0f%%", 100*float64(k)/float64(len(rows))) }
	calS := ""
	if calibrate {
		calS = fmt.Sprintf("%+8.1f", -cal)
	}
	fmt.Printf("%-12s %7s %7s %7s %7s %8.1fms %9s\n", name, p(t5), p(t10), p(t20), p(t30), iqr, calS)
}

func evalTrack(cacheDir, title, path string) trackOut {
	o := trackOut{title: title, ext: strings.ToLower(filepath.Ext(path))}
	h := sha256.Sum256([]byte(path))
	f, err := os.Open(filepath.Join(cacheDir, fmt.Sprintf("%x.gob", h[:8])))
	if err != nil {
		return o
	}
	var ref analysis.Result
	err = gob.NewDecoder(f).Decode(&ref)
	f.Close()
	if err != nil || !ref.GridEdited || len(ref.Beats) == 0 || ref.DownbeatIndex >= len(ref.Beats) {
		return o
	}
	samples, err := analysis.DecodePCM(path, analysis.AnalysisRate)
	if err != nil {
		return o
	}
	encDelay := analysis.EncoderDelayMs(path)
	br := analysis.DetectBeatsWithEncoderDelay(samples, analysis.AnalysisRate, encDelay)
	if len(br.Beats) < 16 || math.Abs(br.BPM-ref.BPM) > 0.6 {
		return o
	}
	o.period = 60000 / br.BPM
	o.base = fold(br.Beats[8]-ref.Beats[ref.DownbeatIndex], o.period) // beat 0 may be clamped to 0 on a hot start

	envs := map[string][]float64{
		"low":  envelope(samples, 150),
		"mid":  envelope(samples, 1000),
		"full": envelope(samples, 0),
	}
	avgs := map[string][]float64{}
	for k, e := range envs {
		avgs[k] = beatAverage(e, br.Beats, encDelay)
	}
	for _, v := range variants {
		o.attack = append(o.attack, attackMs(avgs[v.band], v.frac))
	}
	o.ok = true
	return o
}

// envelope returns a sample-rate energy envelope: optional 2nd-order
// Butterworth lowpass at cutoff Hz (0 = full band), squared, then smoothed
// with a 2ms one-pole run forward and backward (zero phase, so smoothing adds
// no delay).
func envelope(x []float32, cutoff float64) []float64 {
	sr := float64(analysis.AnalysisRate)
	y := make([]float64, len(x))
	if cutoff > 0 {
		w := math.Tan(math.Pi * cutoff / sr)
		k := 1 / (1 + math.Sqrt2*w + w*w)
		b0 := w * w * k
		b1, b2 := 2*b0, b0
		a1 := 2 * (w*w - 1) * k
		a2 := (1 - math.Sqrt2*w + w*w) * k
		var x1, x2, y1, y2 float64
		// Forward and backward so the filter adds no group delay.
		for i, v := range x {
			s := b0*float64(v) + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1, y2, y1 = x1, float64(v), y1, s
			y[i] = s
		}
		x1, x2, y1, y2 = 0, 0, 0, 0
		for i := len(y) - 1; i >= 0; i-- {
			v := y[i]
			s := b0*v + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1, y2, y1 = x1, v, y1, s
			y[i] = s
		}
	} else {
		for i, v := range x {
			y[i] = float64(v)
		}
	}
	for i := range y {
		y[i] *= y[i]
	}
	a := math.Exp(-1 / (0.002 * sr))
	var s float64
	for i := range y {
		s = a*s + (1-a)*y[i]
		y[i] = s
	}
	s = 0
	for i := len(y) - 1; i >= 0; i-- {
		s = a*s + (1-a)*y[i]
		y[i] = s
	}
	return y
}

// beatAverage averages env over +-winMs around every grid beat (grid time
// minus the encoder delay is the decoded audio's time), each beat window
// normalized to unit mean so loud sections don't dominate.
func beatAverage(env []float64, beats []float64, encDelay float64) []float64 {
	sr := float64(analysis.AnalysisRate)
	half := int(winMs * sr / 1000)
	avg := make([]float64, 2*half+1)
	for _, b := range beats {
		c := int(math.Round((b - encDelay) * sr / 1000))
		if c-half < 0 || c+half >= len(env) {
			continue
		}
		w := env[c-half : c+half+1]
		var m float64
		for _, v := range w {
			m += v
		}
		m /= float64(len(w))
		if m <= 0 {
			continue
		}
		for i, v := range w {
			avg[i] += v / m
		}
	}
	return avg
}

// attackMs locates the attack in a beat average, in ms relative to the grid
// beat. It finds the main peak within +-60ms, the trough before it (within
// 80ms), then either the steepest rise between them (frac 0), the peak
// itself (frac 1), or the last crossing of trough + frac*(peak-trough).
func attackMs(avg []float64, frac float64) float64 {
	sr := float64(analysis.AnalysisRate)
	half := (len(avg) - 1) / 2
	ms := func(i int) float64 { return float64(i-half) * 1000 / sr }
	lim := int(60 * sr / 1000)
	pk := half - lim
	for i := half - lim; i <= half+lim; i++ {
		if avg[i] > avg[pk] {
			pk = i
		}
	}
	if frac >= 1 {
		return ms(pk)
	}
	lo := max(0, pk-int(80*sr/1000))
	tr := pk
	for i := pk; i >= lo; i-- {
		if avg[i] < avg[tr] {
			tr = i
		}
	}
	if frac == 0 {
		best, bi := -math.MaxFloat64, pk
		for i := tr + 1; i <= pk; i++ {
			if d := avg[i] - avg[i-1]; d > best {
				best, bi = d, i
			}
		}
		return ms(bi)
	}
	th := avg[tr] + frac*(avg[pk]-avg[tr])
	for i := pk; i > tr; i-- {
		if avg[i-1] < th {
			return ms(i)
		}
	}
	return ms(tr)
}

func median(x []float64) float64 {
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)/2]
}

func fold(x, p float64) float64 {
	m := math.Mod(x, p)
	if m < -p/2 {
		m += p
	} else if m >= p/2 {
		m -= p
	}
	return m
}

// runManifest writes, per rekordbox-graded track, our grid's phase error vs
// rekordbox (offset 0) and every variant's raw attack relative to our grid, to
// see how much the attack varies across a library and whether rekordbox's
// placement follows it.
func runManifest(path, outPath string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var es []struct {
		File        string  `json:"file"`
		BPM         float64 `json:"bpm"`
		FirstBeatMs float64 `json:"first_beat_ms"`
	}
	json.Unmarshal(b, &es)
	analysis.SetGridOffset(0)
	lines := make([]string, len(es))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, e := range es {
		wg.Add(1)
		go func(i int, file string, bpm, first float64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			samples, err := analysis.DecodePCM(file, analysis.AnalysisRate)
			if err != nil || bpm <= 0 {
				return
			}
			enc := analysis.EncoderDelayMs(file)
			br := analysis.DetectBeatsWithEncoderDelay(samples, analysis.AnalysisRate, enc)
			if len(br.Beats) < 16 || math.Abs(br.BPM-bpm) > 0.6 {
				return
			}
			p := 60000 / br.BPM
			ext := strings.ToLower(filepath.Ext(file))
			if ext == ".mp3" && !analysis.FFmpegTrimsStart(file) {
				ext = ".mp3u" // no gapless metadata: ffmpeg keeps the encoder delay
			}
			l := fmt.Sprintf("%s,%.2f,%.2f", ext, p, fold(br.Beats[8]-first, p)) // beat 0 may be clamped on a hot start
			avgs := map[string][]float64{}
			for _, band := range []struct {
				k string
				c float64
			}{{"low", 150}, {"mid", 1000}, {"full", 0}} {
				avgs[band.k] = beatAverage(envelope(samples, band.c), br.Beats, enc)
			}
			for _, v := range variants {
				l += fmt.Sprintf(",%.2f", attackMs(avgs[v.band], v.frac))
			}
			lines[i] = l
		}(i, e.File, e.BPM, e.FirstBeatMs)
	}
	wg.Wait()
	f, _ := os.Create(outPath)
	fmt.Fprint(f, "ext,period,base")
	for _, v := range variants {
		fmt.Fprintf(f, ",%s", v.name)
	}
	fmt.Fprintln(f)
	n := 0
	for _, l := range lines {
		if l != "" {
			fmt.Fprintln(f, l)
			n++
		}
	}
	f.Close()
	fmt.Printf("%d tracks -> %s\n", n, outPath)
}

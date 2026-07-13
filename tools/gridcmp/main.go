// SPDX-License-Identifier: GPL-3.0-or-later

// Command gridcmp renders a per-track beat-grid comparison: the audio waveform
// with rekordbox's grid drawn as green ticks along the top and our detected grid
// as red ticks along the bottom, so phase (and half-beat) disagreements are
// visible at a glance. It reads a reference manifest (see tools/gtgen) and
// writes a PNG per track plus an index.html sorted worst-first.
//
// Usage:
//
//	go run ./tools/gridcmp -manifest /tmp/easy_gt.json -out /tmp/gridcmp -sec 12
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vynulldev/vynull/analysis"
)

type entry struct {
	File        string    `json:"file"`
	BPM         float64   `json:"bpm"`
	FirstBeatMs float64   `json:"first_beat_ms"`
	Title       string    `json:"title"`
	RBBeats     []float64 `json:"rb_beats,omitempty"` // full rekordbox PQTZ grid, if present
}

type row struct {
	png              string
	title            string
	frac, errMs      float64
	kick, bpm, ours0 float64
	rb0              float64
}

func main() {
	manifest := flag.String("manifest", "/tmp/easy_gt.json", "reference manifest json")
	outDir := flag.String("out", "/tmp/gridcmp", "output directory")
	sec := flag.Float64("sec", 6, "seconds of audio to render")
	start := flag.Float64("start", 0, "start offset seconds")
	W := flag.Int("w", 1800, "image width px")
	H := flag.Int("h", 340, "image height px")
	limit := flag.Int("n", 0, "limit number of tracks (0 = all)")
	flag.Parse()

	b, err := os.ReadFile(*manifest)
	if err != nil {
		fatal(err)
	}
	var entries []entry
	if err := json.Unmarshal(b, &entries); err != nil {
		fatal(err)
	}
	if *limit > 0 && *limit < len(entries) {
		entries = entries[:*limit]
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}

	rate := analysis.AnalysisRate
	var rows []row
	for i, e := range entries {
		samples, err := analysis.DecodePCM(e.File, rate)
		if err != nil || len(samples) == 0 {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", e.Title, err)
			continue
		}
		res := analysis.DetectBeatsWithEncoderDelay(samples, rate, analysis.EncoderDelayMs(e.File))
		if res == nil || len(res.Beats) == 0 || e.BPM <= 0 {
			continue
		}
		P := 60000.0 / e.BPM
		errMs := math.Mod(res.Beats[0]-e.FirstBeatMs, P)
		if errMs < -P/2 {
			errMs += P
		} else if errMs >= P/2 {
			errMs -= P
		}
		img := render(samples, rate, e, res, errMs/P, *start, *sec, *W, *H)
		name := fmt.Sprintf("%03d_%s", i, sanitize(e.Title))
		fn := name + ".png"
		f, err := os.Create(filepath.Join(*outDir, fn))
		if err != nil {
			fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			fatal(err)
		}
		f.Close()
		rows = append(rows, row{fn, e.Title, errMs / P, errMs, res.KickRatio, res.BPM, res.Beats[0], e.FirstBeatMs})
	}

	sort.Slice(rows, func(i, j int) bool { return math.Abs(rows[i].frac) > math.Abs(rows[j].frac) })
	writeIndex(filepath.Join(*outDir, "index.html"), rows, *sec)
	fmt.Fprintf(os.Stderr, "rendered %d tracks -> %s/index.html\n", len(rows), *outDir)
}

// render draws the waveform (grey full-band + blue sub-bass overlay) with
// rekordbox's grid as green ticks from the top edge and our grid as red ticks
// from the bottom edge. Downbeats (every 4th beat, and our reported downbeat) are
// drawn thicker.
func render(samples []float32, rate int, e entry, res *analysis.BeatResult, frac, startSec, sec float64, W, H int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for i := range img.Pix {
		img.Pix[i] = 255 // white background
	}
	mid := H / 2
	s0 := int(startSec * float64(rate))
	if s0 < 0 {
		s0 = 0
	}
	total := int(sec * float64(rate))
	if s0+total > len(samples) {
		total = len(samples) - s0
	}
	if total < 1 {
		total = 1
	}
	spp := float64(total) / float64(W)
	bass := onePole(samples, float64(rate), 150.0)

	for x := 0; x < W; x++ {
		a := s0 + int(float64(x)*spp)
		b := s0 + int(float64(x+1)*spp)
		if b > len(samples) {
			b = len(samples)
		}
		var pk, bpk float64
		for s := a; s < b; s++ {
			if v := math.Abs(float64(samples[s])); v > pk {
				pk = v
			}
			if v := math.Abs(float64(bass[s])); v > bpk {
				bpk = v
			}
		}
		h := int(pk * float64(mid-2))
		vbar(img, x, mid-h, mid+h, color.RGBA{205, 205, 205, 255})
		hb := int(bpk * float64(mid-2) * 1.5) // bass is quieter; scale up for visibility
		if hb > mid-2 {
			hb = mid - 2
		}
		vbar(img, x, mid-hb, mid+hb, color.RGBA{120, 150, 210, 255})
	}

	x0 := func(tms float64) int {
		return int((tms/1000*float64(rate) - float64(s0)) / spp)
	}
	winLo, winHi := startSec*1000, (startSec+sec)*1000
	P := 60000.0 / e.BPM

	// rekordbox grid: green ticks down from the top edge. Prefer the true PQTZ
	// beat array (integer-ms, possibly variable spacing); fall back to a constant
	// db-BPM extrapolation only when the array is absent.
	green := color.RGBA{20, 170, 20, 255}
	if len(e.RBBeats) > 0 {
		for i, t := range e.RBBeats {
			if t < winLo || t > winHi {
				continue
			}
			thick, hgt := 1, int(0.34*float64(H))
			if i%4 == 0 {
				thick, hgt = 2, int(0.46*float64(H))
			}
			vline(img, x0(t), 0, hgt, green, thick)
		}
	} else {
		kStart := int(math.Floor((winLo - e.FirstBeatMs) / P))
		for k := kStart; ; k++ {
			t := e.FirstBeatMs + float64(k)*P
			if t > winHi {
				break
			}
			if t < winLo {
				continue
			}
			thick, hgt := 1, int(0.34*float64(H))
			if k%4 == 0 {
				thick, hgt = 2, int(0.46*float64(H))
			}
			vline(img, x0(t), 0, hgt, green, thick)
		}
	}

	// our grid: red ticks up from the bottom edge.
	for _, t := range res.Beats {
		if t < winLo || t > winHi {
			continue
		}
		x := x0(t)
		thick := 1
		hgt := int(0.34 * float64(H))
		if math.Abs(math.Mod(t-res.Downbeat, 4*P)) < 1 || math.Abs(math.Mod(t-res.Downbeat, 4*P)-4*P) < 1 {
			thick, hgt = 2, int(0.46*float64(H))
		}
		vline(img, x, H-hgt, H, color.RGBA{215, 40, 40, 255}, thick)
	}

	// Disagreement marker: colored border by how far our first beat is from
	// rekordbox's (green ok, amber marginal, red off / half-beat).
	mark := color.RGBA{40, 180, 40, 255}
	switch a := math.Abs(frac); {
	case a > 0.15:
		mark = color.RGBA{230, 40, 40, 255}
	case a > 0.05:
		mark = color.RGBA{230, 170, 30, 255}
	}
	for t := 0; t < 5; t++ {
		for x := 0; x < W; x++ {
			img.SetRGBA(x, t, mark)
			img.SetRGBA(x, H-1-t, mark)
		}
		for y := 0; y < H; y++ {
			img.SetRGBA(t, y, mark)
			img.SetRGBA(W-1-t, y, mark)
		}
	}
	return img
}

func vbar(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	for y := y0; y <= y1; y++ {
		img.SetRGBA(x, y, c)
	}
}

func vline(img *image.RGBA, x, y0, y1 int, c color.RGBA, thick int) {
	for dx := 0; dx < thick; dx++ {
		for y := y0; y < y1; y++ {
			img.SetRGBA(x+dx, y, c)
		}
	}
}

// onePole is a simple single-pole low-pass to isolate the sub-bass kick envelope.
func onePole(samples []float32, rate, cutoff float64) []float32 {
	alpha := cutoff / (rate/2.0 + cutoff)
	out := make([]float32, len(samples))
	if len(samples) == 0 {
		return out
	}
	out[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		out[i] = out[i-1] + float32(alpha)*(samples[i]-out[i-1])
	}
	return out
}

var nonword = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	s = nonword.ReplaceAllString(strings.TrimSpace(s), "_")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "track"
	}
	return s
}

func writeIndex(path string, rows []row, sec float64) {
	var sb strings.Builder
	sb.WriteString("<!doctype html><meta charset=utf-8><title>grid comparison</title>")
	sb.WriteString("<style>body{background:#111;color:#ddd;font:13px system-ui;margin:0;padding:16px}")
	sb.WriteString("h1{font-size:16px}.t{margin:14px 0;border-top:1px solid #333;padding-top:10px}")
	sb.WriteString(".hi{color:#e55}.ok{color:#5c5}img{width:100%;display:block;background:#fff}")
	sb.WriteString("code{color:#9cf}</style>")
	fmt.Fprintf(&sb, "<h1>beat-grid comparison — %d tracks, first %.0fs · <span style=color:#2a2>green=rekordbox (top)</span> · <span style=color:#e33>red=ours (bottom)</span></h1>", len(rows), sec)
	for _, r := range rows {
		cls := "ok"
		if math.Abs(r.frac) > 0.15 {
			cls = "hi"
		}
		fmt.Fprintf(&sb, "<div class=t><b>%s</b> <span class=%s>Δ %+.0f ms (%+.2f beat)</span> "+
			"<code>bpm %.2f · kick %.2f · rb0 %.0f · our0 %.0f</code><br><img src=%q loading=lazy></div>",
			html.EscapeString(r.title), cls, r.errMs, r.frac, r.bpm, r.kick, r.rb0, r.ours0, html.EscapeString(r.png))
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gridcmp:", err)
	os.Exit(1)
}

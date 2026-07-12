// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestBeatGridAccuracy measures the detector's beat-grid PHASE against
// rekordbox's own grids. Point VYNULL_BEAT_REF at a JSON manifest of
// [{"file","bpm","first_beat_ms","title"}, ...] (see tools/gtgen, which builds
// one from a rekordbox library export). It decodes each track, runs DetectBeats,
// folds the first-beat difference into [-P/2, +P/2), and reports how often we
// align with rekordbox plus the half-beat tail. Skipped when the env var is
// unset, so machines without the (private, copyrighted) audio still pass.
//
//	VYNULL_BEAT_REF=/path/manifest.json go test ./analysis/ -run BeatGridAccuracy -v
//
// VYNULL_BEAT_REF_N caps the track count (quick subset); VYNULL_BEAT_REF_WORKERS
// overrides the worker count.
func TestBeatGridAccuracy(t *testing.T) {
	path := os.Getenv("VYNULL_BEAT_REF")
	if path == "" {
		t.Skip("set VYNULL_BEAT_REF to a reference manifest to run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var gt []struct {
		File        string  `json:"file"`
		BPM         float64 `json:"bpm"`
		FirstBeatMs float64 `json:"first_beat_ms"`
		Title       string  `json:"title"`
	}
	if err := json.Unmarshal(data, &gt); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if off, _ := strconv.Atoi(os.Getenv("VYNULL_BEAT_REF_OFFSET")); off > 0 && off < len(gt) {
		gt = gt[off:]
	}
	if lim, _ := strconv.Atoi(os.Getenv("VYNULL_BEAT_REF_N")); lim > 0 && lim < len(gt) {
		gt = gt[:lim]
	}

	workers := runtime.NumCPU()
	if w, _ := strconv.Atoi(os.Getenv("VYNULL_BEAT_REF_WORKERS")); w > 0 {
		workers = w
	}

	// Phase-tuning overrides for A/B sweeps against the reference. The defaults
	// are the shipped config (windowed phase); these let a run force the global
	// tempogram or re-sweep the windowed/gate parameters without recompiling.
	envF := func(k string, dst *float64) {
		if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
			*dst = v
		}
	}
	switch os.Getenv("VYNULL_BEAT_REF_PHASE") {
	case "windowed":
		WindowedPhase = true
	case "global":
		WindowedPhase = false
	}
	envF("VYNULL_BEAT_REF_WINSEC", &WindowSec)
	envF("VYNULL_BEAT_REF_AMPW", &AmpWeight)
	envF("VYNULL_BEAT_REF_CLARW", &ClarityWeight)
	envF("VYNULL_BEAT_REF_GATE", &HalfBeatGate)
	t.Logf("windowed=%v winSec=%.1f ampW=%.1f clarW=%.1f gate=%.1f", WindowedPhase, WindowSec, AmpWeight, ClarityWeight, HalfBeatGate)

	type result struct {
		name    string
		e, frac float64 // folded phase error (ms) and as a fraction of a beat
		kick, P float64 // KickRatio diagnostic and beat period (ms)
		ok      bool
	}
	results := make([]result, len(gt))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				g := gt[idx]
				name := g.Title
				if name == "" {
					name = filepath.Base(g.File)
				}
				samples, err := DecodePCM(g.File, AnalysisRate)
				if err != nil || len(samples) == 0 {
					continue
				}
				r := DetectBeats(samples, AnalysisRate)
				if r == nil || len(r.Beats) == 0 || g.BPM <= 0 {
					continue
				}
				P := 60000.0 / g.BPM
				e := math.Mod(r.Beats[0]-g.FirstBeatMs, P)
				if e < -P/2 {
					e += P
				} else if e >= P/2 {
					e -= P
				}
				results[idx] = result{name: name, e: e, frac: e / P, kick: r.KickRatio, P: P, ok: true}
			}
		}()
	}
	for i := range gt {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var abs []float64
	var aligned10, aligned20, aligned50, halfBeat, n int
	var sumAbs, sumSigned float64
	worst := make([]result, 0, len(results))
	for _, r := range results {
		if !r.ok {
			continue
		}
		n++
		a := math.Abs(r.e)
		abs = append(abs, a)
		sumAbs += a
		sumSigned += r.e
		if a < 10 {
			aligned10++
		}
		if a < 20 {
			aligned20++
		}
		if a < 50 {
			aligned50++
		}
		if math.Abs(r.frac) > 0.4 {
			halfBeat++
		}
		worst = append(worst, r)
	}
	if n == 0 {
		t.Fatalf("no tracks scored (of %d in manifest) — audio missing/undecodable?", len(gt))
	}
	// Optional per-track dump for offline gate sweeps: err_ms,frac,kick_ratio,period_ms.
	if dp := os.Getenv("VYNULL_BEAT_REF_DUMP"); dp != "" {
		var sb strings.Builder
		sb.WriteString("err_ms,frac,kick,period_ms,name\n")
		for _, r := range results {
			if r.ok {
				sb.WriteString(strconv.FormatFloat(r.e, 'f', 2, 64) + "," +
					strconv.FormatFloat(r.frac, 'f', 4, 64) + "," +
					strconv.FormatFloat(r.kick, 'f', 4, 64) + "," +
					strconv.FormatFloat(r.P, 'f', 2, 64) + "," + r.name + "\n")
			}
		}
		if err := os.WriteFile(dp, []byte(sb.String()), 0o644); err != nil {
			t.Logf("dump write: %v", err)
		} else {
			t.Logf("dumped %d rows -> %s", n, dp)
		}
	}
	sort.Float64s(abs)
	sort.Slice(worst, func(i, j int) bool { return math.Abs(worst[i].e) > math.Abs(worst[j].e) })
	pct := func(x int) float64 { return 100 * float64(x) / float64(n) }

	t.Logf("beat-grid phase vs rekordbox — %d tracks scored (%d skipped)", n, len(gt)-n)
	t.Logf("  aligned  <10ms %.1f%%   <20ms %.1f%%   <50ms %.1f%%", pct(aligned10), pct(aligned20), pct(aligned50))
	t.Logf("  half-beat off (|Δ| > 0.4 beat): %.1f%%", pct(halfBeat))
	t.Logf("  |Δ| mean %.1f ms   median %.1f ms   bias %+.1f ms", sumAbs/float64(n), abs[n/2], sumSigned/float64(n))
	for i := 0; i < 10 && i < len(worst); i++ {
		t.Logf("    worst%2d: %+7.1f ms (%+.2f beat)  %s", i+1, worst[i].e, worst[i].frac, trunc(worst[i].name, 44))
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

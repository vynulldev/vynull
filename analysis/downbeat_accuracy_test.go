// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"sync"
	"testing"
)

// TestDownbeatAccuracy compares bar labels separately from tempo and beat
// timing. The phase-only accuracy test folds away whole beats, so it cannot
// detect a wrong beat 1. Use a gtgen manifest containing downbeat_ms (including
// an explicit zero for a downbeat at the beginning of the file):
//
//	VYNULL_DOWNBEAT_REF=/path/manifest.json go test ./analysis -run '^TestDownbeatAccuracy$' -v
//
// VYNULL_DOWNBEAT_REF_N and VYNULL_DOWNBEAT_REF_OFFSET select a subset;
// VYNULL_DOWNBEAT_REF_WORKERS defaults to 2 to bound decoded-audio memory.
func TestDownbeatAccuracy(t *testing.T) {
	path := os.Getenv("VYNULL_DOWNBEAT_REF")
	if path == "" {
		t.Skip("set VYNULL_DOWNBEAT_REF to a manifest with downbeat_ms")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var refs []struct {
		File        string   `json:"file"`
		BPM         float64  `json:"bpm"`
		FirstBeatMs float64  `json:"first_beat_ms"`
		DownbeatMs  *float64 `json:"downbeat_ms"`
		Title       string   `json:"title"`
	}
	if err := json.Unmarshal(data, &refs); err != nil {
		t.Fatal(err)
	}
	if off, _ := strconv.Atoi(os.Getenv("VYNULL_DOWNBEAT_REF_OFFSET")); off > 0 {
		refs = refs[min(off, len(refs)):]
	}
	if n, _ := strconv.Atoi(os.Getenv("VYNULL_DOWNBEAT_REF_N")); n > 0 {
		refs = refs[:min(n, len(refs))]
	}
	type score struct {
		skipped, tempo, phase bool
		before, after         bool
	}
	results := make([]score, len(refs))
	workers := 2
	if n, _ := strconv.Atoi(os.Getenv("VYNULL_DOWNBEAT_REF_WORKERS")); n > 0 {
		workers = n
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				g := refs[i]
				if g.DownbeatMs == nil || g.BPM <= 0 {
					results[i].skipped = true
					continue
				}
				x, err := DecodePCM(g.File, AnalysisRate)
				if err != nil {
					results[i].skipped = true
					continue
				}
				br := DetectBeatsWithEncoderDelay(x, AnalysisRate, EncoderDelayMs(g.File))
				if len(br.Beats) < 4 || math.Abs(br.BPM-g.BPM) > 0.05 {
					results[i].tempo = true
					continue
				}
				period := 60000 / g.BPM
				if math.Abs(math.Remainder(br.Beats[0]-g.FirstBeatMs, period)) > 50 {
					results[i].phase = true
					continue
				}
				correct := func(ms float64) bool {
					return int(math.Round((ms-*g.DownbeatMs)/period))%4 == 0
				}
				baseline := SilentLeadIndex(br.Beats, x, AnalysisRate)
				results[i].before = correct(br.Beats[baseline])
				results[i].after = correct(br.Downbeat)
			}
		}()
	}
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var scored, skipped, tempo, phase, before, after, fixed, broken int
	for i, r := range results {
		switch {
		case r.skipped:
			skipped++
		case r.tempo:
			tempo++
		case r.phase:
			phase++
		default:
			scored++
			if r.before {
				before++
			}
			if r.after {
				after++
			}
			if !r.before && r.after {
				fixed++
			}
			if r.before && !r.after {
				broken++
				t.Logf("regression: %s (%s)", refs[i].Title, refs[i].File)
			}
		}
	}
	if scored == 0 {
		t.Fatalf("no aligned tracks scored: missing %d, tempo %d, phase %d", skipped, tempo, phase)
	}
	t.Logf("scored %d; missing audio/labels %d, tempo mismatch %d, phase mismatch %d", scored, skipped, tempo, phase)
	t.Logf("bar exact: baseline %d (%.2f%%), current %d (%.2f%%); fixed %d, regressed %d",
		before, 100*float64(before)/float64(scored), after, 100*float64(after)/float64(scored), fixed, broken)
	if after < before {
		t.Error("downbeat agreement regressed against the opening-beat baseline")
	}
}

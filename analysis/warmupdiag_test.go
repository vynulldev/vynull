// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/json"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
)

// TestWarmupDiag quantifies the multiBandOnset EMA warmup spike's effect on
// windowedTempogramPhase, on real tracks. The band-norm EMA (mean[b] starts at
// 0, alpha 0.99) makes the first ~1.2s of flux frames hugely inflated; window
// 0's phasor magnitude is therefore inflated, and with AmpWeight=3 its combine
// weight may dominate the whole track. For each sampled manifest track this
// prints window 0's share of the total combine weight and how far the phase
// moves when the warmup-contaminated windows are excluded.
//
//	VYNULL_WARMUP_DIAG=/path/manifest.json go test ./analysis/ -run WarmupDiag -v
//
// VYNULL_WARMUP_DIAG_N caps the sample (default 60, stride-sampled across the
// manifest for genre spread).
func TestWarmupDiag(t *testing.T) {
	path := os.Getenv("VYNULL_WARMUP_DIAG")
	if path == "" {
		t.Skip("set VYNULL_WARMUP_DIAG to a reference manifest to run")
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
	nSample := 60
	if v, _ := strconv.Atoi(os.Getenv("VYNULL_WARMUP_DIAG_N")); v > 0 {
		nSample = v
	}
	stride := len(gt) / nSample
	if stride < 1 {
		stride = 1
	}
	var sample []int
	for i := 0; i < len(gt) && len(sample) < nSample; i += stride {
		sample = append(sample, i)
	}

	const warmupMs = 1500.0 // exclude windows starting inside the EMA warmup

	type res struct {
		w0Share  float64 // window 0's fraction of total combine weight
		magRatio float64 // window 0 phasor magnitude / median other-window magnitude
		deltaMs  float64 // |phase(all) - phase(excluding warmup windows)| folded
		nWin     int
		ok       bool
	}
	results := make([]res, len(sample))

	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < runtime.NumCPU()/2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for si := range jobs {
				g := gt[sample[si]]
				if g.BPM <= 0 {
					continue
				}
				samples, err := DecodePCM(g.File, AnalysisRate)
				if err != nil || len(samples) == 0 {
					continue
				}
				onset, msPerFrame := multiBandOnset(samples, AnalysisRate)
				if onset == nil {
					continue
				}
				msPerBeat := 60000.0 / g.BPM
				period := msPerBeat / msPerFrame
				if period <= 1 || len(onset) < int(period*2) {
					continue
				}
				w2pi := 2 * math.Pi / period
				winFrames := int(WindowSec * 1000 / msPerFrame)
				if min := int(period * 2); winFrames < min {
					winFrames = min
				}
				hop := winFrames / 2
				if hop < 1 {
					hop = 1
				}
				var weights, mags []float64
				var ZrAll, ZiAll, ZrLate, ZiLate float64
				for start := 0; start+winFrames <= len(onset); start += hop {
					var zr, zi, esum float64
					for n := start; n < start+winFrames; n++ {
						e := onset[n]
						zr += e * math.Cos(w2pi*float64(n))
						zi += e * math.Sin(w2pi*float64(n))
						esum += e
					}
					mag := math.Hypot(zr, zi)
					if mag < 1e-12 || esum < 1e-12 {
						weights = append(weights, 0)
						mags = append(mags, 0)
						continue
					}
					weight := math.Pow(mag, AmpWeight) * math.Pow(mag/esum, ClarityWeight)
					weights = append(weights, weight)
					mags = append(mags, mag)
					ZrAll += weight * zr / mag
					ZiAll += weight * zi / mag
					if float64(start)*msPerFrame >= warmupMs {
						ZrLate += weight * zr / mag
						ZiLate += weight * zi / mag
					}
				}
				if len(weights) < 3 {
					continue
				}
				var wsum float64
				for _, x := range weights {
					wsum += x
				}
				others := append([]float64(nil), mags[1:]...)
				sort.Float64s(others)
				medOther := others[len(others)/2]
				phase := func(zr, zi float64) (float64, bool) {
					if zr == 0 && zi == 0 {
						return 0, false
					}
					n0 := math.Mod(math.Atan2(zi, zr)/w2pi, period)
					if n0 < 0 {
						n0 += period
					}
					return n0 * msPerFrame, true
				}
				pAll, ok1 := phase(ZrAll, ZiAll)
				pLate, ok2 := phase(ZrLate, ZiLate)
				if !ok1 || !ok2 {
					continue
				}
				d := math.Mod(pAll-pLate, msPerBeat)
				if d < -msPerBeat/2 {
					d += msPerBeat
				} else if d >= msPerBeat/2 {
					d -= msPerBeat
				}
				r := res{
					w0Share: weights[0] / (wsum + 1e-30),
					deltaMs: math.Abs(d),
					nWin:    len(weights),
					ok:      true,
				}
				if medOther > 0 {
					r.magRatio = mags[0] / medOther
				}
				results[si] = r
			}
		}()
	}
	for i := range sample {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var shares, ratios, deltas []float64
	dom50, dom90, moved20, moved50 := 0, 0, 0, 0
	for _, r := range results {
		if !r.ok {
			continue
		}
		shares = append(shares, r.w0Share)
		ratios = append(ratios, r.magRatio)
		deltas = append(deltas, r.deltaMs)
		if r.w0Share > 0.5 {
			dom50++
		}
		if r.w0Share > 0.9 {
			dom90++
		}
		if r.deltaMs > 20 {
			moved20++
		}
		if r.deltaMs > 50 {
			moved50++
		}
	}
	n := len(shares)
	if n == 0 {
		t.Fatal("no tracks scored")
	}
	sort.Float64s(shares)
	sort.Float64s(ratios)
	sort.Float64s(deltas)
	pct := func(c int) float64 { return 100 * float64(c) / float64(n) }
	t.Logf("EMA warmup diagnostic — %d tracks", n)
	t.Logf("  window-0 weight share: median %.3f  p90 %.3f  max %.3f", shares[n/2], shares[n*9/10], shares[n-1])
	t.Logf("  window-0 dominates (>50%% weight): %.1f%%   (>90%%): %.1f%%", pct(dom50), pct(dom90))
	t.Logf("  window-0 mag / median other mag: median %.1fx  p90 %.1fx", ratios[n/2], ratios[n*9/10])
	t.Logf("  phase shift when excluding warmup windows: median %.1f ms  p90 %.1f ms", deltas[n/2], deltas[n*9/10])
	t.Logf("  tracks where phase moves >20ms: %.1f%%   >50ms: %.1f%%", pct(moved20), pct(moved50))
}

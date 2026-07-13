// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/json"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestPhaseDiag is a diagnostic sweep for the half-beat ambiguity. For each
// track it computes, per candidate onset envelope, the SELF-FLIP ratio
// s = comb(ourPhase) / comb(ourPhase+P/2): s < 1 means that envelope votes to
// flip our grid by half a beat. Evaluated against the reference error frac, a
// good discriminator has s < 1 on half-beat-off tracks and s > 1 on aligned
// ones. Also dumps each track's first-strong-onset phase distance to our grid
// and to the reference grid, to test "rekordbox anchors a beat on the first
// onset". Env-gated; writes CSV to VYNULL_PHASE_DIAG_OUT.
//
//	VYNULL_PHASE_DIAG=/path/manifest.json VYNULL_PHASE_DIAG_OUT=/tmp/diag.csv \
//	  go test ./analysis/ -run PhaseDiag -v -timeout 30m
func TestPhaseDiag(t *testing.T) {
	path := os.Getenv("VYNULL_PHASE_DIAG")
	if path == "" {
		t.Skip("set VYNULL_PHASE_DIAG to a reference manifest to run")
	}
	outPath := os.Getenv("VYNULL_PHASE_DIAG_OUT")
	if outPath == "" {
		outPath = "/tmp/phasediag.csv"
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
	if lim, _ := strconv.Atoi(os.Getenv("VYNULL_PHASE_DIAG_N")); lim > 0 && lim < len(gt) {
		gt = gt[:lim]
	}
	workers := runtime.NumCPU()

	names := []string{"s_mb", "s_mblog", "s_low", "s_full", "s_hfc", "s_intro", "s_kick"}
	type row struct {
		name           string
		frac, period   float64
		s              [7]float64
		dFirstO, dFRef float64 // first-onset phase distance to ours / reference, in beats
		ok             bool
	}
	rows := make([]row, len(gt))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				g := gt[idx]
				samples, err := DecodePCM(g.File, AnalysisRate)
				if err != nil || len(samples) == 0 || g.BPM <= 0 {
					continue
				}
				res := DetectBeatsWithEncoderDelay(samples, AnalysisRate, EncoderDelayMs(g.File))
				if res == nil || len(res.Beats) == 0 {
					continue
				}
				P := 60000.0 / g.BPM
				e := math.Mod(res.Beats[0]-g.FirstBeatMs, P)
				if e < -P/2 {
					e += P
				} else if e >= P/2 {
					e -= P
				}

				// Envelope-domain phases: shift the grid phases by the pipeline+
				// encoder latency so combs land on envelope peaks; identical shift
				// for both candidates, so ratios compare like with like.
				lat := TempogramLatencyMs + EncoderDelayMs(g.File)
				pOurs := math.Mod(res.Beats[0]+lat, P)
				pRef := math.Mod(g.FirstBeatMs+lat, P)

				mb, mbMs := multiBandOnset(samples, AnalysisRate)
				if mb == nil {
					continue
				}
				mblog, _ := multiBandLogOnset(samples, AnalysisRate)
				low := fluxEnvelope(samples, AnalysisRate, 150.0)
				full := fluxEnvelope(samples, AnalysisRate, 0)
				hfc := hfcEnvelope(samples, AnalysisRate)
				intro := mb
				if n := int(45000.0 / mbMs); n < len(mb) {
					intro = mb[:n]
				}
				kick := kickSTFTEnvelope(samples, AnalysisRate, 180.0)

				envs := [][]float64{mb, mblog, low, full, hfc, intro, kick}
				var r row
				r.name, r.frac, r.period, r.ok = g.Title, e/P, P, true
				for i, env := range envs {
					a := combWide(env, mbMs, pOurs, P)
					b := combWide(env, mbMs, pOurs+P/2, P)
					r.s[i] = (a + 1e-9) / (b + 1e-9)
				}

				// First strong onset (multiband): first frame reaching 40% of the
				// envelope's 99th-percentile level; its phase distance to each grid,
				// folded to [0, 0.5] beats.
				if fo := firstStrongOnset(mb); fo >= 0 {
					foMs := float64(fo) * mbMs
					r.dFirstO = beatDist(foMs, pOurs, P)
					r.dFRef = beatDist(foMs, pRef, P)
				} else {
					r.dFirstO, r.dFRef = -1, -1
				}
				rows[idx] = r
			}
		}()
	}
	for i := range gt {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var sb strings.Builder
	sb.WriteString("frac,period_ms," + strings.Join(names, ",") + ",d_first_ours,d_first_ref,name\n")
	n := 0
	for _, r := range rows {
		if !r.ok {
			continue
		}
		n++
		sb.WriteString(strconv.FormatFloat(r.frac, 'f', 4, 64) + "," + strconv.FormatFloat(r.period, 'f', 2, 64))
		for _, s := range r.s {
			sb.WriteString("," + strconv.FormatFloat(s, 'f', 4, 64))
		}
		sb.WriteString("," + strconv.FormatFloat(r.dFirstO, 'f', 4, 64) +
			"," + strconv.FormatFloat(r.dFRef, 'f', 4, 64) + "," + r.name + "\n")
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %d rows -> %s", n, outPath)
}

// beatDist folds |tMs - phase| to the nearest comb point and returns the
// distance in beats, in [0, 0.5].
func beatDist(tMs, phaseMs, periodMs float64) float64 {
	d := math.Mod(tMs-phaseMs, periodMs)
	if d < 0 {
		d += periodMs
	}
	if d > periodMs/2 {
		d = periodMs - d
	}
	return d / periodMs
}

// firstStrongOnset returns the first frame reaching 40% of the envelope's
// 99th-percentile level, or -1.
func firstStrongOnset(env []float64) int {
	if len(env) == 0 {
		return -1
	}
	sorted := append([]float64(nil), env...)
	sort.Float64s(sorted)
	p99 := sorted[len(sorted)*99/100]
	thr := 0.4 * p99
	for i, v := range env {
		if v >= thr {
			return i
		}
	}
	return -1
}

// multiBandLogOnset is multiBandOnset with log-compressed band magnitudes and no
// EMA normalization: flux of log(1+mag) per band, summed. Log compression is a
// candidate for how rekordbox weighs quiet-band onsets; if its phase estimator
// runs on log-domain flux, the beat-frequency phase can differ from linear flux
// on exactly the tracks whose off-beat carries the loud energy.
func multiBandLogOnset(samples []float32, sampleRate int) ([]float64, float64) {
	const frameSize, hop = 1024, 512
	n := (len(samples) - frameSize) / hop
	if n < 4 {
		return nil, 0
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(frameSize-1)))
	}
	windows := make([][]float64, n)
	for k := 0; k < n; k++ {
		off := k * hop
		w := make([]float64, frameSize)
		for j := 0; j < frameSize; j++ {
			w[j] = float64(samples[off+j]) * win[j]
		}
		windows[k] = w
	}
	re, im := batchFFT(windows, frameSize)

	freqPerBin := float64(sampleRate) / float64(frameSize)
	nyq := float64(sampleRate) / 2
	nb := len(rbBandEdgesHz)
	hiBin := make([]int, nb)
	for b := 0; b < nb; b++ {
		hz := rbBandEdgesHz[b]
		if hz > nyq {
			hz = nyq
		}
		hb := int(hz / freqPerBin)
		if hb > frameSize/2 {
			hb = frameSize / 2
		}
		hiBin[b] = hb
	}

	onset := make([]float64, n)
	prev := make([]float64, nb)
	cur := make([]float64, nb)
	for k := 0; k < n && k < len(re); k++ {
		lo := 1
		for b := 0; b < nb; b++ {
			var s float64
			for bin := lo; bin < hiBin[b]; bin++ {
				s += math.Sqrt(re[k][bin]*re[k][bin] + im[k][bin]*im[k][bin])
			}
			cur[b] = math.Log1p(s * 1000) // scale into log1p's sensitive range
			lo = hiBin[b]
		}
		if k > 0 {
			var flux float64
			for b := 0; b < nb; b++ {
				if d := cur[b] - prev[b]; d > 0 {
					flux += d
				}
			}
			onset[k] = flux
		}
		copy(prev, cur)
	}
	return onset, float64(hop) / float64(sampleRate) * 1000.0
}

// hfcEnvelope is a high-frequency-content weighted flux (bin-index weighting),
// emphasizing hats/snares over kick: if rekordbox anchors relative to the
// back-beat layer instead of the kick, this envelope would know.
func hfcEnvelope(samples []float32, rate int) []float64 {
	const frameSize, hop = 1024, 512
	n := (len(samples) - frameSize) / hop
	if n < 4 {
		return nil
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(frameSize-1)))
	}
	windows := make([][]float64, n)
	for k := 0; k < n; k++ {
		off := k * hop
		w := make([]float64, frameSize)
		for j := 0; j < frameSize; j++ {
			w[j] = float64(samples[off+j]) * win[j]
		}
		windows[k] = w
	}
	re, im := batchFFT(windows, frameSize)
	env := make([]float64, n)
	prev := 0.0
	for k := 0; k < n && k < len(re); k++ {
		var s float64
		for bin := 1; bin < frameSize/2; bin++ {
			s += float64(bin) * math.Sqrt(re[k][bin]*re[k][bin]+im[k][bin]*im[k][bin])
		}
		if k > 0 && s > prev {
			env[k] = s - prev
		}
		prev = s
	}
	return env
}

// kickSTFTEnvelope is the half-wave-rectified flux of the STFT magnitude summed
// over bins below maxHz: a sharp sub-bass kick onset with a steep bin cutoff,
// unlike the smeared single-pole variant in fluxEnvelope. On the half-beat
// specimen that motivated it, this envelope discriminates the two phases nearly
// 6:1 where the single-pole one is unstable.
func kickSTFTEnvelope(samples []float32, rate int, maxHz float64) []float64 {
	const frameSize, hop = 1024, 512
	n := (len(samples) - frameSize) / hop
	if n < 4 {
		return nil
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(frameSize-1)))
	}
	windows := make([][]float64, n)
	for k := 0; k < n; k++ {
		off := k * hop
		w := make([]float64, frameSize)
		for j := 0; j < frameSize; j++ {
			w[j] = float64(samples[off+j]) * win[j]
		}
		windows[k] = w
	}
	re, im := batchFFT(windows, frameSize)
	hi := int(maxHz / (float64(rate) / frameSize))
	if hi < 2 {
		hi = 2
	}
	env := make([]float64, n)
	prev := 0.0
	for k := 0; k < n && k < len(re); k++ {
		var s float64
		for bin := 1; bin < hi && bin < len(re[k]); bin++ {
			s += math.Sqrt(re[k][bin]*re[k][bin] + im[k][bin]*im[k][bin])
		}
		if k > 0 && s > prev {
			env[k] = s - prev
		}
		prev = s
	}
	return env
}

// fluxEnvelope builds a half-wave-rectified frame-RMS flux envelope at the same
// 1024/512 framing as multiBandOnset. cutoffHz > 0 low-passes first (sub-bass).
func fluxEnvelope(samples []float32, rate int, cutoffHz float64) []float64 {
	src := samples
	if cutoffHz > 0 {
		src = lowPass(samples, float64(rate), cutoffHz)
	}
	const frameSize, hop = 1024, 512
	n := (len(src) - frameSize) / hop
	if n < 4 {
		return nil
	}
	env := make([]float64, n)
	prev := 0.0
	for k := 0; k < n; k++ {
		off := k * hop
		var s float64
		for j := 0; j < frameSize; j++ {
			v := float64(src[off+j])
			s += v * v
		}
		rms := math.Sqrt(s / frameSize)
		if k > 0 && rms > prev {
			env[k] = rms - prev
		}
		prev = rms
	}
	return env
}

// combWide sums an envelope on the beat comb at phaseMs, peak-picking a wider
// ±3-frame window than combEnergy so latency-centering error is tolerated.
func combWide(env []float64, msPerFrame, phaseMs, periodMs float64) float64 {
	if periodMs <= 0 || msPerFrame <= 0 || len(env) == 0 {
		return 0
	}
	dur := float64(len(env)) * msPerFrame
	start := math.Mod(phaseMs, periodMs)
	if start < 0 {
		start += periodMs
	}
	var sum float64
	for t := start; t < dur; t += periodMs {
		f := int(math.Round(t / msPerFrame))
		best := 0.0
		for d := -3; d <= 3; d++ {
			if i := f + d; i >= 0 && i < len(env) && env[i] > best {
				best = env[i]
			}
		}
		sum += best
	}
	return sum
}

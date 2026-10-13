// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

// Encoder produces the Pioneer wire-format blobs for a track. AnalyzeTrack runs
// the DSP and then delegates encoding to the installed Encoder, so the encoders
// can live outside this package (link/prolink) without inverting the
// dependency — analysis knows only this interface.
type Encoder interface {
	// Encode fills the wire-format fields on r (waveforms, beat grid, PQT2,
	// PSSI) from the decoded samples and the DSP results already set on r
	// (BPM, Beats, DownbeatIndex, Phrases). beats carries the full beat result
	// (its Downbeat ms is needed by the beat-grid encoder).
	Encode(samples []float32, sampleRate int, r *Result, beats *BeatResult)
}

// waveformEncoder is the installed encoder. It defaults to nil now that the
// built-in Pioneer encoder lives in link/prolink; the process must install it
// via SetEncoder (main does this at startup) before AnalyzeTrack runs.
var waveformEncoder Encoder

// SetEncoder installs the wire-format encoder used by AnalyzeTrack.
func SetEncoder(e Encoder) { waveformEncoder = e }

// GridEncoder is optionally implemented by an Encoder to rebuild only the
// beat-grid blobs from a Result's Beats, e.g. after the grid is shifted by the
// user grid offset without a full re-analysis.
type GridEncoder interface {
	EncodeGrid(r *Result)
}

// regenerateGrid rebuilds r's beat-grid blobs from r.Beats via the installed
// encoder, or the brand-neutral blob when no grid encoder is installed.
func regenerateGrid(r *Result) {
	if g, ok := waveformEncoder.(GridEncoder); ok {
		g.EncodeGrid(r)
		return
	}
	r.BeatGridPQT2 = nil
	if len(r.Beats) == 0 {
		r.BeatGrid = nil
		return
	}
	db := r.Beats[0]
	if r.DownbeatIndex >= 0 && r.DownbeatIndex < len(r.Beats) {
		db = r.Beats[r.DownbeatIndex]
	}
	r.BeatGrid = GenerateBeatGridFromBeats(&BeatResult{BPM: r.BPM, Beats: r.Beats, Downbeat: db})
}

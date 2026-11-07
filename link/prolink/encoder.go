// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"context"
	"math"

	"github.com/vynulldev/vynull/analysis"
)

// NewEncoder returns the built-in Pro DJ Link wire-format encoder. Install it
// into analysis via analysis.SetEncoder(prolink.NewEncoder()) at startup so
// AnalyzeTrack produces the Pioneer blobs.
func NewEncoder() analysis.Encoder { return pioneerEncoder{} }

// pioneerEncoder is the built-in Pro DJ Link encoder. The logic here is exactly
// the encode sequence AnalyzeTrack used inline, so the output is byte-for-byte
// unchanged.
type pioneerEncoder struct{}

func (pioneerEncoder) Encode(ctx context.Context, samples []float32, sampleRate int, r *analysis.Result, beats *analysis.BeatResult) error {
	durationMs := float64(len(samples)) / float64(sampleRate) * 1000.0

	// Each generator takes seconds on an hour-long mix: check ctx between them.
	if err := ctx.Err(); err != nil {
		return err
	}
	// The detail waveforms start r.WaveLeadMs of pre-roll earlier: the audio
	// the deck plays before our decode's first sample, so they line up with
	// the grid in the deck's timeline. The previews spread the track over a
	// few hundred entries, so the lead is a fraction of one there.
	detail := withLead(GenerateDetail(samples, sampleRate), r.WaveLeadMs, []byte{0xff, 0x80})
	if err := ctx.Err(); err != nil {
		return err
	}
	r.WaveDetail = detail
	r.WaveDetailMono = GenerateDetailMono(detail)
	r.WavePreview = GeneratePreview(samples, sampleRate)
	r.WavePreviewANLZ = GeneratePreviewANLZ(samples, sampleRate)
	r.WaveTinyANLZ = GenerateTinyPreviewANLZ(samples)
	if err := ctx.Err(); err != nil {
		return err
	}
	r.WaveColorPreview = GenerateColorPreview(samples, sampleRate)
	if err := ctx.Err(); err != nil {
		return err
	}
	r.WaveDetail3Band = withLead(GenerateDetail3Band(samples, sampleRate), r.WaveLeadMs, []byte{0, 0, 0})
	if err := ctx.Err(); err != nil {
		return err
	}
	r.WavePreview3Band = GeneratePreview3Band(samples, sampleRate)

	// Beat grid: prefer detected beat positions, else fall back to BPM.
	if len(beats.Beats) > 0 {
		r.BeatGrid = analysis.GenerateBeatGridFromBeats(beats)
		r.BeatGridPQT2 = GeneratePQT2(r.BPM, beats.Beats, r.DownbeatIndex)
	} else {
		r.BeatGrid = analysis.GenerateBeatGrid(r.BPM, durationMs, 0)
	}

	r.SongStructure = GeneratePSSI(r.Phrases, r.BPM)
	return nil
}

// EncodeGrid rebuilds just the beat-grid blobs from r.Beats (analysis.GridEncoder),
// used when a cached grid is shifted by the user grid offset.
func (pioneerEncoder) EncodeGrid(r *analysis.Result) {
	if len(r.Beats) == 0 {
		return
	}
	db := r.Beats[0]
	if r.DownbeatIndex >= 0 && r.DownbeatIndex < len(r.Beats) {
		db = r.Beats[r.DownbeatIndex]
	}
	r.BeatGrid = analysis.GenerateBeatGridFromBeats(&analysis.BeatResult{BPM: r.BPM, Beats: r.Beats, Downbeat: db})
	r.BeatGridPQT2 = GeneratePQT2(r.BPM, r.Beats, r.DownbeatIndex)
}

// withLead prepends leadMs of pre-roll, as entries of silence (one entry
// each), to a detail waveform at analysis.DetailEntriesPerSec.
func withLead(wave []byte, leadMs float64, silence []byte) []byte {
	n := int(math.Round(leadMs * analysis.DetailEntriesPerSec / 1000))
	if n <= 0 || len(wave) == 0 {
		return wave
	}
	out := make([]byte, 0, n*len(silence)+len(wave))
	for range n {
		out = append(out, silence...)
	}
	return append(out, wave...)
}

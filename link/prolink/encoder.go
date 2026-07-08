// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import "github.com/vynulldev/vynull/analysis"

// NewEncoder returns the built-in Pro DJ Link wire-format encoder. Install it
// into analysis via analysis.SetEncoder(prolink.NewEncoder()) at startup so
// AnalyzeTrack produces the Pioneer blobs.
func NewEncoder() analysis.Encoder { return pioneerEncoder{} }

// pioneerEncoder is the built-in Pro DJ Link encoder. The logic here is exactly
// the encode sequence AnalyzeTrack used inline, so the output is byte-for-byte
// unchanged.
type pioneerEncoder struct{}

func (pioneerEncoder) Encode(samples []float32, sampleRate int, r *analysis.Result, beats *analysis.BeatResult) {
	durationMs := float64(len(samples)) / float64(sampleRate) * 1000.0

	detail := GenerateDetail(samples, sampleRate)
	r.WaveDetail = detail
	r.WaveDetailMono = GenerateDetailMono(detail)
	r.WavePreview = GeneratePreview(samples, sampleRate)
	r.WavePreviewANLZ = GeneratePreviewANLZ(samples, sampleRate)
	r.WaveTinyANLZ = GenerateTinyPreviewANLZ(samples)
	r.WaveColorPreview = GenerateColorPreview(samples, sampleRate)
	r.WaveDetail3Band = GenerateDetail3Band(samples, sampleRate)
	r.WavePreview3Band = GeneratePreview3Band(samples, sampleRate)

	// Beat grid: prefer detected beat positions, else fall back to BPM.
	if len(beats.Beats) > 0 {
		r.BeatGrid = analysis.GenerateBeatGridFromBeats(beats)
		r.BeatGridPQT2 = GeneratePQT2(r.BPM, beats.Beats, r.DownbeatIndex)
	} else {
		r.BeatGrid = analysis.GenerateBeatGrid(r.BPM, durationMs, 0)
	}

	r.SongStructure = GeneratePSSI(r.Phrases, r.BPM)
}

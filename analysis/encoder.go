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

// waveformEncoder is the installed encoder. It defaults to the built-in Pioneer
// encoder; a backend replaces it via SetEncoder once the encoders move to
// link/prolink.
var waveformEncoder Encoder = pioneerEncoder{}

// SetEncoder installs the wire-format encoder used by AnalyzeTrack.
func SetEncoder(e Encoder) { waveformEncoder = e }

// pioneerEncoder is the built-in Pro DJ Link encoder. It will move to
// link/prolink in the next step; the logic here is exactly the encode sequence
// AnalyzeTrack used inline, so the output is byte-for-byte unchanged.
type pioneerEncoder struct{}

func (pioneerEncoder) Encode(samples []float32, sampleRate int, r *Result, beats *BeatResult) {
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
		r.BeatGrid = GenerateBeatGridFromBeats(beats)
		r.BeatGridPQT2 = GeneratePQT2(r.BPM, beats.Beats, r.DownbeatIndex)
	} else {
		r.BeatGrid = GenerateBeatGrid(r.BPM, durationMs, 0)
	}

	r.SongStructure = GeneratePSSI(r.Phrases, r.BPM)
}

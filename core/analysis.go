// SPDX-License-Identifier: GPL-3.0-or-later

package core

// Analysis is the brand-agnostic result of DSP on a track: tempo, beat grid,
// waveforms as per-band amplitude data, key, and detected cues. Each backend
// encodes it to its own wire format — Pioneer PWV/ANLZ, Engine zlib BLOBs — so
// the DSP runs once and the cache can store neutral data.
type Analysis struct {
	BPM      float64
	Beats    []Beat // full beat grid
	Detail   BandWaveform
	Overview BandWaveform
	Key      Key
	Cues     []Cue
	Phrases  []Phrase // song structure (intro/verse/chorus/…), optional
}

// Beat is one beat in the grid.
type Beat struct {
	TimeMs   float64
	Downbeat bool // true on beat 1 of the bar
}

// BandWaveform is a waveform as per-point amplitude split across frequency
// bands (bass/mid/treble), each 0..1. Backends map this onto their own colour-
// waveform encodings; PointsPerSec records the sampling density.
type BandWaveform struct {
	PointsPerSec float64
	Bass         []float32
	Mid          []float32
	Treble       []float32
}

// Phrase is a detected song-structure section.
type Phrase struct {
	StartMs float64
	EndMs   float64
	Kind    string // "intro", "verse", "chorus", "outro", …
}

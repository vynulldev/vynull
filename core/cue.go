// SPDX-License-Identifier: GPL-3.0-or-later

package core

// CueKind distinguishes a plain cue from a saved loop.
type CueKind uint8

const (
	KindCue  CueKind = iota + 1 // a hot or memory cue
	KindLoop                    // a saved loop (has an end)
)

// Cue is a hot cue, memory cue, or saved loop on a track. TimeMs is the start;
// for a loop, LoopEndMs is the end.
type Cue struct {
	Number    uint16  `json:"number"` // 1-based hot-cue slot (A=1, B=2, …); 0 for memory cues
	Kind      CueKind `json:"kind"`
	TimeMs    uint32  `json:"time_ms"`
	LoopEndMs uint32  `json:"loop_end_ms,omitempty"`
	Color     uint32  `json:"color,omitempty"` // packed RGB or palette index
	Comment   string  `json:"comment,omitempty"`
}

// Key is a musical key in both notations; a backend shows whichever its players
// use (Camelot "8A" / standard "Am").
type Key struct {
	Camelot  string `json:"camelot"`
	Standard string `json:"standard"`
}

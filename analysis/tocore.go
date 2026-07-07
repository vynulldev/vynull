// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "github.com/vynulldev/vynull/core"

// phraseKindName maps the mood_high_phrase enum to the neutral labels used in
// core.Analysis (mirrors the API's phrase naming in api/api.go).
var phraseKindName = map[uint16]string{
	1: "intro", 2: "up", 3: "down", 5: "chorus", 6: "outro",
}

// Core projects the Pioneer-flavoured Result onto the brand-neutral
// core.Analysis — the DSP facts (tempo, beat grid, key, song structure) a
// backend re-encodes into its own wire format.
//
// Step 2 of the modular-backends refactor (docs/design/modular-backends.md):
// this is the DSP→neutral half. Waveform band data (core.BandWaveform) and cues
// are not carried yet; they arrive with the encoder split, when the PWV/ANLZ
// packers move out of this package and consume core.Analysis instead of raw
// samples.
func (r *Result) Core() *core.Analysis {
	a := &core.Analysis{
		BPM: r.BPM,
		Key: core.Key{Camelot: r.KeyCamelot, Standard: r.KeyStandard},
	}
	if len(r.Beats) > 0 {
		a.Beats = make([]core.Beat, len(r.Beats))
		for i, ms := range r.Beats {
			// Beats are labelled 1..4 repeating; DownbeatIndex marks the first
			// beat 1. Fold the offset into [0,4) so beats before it classify too.
			a.Beats[i] = core.Beat{
				TimeMs:   ms,
				Downbeat: ((i-r.DownbeatIndex)%4+4)%4 == 0,
			}
		}
	}
	for _, p := range r.Phrases {
		a.Phrases = append(a.Phrases, core.Phrase{
			StartMs: p.StartMs,
			EndMs:   p.EndMs,
			Kind:    phraseKindName[p.Kind],
		})
	}
	return a
}

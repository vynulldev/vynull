// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "github.com/vynulldev/vynull/core"

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
	for _, p := range r.RekordboxPhrases() {
		_, section := PhraseLabel(p)
		a.Phrases = append(a.Phrases, core.Phrase{
			StartMs: p.StartMs,
			EndMs:   p.EndMs,
			Kind:    section,
		})
	}
	return a
}

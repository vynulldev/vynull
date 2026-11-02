// SPDX-License-Identifier: GPL-3.0-or-later

package library

import "time"

// AnalysisFacts are the values an analysis result gives its track.
type AnalysisFacts struct {
	BPM        float64
	Duration   int // seconds
	KeyCamelot string
}

// ApplyAnalysis fills a track's analysis-derived fields from an analysis
// result, the one write-back every analysis path uses. Duration and key are
// filled when empty, and the bitrate is estimated from size and duration when
// unknown, so edits are never clobbered. BPM is filled when empty and also
// follows a re-analysis that changed it (a cache-version bump, say) unless
// the user set it, which DetectedBPM marks: the beat grid served to decks
// always comes from the analysis, so the track must show the tempo they play.
// The library saves only when something changed, which it reports.
func (l *Library) ApplyAnalysis(id uint32, a AnalysisFacts) bool {
	l.mu.Lock()
	changed := false
	if t := l.byID[id]; t != nil {
		changed = a.applyTo(t)
		// A library over a music dir keeps only edits (see UseOverlay). What
		// analysis fills in is the music dir's, not an edit: it goes into
		// the track as built too, so a fresh export's values still apply.
		if l.overlay != nil {
			if b, ok := l.overlay.base[id]; ok {
				a.applyTo(&b.track)
				l.overlay.base[id] = b
			}
		}
	}
	l.mu.Unlock()
	if changed {
		l.Save()
	}
	return changed
}

// applyTo fills t's analysis-derived fields as ApplyAnalysis describes,
// reporting whether any changed.
func (a AnalysisFacts) applyTo(t *Track) bool {
	changed := false
	if a.BPM > 0 && t.BPM != a.BPM && (t.BPM == 0 || t.DetectedBPM == 0) {
		t.BPM = a.BPM
		changed = true
	}
	if t.Duration == 0 && a.Duration > 0 {
		t.Duration = DurationSec(time.Duration(a.Duration) * time.Second)
		changed = true
	}
	if t.Key == "" && a.KeyCamelot != "" {
		t.Key = a.KeyCamelot
		changed = true
	}
	if secs := int64(t.Duration.Seconds()); t.Bitrate == 0 && t.FileSize > 0 && secs > 0 {
		t.Bitrate = int(t.FileSize * 8 / secs / 1000)
		changed = true
	}
	return changed
}

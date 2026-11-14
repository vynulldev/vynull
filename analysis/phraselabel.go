// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "strings"

// phraselabel.go names phrases as rekordbox does, from its song structure's
// mood and, in High mood, the variant flags (Deep Symmetry's ANLZ analysis,
// "Phrase labels"). Vynull's own phrases carry no mood or variant.

// midLabels and lowLabels name the Mid and Low moods' phrase kinds 1-10.
var (
	midLabels = [11]string{1: "Intro", 2: "Verse 1", 3: "Verse 2", 4: "Verse 3", 5: "Verse 4",
		6: "Verse 5", 7: "Verse 6", 8: "Bridge", 9: "Chorus", 10: "Outro"}
	lowLabels = [11]string{1: "Intro", 2: "Verse 1", 3: "Verse 1", 4: "Verse 1", 5: "Verse 2",
		6: "Verse 2", 7: "Verse 2", 8: "Bridge", 9: "Chorus", 10: "Outro"}
	highLabels = map[uint16]string{1: "Intro", 2: "Up", 3: "Down", 5: "Chorus", 6: "Outro"}
)

// PhraseLabel returns a phrase's label, such as "Verse 2" or "Chorus 1", and
// its section, the label's kind in lower case without a number ("verse",
// "chorus"), which UIs colour by. Both are empty for a kind its mood doesn't
// have. Vynull's own phrases (mood 0) use the High names without a variant.
func PhraseLabel(p Phrase) (label, section string) {
	switch p.Mood {
	case 2, 3:
		table := &midLabels
		if p.Mood == 3 {
			table = &lowLabels
		}
		if int(p.Kind) < len(table) {
			label = table[p.Kind]
		}
	default:
		label = highLabels[p.Kind]
		if p.Mood == 1 && label != "" {
			label += highVariant(p)
		}
	}
	return label, strings.ToLower(strings.TrimRight(label, " 0123456789"))
}

// highVariant is the number a High-mood phrase's flags add to its label: k1
// picks Intro, Chorus and Outro 1 (set) or 2; k3 and k2 pick Up 2 and Up 3.
func highVariant(p Phrase) string {
	switch {
	case p.Kind == 3:
		return ""
	case p.Kind == 2 && p.K2 != 0:
		return " 3"
	case p.Kind == 2 && p.K3 != 0:
		return " 2"
	case p.Kind == 2, p.K1 != 0:
		return " 1"
	}
	return " 2"
}

// RekordboxPhrases returns r's phrases, with the mood, variant flags and fill
// of rekordbox's song structure filled in for an imported analysis cached
// before phrases kept them. A phrase that no longer matches its song
// structure entry (Vynull has since made its own) is left without.
func (r *Result) RekordboxPhrases() []Phrase {
	if !r.Imported || len(r.Phrases) == 0 || r.Phrases[0].Mood != 0 || len(r.SongStructure) == 0 {
		return r.Phrases
	}
	parsed := ParsePSSI(r.SongStructure)
	if len(parsed) != len(r.Phrases) {
		return r.Phrases
	}
	out := make([]Phrase, len(r.Phrases))
	for i, p := range r.Phrases {
		if q := parsed[i]; q.StartBeat == p.StartBeat && q.Kind == p.Kind {
			p.Mood, p.K1, p.K2, p.K3, p.FillBeat = q.Mood, q.K1, q.K2, q.K3, q.FillBeat
		}
		out[i] = p
	}
	return out
}

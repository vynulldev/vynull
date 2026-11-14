// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"testing"
)

// pssiEntry is one phrase of a song structure test blob.
type pssiEntry struct {
	beat, kind     uint16
	k1, k2, k3, b  uint8
	fill, fillBeat uint16
}

// pssiBlob builds a song structure as rekordbox 6 writes it, masked.
func pssiBlob(mood uint16, endBeat uint16, entries []pssiEntry) []byte {
	n := len(entries)
	region := make([]byte, 14+n*24)
	binary.BigEndian.PutUint16(region[0:], mood)
	binary.BigEndian.PutUint16(region[8:], endBeat)
	for i, e := range entries {
		o := region[14+i*24:]
		binary.BigEndian.PutUint16(o[0:], uint16(i+1))
		binary.BigEndian.PutUint16(o[2:], e.beat)
		binary.BigEndian.PutUint16(o[4:], e.kind)
		o[7], o[9], o[11], o[19], o[21] = e.k1, e.k2, e.b, e.k3, byte(e.fill)
		binary.BigEndian.PutUint16(o[22:], e.fillBeat)
	}
	for i := range region {
		region[i] ^= PSSIMaskBase[i%len(PSSIMaskBase)] + byte(n)
	}
	blob := make([]byte, 6, 6+len(region))
	binary.BigEndian.PutUint32(blob[0:], 24)
	binary.BigEndian.PutUint16(blob[4:], uint16(n))
	return append(blob, region...)
}

// Phrases are named by their mood's table, and in High mood by the variant
// their flags pick (Deep Symmetry's ANLZ analysis, "Phrase labels").
func TestPhraseLabel(t *testing.T) {
	for _, c := range []struct {
		p              Phrase
		label, section string
	}{
		{Phrase{Kind: 1}, "Intro", "intro"},
		{Phrase{Kind: 2}, "Up", "up"},
		{Phrase{Kind: 5}, "Chorus", "chorus"},
		{Phrase{Kind: 4}, "", ""},
		{Phrase{Mood: 1, Kind: 1, K1: 1}, "Intro 1", "intro"},
		{Phrase{Mood: 1, Kind: 1}, "Intro 2", "intro"},
		{Phrase{Mood: 1, Kind: 2}, "Up 1", "up"},
		{Phrase{Mood: 1, Kind: 2, K3: 1}, "Up 2", "up"},
		{Phrase{Mood: 1, Kind: 2, K2: 1}, "Up 3", "up"},
		{Phrase{Mood: 1, Kind: 3, K1: 1}, "Down", "down"},
		{Phrase{Mood: 1, Kind: 5, K1: 1}, "Chorus 1", "chorus"},
		{Phrase{Mood: 1, Kind: 5}, "Chorus 2", "chorus"},
		{Phrase{Mood: 1, Kind: 6, K1: 1}, "Outro 1", "outro"},
		{Phrase{Mood: 1, Kind: 6}, "Outro 2", "outro"},
		{Phrase{Mood: 1, Kind: 9}, "", ""},
		{Phrase{Mood: 2, Kind: 1}, "Intro", "intro"},
		{Phrase{Mood: 2, Kind: 2}, "Verse 1", "verse"},
		{Phrase{Mood: 2, Kind: 7}, "Verse 6", "verse"},
		{Phrase{Mood: 2, Kind: 8}, "Bridge", "bridge"},
		{Phrase{Mood: 2, Kind: 9}, "Chorus", "chorus"},
		{Phrase{Mood: 2, Kind: 10}, "Outro", "outro"},
		{Phrase{Mood: 2, Kind: 11}, "", ""},
		{Phrase{Mood: 3, Kind: 4}, "Verse 1", "verse"},
		{Phrase{Mood: 3, Kind: 5}, "Verse 2", "verse"},
		{Phrase{Mood: 3, Kind: 8}, "Bridge", "bridge"},
	} {
		if label, section := PhraseLabel(c.p); label != c.label || section != c.section {
			t.Errorf("%+v: %q %q, want %q %q", c.p, label, section, c.label, c.section)
		}
	}
}

// ParsePSSI keeps the mood, the variant flags and the fill of each phrase.
func TestParsePSSIKeepsMoodFlagsAndFill(t *testing.T) {
	got := ParsePSSI(pssiBlob(1, 200, []pssiEntry{
		{beat: 1, kind: 1, k1: 1},
		{beat: 33, kind: 2, k2: 1, b: 1, fill: 1, fillBeat: 61},
		{beat: 65, kind: 5, k1: 1},
	}))
	if len(got) != 3 || got[0].Mood != 1 || got[0].K1 != 1 || got[1].K2 != 1 || got[1].FillBeat != 61 || got[2].FillBeat != 0 {
		t.Fatalf("parsed %+v", got)
	}
	if got := ParsePSSI(pssiBlob(2, 100, []pssiEntry{{beat: 1, kind: 8}})); len(got) != 1 || got[0].Mood != 2 || got[0].Kind != 8 {
		t.Fatalf("mid mood parsed %+v", got)
	}
}

// An imported analysis cached before phrases kept their mood gets it from its
// song structure; Vynull's own phrases, and ones it has since remade, don't.
func TestRekordboxPhrasesFillsCachedImports(t *testing.T) {
	blob := pssiBlob(2, 129, []pssiEntry{{beat: 1, kind: 1}, {beat: 65, kind: 8}})
	cached := []Phrase{{StartBeat: 1, EndBeat: 65, Kind: 1, StartMs: 10, HasVocal: true}, {StartBeat: 65, EndBeat: 129, Kind: 8}}
	r := &Result{Imported: true, SongStructure: blob, Phrases: cached}
	got := r.RekordboxPhrases()
	if got[0].Mood != 2 || got[1].Mood != 2 || got[0].StartMs != 10 || !got[0].HasVocal || cached[0].Mood != 0 {
		t.Fatalf("imported: %+v (cached %+v)", got, cached)
	}
	if got := (&Result{SongStructure: blob, Phrases: cached}).RekordboxPhrases(); got[0].Mood != 0 {
		t.Fatalf("own analysis got a mood: %+v", got)
	}
	remade := []Phrase{{StartBeat: 1, EndBeat: 65, Kind: 1}, {StartBeat: 65, EndBeat: 129, Kind: 5}}
	if got := (&Result{Imported: true, SongStructure: blob, Phrases: remade}).RekordboxPhrases(); got[0].Mood != 2 || got[1].Mood != 0 {
		t.Fatalf("remade phrase took the old mood: %+v", got)
	}
}

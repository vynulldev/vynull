// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import "github.com/vynulldev/vynull/analysis"

// keynotation.go names keys as rekordbox does over the link: by the key's
// place on the wheel, in its device setting's notation, Classic ("Am") or
// Alphanumeric ("8A"), however the library spells it ("08A" too). A name
// that isn't a key ("Alpha") goes out as stored. The device setting is
// Classic unless it says Alphanumeric (link-spec KEY_NOTATION_ORACLE.md and
// smart-string-property-matrix, rekordbox 7.2.19).

// KeyDisplaySource gives the dbserver the device setting's key notation,
// "classic" or "alphanumeric".
type KeyDisplaySource interface {
	KeyDisplay() string
}

// classicKeyNames lists rekordbox's 24 harmonic positions in order, in Classic
// (musical) notation. The 1-based position is the "new key" index served as
// the KEY menu's row ID (1A=1, 1B=2, ... 12B=24).
var classicKeyNames = [24]string{
	"Abm", "B", "Ebm", "F#", "Bbm", "Db", "Fm", "Ab", "Cm", "Eb", "Gm", "Bb",
	"Dm", "F", "Am", "C", "Em", "G", "Bm", "D", "F#m", "A", "Dbm", "E",
}

// camelotKeyNames lists the same positions in Alphanumeric notation, 1A to 12B.
var camelotKeyNames = func() (names [24]string) {
	for i := range names {
		k, _ := analysis.KeyAtIndex(i + 1)
		names[i] = k.Camelot()
	}
	return names
}()

// keyIndexes maps both notations' names to their 1-24 index, so a key
// spelled as either needs no parsing.
var keyIndexes = func() map[string]int {
	m := make(map[string]int, 48)
	for i := range 24 {
		m[classicKeyNames[i]] = i + 1
		m[camelotKeyNames[i]] = i + 1
	}
	return m
}()

// classicKeys reports whether keys are served in Classic notation.
func (h *Handler) classicKeys() bool {
	return h.keys == nil || h.keys.KeyDisplay() != "alphanumeric"
}

// keyName returns a key name as the decks are served it: in Classic or
// Alphanumeric notation when it names a key, else as it is.
func keyName(key string, classic bool) string {
	i, ok := keyIndexes[key]
	if !ok {
		k, parsed := analysis.ParseKey(key)
		if !parsed {
			return key
		}
		i = k.Index()
	}
	return keyNameAt(i, classic)
}

// keyNameAt names the key at a 1-24 key index in either notation.
func keyNameAt(i int, classic bool) string {
	if classic {
		return classicKeyNames[i-1]
	}
	return camelotKeyNames[i-1]
}

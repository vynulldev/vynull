// SPDX-License-Identifier: GPL-3.0-or-later

// Package related suggests tracks that mix well with a given one, for the API
// and web UI. (The CDJ's MATCHING browse deliberately does not use it: like
// rekordbox, it lists only the imported recommendation pairs.) Candidates are
// ranked on rekordbox's own related-track pairs (imported from master.db),
// harmonic key compatibility, tempo (including half and double time), and
// shared genre or artist.
package related

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// Options tunes a search. Zero values take the defaults.
type Options struct {
	Limit        int     // most matches to return (default 25)
	BPMTolerance float64 // allowed tempo difference in percent (default 6)
}

// Match is one suggested track with its score and the reasons behind it,
// strongest first.
type Match struct {
	Track   *library.Track
	Score   float64
	Reasons []string
}

// Score weights. A rekordbox pair outranks any computed match.
const (
	weightPair     = 3.0
	weightSameKey  = 1.0
	weightNearKey  = 0.8 // relative major/minor, or one step on the wheel
	weightBPM      = 1.0 // scaled down linearly to 0 at the tolerance
	weightGenre    = 0.5
	weightArtist   = 0.2
	defaultLimit   = 25
	defaultBPMTol  = 6.0
	halfDoubleCost = 0.1 // a half/double-time match scores a little lower
)

// Find returns tracks related to seedID, best first. A candidate qualifies if
// rekordbox paired it with the seed, or its tempo is within the tolerance and
// it is key-compatible or shares the seed's genre. Tracks whose file is
// missing, and other versions of the seed itself (same artist and title
// before any "(...)" mix name), are skipped.
func Find(lib *library.Library, seedID uint32, o Options) []Match {
	seed := lib.Track(seedID)
	if seed == nil {
		return nil
	}
	if o.Limit <= 0 {
		o.Limit = defaultLimit
	}
	if o.BPMTolerance <= 0 {
		o.BPMTolerance = defaultBPMTol
	}
	pairs := map[uint32]bool{}
	for _, id := range lib.Related(seedID) {
		pairs[id] = true
	}
	seedKey := camelot(seed.Key)

	var out []Match
	for _, c := range lib.Tracks() {
		if c.ID == seed.ID || c.FileMissing || sameSong(seed, c) {
			continue
		}
		m := Match{Track: c}
		paired := pairs[c.ID]
		if paired {
			m.Score += weightPair
			m.Reasons = append(m.Reasons, "paired in rekordbox")
		}

		keyOK := false
		if ck := camelot(c.Key); seedKey != "" && ck != "" {
			switch rel := keyRelation(seedKey, ck); rel {
			case "same":
				m.Score += weightSameKey
				m.Reasons = append(m.Reasons, "same key ("+ck+")")
				keyOK = true
			case "relative", "adjacent":
				m.Score += weightNearKey
				m.Reasons = append(m.Reasons, fmt.Sprintf("%s key (%s to %s)", rel, seedKey, ck))
				keyOK = true
			}
		}

		bpmOK := false
		if pct, factor, ok := tempoDiff(seed.BPM, c.BPM); ok && pct <= o.BPMTolerance {
			bpmOK = true
			s := weightBPM * (1 - pct/o.BPMTolerance)
			reason := fmt.Sprintf("BPM %.1f (%+.1f%%)", c.BPM, signedPct(seed.BPM, c.BPM*factor))
			if factor != 1 {
				s -= halfDoubleCost
				if factor == 2 {
					reason += ", half time"
				} else {
					reason += ", double time"
				}
			}
			m.Score += math.Max(s, 0)
			m.Reasons = append(m.Reasons, reason)
		}

		genreOK := seed.Genre != "" && strings.EqualFold(strings.TrimSpace(seed.Genre), strings.TrimSpace(c.Genre))
		if genreOK {
			m.Score += weightGenre
			m.Reasons = append(m.Reasons, "same genre")
		}
		if seed.Artist != "" && strings.EqualFold(seed.Artist, c.Artist) {
			m.Score += weightArtist
			m.Reasons = append(m.Reasons, "same artist")
		}

		if paired || (bpmOK && (keyOK || genreOK)) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return strings.ToLower(out[i].Track.Title) < strings.ToLower(out[j].Track.Title)
	})
	if len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out
}

// sameSong reports whether two tracks are versions of one song: the same
// artist and the same title once a trailing "(... Mix)"-style part is dropped.
func sameSong(a, b *library.Track) bool {
	base := func(t string) string {
		if i := strings.Index(t, "("); i > 0 {
			t = t[:i]
		}
		return strings.ToLower(strings.TrimSpace(t))
	}
	return base(a.Title) != "" && base(a.Title) == base(b.Title) &&
		strings.EqualFold(strings.TrimSpace(a.Artist), strings.TrimSpace(b.Artist))
}

// camelot returns a key in Camelot notation ("8A"), from either notation, or
// "" when it is unknown.
func camelot(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if _, _, ok := parseCamelot(key); ok {
		return strings.ToUpper(key)
	}
	c, _ := analysis.KeyNamesFrom(key)
	return c
}

func parseCamelot(s string) (int, byte, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 2 || len(s) > 3 {
		return 0, 0, false
	}
	letter := s[len(s)-1]
	if letter != 'A' && letter != 'B' {
		return 0, 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 || n > 12 {
		return 0, 0, false
	}
	return n, letter, true
}

// keyRelation classifies two Camelot keys: "same", "relative" (same number,
// other letter), "adjacent" (one step on the wheel, same letter), or "".
func keyRelation(a, b string) string {
	an, al, ok1 := parseCamelot(a)
	bn, bl, ok2 := parseCamelot(b)
	if !ok1 || !ok2 {
		return ""
	}
	switch {
	case an == bn && al == bl:
		return "same"
	case an == bn:
		return "relative"
	case al == bl && ((an-bn+12)%12 == 1 || (bn-an+12)%12 == 1):
		return "adjacent"
	}
	return ""
}

// tempoDiff returns how far candidate is from seed in percent, after the
// best of 1x, 2x (candidate at half time), or 0.5x (double time), and that
// factor. ok is false when either tempo is unknown.
func tempoDiff(seed, cand float64) (pct, factor float64, ok bool) {
	if seed <= 0 || cand <= 0 {
		return 0, 0, false
	}
	best := math.Inf(1)
	for _, f := range []float64{1, 2, 0.5} {
		if d := math.Abs(cand*f-seed) / seed * 100; d < best {
			best, factor = d, f
		}
	}
	return best, factor, true
}

func signedPct(seed, cand float64) float64 {
	return (cand - seed) / seed * 100
}

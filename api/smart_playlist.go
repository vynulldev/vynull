// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
)

// SmartRules describes a tree of conditions evaluated against
// library.Track to populate a smart playlist. Combinator is "all" (AND)
// or "any" (OR); Conditions is the immediate child list. Each child is
// either a leaf (Field/Operator/Value) or a Group nesting another
// SmartRules — so the user can express, for example,
// "BPM 120-128 AND (Genre is Trance OR Genre is Progressive)".
//
// Nil / empty rules match every track (an empty smart playlist returns
// the whole library); callers usually treat that as a user mistake and
// either reject up front or show a warning, but the evaluator is happy.
type SmartRules struct {
	Combinator string      `json:"combinator"` // "all" or "any" — defaults to "all"
	Conditions []SmartCond `json:"conditions"`
}

// SmartCond is either a leaf condition (Field/Operator/Value set) or a
// nested group (Group set). The two modes are mutually exclusive; the
// evaluator picks whichever is populated. Plain `any` for Value so we
// can carry numbers, strings, [2]float64 ranges, []string lists, etc.
type SmartCond struct {
	Group    *SmartRules `json:"group,omitempty"`
	Field    string      `json:"field,omitempty"`
	Operator string      `json:"operator,omitempty"`
	Value    any         `json:"value,omitempty"`
}

// TagLookup is the subset of TagStore the evaluator needs. Kept narrow
// so unit tests can supply a fake without bringing in the JSON store.
type TagLookup interface {
	GetTagsForTrack(trackID uint32) []TagInfo
}

// Match returns true if `t` satisfies the rule tree. tags may be nil if
// no tag store is wired; tag-related conditions then degrade to "no
// tag → don't match" rather than panicking. It compiles the rules for the
// one track; a playlist's tracks are matched with matchTracks.
func (r *SmartRules) Match(t *library.Track, tags TagLookup, now time.Time) bool {
	return r.compile(now)(t, tags)
}

// smartMatcher reports whether a track satisfies a compiled rule, given the
// tags (which may be nil).
type smartMatcher func(t *library.Track, tags TagLookup) bool

func never(*library.Track, TagLookup) bool { return false }

// compile turns the rule tree into a matcher, parsing and folding each
// condition's value once. now is the day relative dates count back from.
func (r *SmartRules) compile(now time.Time) smartMatcher {
	if r == nil || len(r.Conditions) == 0 {
		return func(*library.Track, TagLookup) bool { return true }
	}
	conds := make([]smartMatcher, len(r.Conditions))
	for i, c := range r.Conditions {
		if c.Group != nil {
			conds[i] = c.Group.compile(now)
		} else {
			conds[i] = compileLeaf(c.Field, c.Operator, c.Value, now)
		}
	}
	if r.Combinator == "any" {
		return func(t *library.Track, tags TagLookup) bool {
			for _, m := range conds {
				if m(t, tags) {
					return true
				}
			}
			return false
		}
	}
	return func(t *library.Track, tags TagLookup) bool {
		for _, m := range conds {
			if !m(t, tags) {
				return false
			}
		}
		return true
	}
}

// numericFields reads the numeric fields rules compare.
var numericFields = map[string]func(*library.Track) float64{
	"bpm":  func(t *library.Track) float64 { return t.BPM },
	"year": func(t *library.Track) float64 { return float64(t.Year) },
	// rekordbox reads the rating as a signed byte.
	"rating":       func(t *library.Track) float64 { return float64(int8(t.Rating)) },
	"play_count":   func(t *library.Track) float64 { return float64(t.PlayCount) },
	"duration_sec": func(t *library.Track) float64 { return t.Duration.Seconds() },
	"color":        func(t *library.Track) float64 { return float64(t.ColorID) },
}

// textFields reads the text fields rules compare.
var textFields = map[string]func(*library.Track) string{
	"title":           func(t *library.Track) string { return t.Title },
	"artist":          func(t *library.Track) string { return t.Artist },
	"album":           func(t *library.Track) string { return t.Album },
	"genre":           func(t *library.Track) string { return t.Genre },
	"comment":         func(t *library.Track) string { return t.Comment },
	"label":           func(t *library.Track) string { return t.Label },
	"file_name":       func(t *library.Track) string { return filepath.Base(t.FilePath) },
	"mix_name":        func(t *library.Track) string { return t.MixName },
	"album_artist":    func(t *library.Track) string { return t.AlbumArtist },
	"composer":        func(t *library.Track) string { return t.Composer },
	"remixer":         func(t *library.Track) string { return t.Remixer },
	"original_artist": func(t *library.Track) string { return t.OriginalArtist },
	"file_type":       func(t *library.Track) string { return t.FileType },
}

// dateFields reads the date fields rules compare.
var dateFields = map[string]func(*library.Track) time.Time{
	"date_added":   func(t *library.Track) time.Time { return t.DateAdded },
	"date_created": func(t *library.Track) time.Time { return t.DateCreated },
	"release_date": func(t *library.Track) time.Time { return t.ReleaseDate },
}

// compileLeaf compiles a single field condition. Unknown field/operator
// combinations never match (silent reject) so an out-of-date editor
// can't crash track resolution; callers that want strict mode can
// validate before persistence via SmartRulesValidate.
func compileLeaf(field, op string, value any, now time.Time) smartMatcher {
	if get, ok := numericFields[field]; ok {
		match := compileNumeric(op, value)
		if field == "bpm" {
			// rekordbox's smart playlists skip a track whose stored BPM reads
			// as negative (link-spec smart-numeric-boundaries, rekordbox 7.2.19).
			return func(t *library.Track, tags TagLookup) bool {
				bpm := t.BPM
				return bpm >= 0 && match(bpm)
			}
		}
		return func(t *library.Track, tags TagLookup) bool { return match(get(t)) }
	}
	if get, ok := textFields[field]; ok {
		match := compileString(op, value)
		return func(t *library.Track, tags TagLookup) bool { return match(foldTrackText(get(t))) }
	}
	if get, ok := dateFields[field]; ok {
		match := compileDate(op, value, now)
		return func(t *library.Track, tags TagLookup) bool { return match(get(t)) }
	}
	switch field {
	case "key":
		return compileKey(op, value)
	case "tag":
		return compileTag(op, value)
	}
	return never
}

// compileNumeric handles eq/ne/lt/lte/gt/gte/between/in. Between expects
// a 2-element list. in expects a list of acceptable values.
func compileNumeric(op string, value any) func(x float64) bool {
	no := func(float64) bool { return false }
	switch op {
	case "between":
		lo, hi, ok := pairNumbers(value)
		if !ok {
			return no
		}
		return func(x float64) bool { return x >= lo && x <= hi }
	case "in":
		nums, ok := numList(value)
		if !ok {
			return no
		}
		return func(x float64) bool { return slices.Contains(nums, x) }
	}
	v, ok := toFloat(value)
	if !ok {
		return no
	}
	switch op {
	case "eq", "=", "is":
		return func(x float64) bool { return x == v }
	case "ne", "!=", "is_not":
		return func(x float64) bool { return x != v }
	case "lt", "<":
		return func(x float64) bool { return x < v }
	case "lte", "<=":
		return func(x float64) bool { return x <= v }
	case "gt", ">":
		return func(x float64) bool { return x > v }
	case "gte", ">=":
		return func(x float64) bool { return x >= v }
	}
	return no
}

// compileString is case-insensitive, and takes the track's text folded by
// foldTrackText. Empty haystack with "is_empty" / "not_empty" operators lets
// the user filter for unset fields.
func compileString(op string, value any) func(folded string) bool {
	switch op {
	case "is_empty":
		return func(s string) bool { return s == "" }
	case "not_empty":
		return func(s string) bool { return s != "" }
	}
	no := func(string) bool { return false }
	q, ok := value.(string)
	if !ok {
		return no
	}
	// rekordbox normalizes the track's stored text (composing accents) but
	// takes the rule's value as written, so a rule spelled with a separate
	// combining accent matches nothing.
	qLow := foldText(q)
	switch op {
	case "is", "eq", "=":
		return func(s string) bool { return s == qLow }
	case "is_not", "ne", "!=":
		return func(s string) bool { return s != qLow }
	case "contains":
		return func(s string) bool { return strings.Contains(s, qLow) }
	case "not_contains":
		// rekordbox's does-not-contain skips empty and missing values.
		return func(s string) bool { return s != "" && !strings.Contains(s, qLow) }
	case "starts_with":
		return func(s string) bool { return strings.HasPrefix(s, qLow) }
	case "ends_with":
		return func(s string) bool { return strings.HasSuffix(s, qLow) }
	}
	return no
}

// compileKey handles equality plus "compatible" (Camelot ±1 + relative).
// Camelot notation: "8A", "11B". For "compatible" we accept any track
// whose key wheel position is within ±1 of the target, or the relative
// minor/major (same number, swapped letter).
func compileKey(op string, value any) smartMatcher {
	target, ok := value.(string)
	if !ok {
		return never
	}
	switch op {
	case "is", "eq", "=", "is_not", "ne", "!=":
		// The rule names the track's key when the names match as text, as
		// rekordbox compares them, or as keys in either notation, since an
		// import stores keys as Camelot ("8A") while rekordbox rules carry
		// the names rekordbox showed ("Am").
		is := compileString("is", target)
		cam, _ := analysis.KeyNamesFrom(target)
		trimmed := target == strings.TrimSpace(target)
		want := op == "is" || op == "eq" || op == "="
		return func(t *library.Track, tags TagLookup) bool {
			k := t.Key
			same := is(foldTrackText(k))
			if !same && cam != "" && trimmed {
				kc, _ := analysis.KeyNamesFrom(k)
				same = cam == kc
			}
			return same == want
		}
	case "compatible":
		// The same key, the relative minor or major (same number, other
		// letter), or one step round the wheel on the same letter; keys that
		// aren't keys compare as text, ignoring case.
		kt, okT := analysis.ParseKey(target)
		return func(t *library.Track, tags TagLookup) bool {
			k := t.Key
			ka, ok := analysis.ParseKey(k)
			if !ok || !okT {
				return strings.EqualFold(k, target)
			}
			return kt == ka || kt == ka.Relative() || kt == ka.Step(1) || kt == ka.Step(-1)
		}
	case "contains", "starts_with", "ends_with":
		// Text operators compare the key's name, as rekordbox does, in
		// either notation: an import stores keys as Camelot, but the rule
		// may be written against rekordbox's classic names.
		match := compileString(op, value)
		return func(t *library.Track, tags TagLookup) bool {
			for _, name := range keyNames(t.Key) {
				if match(foldTrackText(name)) {
					return true
				}
			}
			return false
		}
	case "not_contains":
		match := compileString(op, value)
		return func(t *library.Track, tags TagLookup) bool {
			for _, name := range keyNames(t.Key) {
				if !match(foldTrackText(name)) {
					return false
				}
			}
			return true
		}
	}
	return never
}

// compileTag supports has (specific tag by name), not_has (specific
// tag absent), has_any (track has at least one tag), and none
// (track has zero tags). Tag name comparison is case-insensitive.
func compileTag(op string, value any) smartMatcher {
	trackTags := func(t *library.Track, tags TagLookup) []TagInfo {
		if tags == nil {
			return nil
		}
		return tags.GetTagsForTrack(t.ID)
	}
	switch op {
	case "has_any":
		return func(t *library.Track, tags TagLookup) bool { return len(trackTags(t, tags)) > 0 }
	case "none":
		return func(t *library.Track, tags TagLookup) bool { return len(trackTags(t, tags)) == 0 }
	}
	name, ok := value.(string)
	if !ok {
		return never
	}
	nameLow := strings.ToLower(name)
	found := func(t *library.Track, tags TagLookup) bool {
		for _, tag := range trackTags(t, tags) {
			if strings.ToLower(tag.Name) == nameLow {
				return true
			}
		}
		return false
	}
	switch op {
	case "has", "is", "eq":
		return found
	case "not_has", "is_not", "ne":
		return func(t *library.Track, tags TagLookup) bool { return !found(t, tags) }
	}
	return never
}

// compileDate only supports "in_last_days" (recency filter) and a few
// equality / range modes for completeness. Value is a number of days (or
// months, for in_last_months) for the recency operators, or RFC3339 string
// for the comparison operators.
func compileDate(op string, value any, now time.Time) func(date time.Time) bool {
	match := compileSetDate(op, value, now)
	return func(date time.Time) bool {
		if date.IsZero() {
			// Unset date — only matches "is_empty"/"not_empty" sentinels.
			return op == "is_empty"
		}
		return match(date)
	}
}

// compileSetDate is compileDate for a track whose date is set.
func compileSetDate(op string, value any, now time.Time) func(date time.Time) bool {
	no := func(time.Time) bool { return false }
	ruleDate := func(v any) (time.Time, bool) {
		ts, ok := v.(string)
		if !ok {
			return time.Time{}, false
		}
		t, err := parseRuleDate(ts)
		return t, err == nil
	}
	switch op {
	case "on", "not_on":
		// Same calendar day as the given date (rekordbox's date equality).
		d, ok := ruleDate(value)
		if !ok {
			return no
		}
		y2, m2, d2 := d.UTC().Date()
		return func(date time.Time) bool {
			y1, m1, d1 := date.UTC().Date()
			same := y1 == y2 && m1 == m2 && d1 == d2
			return same == (op == "on")
		}
	case "in_last_days", "not_in_last_days", "in_last_months", "not_in_last_months":
		n, ok := toFloat(value)
		if !ok {
			return no
		}
		months := op == "in_last_months" || op == "not_in_last_months"
		cutoff := recentCutoff(int(n), months, now)
		in := op == "in_last_days" || op == "in_last_months"
		return func(date time.Time) bool {
			y, m, d := date.UTC().Date()
			within := !time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Before(cutoff)
			return within == in
		}
	case "before":
		t, ok := ruleDate(value)
		if !ok {
			return no
		}
		return func(date time.Time) bool { return date.Before(t) }
	case "after":
		t, ok := ruleDate(value)
		if !ok {
			return no
		}
		return func(date time.Time) bool { return date.After(t) }
	case "between":
		ts, ok := value.([]any)
		if !ok || len(ts) != 2 {
			return no
		}
		lo, ok1 := ruleDate(ts[0])
		hi, ok2 := ruleDate(ts[1])
		if !ok1 || !ok2 {
			return no
		}
		return func(date time.Time) bool { return !date.Before(lo) && !date.After(hi) }
	case "not_empty":
		return func(time.Time) bool { return true }
	}
	return no
}

// keyNames is a track key's name in each notation it has: the stored name
// and, for a recognised key, its Camelot and classic names.
func keyNames(key string) []string {
	names := []string{key}
	if cam, std := analysis.KeyNamesFrom(key); cam != "" {
		for _, n := range []string{cam, std} {
			if n != key {
				names = append(names, n)
			}
		}
	}
	return names
}

// foldTrackText folds a track's stored text as rules compare it: normalized
// (composing accents), then folded by foldText.
func foldTrackText(s string) string {
	return foldText(norm.NFC.String(s))
}

// foldText prepares text for comparison the way rekordbox's smart playlists
// compare it (link-spec conformance smart text suites): case-insensitive, a
// precomposed accented letter equals its base letter ("Álpha" is "alpha"),
// fullwidth forms equal ASCII ("Ａｌｐｈａ" is "alpha"), katakana equals
// hiragana, and final sigma equals sigma.
// Text is not otherwise normalized: a base letter followed by a separate
// combining accent ("A\u0301lpha") keeps the accent, and combining marks in a
// different order differ, as in rekordbox.
func foldText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf {
			b.WriteRune(r) // ASCII: no accent to drop, nothing to widen
			continue
		}
		if d := norm.NFD.String(string(r)); len([]rune(d)) > 1 {
			if base := []rune(d)[0]; !unicode.Is(unicode.Mn, base) {
				r = base // precomposed: keep the base letter only
			}
		}
		b.WriteRune(foldWidthKana(r))
	}
	return strings.ReplaceAll(strings.ToLower(b.String()), "ς", "σ")
}

// foldWidthKana folds the forms rekordbox's text matching treats as equal
// (SMART_PLAYLIST_ORACLE, "Text collation"): fullwidth ASCII and the
// ideographic space to their ASCII forms, and katakana to hiragana.
func foldWidthKana(r rune) rune {
	switch {
	case r >= 0xff01 && r <= 0xff5e:
		return r - 0xfee0
	case r == 0x3000:
		return ' '
	case r >= 0x30a1 && r <= 0x30f6:
		return r - 0x60
	}
	return r
}

// parseRuleDate reads a date rule's value: RFC3339, as imported rules and
// the API carry it, or a plain YYYY-MM-DD, as the web editor's date input
// gives it.
func parseRuleDate(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// recentCutoff is the first day "in the last n days" (or months) includes,
// as rekordbox's pastDayToDay and pastMonthToDay compute it (link-spec
// static analysis, smart-relative-date-matrix, rekordbox 7.2.19): the last
// n days end with today, so the cutoff is n-1 days back, and the last n
// months start with the last day of the month n months back. A count
// below one counts as one. Later dates, even future ones, are within.
func recentCutoff(n int, months bool, now time.Time) time.Time {
	if n--; n < 0 {
		n = 0
	}
	y, m, d := now.Date()
	if months {
		return time.Date(y, m-time.Month(n), 0, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(y, m, d-n, 0, 0, 0, 0, time.UTC)
}

// ── small helpers ──────────────────────────────────────────────────────

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case uint8:
		return float64(n), true
	case string:
		var x float64
		_, err := fmt.Sscanf(n, "%f", &x)
		return x, err == nil
	}
	return 0, false
}

func pairNumbers(v any) (float64, float64, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) != 2 {
		return 0, 0, false
	}
	lo, ok1 := toFloat(arr[0])
	hi, ok2 := toFloat(arr[1])
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi, true
}

func numList(v any) ([]float64, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(arr))
	for _, x := range arr {
		f, ok := toFloat(x)
		if !ok {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"strings"
	"time"

	"vynull/library"
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
// tag → don't match" rather than panicking.
func (r *SmartRules) Match(t *library.Track, tags TagLookup, now time.Time) bool {
	if r == nil || len(r.Conditions) == 0 {
		return true
	}
	all := r.Combinator != "any"
	for _, c := range r.Conditions {
		ok := c.match(t, tags, now)
		if all && !ok {
			return false
		}
		if !all && ok {
			return true
		}
	}
	return all // AND with all-true falls through here; OR with all-false falls through with all=false
}

func (c SmartCond) match(t *library.Track, tags TagLookup, now time.Time) bool {
	if c.Group != nil {
		return c.Group.Match(t, tags, now)
	}
	return evalLeaf(c.Field, c.Operator, c.Value, t, tags, now)
}

// evalLeaf evaluates a single field condition. Unknown field/operator
// combinations return false (silent reject) so an out-of-date editor
// can't crash track resolution; callers that want strict mode can
// validate before persistence via SmartRulesValidate.
func evalLeaf(field, op string, value any, t *library.Track, tags TagLookup, now time.Time) bool {
	switch field {
	case "bpm":
		return numericMatch(op, value, t.BPM)
	case "year":
		return numericMatch(op, value, float64(t.Year))
	case "rating":
		return numericMatch(op, value, float64(t.Rating))
	case "play_count":
		return numericMatch(op, value, float64(t.PlayCount))
	case "duration_sec":
		return numericMatch(op, value, t.Duration.Seconds())
	case "color":
		return numericMatch(op, value, float64(t.ColorID))

	case "title":
		return stringMatch(op, value, t.Title)
	case "artist":
		return stringMatch(op, value, t.Artist)
	case "album":
		return stringMatch(op, value, t.Album)
	case "genre":
		return stringMatch(op, value, t.Genre)
	case "comment":
		return stringMatch(op, value, t.Comment)
	case "label":
		return stringMatch(op, value, t.Label)
	case "remixer":
		return stringMatch(op, value, t.Remixer)
	case "original_artist":
		return stringMatch(op, value, t.OriginalArtist)
	case "file_type":
		return stringMatch(op, value, t.FileType)

	case "key":
		return keyMatch(op, value, t.Key)

	case "tag":
		return tagMatch(op, value, t.ID, tags)

	case "date_added":
		return dateMatch(op, value, t.DateAdded, now)
	}
	return false
}

// numericMatch handles eq/ne/lt/lte/gt/gte/between/in. Between expects
// a 2-element list. in expects a list of acceptable values.
func numericMatch(op string, value any, x float64) bool {
	switch op {
	case "between":
		lo, hi, ok := pairNumbers(value)
		if !ok {
			return false
		}
		return x >= lo && x <= hi
	case "in":
		nums, ok := numList(value)
		if !ok {
			return false
		}
		for _, n := range nums {
			if x == n {
				return true
			}
		}
		return false
	}
	v, ok := toFloat(value)
	if !ok {
		return false
	}
	switch op {
	case "eq", "=", "is":
		return x == v
	case "ne", "!=", "is_not":
		return x != v
	case "lt", "<":
		return x < v
	case "lte", "<=":
		return x <= v
	case "gt", ">":
		return x > v
	case "gte", ">=":
		return x >= v
	}
	return false
}

// stringMatch is case-insensitive. Empty haystack with "is_empty" /
// "not_empty" operators lets the user filter for unset fields.
func stringMatch(op string, value any, s string) bool {
	switch op {
	case "is_empty":
		return s == ""
	case "not_empty":
		return s != ""
	}
	q, ok := value.(string)
	if !ok {
		return false
	}
	low := strings.ToLower(s)
	qLow := strings.ToLower(q)
	switch op {
	case "is", "eq", "=":
		return low == qLow
	case "is_not", "ne", "!=":
		return low != qLow
	case "contains":
		return strings.Contains(low, qLow)
	case "not_contains":
		return !strings.Contains(low, qLow)
	case "starts_with":
		return strings.HasPrefix(low, qLow)
	case "ends_with":
		return strings.HasSuffix(low, qLow)
	}
	return false
}

// keyMatch handles equality plus "compatible" (Camelot ±1 + relative).
// Camelot notation: "8A", "11B". For "compatible" we accept any track
// whose key wheel position is within ±1 of the target, or the relative
// minor/major (same number, swapped letter).
func keyMatch(op string, value any, trackKey string) bool {
	target, ok := value.(string)
	if !ok {
		return false
	}
	switch op {
	case "is", "eq", "=":
		return strings.EqualFold(trackKey, target)
	case "is_not", "ne", "!=":
		return !strings.EqualFold(trackKey, target)
	case "compatible":
		return camelotCompatible(trackKey, target)
	}
	return false
}

// camelotCompatible reports whether `a` is a harmonically-compatible
// neighbour of `b`. Returns true for the same key, ±1 on the wheel,
// or the relative minor/major (same number, opposite letter). Both
// inputs are parsed as "<number><A|B>"; non-Camelot strings fall back
// to plain case-insensitive equality.
func camelotCompatible(a, b string) bool {
	an, al, ok := parseCamelot(a)
	bn, bl, ok2 := parseCamelot(b)
	if !ok || !ok2 {
		return strings.EqualFold(a, b)
	}
	if an == bn && al == bl {
		return true
	}
	// Relative minor/major
	if an == bn && al != bl {
		return true
	}
	// Adjacent on the wheel (mod 12)
	diff := (an - bn + 12) % 12
	if diff == 1 || diff == 11 {
		return al == bl
	}
	return false
}

func parseCamelot(s string) (int, byte, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if len(s) < 2 || len(s) > 3 {
		return 0, 0, false
	}
	last := s[len(s)-1]
	if last != 'A' && last != 'B' {
		return 0, 0, false
	}
	numStr := s[:len(s)-1]
	var n int
	_, err := fmt.Sscanf(numStr, "%d", &n)
	if err != nil || n < 1 || n > 12 {
		return 0, 0, false
	}
	return n, last, true
}

// tagMatch supports has (specific tag by name), not_has (specific
// tag absent), has_any (track has at least one tag), and none
// (track has zero tags). Tag name comparison is case-insensitive.
func tagMatch(op string, value any, trackID uint32, tags TagLookup) bool {
	var trackTags []TagInfo
	if tags != nil {
		trackTags = tags.GetTagsForTrack(trackID)
	}
	switch op {
	case "has_any":
		return len(trackTags) > 0
	case "none":
		return len(trackTags) == 0
	}
	name, ok := value.(string)
	if !ok {
		return false
	}
	nameLow := strings.ToLower(name)
	found := false
	for _, tag := range trackTags {
		if strings.ToLower(tag.Name) == nameLow {
			found = true
			break
		}
	}
	switch op {
	case "has", "is", "eq":
		return found
	case "not_has", "is_not", "ne":
		return !found
	}
	return false
}

// dateMatch only supports "in_last_days" (recency filter) and a few
// equality / range modes for completeness. Value is a number of days
// for in_last_days, or RFC3339 string for the comparison operators.
func dateMatch(op string, value any, date time.Time, now time.Time) bool {
	if date.IsZero() {
		// Unset date — only matches "is_empty"/"not_empty" sentinels.
		return op == "is_empty"
	}
	switch op {
	case "in_last_days":
		days, ok := toFloat(value)
		if !ok {
			return false
		}
		return now.Sub(date) <= time.Duration(days*24)*time.Hour
	case "before":
		ts, ok := value.(string)
		if !ok {
			return false
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return false
		}
		return date.Before(t)
	case "after":
		ts, ok := value.(string)
		if !ok {
			return false
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return false
		}
		return date.After(t)
	case "between":
		ts, ok := value.([]any)
		if !ok || len(ts) != 2 {
			return false
		}
		los, ok1 := ts[0].(string)
		his, ok2 := ts[1].(string)
		if !ok1 || !ok2 {
			return false
		}
		lo, e1 := time.Parse(time.RFC3339, los)
		hi, e2 := time.Parse(time.RFC3339, his)
		if e1 != nil || e2 != nil {
			return false
		}
		return !date.Before(lo) && !date.After(hi)
	case "not_empty":
		return true
	case "is_empty":
		return false
	}
	return false
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

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"strings"
	"time"

	"vynull/library"
)

// rbFieldMap maps rekordbox SmartList PropertyName values to the app's
// SmartRules field names. Properties not listed are unsupported and their
// conditions are dropped during translation.
var rbFieldMap = map[string]string{
	"bpm":            "bpm",
	"genre":          "genre",
	"artist":         "artist",
	"album":          "album",
	"name":           "title",
	"comments":       "comment",
	"label":          "label",
	"remixer":        "remixer",
	"originalArtist": "original_artist",
	"key":            "key",
	"rating":         "rating",
	"year":           "year",
	"playCount":      "play_count",
	"duration":       "duration_sec",
	"fileType":       "file_type",
	"myTag":          "tag",
	"stockDate":      "date_added",
	"dateCreated":    "date_added",
}

func isNumericField(field string) bool {
	switch field {
	case "bpm", "year", "rating", "play_count", "duration_sec", "color":
		return true
	}
	return false
}

// rbDate converts a rekordbox SmartList date value (e.g. "2023-01-01" or
// "2023-01-01 12:30:00") to the RFC3339 form dateMatch expects, or "" if it
// can't be parsed (so the caller drops the condition rather than emitting one
// the evaluator silently rejects).
func rbDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339, "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// smartRulesFromRekordbox translates a parsed rekordbox SmartList into the
// app's SmartRules. Returns the rules and the number of conditions that
// could not be mapped (unsupported property/operator) so the caller can log
// partial translations. A smart playlist with all conditions dropped becomes
// an empty rule set — which matches every track — so the caller treats a
// fully-dropped translation as "don't create a smart playlist".
func smartRulesFromRekordbox(rb *library.RBSmartList) (rules *SmartRules, mapped, dropped int) {
	if rb == nil {
		return nil, 0, 0
	}
	sr := &SmartRules{Combinator: "all"}
	if rb.Logical == 2 {
		sr.Combinator = "any"
	}
	for _, c := range rb.Conditions {
		if cond, ok := rbCondToSmart(c); ok {
			sr.Conditions = append(sr.Conditions, cond)
			mapped++
		} else {
			dropped++
		}
	}
	return sr, mapped, dropped
}

// rbCondToSmart maps a single rekordbox condition to a SmartCond. The second
// return is false when the property or operator isn't supported.
//
// rekordbox operator codes: 1=equal, 2=not-equal, 3=greater, 4=less,
// 5=in-range, 6=in-last, 8=contains, 9=not-contains, 10=starts-with,
// 11=ends-with. BPM values are stored ×100; myTag values arrive already
// resolved to the tag name by the dump helper.
func rbCondToSmart(c library.RBSmartCond) (SmartCond, bool) {
	field, ok := rbFieldMap[c.Property]
	if !ok {
		return SmartCond{}, false
	}
	isTag := field == "tag"
	isDate := field == "date_added"
	isNum := isNumericField(field)

	// num parses a rekordbox numeric value string, scaling BPM down by 100.
	num := func(s string) float64 {
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if field == "bpm" {
			f /= 100
		}
		return f
	}

	switch c.Operator {
	case 1: // EQUAL
		switch {
		case isTag:
			return SmartCond{Field: "tag", Operator: "has", Value: c.Left}, true
		case isNum:
			return SmartCond{Field: field, Operator: "eq", Value: num(c.Left)}, true
		case isDate:
			// The evaluator has no exact-day equality; drop rather than emit a
			// condition dateMatch silently rejects (always-false).
			return SmartCond{}, false
		default:
			return SmartCond{Field: field, Operator: "is", Value: c.Left}, true
		}
	case 2: // NOT_EQUAL
		switch {
		case isTag:
			return SmartCond{Field: "tag", Operator: "not_has", Value: c.Left}, true
		case isNum:
			return SmartCond{Field: field, Operator: "ne", Value: num(c.Left)}, true
		case isDate:
			return SmartCond{}, false
		default:
			return SmartCond{Field: field, Operator: "is_not", Value: c.Left}, true
		}
	case 3: // GREATER (after, for dates)
		if isDate {
			if d := rbDate(c.Left); d != "" {
				return SmartCond{Field: field, Operator: "after", Value: d}, true
			}
			return SmartCond{}, false
		}
		if isNum {
			return SmartCond{Field: field, Operator: "gt", Value: num(c.Left)}, true
		}
	case 4: // LESS (before, for dates)
		if isDate {
			if d := rbDate(c.Left); d != "" {
				return SmartCond{Field: field, Operator: "before", Value: d}, true
			}
			return SmartCond{}, false
		}
		if isNum {
			return SmartCond{Field: field, Operator: "lt", Value: num(c.Left)}, true
		}
	case 5: // IN_RANGE
		if isDate {
			if lo, hi := rbDate(c.Left), rbDate(c.Right); lo != "" && hi != "" {
				return SmartCond{Field: field, Operator: "between", Value: []any{lo, hi}}, true
			}
			return SmartCond{}, false
		}
		if isNum {
			return SmartCond{Field: field, Operator: "between", Value: []any{num(c.Left), num(c.Right)}}, true
		}
	case 6: // IN_LAST (relative date)
		if isDate {
			days := num(c.Left)
			switch strings.ToLower(c.Unit) {
			case "month":
				days *= 30
			case "year":
				days *= 365
			}
			return SmartCond{Field: field, Operator: "in_last_days", Value: days}, true
		}
	case 8: // CONTAINS
		switch {
		case isTag:
			return SmartCond{Field: "tag", Operator: "has", Value: c.Left}, true
		case isNum:
			return SmartCond{Field: field, Operator: "eq", Value: num(c.Left)}, true
		default:
			return SmartCond{Field: field, Operator: "contains", Value: c.Left}, true
		}
	case 9: // NOT_CONTAINS
		if isTag {
			return SmartCond{Field: "tag", Operator: "not_has", Value: c.Left}, true
		}
		return SmartCond{Field: field, Operator: "not_contains", Value: c.Left}, true
	case 10: // STARTS_WITH
		if !isNum && !isTag {
			return SmartCond{Field: field, Operator: "starts_with", Value: c.Left}, true
		}
	case 11: // ENDS_WITH
		if !isNum && !isTag {
			return SmartCond{Field: field, Operator: "ends_with", Value: c.Left}, true
		}
	}
	return SmartCond{}, false
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/vynulldev/vynull/library"
)

// rbFieldMap maps rekordbox SmartList PropertyName values to the app's
// SmartRules field names. The names are rekordbox's own, as its property
// vocabulary probe shows (link-spec conformance suite smart-property-matrix,
// rekordbox 7.2.19): it accepts counter (play count), remixedBy, fileName
// (or filename), and mixName, and rejects look-alikes such as playCount,
// remixer, title, color, and composer. Properties not listed are unsupported
// (or, like albumArtist, producer, grouping, and dateReleased, have no track
// data here); their conditions are dropped during translation.
var rbFieldMap = map[string]string{
	"bpm":            "bpm",
	"genre":          "genre",
	"artist":         "artist",
	"album":          "album",
	"name":           "title",
	"comments":       "comment",
	"label":          "label",
	"remixedBy":      "remixer",
	"originalArtist": "original_artist",
	"key":            "key",
	"rating":         "rating",
	"year":           "year",
	"counter":        "play_count",
	"duration":       "duration_sec",
	"fileType":       "file_type",
	"fileName":       "file_name",
	"filename":       "file_name",
	"mixName":        "mix_name",
	"myTag":          "tag",
	"stockDate":      "date_added",
	"dateCreated":    "date_created",
}

func isNumericField(field string) bool {
	switch field {
	case "bpm", "year", "rating", "play_count", "duration_sec", "color":
		return true
	}
	return false
}

// rbDate converts a rekordbox SmartList date value to the RFC3339 form
// dateMatch expects, or "" if rekordbox would not accept it (so the caller
// treats the condition as unsatisfiable). rekordbox is strict
// (smart-date-format-matrix, rekordbox 7.2.19): only YYYY-MM-DD, with no
// surrounding spaces and no time part, nothing on or before 1970-01-01,
// which it treats as no date, and nothing in year 9999.
func rbDate(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil || !t.After(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) || t.Year() >= 9999 {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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
// 5=in-range, 6=in-last, 7=not-in-last, 8=contains, 9=not-contains, 10=starts-with,
// 11=ends-with. BPM values are stored ×100; myTag values arrive already
// resolved to the tag name by the dump helper.
func rbCondToSmart(c library.RBSmartCond) (SmartCond, bool) {
	field, ok := rbFieldMap[c.Property]
	if !ok {
		return SmartCond{}, false
	}
	isTag := field == "tag"
	isDate := field == "date_added" || field == "date_created"
	isNum := isNumericField(field)

	// num parses a rekordbox numeric value string, scaling BPM down by 100.
	num := func(s string) float64 {
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if field == "bpm" {
			f /= 100
		}
		return f
	}

	// contains, starts-with, and ends-with an empty value match nothing in
	// rekordbox (equality with "" still finds empty fields).
	if !isTag && !isNum && !isDate && (c.Operator == 8 || c.Operator == 10 || c.Operator == 11) && c.Left == "" {
		return SmartCond{}, false
	}

	// rekordbox evaluates MyTag conditions only with contains and
	// not-contains, and a blank tag matches nothing.
	if isTag && (c.Operator != 8 && c.Operator != 9 || strings.TrimSpace(c.Left) == "") {
		return SmartCond{}, false
	}

	switch c.Operator {
	case 1: // EQUAL
		switch {
		case isTag:
			return SmartCond{Field: "tag", Operator: "has", Value: c.Left}, true
		case isNum:
			return SmartCond{Field: field, Operator: "eq", Value: num(c.Left)}, true
		case isDate:
			// Same calendar day. A blank or invalid date matches nothing.
			if d := rbDate(c.Left); d != "" {
				return SmartCond{Field: field, Operator: "on", Value: d}, true
			}
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
			// Any other day; against a blank or invalid date, rekordbox
			// matches every track that has a date.
			if d := rbDate(c.Left); d != "" {
				return SmartCond{Field: field, Operator: "not_on", Value: d}, true
			}
			return SmartCond{Field: field, Operator: "not_empty"}, true
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
		// A reversed range (low above high) matches nothing in rekordbox.
		if isDate {
			if lo, hi := rbDate(c.Left), rbDate(c.Right); lo != "" && hi != "" && lo <= hi {
				return SmartCond{Field: field, Operator: "between", Value: []any{lo, hi}}, true
			}
			return SmartCond{}, false
		}
		if isNum {
			if lo, hi := num(c.Left), num(c.Right); lo <= hi {
				return SmartCond{Field: field, Operator: "between", Value: []any{lo, hi}}, true
			}
			return SmartCond{}, false
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
	case 7: // NOT_IN_LAST (relative date)
		if isDate {
			days := num(c.Left)
			switch strings.ToLower(c.Unit) {
			case "month":
				days *= 30
			case "year":
				days *= 365
			}
			return SmartCond{Field: field, Operator: "not_in_last_days", Value: days}, true
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

// RekordboxSmartTracks evaluates a rekordbox smart-playlist rule set (as
// dumped from master.db) against the library, exactly as an imported smart
// playlist is evaluated: the rules are translated by the import's own
// smartRulesFromRekordbox, then matched like PlaylistStore.TracksFor does.
// For callers that serve a dump without importing it into a PlaylistStore,
// such as the conformance server.
func RekordboxSmartTracks(rb *library.RBSmartList, lib *library.Library, tags TagLookup, now time.Time) []uint32 {
	if rbMatchesNothing(rb) {
		return nil
	}
	rules, _, _ := smartRulesFromRekordbox(rb)
	return matchingTracks(rules, lib, tags, now)
}

// rbMatchesNothing reports whether rekordbox would select no tracks for a
// smart playlist's rule set (link-spec conformance smart suites, rekordbox
// 7.2.19): a smart playlist without rules or with an empty rule set, or an
// all-of rule set with any condition rekordbox cannot satisfy (an unknown
// property, an operator that doesn't apply to the property, a reversed
// range, an unparseable date). Translation drops such conditions, which in an
// all-of set would wrongly widen the match; in an any-of set dropping them is
// already right, since they never match.
func rbMatchesNothing(rb *library.RBSmartList) bool {
	if rb == nil || len(rb.Conditions) == 0 {
		return true
	}
	if rb.Logical == 2 {
		return false
	}
	for _, c := range rb.Conditions {
		if _, ok := rbCondToSmart(c); !ok {
			return true
		}
	}
	return false
}

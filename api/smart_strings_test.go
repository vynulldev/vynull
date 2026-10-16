// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"slices"
	"testing"
	"time"

	"github.com/vynulldev/vynull/library"
)

// rekordbox's smart playlist text rules, from link-spec's
// smart-string-property-matrix: the same value sequence in every string
// property gives the same member sets.
func TestSmartStringPropertySemantics(t *testing.T) {
	values := []string{"Alpha", "alpha", "Álpha", "Álpha", "Ａｌｐｈａ", "Alpha Beta", "Beta Alpha", "Beta", "", ""}
	now := time.Now()
	for _, prop := range []string{"artist", "albumArtist", "producer", "mixName", "key", "comments"} {
		var tracks []*library.Track
		for i, v := range values {
			tr := &library.Track{ID: uint32(13001 + i)}
			switch prop {
			case "artist":
				tr.Artist = v
			case "albumArtist":
				tr.AlbumArtist = v
			case "producer":
				tr.Composer = v
			case "mixName":
				tr.MixName = v
			case "key":
				tr.Key = v
			case "comments":
				tr.Comment = v
			}
			tracks = append(tracks, tr)
		}
		members := func(op int, val string) []uint32 {
			rules, mapped, _ := smartRulesFromRekordbox(&library.RBSmartList{Logical: 1,
				Conditions: []library.RBSmartCond{{Property: prop, Operator: op, Left: val}}})
			if mapped == 0 {
				return nil
			}
			var ids []uint32
			for _, tr := range tracks {
				if rules.Match(tr, nil, now) {
					ids = append(ids, tr.ID)
				}
			}
			return ids
		}
		for _, tc := range []struct {
			name string
			op   int
			want []uint32
		}{
			{"equals", 1, []uint32{13001, 13002, 13003, 13004, 13005}},
			{"not equal", 2, []uint32{13006, 13007, 13008, 13009, 13010}},
			{"contains", 8, []uint32{13001, 13002, 13003, 13004, 13005, 13006, 13007}},
			{"does not contain", 9, []uint32{13008}},
			{"starts with", 10, []uint32{13001, 13002, 13003, 13004, 13005, 13006}},
			{"ends with", 11, []uint32{13001, 13002, 13003, 13004, 13005, 13007}},
		} {
			if got := members(tc.op, "Alpha"); !slices.Equal(got, tc.want) {
				t.Errorf("%s %s Alpha = %v, want %v", prop, tc.name, got, tc.want)
			}
		}
	}
}

func TestFoldText(t *testing.T) {
	for in, want := range map[string]string{
		"Ａｌｐｈａ": "alpha", "Álpha": "alpha", "カタカナ": "かたかな", "ΟΔΟΣ": "οδοσ", "οδος": "οδοσ",
	} {
		if got := foldText(in); got != want {
			t.Errorf("foldText(%q) = %q, want %q", in, got, want)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func finishedEntry() HistoryEntry {
	start := time.Date(2026, 7, 1, 21, 34, 5, 0, time.UTC)
	return HistoryEntry{
		StartedAt:    start,
		EndedAt:      start.Add(4*time.Minute + 32*time.Second),
		DeviceNumber: 2,
		DeviceName:   "CDJ-3000",
		TrackID:      101,
		Title:        "First Track",
		Artist:       "Artist A",
		BPM:          128,
		Key:          "8A",
	}
}

func playingEntry() HistoryEntry {
	// Still playing (no EndedAt), missing title/device name → fallbacks.
	return HistoryEntry{
		StartedAt:    time.Date(2026, 7, 1, 21, 39, 0, 0, time.UTC),
		DeviceNumber: 3,
		TrackID:      202,
		Artist:       "Artist B",
	}
}

func TestRenderEntryText(t *testing.T) {
	out := string(renderEntryText(finishedEntry()))
	for _, want := range []string{
		"2026-07-01 21:34:05", // full date-time (rolls across days)
		"First Track — Artist A [128 BPM 8A]  4:32  (CDJ-3000 #2)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text entry missing %q\n%s", want, out)
		}
	}
	// Fallbacks for a still-playing, untitled, unnamed-deck entry.
	p := string(renderEntryText(playingEntry()))
	if !strings.Contains(p, "Track #202") || !strings.Contains(p, "(player 3)") {
		t.Errorf("fallbacks missing: %s", p)
	}
}

func TestRenderEntryCSV(t *testing.T) {
	// Header + one appended row parse as a 2-row CSV.
	data := append(csvHeaderRow(), renderEntryCSV(finishedEntry())...)
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want header+1 row, got %d rows", len(rows))
	}
	if rows[0][0] != "started" || rows[0][2] != "duration_sec" || len(rows[0]) != 10 {
		t.Errorf("unexpected header (no index col expected): %v", rows[0])
	}
	// started, ended, duration_sec=272, device_number=2, device_name, id, title...
	if rows[1][2] != "272" || rows[1][3] != "2" || rows[1][4] != "CDJ-3000" || rows[1][9] != "8A" {
		t.Errorf("row wrong: %v", rows[1])
	}
	// A still-playing entry has empty ended/duration.
	playing, _ := csv.NewReader(strings.NewReader(string(renderEntryCSV(playingEntry())))).Read()
	if playing[1] != "" || playing[2] != "" {
		t.Errorf("playing entry should have empty ended/duration: %v", playing)
	}
}

func TestRenderEntryJSONL(t *testing.T) {
	// Two appended entries form valid JSONL (one object per line).
	data := append(renderEntryJSON(finishedEntry()), renderEntryJSON(playingEntry())...)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 JSONL lines, got %d", len(lines))
	}
	var a map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &a); err != nil {
		t.Fatalf("line 0 not valid json: %v", err)
	}
	if a["title"] != "First Track" || a["device_name"] != "CDJ-3000" || a["duration_sec"].(float64) != 272 {
		t.Errorf("entry0 wrong: %v", a)
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &b); err != nil {
		t.Fatalf("line 1 not valid json: %v", err)
	}
	if _, ok := b["ended"]; ok { // omitempty on a still-playing entry
		t.Errorf("playing entry should omit 'ended': %v", b)
	}
}

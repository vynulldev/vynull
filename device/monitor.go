// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vynull/library"
	"vynull/pdb"
	"vynull/proto"
)

// PlayerState holds the latest known state of a CDJ.
type PlayerState struct {
	Status    *proto.CDJStatus
	LastSeen  time.Time
	TrackName string
	Artist    string
	Key       string
}

// HistoryEntry records a track that was played.
type HistoryEntry struct {
	StartedAt    time.Time
	EndedAt      time.Time
	DeviceNumber uint8
	DeviceName   string
	TrackID      uint32
	Title        string
	Artist       string
	BPM          float64
	Key          string
}

// PlayerMonitor tracks the state of all CDJs on the network.
type PlayerMonitor struct {
	mu      sync.RWMutex
	players map[uint8]*PlayerState
	pdb     *pdb.Database
	lib     *library.Library

	// Display info.
	AnalysisStatus func() string
	// ExportStatus returns a one-line description of any in-flight USB
	// export, or "" when no export is happening. When non-empty the
	// TUI shows it in place of AnalysisStatus.
	ExportStatus func() string
	APIAddr      string // e.g., "127.0.0.1:9443"

	// Track history.
	histMu  sync.RWMutex
	history []HistoryEntry
	// History output: when historyPath is set, each finalized play is appended
	// to histFile (in historyFormat) as it completes, so the file rolls across
	// sessions and a crash loses at most the currently-playing track.
	// historyPath/historyFormat/histFile are guarded by histWriteMu.
	historyPath   string
	historyFormat string
	histFile      *os.File
	histWriteMu   sync.Mutex
	lastTrackID   map[uint8]uint32    // per-device last known track ID
	lastPlaying   map[uint8]time.Time // when track started playing
	// Play-count tracking (rekordbox-style 50%-played threshold). All keyed
	// by deviceNumber; all reset when a new track is loaded on that device.
	playMs       map[uint8]float64   // accumulated playing time on the current load
	playLastSeen map[uint8]time.Time // wall time of the last status packet from this device
	playCounted  map[uint8]bool      // already incremented PlayCount for this load?

	// OnPlayed, when set, is called with the trackID + device once a
	// track has been played past the 50%-duration threshold (same trigger
	// as PlayCount). main.go wires it to a history-playlist appender so
	// every played track lands in a dated "History · YYYY-MM-DD" playlist
	// without the device package depending on the api package.
	OnPlayed func(trackID uint32, deviceNumber uint8)
}

// NewPlayerMonitor creates a new monitor.
func NewPlayerMonitor(db *pdb.Database, lib *library.Library) *PlayerMonitor {
	return &PlayerMonitor{
		players:      make(map[uint8]*PlayerState),
		pdb:          db,
		lib:          lib,
		lastTrackID:  make(map[uint8]uint32),
		lastPlaying:  make(map[uint8]time.Time),
		playMs:       make(map[uint8]float64),
		playLastSeen: make(map[uint8]time.Time),
		playCounted:  make(map[uint8]bool),
	}
}

// States returns a snapshot of all active player states.
func (m *PlayerMonitor) States() map[uint8]*PlayerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[uint8]*PlayerState, len(m.players))
	now := time.Now()
	for k, v := range m.players {
		if now.Sub(v.LastSeen) < 5*time.Second {
			out[k] = v
		}
	}
	return out
}

// History returns a copy of the track history.
func (m *PlayerMonitor) History() []HistoryEntry {
	m.histMu.RLock()
	defer m.histMu.RUnlock()
	return append([]HistoryEntry(nil), m.history...)
}

// SetHistoryOutput enables a rolling, crash-safe track-history file: every
// finalized play is appended to path (in the given format — "text", "csv", or
// "json") as it completes, so the file accumulates across sessions and a crash
// loses at most the currently-playing track. An empty path disables it. The
// parent directory is created if needed; a header is written once when the
// file is first created (a column row for csv; none for json, which is
// newline-delimited JSON).
func (m *PlayerMonitor) SetHistoryOutput(path, format string) {
	if format != "csv" && format != "json" {
		format = "text"
	}
	m.histWriteMu.Lock()
	defer m.histWriteMu.Unlock()

	if m.histFile != nil { // re-config: close the old handle
		m.histFile.Close()
		m.histFile = nil
	}
	m.historyPath = path
	m.historyFormat = format
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("history: mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	// Write the header only for a brand-new (or empty) file so appends across
	// sessions don't repeat it.
	st, statErr := os.Stat(path)
	isNew := os.IsNotExist(statErr) || (statErr == nil && st.Size() == 0)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("history: open %s: %v", path, err)
		return
	}
	m.histFile = f
	if isNew {
		if hdr := historyHeader(format); len(hdr) > 0 {
			_, _ = f.Write(hdr)
			_ = f.Sync()
		}
	}
}

// HistoryPath returns the configured history output path (empty if disabled).
func (m *PlayerMonitor) HistoryPath() string {
	m.histWriteMu.Lock()
	defer m.histWriteMu.Unlock()
	return m.historyPath
}

// FinalizeHistory closes any still-open entries (sets EndedAt = now so the
// last-played track on each deck gets a duration), appends them, and closes
// the file. Call on shutdown.
func (m *PlayerMonitor) FinalizeHistory() {
	now := time.Now()
	var finalized []HistoryEntry
	m.histMu.Lock()
	for i := range m.history {
		if m.history[i].EndedAt.IsZero() {
			m.history[i].EndedAt = now
			finalized = append(finalized, m.history[i])
		}
	}
	m.histMu.Unlock()

	for _, h := range finalized {
		m.appendHistoryEntry(h)
	}

	m.histWriteMu.Lock()
	if m.histFile != nil {
		m.histFile.Close()
		m.histFile = nil
	}
	m.histWriteMu.Unlock()
}

// appendHistoryEntry appends one finalized play to the history file (fsync'd
// so a crash can't lose an already-completed track). No-op if disabled.
func (m *PlayerMonitor) appendHistoryEntry(h HistoryEntry) {
	m.histWriteMu.Lock()
	defer m.histWriteMu.Unlock()
	if m.histFile == nil {
		return
	}
	if _, err := m.histFile.Write(renderEntry(h, m.historyFormat)); err != nil {
		log.Printf("history: append %s: %v", m.historyPath, err)
		return
	}
	_ = m.histFile.Sync()
}

// historyHeader returns the one-time header for a new history file (a column
// row for csv, a banner for text, nothing for json which is line-delimited).
func historyHeader(format string) []byte {
	switch format {
	case "csv":
		return csvHeaderRow()
	case "json":
		return nil
	default:
		return []byte("# Vynull track history\n\n")
	}
}

// renderEntry serializes a single finalized play for appending, in the given
// format ("csv", "json" as one JSON object per line, or text by default).
func renderEntry(h HistoryEntry, format string) []byte {
	switch format {
	case "csv":
		return renderEntryCSV(h)
	case "json":
		return renderEntryJSON(h)
	default:
		return renderEntryText(h)
	}
}

func historyDeck(h HistoryEntry) string {
	if h.DeviceName != "" {
		return fmt.Sprintf("%s #%d", h.DeviceName, h.DeviceNumber)
	}
	return fmt.Sprintf("player %d", h.DeviceNumber)
}

func renderEntryText(h HistoryEntry) []byte {
	duration := ""
	if !h.EndedAt.IsZero() {
		dur := h.EndedAt.Sub(h.StartedAt)
		duration = fmt.Sprintf("%d:%02d", int(dur.Minutes()), int(dur.Seconds())%60)
	}
	title := h.Title
	if title == "" {
		title = fmt.Sprintf("Track #%d", h.TrackID)
	}
	extra := ""
	if h.BPM > 0 {
		extra = fmt.Sprintf(" [%.0f BPM", h.BPM)
		if h.Key != "" {
			extra += fmt.Sprintf(" %s", h.Key)
		}
		extra += "]"
	}
	return []byte(fmt.Sprintf("%s  %s — %s%s  %s  (%s)\n",
		h.StartedAt.Format("2006-01-02 15:04:05"),
		title, h.Artist, extra, duration, historyDeck(h)))
}

func csvHeaderRow() []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"started", "ended", "duration_sec", "device_number", "device_name", "track_id", "title", "artist", "bpm", "key"})
	w.Flush()
	return b.Bytes()
}

func renderEntryCSV(h HistoryEntry) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	ended, durSec := "", ""
	if !h.EndedAt.IsZero() {
		ended = h.EndedAt.Format(time.RFC3339)
		durSec = strconv.Itoa(int(h.EndedAt.Sub(h.StartedAt).Seconds()))
	}
	bpm := ""
	if h.BPM > 0 {
		bpm = strconv.FormatFloat(h.BPM, 'f', -1, 64)
	}
	_ = w.Write([]string{
		h.StartedAt.Format(time.RFC3339),
		ended, durSec,
		strconv.Itoa(int(h.DeviceNumber)), h.DeviceName,
		strconv.FormatUint(uint64(h.TrackID), 10),
		h.Title, h.Artist, bpm, h.Key,
	})
	w.Flush()
	return b.Bytes()
}

func renderEntryJSON(h HistoryEntry) []byte {
	type entry struct {
		Started      string  `json:"started"`
		Ended        string  `json:"ended,omitempty"`
		DurationSec  int     `json:"duration_sec,omitempty"`
		DeviceNumber uint8   `json:"device_number"`
		DeviceName   string  `json:"device_name,omitempty"`
		TrackID      uint32  `json:"track_id"`
		Title        string  `json:"title"`
		Artist       string  `json:"artist"`
		BPM          float64 `json:"bpm,omitempty"`
		Key          string  `json:"key,omitempty"`
	}
	e := entry{
		Started:      h.StartedAt.Format(time.RFC3339),
		DeviceNumber: h.DeviceNumber,
		DeviceName:   h.DeviceName,
		TrackID:      h.TrackID,
		Title:        h.Title,
		Artist:       h.Artist,
		BPM:          h.BPM,
		Key:          h.Key,
	}
	if !h.EndedAt.IsZero() {
		e.Ended = h.EndedAt.Format(time.RFC3339)
		e.DurationSec = int(h.EndedAt.Sub(h.StartedAt).Seconds())
	}
	data, _ := json.Marshal(e)
	return append(data, '\n')
}

// Update processes a CDJ status packet.
func (m *PlayerMonitor) Update(status *proto.CDJStatus) {
	var trackName, artist, key string
	var bpm float64
	if m.pdb != nil && status.TrackID > 0 {
		t := m.pdb.TrackByID(status.TrackID)
		if t != nil {
			trackName = t.Title
			artist = t.Artist
			key = t.Key
			bpm = float64(t.Tempo) / 100.0
		}
	}
	// Fall back to library tracks (used in lazy-analysis mode without PDB).
	if trackName == "" && m.lib != nil && status.TrackID > 0 {
		t := m.lib.Track(status.TrackID)
		if t != nil {
			trackName = t.Title
			artist = t.Artist
			key = t.Key
			bpm = t.BPM
		}
	}

	now := time.Now()
	dev := status.DeviceNumber

	m.mu.Lock()
	var prevPlay uint8 = 0xff
	var prevTID uint32
	if old := m.players[dev]; old != nil {
		prevPlay = old.Status.PlayState
		prevTID = old.Status.TrackID
	}
	m.players[dev] = &PlayerState{
		Status:    status,
		LastSeen:  now,
		TrackName: trackName,
		Artist:    artist,
		Key:       key,
	}
	m.mu.Unlock()

	// Diagnostic: log play-state / loaded-track transitions so a load failure
	// can be correlated with the deck reaching the ENDED state (the suspected
	// trigger for "stuck on now-loading"). Only fires on change, so a steady
	// playing/paused deck stays quiet.
	if status.PlayState != prevPlay || status.TrackID != prevTID {
		log.Printf("deck %d: play-state %s (0x%02x) track=%d [was 0x%02x track=%d]",
			dev, status.PlayStateString(), status.PlayState, status.TrackID, prevPlay, prevTID)
	}

	// Track history + play-count detection: both keyed off "current track
	// loaded on this device", under the same lock so the two stay consistent.
	var finalized *HistoryEntry // a play that just completed → append it below
	m.histMu.Lock()
	prevTrackID := m.lastTrackID[dev]

	if status.TrackID > 0 && status.IsPlaying && status.TrackID != prevTrackID {
		// New track started playing — close (and append) the previous entry.
		for i := len(m.history) - 1; i >= 0; i-- {
			if m.history[i].DeviceNumber == dev && m.history[i].EndedAt.IsZero() {
				m.history[i].EndedAt = now
				e := m.history[i]
				finalized = &e
				break
			}
		}

		// Add new history entry.
		m.history = append(m.history, HistoryEntry{
			StartedAt:    now,
			DeviceNumber: dev,
			DeviceName:   status.Name,
			TrackID:      status.TrackID,
			Title:        trackName,
			Artist:       artist,
			BPM:          bpm,
			Key:          key,
		})
		m.lastTrackID[dev] = status.TrackID
		// Reset play-count accumulator for the new load. We start the
		// "last seen" timestamp at now so the very first delta is zero
		// rather than several seconds (the gap since this device's last
		// status packet, when it was on the previous track).
		m.playMs[dev] = 0
		m.playLastSeen[dev] = now
		m.playCounted[dev] = false
	} else if status.TrackID == 0 && prevTrackID > 0 {
		// Track was unloaded — close (and append) the entry.
		for i := len(m.history) - 1; i >= 0; i-- {
			if m.history[i].DeviceNumber == dev && m.history[i].EndedAt.IsZero() {
				m.history[i].EndedAt = now
				e := m.history[i]
				finalized = &e
				break
			}
		}
		m.lastTrackID[dev] = 0
		delete(m.playMs, dev)
		delete(m.playLastSeen, dev)
		delete(m.playCounted, dev)
	}

	// Play-count accumulator: add the wall-clock delta since the last status
	// packet whenever this device is playing. Uses elapsed playing time, not
	// seek position — so loops/seeks/pauses all behave correctly, and a user
	// can't bump the count by skipping to the end of the track.
	shouldIncrement := false
	var incrementTrackID uint32
	if status.TrackID > 0 && status.TrackID == m.lastTrackID[dev] && !m.playCounted[dev] {
		if last, ok := m.playLastSeen[dev]; ok && status.IsPlaying {
			delta := now.Sub(last).Seconds() * 1000
			// Guard against absurd deltas from clock jumps or a device that
			// vanished and reappeared mid-track — anything > 2s means we
			// missed several status packets, so don't count that gap.
			if delta > 0 && delta < 2000 {
				m.playMs[dev] += delta
			}
		}
		m.playLastSeen[dev] = now

		// Resolve track duration. Prefer library (always set if we own the
		// track), fall back to PDB. If neither knows the duration we can't
		// compute a threshold — skip silently.
		var durationMs float64
		if m.lib != nil {
			if t := m.lib.Track(status.TrackID); t != nil && t.Duration > 0 {
				durationMs = float64(t.Duration.Duration().Milliseconds())
			}
		}
		if durationMs == 0 && m.pdb != nil {
			if t := m.pdb.TrackByID(status.TrackID); t != nil && t.Duration > 0 {
				durationMs = float64(t.Duration) * 1000
			}
		}
		if durationMs > 0 && m.playMs[dev] >= durationMs*0.5 {
			shouldIncrement = true
			incrementTrackID = status.TrackID
			m.playCounted[dev] = true
		}
	}
	m.histMu.Unlock()

	if finalized != nil {
		m.appendHistoryEntry(*finalized)
	}

	// Persist outside the lock — IncrementPlayCount takes the library mutex
	// and calls Save() which touches disk; we don't want to hold histMu
	// across that. Same reason OnPlayed fires after the unlock.
	if shouldIncrement && m.lib != nil {
		m.lib.IncrementPlayCount(incrementTrackID)
	}
	if shouldIncrement && m.OnPlayed != nil {
		m.OnPlayed(incrementTrackID, dev)
	}
}

// Render returns the current display string.
func (m *PlayerMonitor) Render() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder

	b.WriteString("\033[1m  VYNULL · DJ LINK MONITOR\033[0m")
	if m.APIAddr != "" {
		b.WriteString(fmt.Sprintf("  \033[90mAPI: http://%s\033[0m", m.APIAddr))
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", 72) + "\n")

	var nums []uint8
	for num := range m.players {
		nums = append(nums, num)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })

	now := time.Now()
	var masterBPM float64

	for _, num := range nums {
		p := m.players[num]
		if now.Sub(p.LastSeen) > 5*time.Second {
			continue
		}

		s := p.Status
		if s.IsMaster && s.BPM > 0 {
			masterBPM = float64(s.BPM) / 100.0
		}

		masterTag := "  "
		if s.IsMaster {
			masterTag = "M "
		}

		stateColor := "\033[90m"
		switch {
		case s.IsPlaying:
			stateColor = "\033[32m"
		case s.PlayState == 0x06 || s.PlayState == 0x05:
			stateColor = "\033[33m"
		}

		b.WriteString(fmt.Sprintf("\n  \033[1m%s[%d] %s\033[0m\n", masterTag, s.DeviceNumber, s.Name))

		state := s.PlayStateString()
		b.WriteString(fmt.Sprintf("    %s● %s\033[0m", stateColor, state))
		if s.IsSync {
			b.WriteString("  \033[36mSYNC\033[0m")
		}
		if s.IsOnAir {
			b.WriteString("  \033[31mON-AIR\033[0m")
		}
		b.WriteString("\n")

		if s.TrackID > 0 {
			title := p.TrackName
			if title == "" {
				title = fmt.Sprintf("Track #%d", s.TrackID)
			}
			b.WriteString(fmt.Sprintf("    \033[1m%s\033[0m\n", title))
			if p.Artist != "" {
				b.WriteString(fmt.Sprintf("    %s\n", p.Artist))
			}

			bpm := float64(s.BPM) / 100.0
			pitch := float64(s.Pitch-0x100000) / float64(0x100000) * 100.0
			line := fmt.Sprintf("    BPM: %.1f", bpm)
			if pitch < -0.05 || pitch > 0.05 {
				line += fmt.Sprintf(" (%+.1f%%)", pitch)
			}
			if p.Key != "" {
				line += fmt.Sprintf("  Key: %s", p.Key)
			}
			if s.BeatInBar >= 1 && s.BeatInBar <= 4 {
				beats := [4]string{"·", "·", "·", "·"}
				beats[s.BeatInBar-1] = "●"
				line += fmt.Sprintf("  Beat: %s", strings.Join(beats[:], " "))
			}
			b.WriteString(line + "\n")
		}
	}

	if masterBPM > 0 {
		b.WriteString(fmt.Sprintf("\n  %s\n", strings.Repeat("─", 40)))
		b.WriteString(fmt.Sprintf("  Master Tempo: \033[1m%.1f BPM\033[0m\n", masterBPM))
	}

	// Track history.
	m.histMu.RLock()
	if len(m.history) > 0 {
		b.WriteString(fmt.Sprintf("\n  %s\n", strings.Repeat("─", 72)))
		b.WriteString("  \033[1mTRACK HISTORY\033[0m\n\n")

		// Show last 10 entries, newest first.
		start := 0
		if len(m.history) > 10 {
			start = len(m.history) - 10
		}
		for i := len(m.history) - 1; i >= start; i-- {
			h := m.history[i]
			duration := ""
			if !h.EndedAt.IsZero() {
				dur := h.EndedAt.Sub(h.StartedAt)
				duration = fmt.Sprintf(" (%d:%02d)", int(dur.Minutes()), int(dur.Seconds())%60)
			} else {
				dur := now.Sub(h.StartedAt)
				duration = fmt.Sprintf(" \033[32m▶ %d:%02d\033[0m", int(dur.Minutes()), int(dur.Seconds())%60)
			}

			title := h.Title
			if title == "" {
				title = fmt.Sprintf("Track #%d", h.TrackID)
			}

			extra := ""
			if h.BPM > 0 {
				extra += fmt.Sprintf(" %.0f", h.BPM)
			}
			if h.Key != "" {
				extra += fmt.Sprintf(" %s", h.Key)
			}

			b.WriteString(fmt.Sprintf("  \033[90m%s\033[0m [%d] %s — %s%s%s\n",
				h.StartedAt.Format("15:04:05"),
				h.DeviceNumber,
				title,
				h.Artist,
				extra,
				duration))
		}
	}
	m.histMu.RUnlock()

	// Library/analysis status.
	if m.AnalysisStatus != nil {
		status := m.AnalysisStatus()
		if status != "" {
			b.WriteString(fmt.Sprintf("\n  %s\n", strings.Repeat("─", 72)))
			b.WriteString(fmt.Sprintf("  \033[1mLIBRARY\033[0m  %s\n", status))
		}
	}

	b.WriteString(fmt.Sprintf("\n  \033[90m%s\033[0m\n", now.Format("15:04:05")))

	return b.String()
}

// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"vynull/library"
	"vynull/proto"
)

// ---------- styles ----------

var (
	tabActiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("57")).
			Bold(true).
			Padding(0, 2)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Padding(0, 2)

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	hintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange — used for export progress + other live highlights

	playerNameStyle = lipgloss.NewStyle().Bold(true)
	playingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))  // green
	pausedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
	idleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	syncStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))  // cyan
	onAirStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	masterTagStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)

	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("57"))

	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// ---------- model ----------

type tuiTab int

const (
	tabPlayers tuiTab = iota
	tabLibrary
	tabSettings
)

var tabNames = []string{"Players", "Library", "Settings"}

type tuiModel struct {
	monitor  *PlayerMonitor
	lib      *library.Library
	settings *CDJSettings
	peers    *PeerTracker
	apiAddr  string

	active   tuiTab
	width    int
	height   int

	// Library tab state
	libCursor int
	libOffset int
	libFilter string

	// Settings tab state
	settingsCursor int
	settingsOffset int
}

// NewTUI returns a bubbletea program rendering the player monitor +
// library browser + settings viewer. Run with .Run() — it blocks until
// the user presses 'q' or sends SIGINT/SIGTERM.
func NewTUI(monitor *PlayerMonitor, lib *library.Library, settings *CDJSettings, peers *PeerTracker, apiAddr string) *tea.Program {
	m := tuiModel{
		monitor:  monitor,
		lib:      lib,
		settings: settings,
		peers:    peers,
		apiAddr:  apiAddr,
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

// ---------- bubbletea Model interface ----------

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m tuiModel) Init() tea.Cmd {
	return tick()
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tick()

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right", "l":
			m.active = (m.active + 1) % tuiTab(len(tabNames))
			return m, nil
		case "shift+tab", "left", "h":
			m.active = (m.active + tuiTab(len(tabNames)) - 1) % tuiTab(len(tabNames))
			return m, nil
		case "up", "k":
			m.moveCursor(-1)
			return m, nil
		case "down", "j":
			m.moveCursor(1)
			return m, nil
		case "pgup":
			m.moveCursor(-10)
			return m, nil
		case "pgdown":
			m.moveCursor(10)
			return m, nil
		case "home", "g":
			m.setCursor(0)
			return m, nil
		case "end", "G":
			m.setCursor(1 << 30)
			return m, nil
		}
	}
	return m, nil
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.active {
	case tabLibrary:
		m.libCursor += delta
		if m.libCursor < 0 {
			m.libCursor = 0
		}
		max := len(m.lib.Tracks()) - 1
		if m.libCursor > max {
			m.libCursor = max
		}
	case tabSettings:
		m.settingsCursor += delta
		if m.settingsCursor < 0 {
			m.settingsCursor = 0
		}
	}
}

func (m *tuiModel) setCursor(pos int) {
	switch m.active {
	case tabLibrary:
		max := len(m.lib.Tracks()) - 1
		if pos > max {
			pos = max
		}
		if pos < 0 {
			pos = 0
		}
		m.libCursor = pos
	case tabSettings:
		if pos < 0 {
			pos = 0
		}
		m.settingsCursor = pos
	}
}

func (m tuiModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "(initializing…)"
	}
	divider := dividerStyle.Render(strings.Repeat("─", m.width))

	// Top block: tabs, divider, the active tab's content, then the
	// status line in its original position right under the content's
	// divider. Only the hint bar gets pinned to the bottom — keeping
	// the analysis status near the content is where the eye expects
	// background-task progress.
	var top strings.Builder
	top.WriteString(m.renderTabs())
	top.WriteString("\n")
	top.WriteString(divider)
	top.WriteString("\n")
	switch m.active {
	case tabPlayers:
		top.WriteString(m.renderPlayers())
	case tabLibrary:
		top.WriteString(m.renderLibrary())
	case tabSettings:
		top.WriteString(m.renderSettings())
	}
	top.WriteString("\n")
	top.WriteString(divider)
	top.WriteString("\n")
	if status := m.renderStatus(); status != "" {
		top.WriteString(status)
	}

	// Bottom block: just the keybinding hints, pinned to the terminal
	// bottom so they don't visually crowd the content/status above.
	bottom := m.renderHints()

	// Each "\n" in top consumes one terminal row; the trailing text
	// after the last "\n" consumes one more. Same for bottom. Compute
	// how many rows separate them and fill with blank lines so bottom
	// sticks to the bottom of the screen. Always leave at least one
	// blank line as breathing room when content overflows.
	topRows := strings.Count(top.String(), "\n") + 1
	bottomRows := strings.Count(bottom, "\n") + 1
	pad := m.height - topRows - bottomRows
	if pad < 1 {
		pad = 1
	}
	return top.String() + strings.Repeat("\n", pad) + bottom
}

// ---------- tab rendering ----------

func (m tuiModel) renderTabs() string {
	header := titleStyle.Render("  VYNULL") + "  "
	if m.apiAddr != "" {
		header += dimStyle.Render("API: http://"+m.apiAddr) + "  "
	}
	var tabs []string
	for i, name := range tabNames {
		if tuiTab(i) == m.active {
			tabs = append(tabs, tabActiveStyle.Render(name))
		} else {
			tabs = append(tabs, tabInactiveStyle.Render(name))
		}
	}
	return header + strings.Join(tabs, " ")
}

// renderStatus shows the analysis engine's current activity line (e.g.
// "Analyzing: foo.mp3 (5 queued, 200 tracks, 50 cached)" or "Ready (...)")
// just above the keybinding hints, where the eye naturally lands for
// background-task progress. A live USB export takes precedence and is
// highlighted in the orange accent colour since it's a user-initiated
// action with a wait. Returns "" when no status is available so View
// can skip the line entirely.
func (m tuiModel) renderStatus() string {
	if m.monitor == nil {
		return ""
	}
	if m.monitor.ExportStatus != nil {
		if s := m.monitor.ExportStatus(); s != "" {
			return "  " + accentStyle.Render("⇪ "+s)
		}
	}
	if m.monitor.AnalysisStatus == nil {
		return ""
	}
	status := m.monitor.AnalysisStatus()
	if status == "" {
		return ""
	}
	return "  " + dimStyle.Render("· "+status)
}

func (m tuiModel) renderHints() string {
	common := "tab/←→: switch tab  •  ↑↓: navigate  •  q: quit"
	switch m.active {
	case tabLibrary:
		return hintStyle.Render(common + "  •  g/G: top/bottom")
	default:
		return hintStyle.Render(common)
	}
}

// ---------- Players tab ----------

func (m tuiModel) renderPlayers() string {
	var b strings.Builder

	// Mixer strip: print one line per DJM-class peer at the top, so the
	// user can see the mixer is detected even when no CDJs are playing.
	// Channel-level state (on-air, master, per-channel BPM) isn't
	// parsed yet — placeholder hint until we have a per-model pcap.
	if m.peers != nil {
		for _, p := range m.peers.Peers() {
			if p.DeviceType != proto.DeviceMixer {
				continue
			}
			b.WriteString(fmt.Sprintf("\n  %s %s %s\n",
				masterTagStyle.Render("MIXER"),
				playerNameStyle.Render(fmt.Sprintf("[%d] %s", p.DeviceNumber, p.Name)),
				dimStyle.Render(p.IP.String()+"  · channel state · unparsed"),
			))
		}
	}

	states := m.monitor.States()
	if len(states) == 0 {
		if b.Len() == 0 {
			return dimStyle.Render("  No active players. Waiting for CDJs to announce…")
		}
		return b.String()
	}
	var nums []uint8
	for n := range states {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	for _, num := range nums {
		p := states[num]
		s := p.Status
		masterTag := "  "
		if s.IsMaster {
			masterTag = masterTagStyle.Render("M ")
		}
		b.WriteString(fmt.Sprintf("\n  %s%s\n", masterTag, playerNameStyle.Render(fmt.Sprintf("[%d] %s", s.DeviceNumber, s.Name))))

		state := s.PlayStateString()
		var stateStyled string
		switch {
		case s.IsPlaying:
			stateStyled = playingStyle.Render("● " + state)
		case s.PlayState == 0x05 || s.PlayState == 0x06:
			stateStyled = pausedStyle.Render("● " + state)
		default:
			stateStyled = idleStyle.Render("● " + state)
		}
		extras := ""
		if s.IsSync {
			extras += "  " + syncStyle.Render("SYNC")
		}
		if s.IsOnAir {
			extras += "  " + onAirStyle.Render("ON-AIR")
		}
		b.WriteString("    " + stateStyled + extras + "\n")

		if s.TrackID > 0 {
			title := p.TrackName
			if title == "" {
				title = fmt.Sprintf("Track #%d", s.TrackID)
			}
			b.WriteString("    " + titleStyle.Render(title) + "\n")
			if p.Artist != "" {
				b.WriteString("    " + dimStyle.Render(p.Artist) + "\n")
			}
			bpm := float64(s.BPM) / 100.0
			pitch := float64(s.Pitch-0x100000) / float64(0x100000) * 100.0
			line := fmt.Sprintf("    BPM: %.1f", bpm)
			if pitch < -0.05 || pitch > 0.05 {
				line += fmt.Sprintf("  Pitch: %+.2f%%", pitch)
			}
			if p.Key != "" {
				line += "  Key: " + p.Key
			}
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	return b.String()
}

// ---------- Library tab ----------

func (m tuiModel) renderLibrary() string {
	tracks := m.lib.Tracks()
	if len(tracks) == 0 {
		return dimStyle.Render("\n  Library is empty. Add tracks via POST /api/tracks/add or --music-dir.")
	}
	// Sort by ID for stable display.
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].ID < tracks[j].ID })

	rows := m.viewportRows() - 3 // header + 2 padding
	if rows < 1 {
		rows = 1
	}
	if m.libCursor >= len(tracks) {
		m.libCursor = len(tracks) - 1
	}
	// Adjust offset so cursor stays in view
	if m.libCursor < m.libOffset {
		m.libOffset = m.libCursor
	}
	if m.libCursor >= m.libOffset+rows {
		m.libOffset = m.libCursor - rows + 1
	}
	end := m.libOffset + rows
	if end > len(tracks) {
		end = len(tracks)
	}

	// Keep every "\n" OUTSIDE of lipgloss styled spans. lipgloss's
	// background-style rendering treats trailing whitespace + newlines
	// inside the styled string oddly — the cursor row used to appear
	// shifted right of the rest of the table because the header's
	// styled "\n" interacted with the cursor row's background span.
	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(titleStyle.Render(fmt.Sprintf("%d tracks", len(tracks))))
	b.WriteString(dimStyle.Render(fmt.Sprintf("  (showing %d-%d)", m.libOffset+1, end)))
	b.WriteByte('\n')
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-6s %-30s %-25s %6s %5s",
		"ID", "TITLE", "ARTIST", "BPM", "KEY")))
	b.WriteByte('\n')
	for i := m.libOffset; i < end; i++ {
		t := tracks[i]
		line := fmt.Sprintf("  %-6d %-30s %-25s %6.1f %5s",
			t.ID, truncate(t.Title, 30), truncate(t.Artist, 25), t.BPM, t.Key)
		if i == m.libCursor {
			b.WriteString(cursorStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------- Settings tab ----------

func (m tuiModel) renderSettings() string {
	if m.settings == nil {
		return dimStyle.Render("\n  Settings not available.")
	}
	cfg := m.settings.Config()
	type row struct{ label, value string }
	rows := []row{
		{"--- MYSETTING ---", ""},
		{"on_air_display", cfg.MySetting.OnAirDisplay},
		{"lcd_brightness", cfg.MySetting.LCDBrightness},
		{"quantize", cfg.MySetting.Quantize},
		{"auto_cue_level", cfg.MySetting.AutoCueLevel},
		{"language", cfg.MySetting.Language},
		{"jog_ring_brightness", cfg.MySetting.JogRingBrightness},
		{"slip_flashing", cfg.MySetting.SlipFlashing},
		{"eject_lock", cfg.MySetting.EjectLock},
		{"play_mode", cfg.MySetting.PlayMode},
		{"hotcue_autoload", cfg.MySetting.HotcueAutoload},
		{"time_mode", cfg.MySetting.TimeMode},
		{"jog_mode", cfg.MySetting.JogMode},
		{"tempo_range", cfg.MySetting.TempoRange},
		{"phase_meter", cfg.MySetting.PhaseMeter},
		{"--- MYSETTING2 ---", ""},
		{"vinyl_speed_adjust", cfg.MySetting2.VinylSpeedAdjust},
		{"jog_display_mode", cfg.MySetting2.JogDisplayMode},
		{"waveform", cfg.MySetting2.Waveform},
		{"beat_jump_beat_value", cfg.MySetting2.BeatJumpBeatValue},
		{"--- DEVSETTING ---", ""},
		{"overview_type", cfg.DevSetting.OverviewType},
		{"waveform_color", cfg.DevSetting.WaveformColor},
		{"key_display", cfg.DevSetting.KeyDisplay},
		{"wave_position", cfg.DevSetting.WavePosition},
	}

	var b strings.Builder
	b.WriteString("\n  " + dimStyle.Render("(read-only view — edit settings.json to change)") + "\n\n")
	for i, r := range rows {
		if r.value == "" {
			b.WriteString("  " + titleStyle.Render(r.label) + "\n")
			continue
		}
		line := fmt.Sprintf("  %-26s %s", r.label, r.value)
		_ = i
		b.WriteString(line + "\n")
	}
	return b.String()
}

// ---------- helpers ----------

func (m tuiModel) viewportRows() int {
	// reserve lines for header (2), divider (2), status (1), hints (2)
	return m.height - 7
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return s[:n-1] + "…"
}

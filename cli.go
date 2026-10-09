// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

// CLI subcommands: thin clients for the HTTP API of a RUNNING vynull server,
// so the library is scriptable from the shell (fzf/rofi pickers, watch-folder
// scripts, cron). `vynull <command> ...` runs a subcommand and exits;
// `vynull` with no subcommand (or with only flags) starts the server as
// always.
//
//	vynull search [query]        list/filter tracks
//	vynull add <path>...         add files or folders to the library
//	vynull load <track> <deck>   load a track on a CDJ (1-4); track = ID or search
//	vynull players               connected players and what they're playing
//	vynull playlists             playlists with counts
//	vynull status                server, analysis, and link state
//
// Every subcommand takes --addr (default http://127.0.0.1:9443, or
// VYNULL_ADDR) and the listing ones take --json for machine-readable output.

// cliCommands is the single source of truth for the subcommand surface:
// the dispatcher, the server --help output, and per-command -h all render
// from it, so they cannot drift.
var cliCommands = []struct {
	name, args, desc string
	fn               func([]string) error
}{
	{name: "search", args: "[query]", desc: "list tracks, filtered by title/artist/album; --json for pipelines"},
	{name: "add", args: "<path>...", desc: "add files or folders to the library"},
	{name: "load", args: "<track-id|query> <deck 1-4>", desc: "load a track on a CDJ; a query must match exactly one track"},
	{name: "players", desc: "connected players and what they're playing"},
	{name: "playlists", desc: "playlists with counts"},
	{name: "status", desc: "server, analysis, and link state"},
	{name: "sim", args: "<status|add|remove|renumber|load|play|pause|cue|seek|pitch|onair|master|eject> [args] [--deck N]", desc: "manage and drive virtual playing CDJs (server needs --simulate)"},
}

// The handlers are bound here rather than in the literal: cliFlags reads
// cliCommands for per-command -h, which would otherwise make the package
// variable initialization cyclic.
func init() {
	fns := map[string]func([]string) error{
		"search": cliSearch, "add": cliAdd, "load": cliLoad,
		"players": cliPlayers, "playlists": cliPlaylists, "status": cliStatus,
		"sim": cliSim,
	}
	for i := range cliCommands {
		cliCommands[i].fn = fns[cliCommands[i].name]
	}
}

// printCommandsUsage renders the subcommand section (shared with the server
// --help output in config.go).
func printCommandsUsage(w io.Writer) {
	fmt.Fprintln(w, "Commands (clients for a running server):")
	for _, c := range cliCommands {
		sig := c.name
		if c.args != "" {
			sig += " " + c.args
		}
		fmt.Fprintf(w, "  %-34s %s\n", sig, c.desc)
	}
	fmt.Fprintln(w, "\n  Commands talk to http://127.0.0.1:9443; override with --addr or VYNULL_ADDR.")
	fmt.Fprintln(w)
}

// runCLI dispatches os.Args-style args; returns false when the first arg is
// not a subcommand (i.e. the caller should start the server).
func runCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" {
		printCommandsUsage(os.Stdout)
		fmt.Println("Run with --help for the server flags, or <command> -h for a command.")
		return true
	}
	for _, c := range cliCommands {
		if c.name != args[0] {
			continue
		}
		if err := c.fn(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "vynull "+args[0]+":", err)
			os.Exit(1)
		}
		return true
	}
	return false
}

// cliClient holds the per-invocation flags shared by all subcommands.
type cliClient struct {
	addr string
	json bool
}

// cliFlags parses --addr/--json from args, returning the client and the
// remaining positional args.
func cliFlags(cmd string, args []string, withJSON bool) (*cliClient, []string, error) {
	c := &cliClient{addr: os.Getenv("VYNULL_ADDR")}
	if c.addr == "" {
		c.addr = "http://127.0.0.1:9443"
	}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--addr":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--addr requires a value")
			}
			i++
			c.addr = args[i]
		case strings.HasPrefix(a, "--addr="):
			c.addr = strings.TrimPrefix(a, "--addr=")
		case a == "--json" && withJSON:
			c.json = true
		case a == "-h" || a == "--help":
			for _, c := range cliCommands {
				if strings.HasPrefix(cmd, c.name) {
					sig := c.name
					if c.args != "" {
						sig += " " + c.args
					}
					fmt.Printf("usage: vynull %s\n  %s\n", sig, c.desc)
					os.Exit(0)
				}
			}
			return nil, nil, fmt.Errorf("usage: vynull %s", cmd)
		case strings.HasPrefix(a, "-"):
			return nil, nil, fmt.Errorf("unknown flag %q", a)
		default:
			rest = append(rest, a)
		}
	}
	if !strings.HasPrefix(c.addr, "http") {
		c.addr = "http://" + c.addr
	}
	return c, rest, nil
}

func (c *cliClient) get(path string, out any) error {
	resp, err := http.Get(c.addr + path)
	if err != nil {
		return fmt.Errorf("%v (is the server running? set --addr or VYNULL_ADDR)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *cliClient) post(path string, body any, out any) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(c.addr+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%v (is the server running? set --addr or VYNULL_ADDR)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// cliTrack mirrors the /api/tracks fields the CLI shows.
type cliTrack struct {
	ID     uint32  `json:"id"`
	Title  string  `json:"title"`
	Artist string  `json:"artist"`
	Album  string  `json:"album"`
	BPM    float64 `json:"bpm"`
	Key    string  `json:"key"`
}

func (c *cliClient) tracks(query string) ([]cliTrack, error) {
	var ts []cliTrack
	if err := c.get("/api/tracks", &ts); err != nil {
		return nil, err
	}
	if query == "" {
		return ts, nil
	}
	q := strings.ToLower(query)
	var hits []cliTrack
	for _, t := range ts {
		if strings.Contains(strings.ToLower(t.Title), q) ||
			strings.Contains(strings.ToLower(t.Artist), q) ||
			strings.Contains(strings.ToLower(t.Album), q) {
			hits = append(hits, t)
		}
	}
	return hits, nil
}

func cliSearch(args []string) error {
	c, rest, err := cliFlags("search [query]", args, true)
	if err != nil {
		return err
	}
	hits, err := c.tracks(strings.Join(rest, " "))
	if err != nil {
		return err
	}
	if c.json {
		return json.NewEncoder(os.Stdout).Encode(hits)
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tBPM\tKEY\tARTIST\tTITLE")
	for _, t := range hits {
		bpm := ""
		if t.BPM > 0 {
			bpm = strconv.FormatFloat(t.BPM, 'f', -1, 64)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.ID, bpm, t.Key, t.Artist, t.Title)
	}
	return w.Flush()
}

func cliAdd(args []string) error {
	c, rest, err := cliFlags("add <path>...", args, false)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: vynull add <file-or-folder>...")
	}
	// The server resolves paths on ITS filesystem; make them absolute so a
	// relative invocation works from any directory (same-host assumption).
	paths := make([]string, len(rest))
	for i, p := range rest {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		paths[i] = abs
	}
	var resp struct {
		Status string `json:"status"`
		Added  int    `json:"added"`
		Total  int    `json:"total"`
		JobID  string `json:"job_id"`
	}
	if err := c.post("/api/tracks/add", map[string]any{"paths": paths}, &resp); err != nil {
		return err
	}
	if resp.JobID != "" {
		fmt.Printf("queued (job %s) — %d tracks in library; analysis runs in the background\n", resp.JobID, resp.Total)
		return nil
	}
	fmt.Printf("added %d (library now %d tracks); analysis runs in the background\n", resp.Added, resp.Total)
	return nil
}

func cliLoad(args []string) error {
	c, rest, err := cliFlags("load <track-id|query> <deck 1-4>", args, false)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: vynull load <track-id|query> <deck 1-4>")
	}
	deck, err := strconv.Atoi(rest[len(rest)-1])
	if err != nil || deck < 1 || deck > 4 {
		return fmt.Errorf("deck must be 1-4 (got %q)", rest[len(rest)-1])
	}
	sel := strings.Join(rest[:len(rest)-1], " ")
	trackID, title, err := c.resolveOneTrack(sel)
	if err != nil {
		return err
	}
	if err := c.post("/api/load", map[string]any{"track_id": trackID, "device_number": deck}, nil); err != nil {
		return err
	}
	if title != "" {
		fmt.Printf("loading %q (track %d) on deck %d\n", title, trackID, deck)
	} else {
		fmt.Printf("loading track %d on deck %d\n", trackID, deck)
	}
	return nil
}

func cliPlayers(args []string) error {
	c, _, err := cliFlags("players", args, true)
	if err != nil {
		return err
	}
	var players []struct {
		DeviceNumber uint8   `json:"device_number"`
		Name         string  `json:"name"`
		TrackTitle   string  `json:"track_title"`
		Artist       string  `json:"artist"`
		BPM          float64 `json:"bpm"`
		Key          string  `json:"key"`
		IsPlaying    bool    `json:"is_playing"`
		IsMaster     bool    `json:"is_master"`
		OnAir        bool    `json:"on_air"`
		Source       string  `json:"source"`
	}
	if err := c.get("/api/players", &players); err != nil {
		return err
	}
	if c.json {
		return json.NewEncoder(os.Stdout).Encode(players)
	}
	if len(players) == 0 {
		fmt.Println("no players on the link")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DECK\tNAME\tSTATE\tBPM\tKEY\tTRACK")
	for _, p := range players {
		state := "idle"
		if p.IsPlaying {
			state = "playing"
		}
		if p.IsMaster {
			state += "*"
		}
		if p.OnAir {
			state += " on-air"
		}
		track := p.TrackTitle
		if p.Artist != "" {
			track = p.Artist + " — " + track
		}
		if p.Source != "" {
			track += " [" + p.Source + "]"
		}
		bpm := ""
		if p.BPM > 0 {
			bpm = strconv.FormatFloat(p.BPM, 'f', 1, 64)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", p.DeviceNumber, p.Name, state, bpm, p.Key, track)
	}
	return w.Flush()
}

func cliPlaylists(args []string) error {
	c, _, err := cliFlags("playlists", args, true)
	if err != nil {
		return err
	}
	var pls []struct {
		ID       uint32   `json:"id"`
		Name     string   `json:"name"`
		IsFolder bool     `json:"is_folder"`
		IsSmart  bool     `json:"is_smart"`
		TrackIDs []uint32 `json:"track_ids"`
	}
	if err := c.get("/api/playlists", &pls); err != nil {
		return err
	}
	if c.json {
		return json.NewEncoder(os.Stdout).Encode(pls)
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tTRACKS\tNAME")
	for _, p := range pls {
		kind, n := "playlist", strconv.Itoa(len(p.TrackIDs))
		if p.IsFolder {
			kind, n = "folder", "-"
		} else if p.IsSmart {
			kind, n = "smart", "~"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", p.ID, kind, n, p.Name)
	}
	return w.Flush()
}

func cliStatus(args []string) error {
	c, _, err := cliFlags("status", args, true)
	if err != nil {
		return err
	}
	var st struct {
		DeviceName   string `json:"device_name"`
		DeviceNumber uint8  `json:"device_number"`
		TrackCount   int    `json:"track_count"`
		Peers        []struct {
			Name       string `json:"name"`
			DeviceType string `json:"device_type"`
		} `json:"peers"`
		Players  []json.RawMessage `json:"players"`
		Analysis *struct {
			Status   string `json:"status"`
			Pending  int    `json:"pending"`
			Analyzed int    `json:"analyzed"`
			Cached   int    `json:"cached"`
		} `json:"analysis"`
	}
	if err := c.get("/api/status", &st); err != nil {
		return err
	}
	if c.json {
		return json.NewEncoder(os.Stdout).Encode(st)
	}
	fmt.Printf("%s (device %d) — %d tracks\n", st.DeviceName, st.DeviceNumber, st.TrackCount)
	fmt.Printf("peers: %d", len(st.Peers))
	for _, p := range st.Peers {
		fmt.Printf("  [%s %s]", p.DeviceType, p.Name)
	}
	fmt.Printf("\nplayers on link: %d\n", len(st.Players))
	if st.Analysis != nil {
		fmt.Printf("analysis: %s\n", st.Analysis.Status)
	}
	return nil
}

// resolveOneTrack turns a selector (a numeric track ID or a search query that
// must match exactly one track) into a track ID. On an ambiguous query it
// lists the matches to stderr and returns an error.
func (c *cliClient) resolveOneTrack(sel string) (uint32, string, error) {
	if id, err := strconv.ParseUint(sel, 10, 32); err == nil {
		return uint32(id), "", nil
	}
	hits, err := c.tracks(sel)
	if err != nil {
		return 0, "", err
	}
	switch len(hits) {
	case 0:
		return 0, "", fmt.Errorf("no track matches %q", sel)
	case 1:
		return hits[0].ID, hits[0].Title, nil
	default:
		for _, t := range hits {
			fmt.Fprintf(os.Stderr, "  %d  %s — %s\n", t.ID, t.Artist, t.Title)
		}
		return 0, "", fmt.Errorf("%q matches %d tracks — use the ID", sel, len(hits))
	}
}

// cliSimStatus mirrors device.SimDeckStatus (one deck). cliSimDecks is the
// manager-level /api/sim/status payload.
type cliSimStatus struct {
	Number       uint8   `json:"number"`
	Loaded       bool    `json:"loaded"`
	TrackID      uint32  `json:"track_id"`
	State        string  `json:"state"`
	Playing      bool    `json:"playing"`
	PositionMs   float64 `json:"position_ms"`
	DurationMs   float64 `json:"duration_ms"`
	BPM          float64 `json:"bpm"`
	EffectiveBPM float64 `json:"effective_bpm"`
	PitchPct     float64 `json:"pitch_pct"`
	BeatInTrack  uint32  `json:"beat_in_track"`
	BeatInBar    uint8   `json:"beat_in_bar"`
	OnAir        bool    `json:"on_air"`
	Master       bool    `json:"master"`
	Sync         bool    `json:"sync"`
}

type cliSimDecks struct {
	Decks []cliSimStatus `json:"decks"`
	Max   int            `json:"max"`
}

func simPitchStr(p float64) string {
	s := strconv.FormatFloat(p, 'f', 1, 64)
	if p >= 0 {
		s = "+" + s
	}
	return s + "%"
}

// cliSim manages and drives the virtual playing decks. Manager subcommands
// (status/add/remove/renumber) act on the set of decks; the transport
// subcommands target one deck chosen with --deck (default: the lowest number).
func cliSim(args []string) error {
	// Pull --deck N / --deck=N before the shared flag parser rejects it.
	deck := 0
	var filtered []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--deck":
			if i+1 >= len(args) {
				return fmt.Errorf("--deck requires a number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("--deck must be a number (got %q)", args[i])
			}
			deck = n
		case strings.HasPrefix(a, "--deck="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--deck="))
			if err != nil {
				return fmt.Errorf("--deck must be a number")
			}
			deck = n
		default:
			filtered = append(filtered, a)
		}
	}

	c, rest, err := cliFlags("sim <status|add|remove|renumber|load|play|pause|cue|seek|pitch|onair|master|eject> [args] [--deck N]", filtered, true)
	if err != nil {
		return err
	}
	action := "status"
	if len(rest) > 0 {
		action = rest[0]
		rest = rest[1:]
	}

	switch action {
	case "status", "list", "ls":
		return c.cliSimList()
	case "add":
		num := 0
		if len(rest) > 0 {
			if num, err = strconv.Atoi(rest[0]); err != nil {
				return fmt.Errorf("usage: vynull sim add [number]")
			}
		}
		if err := c.post("/api/sim/add", map[string]any{"number": num}, nil); err != nil {
			return err
		}
		return c.cliSimList()
	case "remove", "rm":
		if len(rest) < 1 {
			return fmt.Errorf("usage: vynull sim remove <number>")
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return fmt.Errorf("player number must be a number (got %q)", rest[0])
		}
		if err := c.post("/api/sim/remove", map[string]any{"number": n}, nil); err != nil {
			return err
		}
		return c.cliSimList()
	case "renumber", "mv":
		if len(rest) < 2 {
			return fmt.Errorf("usage: vynull sim renumber <old> <new>")
		}
		from, e1 := strconv.Atoi(rest[0])
		to, e2 := strconv.Atoi(rest[1])
		if e1 != nil || e2 != nil {
			return fmt.Errorf("usage: vynull sim renumber <old> <new>")
		}
		if err := c.post("/api/sim/renumber", map[string]any{"number": from, "to": to}, nil); err != nil {
			return err
		}
		return c.cliSimList()
	}

	// Per-deck transport actions. Default to the lowest-numbered deck.
	if deck == 0 {
		if deck, err = c.simDefaultDeck(); err != nil {
			return err
		}
	}
	base := "/api/sim/" + strconv.Itoa(deck) + "/"

	var st cliSimStatus
	switch action {
	case "play", "pause", "cue", "eject":
		err = c.post(base+action, nil, &st)
	case "load":
		if len(rest) < 1 {
			return fmt.Errorf("usage: vynull sim load <track-id|query> [--deck N]")
		}
		trackID, _, rerr := c.resolveOneTrack(strings.Join(rest, " "))
		if rerr != nil {
			return rerr
		}
		err = c.post(base+"load", map[string]any{"track_id": trackID}, &st)
	case "seek":
		if len(rest) < 1 {
			return fmt.Errorf("usage: vynull sim seek <ms|m:ss> [--deck N]")
		}
		ms, perr := parsePosition(rest[0])
		if perr != nil {
			return perr
		}
		err = c.post(base+"seek", map[string]any{"position_ms": ms}, &st)
	case "pitch":
		if len(rest) < 1 {
			return fmt.Errorf("usage: vynull sim pitch <percent> [--deck N]")
		}
		pct, perr := strconv.ParseFloat(strings.TrimSuffix(rest[0], "%"), 64)
		if perr != nil {
			return fmt.Errorf("pitch must be a number (got %q)", rest[0])
		}
		err = c.post(base+"pitch", map[string]any{"pitch_pct": pct}, &st)
	case "onair", "master":
		on := true
		if len(rest) > 0 {
			switch strings.ToLower(rest[0]) {
			case "on", "true", "1", "yes":
				on = true
			case "off", "false", "0", "no":
				on = false
			default:
				return fmt.Errorf("usage: vynull sim %s [on|off] [--deck N]", action)
			}
		}
		err = c.post(base+action, map[string]any{"on": on}, &st)
	default:
		return fmt.Errorf("unknown sim action %q", action)
	}
	if err != nil {
		return err
	}

	if c.json {
		return json.NewEncoder(os.Stdout).Encode(st)
	}
	if !st.Loaded {
		fmt.Printf("player %d: %s%s (no track)\n", st.Number, st.State, simFlagsStr(st))
		return nil
	}
	fmt.Printf("player %d: %s%s  %s/%s  %.1f→%.1f BPM  pitch %s  beat %d (%d/4)  track #%d\n",
		st.Number, st.State, simFlagsStr(st), fmtMs(st.PositionMs), fmtMs(st.DurationMs),
		st.BPM, st.EffectiveBPM, simPitchStr(st.PitchPct), st.BeatInTrack, st.BeatInBar, st.TrackID)
	return nil
}

// simFlagsStr renders the active on-air/master/sync flags as a compact suffix.
func simFlagsStr(st cliSimStatus) string {
	var f []string
	if st.OnAir {
		f = append(f, "ON-AIR")
	}
	if st.Master {
		f = append(f, "MASTER")
	}
	if st.Sync {
		f = append(f, "SYNC")
	}
	if len(f) == 0 {
		return ""
	}
	return " [" + strings.Join(f, " ") + "]"
}

// simDefaultDeck returns the lowest-numbered deck, erroring when none exist.
func (c *cliClient) simDefaultDeck() (int, error) {
	var list cliSimDecks
	if err := c.get("/api/sim/status", &list); err != nil {
		return 0, err
	}
	if len(list.Decks) == 0 {
		return 0, fmt.Errorf("no virtual decks (add one with 'vynull sim add')")
	}
	return int(list.Decks[0].Number), nil // decks come sorted ascending
}

// cliSimList prints the deck table (or JSON).
func (c *cliClient) cliSimList() error {
	var list cliSimDecks
	if err := c.get("/api/sim/status", &list); err != nil {
		return err
	}
	if c.json {
		return json.NewEncoder(os.Stdout).Encode(list)
	}
	if len(list.Decks) == 0 {
		fmt.Printf("no virtual CDJs (max %d; add one with 'vynull sim add')\n", list.Max)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DECK\tSTATE\tPOS/DUR\tBPM\tPITCH\tFLAGS\tTRACK")
	for _, d := range list.Decks {
		track, bpm := "-", "-"
		if d.Loaded {
			track = fmt.Sprintf("#%d", d.TrackID)
			bpm = fmt.Sprintf("%.1f→%.1f", d.BPM, d.EffectiveBPM)
		}
		flags := strings.TrimSpace(strings.Trim(simFlagsStr(d), "[]"))
		if flags == "" {
			flags = "-"
		}
		fmt.Fprintf(w, "%d\t%s\t%s/%s\t%s\t%s\t%s\t%s\n",
			d.Number, d.State, fmtMs(d.PositionMs), fmtMs(d.DurationMs), bpm, simPitchStr(d.PitchPct), flags, track)
	}
	return w.Flush()
}

// parsePosition accepts a raw millisecond value or an "m:ss" timestamp.
func parsePosition(s string) (float64, error) {
	if strings.Contains(s, ":") {
		parts := strings.SplitN(s, ":", 2)
		m, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		sec, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("bad position %q (use ms or m:ss)", s)
		}
		return (float64(m)*60 + sec) * 1000, nil
	}
	ms, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad position %q (use ms or m:ss)", s)
	}
	return ms, nil
}

// fmtMs renders milliseconds as m:ss.
func fmtMs(ms float64) string {
	if ms < 0 {
		ms = 0
	}
	total := int(ms / 1000)
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

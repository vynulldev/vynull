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
}

// The handlers are bound here rather than in the literal: cliFlags reads
// cliCommands for per-command -h, which would otherwise make the package
// variable initialization cyclic.
func init() {
	fns := map[string]func([]string) error{
		"search": cliSearch, "add": cliAdd, "load": cliLoad,
		"players": cliPlayers, "playlists": cliPlaylists, "status": cliStatus,
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
	var trackID uint32
	var title string
	if id, err := strconv.ParseUint(sel, 10, 32); err == nil {
		trackID = uint32(id)
	} else {
		// Query form: must match exactly one track, or we list the matches.
		hits, err := c.tracks(sel)
		if err != nil {
			return err
		}
		switch len(hits) {
		case 0:
			return fmt.Errorf("no track matches %q", sel)
		case 1:
			trackID, title = hits[0].ID, hits[0].Title
		default:
			for _, t := range hits {
				fmt.Fprintf(os.Stderr, "  %d  %s — %s\n", t.ID, t.Artist, t.Title)
			}
			return fmt.Errorf("%q matches %d tracks — use the ID", sel, len(hits))
		}
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

// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"vynull/analysis"
	"vynull/proto"
)

type Config struct {
	Interface    string
	MusicDir     string
	DeviceNumber uint8
	DeviceName   string
	DeviceType   proto.DeviceType
	MediaSlot    uint8
	GenerateDir  string // if set, generate USB structure instead of serving
	GenerateCopy bool   // copy files instead of symlinking
	LazyAnalysis bool   // if true, analyze tracks on-demand instead of upfront
	Transcode    bool   // if true, transcode FLAC/WAV/AIFF to MP3 for NFS serving
	DataDir      string // directory for cached analysis and settings data
	GPU          bool   // if true, use GPU-accelerated analysis (requires cuda build tag)
	RGB3Band     bool   // if true, encode PWV5/PWV4 with per-band global normalization (3-band RGB style)
	ReplayDir    string // if set, replay captured rekordbox responses from this directory
	AnalyzeFile  string // if set, analyze this file and print beat info
	AnalyzePDB   string // optional PDB file to show rekordbox data for comparison
	AnalyzeANLZ  string // if set, dump ANLZ file sections (.DAT or .EXT)
	AnalyzeHTML  string // if set, write HTML comparison report to this file
	AnalyzeCSV   string // if set, write per-section CSV files for --anlz into this directory
	PWV4Override string // path to raw PWV4 bytes to inject for every track at serve time
	PWV5Override string // path to raw PWV5 bytes to inject for every track at serve time
	SettingsFile   string // path to JSON settings config (default: <data-dir>/settings.json)
	ImportSettings string // path to a PIONEER directory containing rekordbox .DAT files to import
	ExportPlaylist string // with --generate, exports only this playlist (matched by name) — USB tree contains just that playlist
	Web            bool   // if true, serve an HTML UI at the API listen address
	Listen         string // API + web listen address (default: 127.0.0.1:9443; use 0.0.0.0:9443 to expose to LAN)
	LogLevel       string // log verbosity: error|warn|info|debug|trace (default: info)
}

func parseFlags() Config {
	var cfg Config
	var deviceNum int
	var mode string

	flag.StringVar(&cfg.Interface, "interface", "", "network interface to use (required for serving)")
	flag.StringVar(&cfg.MusicDir, "music-dir", "", "path to music directory (required)")
	flag.IntVar(&deviceNum, "device-number", 0, "device number (default: 17 for rekordbox, 3 for cdj)")
	flag.StringVar(&cfg.DeviceName, "device-name", "", "device name broadcast to CDJs")
	flag.StringVar(&mode, "mode", "rekordbox", "emulation mode: rekordbox or cdj")
	flag.StringVar(&cfg.GenerateDir, "generate", "", "generate Rekordbox USB structure at this path (no server)")
	flag.BoolVar(&cfg.GenerateCopy, "copy-files", false, "copy files when generating (default: symlink)")
	flag.BoolVar(&cfg.LazyAnalysis, "lazy-analysis", false, "analyze tracks on-demand when CDJs request them (fast startup)")
	flag.BoolVar(&cfg.Transcode, "transcode", false, "transcode FLAC/WAV/AIFF to MP3 for CDJ playback")
	flag.BoolVar(&cfg.GPU, "gpu", analysis.GPUDefault, "use GPU-accelerated analysis. Default is true on cuda builds and false otherwise. Pass --gpu=false to force CPU on a cuda build.")
	flag.BoolVar(&cfg.RGB3Band, "rgb-3band", false, "encode PWV5/PWV4 waveforms with per-band global normalization (3-band RGB style — CDJs show more dynamic mid/high content). Bumps cache key; existing cached analyses regenerate.")
	flag.StringVar(&cfg.DataDir, "data-dir", "", "directory for cached analysis/settings (default: ~/.vynull)")
	flag.StringVar(&cfg.ReplayDir, "replay", "", "replay captured rekordbox response packets from this directory")
	flag.StringVar(&cfg.AnalyzeFile, "analyze", "", "analyze a single audio file and print beat/BPM info (no server)")
	flag.StringVar(&cfg.AnalyzePDB, "pdb", "", "PDB file to show rekordbox track data (use with --analyze or standalone)")
	flag.StringVar(&cfg.AnalyzeANLZ, "anlz", "", "dump all sections from an ANLZ .DAT or .EXT file")
	flag.StringVar(&cfg.AnalyzeHTML, "html", "", "write HTML analysis comparison report to this file")
	flag.StringVar(&cfg.AnalyzeCSV, "anlz-csv", "", "with --anlz, write per-section CSV files (pwv4.csv, pwv5.csv) to this directory")
	flag.StringVar(&cfg.PWV4Override, "pwv4-override", "", "inject raw PWV4 bytes from this file for every track at serve time (for CDJ rendering experiments)")
	flag.StringVar(&cfg.PWV5Override, "pwv5-override", "", "inject raw PWV5 bytes from this file for every track at serve time (for CDJ rendering experiments)")
	flag.StringVar(&cfg.SettingsFile, "settings", "", "path to JSON CDJ settings config (default: <data-dir>/settings.json; created with defaults if missing; legacy settings.yaml is migrated automatically)")
	flag.StringVar(&cfg.ImportSettings, "import-settings", "", "import rekordbox MYSETTING/MYSETTING2/DJMMYSETTING/DEVSETTING .DAT files from this directory into the JSON config and exit (point at a /PIONEER directory on a rekordbox USB)")
	flag.StringVar(&cfg.ExportPlaylist, "export-playlist", "", "with --generate, export only this playlist (matched by name, case-insensitive). The resulting USB contains just the playlist's tracks and a single-playlist tree.")
	flag.BoolVar(&cfg.Web, "web", false, "serve an HTML UI alongside the existing JSON API (off by default)")
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:9443", "API + web listen address. Use 0.0.0.0:9443 to expose on all interfaces (e.g. for access from another device on the LAN)")
	flag.StringVar(&cfg.LogLevel, "log-level", "info", "log verbosity: error, warn, info, debug, or trace. Trace adds per-packet NFS / dbserver hex dumps; debug adds mount and portmap detail. Default info keeps the operationally-useful lines visible without per-packet spam.")

	flag.Usage = printGroupedUsage
	flag.Parse()

	// Default data dir.
	if cfg.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.DataDir = home + "/.vynull"
	}

	switch mode {
	case "rekordbox":
		cfg.DeviceType = proto.DeviceRekordbox
		cfg.MediaSlot = proto.SlotRekordbox
		if deviceNum == 0 {
			deviceNum = 17
		}
		if cfg.DeviceName == "" {
			// Displayed name on the CDJ. rekordbox sends "rekordbox"
			// here; the deck appears to grant "rekordbox source" treatment
			// from the device number / slot type / link handshake rather
			// than this string, so showing our own name should be cosmetic.
			// If a deck ever refuses the link or settings after this, revert
			// to "rekordbox" (or pass --device-name rekordbox).
			cfg.DeviceName = "Vynull"
		}
	case "cdj":
		cfg.DeviceType = proto.DeviceCDJ
		cfg.MediaSlot = proto.SlotUSB
		if deviceNum == 0 {
			deviceNum = 3
		}
		if cfg.DeviceName == "" {
			cfg.DeviceName = "Virtual CDJ"
		}
	default:
		fmt.Fprintf(os.Stderr, "error: --mode must be 'rekordbox' or 'cdj'\n")
		os.Exit(1)
	}

	cfg.DeviceNumber = uint8(deviceNum)

	// --analyze / --pdb / --anlz / --html / --import-settings mode needs no other flags.
	if cfg.AnalyzeFile != "" || cfg.AnalyzePDB != "" || cfg.AnalyzeANLZ != "" || cfg.AnalyzeHTML != "" || cfg.ImportSettings != "" {
		cfg.DeviceNumber = uint8(deviceNum)
		return cfg
	}

	var errs []string
	if cfg.GenerateDir == "" && cfg.Interface == "" {
		errs = append(errs, "--interface is required (unless using --generate)")
	}
	if cfg.MusicDir == "" && cfg.GenerateDir != "" {
		errs = append(errs, "--music-dir is required for --generate")
	}
	if deviceNum < 1 || deviceNum > 127 {
		errs = append(errs, "--device-number must be between 1 and 127")
	}

	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "error: %s\n", e)
		}
		flag.Usage()
		os.Exit(1)
	}

	return cfg
}

// ---------- grouped --help output ----------

// flagGroup names a section in the --help output and the flags that
// belong to it. Listing here is the source of truth for display order;
// flag definitions live above in parseFlags and stay in alphabetical
// order so the Go flag package can register and parse them.
type flagGroup struct {
	title string
	flags []string
}

var flagGroups = []flagGroup{
	{"Network identity (required to serve)", []string{
		"interface", "mode", "device-number", "device-name",
	}},
	{"Library + analysis", []string{
		"music-dir", "data-dir", "lazy-analysis", "transcode", "gpu",
	}},
	{"CDJ settings", []string{
		"settings", "import-settings",
	}},
	{"HTTP API and Web UI", []string{
		"listen", "web",
	}},
	{"Logging", []string{
		"log-level",
	}},
	{"USB export (alternative to serving)", []string{
		"generate", "copy-files", "export-playlist",
	}},
	{"One-shot inspection (exits after)", []string{
		"analyze", "pdb", "anlz", "anlz-csv", "html",
	}},
	{"Waveform reverse-engineering / experiments", []string{
		"rgb-3band", "pwv4-override", "pwv5-override", "replay",
	}},
}

// printGroupedUsage replaces flag.PrintDefaults' alphabetical dump with
// flags organised into logical groups (see flagGroups). Mirrors the
// "  -flag value\n        description" indentation Go's stdlib uses so
// existing tooling (godoc, completions) still recognises the layout.
func printGroupedUsage() {
	w := flag.CommandLine.Output()
	fmt.Fprintf(w, "Usage: %s [flags]\n\n", os.Args[0])

	seen := map[string]bool{}
	for _, g := range flagGroups {
		fmt.Fprintf(w, "%s:\n", g.title)
		for _, name := range g.flags {
			f := flag.Lookup(name)
			if f == nil {
				continue
			}
			seen[name] = true
			printFlag(w, f)
		}
		fmt.Fprintln(w)
	}

	// Any flags not assigned to a group go in an "Other" section so we
	// never silently drop one (e.g. a newly-added flag whose author forgot
	// to add it to flagGroups).
	var other []string
	flag.VisitAll(func(f *flag.Flag) {
		if !seen[f.Name] {
			other = append(other, f.Name)
		}
	})
	if len(other) > 0 {
		fmt.Fprintln(w, "Other:")
		for _, name := range other {
			printFlag(w, flag.Lookup(name))
		}
	}
}

func printFlag(w io.Writer, f *flag.Flag) {
	name, usage := flag.UnquoteUsage(f)
	header := "  -" + f.Name
	if name != "" {
		header += " " + name
	}
	// Skip the auto-default for "false" (bool default) and "0" (which usually
	// means "unset, derive later" in our flags) — the description carries
	// the real meaning in those cases.
	if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
		header += "  (default " + f.DefValue + ")"
	}
	fmt.Fprintln(w, header)
	// Wrap usage at ~78 columns, indented 8 spaces.
	for _, line := range wrap(usage, 70) {
		fmt.Fprintln(w, "        "+line)
	}
}

// wrap splits s into lines of at most maxWidth, breaking on whitespace.
func wrap(s string, maxWidth int) []string {
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len(line)+1+len(w) > maxWidth {
				out = append(out, line)
				line = w
			} else {
				line += " " + w
			}
		}
		out = append(out, line)
	}
	return out
}

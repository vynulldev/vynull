// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"vynull/analysis"
	"vynull/api"
	"vynull/dbserver"
	"vynull/device"
	"vynull/export"
	"vynull/internal/dlog"
	"vynull/internal/netutil"
	"vynull/library"
	"vynull/nfs"
	"vynull/pdb"
)

func main() {
	cfg := parseFlags()

	// Apply --log-level before anything else so startup messages obey it.
	if lvl, ok := dlog.Parse(cfg.LogLevel); ok {
		dlog.SetLevel(lvl)
	} else {
		fmt.Fprintf(os.Stderr, "warning: unknown --log-level %q; using info\n", cfg.LogLevel)
	}

	// Apply --rgb-3band before any analysis runs so generators see it.
	analysis.RGB3BandMode = cfg.RGB3Band

	// Load waveform overrides for CDJ rendering experiments. These bypass the
	// cache via analysis.ApplyOverrides at serve time.
	if cfg.PWV4Override != "" {
		b, err := os.ReadFile(cfg.PWV4Override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --pwv4-override: %v\n", err)
			os.Exit(1)
		}
		if len(b)%6 != 0 {
			fmt.Fprintf(os.Stderr, "warning: --pwv4-override file is %d bytes, not a multiple of 6 (PWV4 entry size)\n", len(b))
		}
		analysis.PWV4Override = b
		fmt.Printf("PWV4 override loaded: %d bytes (%d entries)\n", len(b), len(b)/6)
	}
	if cfg.PWV5Override != "" {
		b, err := os.ReadFile(cfg.PWV5Override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --pwv5-override: %v\n", err)
			os.Exit(1)
		}
		if len(b)%2 != 0 {
			fmt.Fprintf(os.Stderr, "warning: --pwv5-override file is %d bytes, not a multiple of 2 (PWV5 entry size)\n", len(b))
		}
		analysis.PWV5Override = b
		fmt.Printf("PWV5 override loaded: %d bytes (%d entries)\n", len(b), len(b)/2)
	}

	// Import rekordbox MYSETTING/.../DEVSETTING .DAT files into the
	// YAML config, then exit. Doesn't need an interface, library, etc.
	if cfg.ImportSettings != "" {
		settingsPath := cfg.SettingsFile
		if settingsPath == "" {
			settingsPath = filepath.Join(cfg.DataDir, "settings.json")
		}
		// Load existing config (or defaults), import, save.
		s := device.NewCDJSettingsAt(settingsPath)
		cur := s.Config()
		imported, err := device.ImportSettingsDir(cfg.ImportSettings, &cur)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import: %v\n", err)
			os.Exit(1)
		}
		if len(imported) == 0 {
			fmt.Fprintf(os.Stderr, "no .DAT files found in %s\n", cfg.ImportSettings)
			os.Exit(1)
		}
		if err := device.SaveConfig(settingsPath, cur); err != nil {
			fmt.Fprintf(os.Stderr, "save: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("imported %v into %s\n", imported, settingsPath)
		return
	}

	// Analyze / PDB / ANLZ inspect mode.
	if cfg.AnalyzeFile != "" || cfg.AnalyzePDB != "" || cfg.AnalyzeANLZ != "" || cfg.AnalyzeHTML != "" {
		if cfg.AnalyzeANLZ != "" {
			dumpANLZFile(cfg.AnalyzeANLZ)
			if cfg.AnalyzeCSV != "" {
				if err := dumpANLZToCSV(cfg.AnalyzeANLZ, cfg.AnalyzeCSV); err != nil {
					fmt.Fprintf(os.Stderr, "error: --anlz-csv: %v\n", err)
					os.Exit(1)
				}
			}
		}
		runAnalyze(cfg.AnalyzeFile, cfg.AnalyzePDB, cfg.AnalyzeHTML)
		return
	}

	// Log destination while serving:
	//   --log-file PATH  → append to that file (TUI or headless)
	//   TUI (default)    → an auto temp file, since the TUI owns the terminal
	//   headless         → stdout (no redirect)
	if cfg.GenerateDir == "" && cfg.AnalyzeFile == "" {
		var logFile *os.File
		var err error
		switch {
		case cfg.LogFile != "":
			logFile, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		case cfg.TUI:
			logFile, err = os.Create(filepath.Join(os.TempDir(), fmt.Sprintf("vynull-%s.log", time.Now().Format("20060102-150405"))))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not open log file: %v (logging to stdout)\n", err)
		} else if logFile != nil {
			log.SetOutput(logFile)
			defer logFile.Close()
			fmt.Printf("Logging to %s\n", logFile.Name())
			// Mirror runtime crash output (panics from any goroutine
			// including the HTTP server / dbserver / device loops) to
			// the same log file. Without this, panics under the
			// bubbletea TUI's altscreen scroll past in jumbled stderr
			// and the log file is silent. Available since Go 1.23.
			if err := debug.SetCrashOutput(logFile, debug.CrashOptions{}); err != nil {
				log.Printf("warning: SetCrashOutput failed: %v", err)
			}
			// Also catch panics in the main goroutine and log them
			// before exiting (SetCrashOutput logs the stack but the
			// process still exits — adding our own recover lets us
			// write a structured "panic:" line above the runtime dump
			// for grep-ability).
			defer func() {
				if r := recover(); r != nil {
					log.Printf("PANIC main: %v\n%s", r, debug.Stack())
					panic(r) // re-panic so runtime crash output still fires
				}
			}()
		}
	}
	log.Printf("started with args: %v", os.Args[1:])

	var lib *library.Library
	if cfg.MusicDir != "" {
		var err error
		lib, err = library.Scan(cfg.MusicDir)
		if err != nil {
			log.Fatalf("scanning music library: %v", err)
		}
	} else {
		lib = library.New()
		lib.SetDBPath(filepath.Join(cfg.DataDir, "library.json"))
		log.Printf("library mode: add tracks via API (/api/tracks/add)")
	}

	// Enable disk-cached artwork (persists across restarts, loaded on-demand).
	lib.Artwork.SetCacheDir(filepath.Join(cfg.DataDir, "artwork"))

	// Artwork is extracted lazily on demand — the web UI's /api/artwork probes
	// a file on first request (cached thereafter via the disk cache + the
	// ArtChecked flag), and loading a track on a CDJ extracts via analysis.
	// No startup or background sweep, so startup stays fast on large libraries.
	if cached := lib.Artwork.Count(); cached > 0 {
		log.Printf("library: %d artworks loaded from cache", cached)
	}

	// Generate USB structure mode — no network needed. The orchestration
	// lives in the export package so the HTTP /api/export endpoint can
	// re-use the same pipeline.
	if cfg.GenerateDir != "" {
		settingsPath := cfg.SettingsFile
		if settingsPath == "" {
			settingsPath = filepath.Join(cfg.DataDir, "settings.json")
		}
		gSettings := device.NewCDJSettingsAt(settingsPath)
		settingsBodies := pdb.SettingsBodies{
			MySetting:    gSettings.GetMySetting(),
			MySetting2:   gSettings.GetMySetting2(),
			DjmMySetting: gSettings.GetDjmMySetting(),
			DevSetting:   gSettings.GetDevSettingDat(),
		}

		opts := export.Options{
			Library:       lib,
			SrcDir:        cfg.MusicDir,
			DestDir:       cfg.GenerateDir,
			CopyFiles:     cfg.GenerateCopy,
			Settings:      settingsBodies,
			ArtworkLookup: artworkLookup(lib),
		}

		// Subset selection: --export-playlist NAME restricts to one
		// playlist's tracks and writes a single-entry playlist tree.
		if cfg.ExportPlaylist != "" {
			ps := api.NewPlaylistStore(cfg.DataDir)
			pl := findPlaylistByName(ps, cfg.ExportPlaylist)
			if pl == nil {
				log.Fatalf("export: playlist %q not found in %s/playlists.json", cfg.ExportPlaylist, cfg.DataDir)
			}
			// SmartRules need the library + tag lookup; the tag store
			// isn't wired in --generate mode, so smart-playlist filtering
			// here ignores tag predicates. Worst case is a slightly
			// over-selective subset — exporting still works.
			trackIDs := ps.TracksFor(pl.ID, lib, nil)
			if len(trackIDs) == 0 {
				log.Fatalf("export: playlist %q (id %d) has no tracks", pl.Name, pl.ID)
			}
			opts.Tracks = export.FilterTracks(export.LibraryToTracks(lib), trackIDs)
			opts.Playlists = export.SinglePlaylist(pl.Name, trackIDs)
			log.Printf("export: subset %q → %d tracks", pl.Name, len(opts.Tracks))
		}

		if err := export.Run(opts); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	iface, err := netutil.ResolveInterface(cfg.Interface)
	if err != nil {
		log.Fatalf("resolving interface: %v", err)
	}

	log.Printf("using interface %s: ip=%s mac=%s broadcast=%s",
		cfg.Interface, iface.IP, iface.MAC, iface.Broadcast)

	// Try to load PDB database for real track IDs.
	// NFS root: music dir if set, otherwise "/" for absolute path serving.
	nfsRoot := cfg.MusicDir
	if nfsRoot == "" {
		nfsRoot = "/"
	}

	var pdbDB *pdb.Database
	if cfg.MusicDir != "" {
		pdbPath := filepath.Join(cfg.MusicDir, "PIONEER", "rekordbox", "export.pdb")
		var err error
		pdbDB, err = pdb.Open(pdbPath)
		if err != nil {
			log.Printf("pdb: not available (%v), using ID3 tags", err)
		} else {
			log.Printf("pdb: loaded %d tracks from %s", len(pdbDB.Tracks), pdbPath)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Always create a cached analysis store so API-added tracks get analyzed.
	cacheDir := filepath.Join(cfg.DataDir, "analysis")
	analysisStore := analysis.NewStoreWithCache(cacheDir)
	hasANLZ := false

	if cfg.LazyAnalysis {
		log.Printf("lazy-analysis mode: tracks will be analyzed on-demand (cache: %s)", cacheDir)
	} else if pdbDB != nil {
		// PDB paths are relative to USB root (e.g., /Contents/...).
		// Build absolute paths for ffmpeg, analyze, then restore.
		origPaths := make([]string, len(pdbDB.Tracks))
		for i, t := range pdbDB.Tracks {
			origPaths[i] = t.FilePath
			t.FilePath = filepath.Join(cfg.MusicDir, t.FilePath)
		}
		log.Printf("analyzing %d tracks...", len(pdbDB.Tracks))
		analysis.AnalyzeAll(pdbDB.Tracks, runtime.NumCPU(), analysisStore, nil)
		for i, t := range pdbDB.Tracks {
			t.FilePath = origPaths[i]
		}
		// Populate Keys map from analysis results (original PDB may lack keys).
		// Always rebuild from detected keys since the export PDB often has
		// KeyID values but an empty Keys table.
		var nextKeyID uint32 = 1
		keyNameToID := make(map[string]uint32)
		for _, t := range pdbDB.Tracks {
			if t.Key != "" {
				if id, ok := keyNameToID[t.Key]; ok {
					t.KeyID = id
				} else {
					t.KeyID = nextKeyID
					keyNameToID[t.Key] = nextKeyID
					pdbDB.Keys[nextKeyID] = t.Key
					nextKeyID++
				}
			}
		}
		if len(keyNameToID) > 0 {
			log.Printf("analysis: added %d keys to PDB", len(keyNameToID))
		}

		// Populate artwork cache from analysis results.
		artCount := 0
		for _, t := range pdbDB.Tracks {
			r := analysisStore.Get(t.ID)
			if r != nil && r.Artwork != nil {
				lib.Artwork.AddWithID(t.ArtworkID, "image/jpeg", r.Artwork)
				artCount++
			}
		}

		// Also try loading artwork from PIONEER/Artwork/ directory (Rekordbox USB).
		artDir := filepath.Join(cfg.MusicDir, "PIONEER", "Artwork")
		for _, t := range pdbDB.Tracks {
			if t.ArtworkID == 0 || lib.Artwork.Get(t.ArtworkID) != nil {
				continue
			}
			// Try aX_m.jpg (medium) then aX.jpg (thumbnail).
			for _, pattern := range []string{"a%d_m.jpg", "a%d.jpg"} {
				name := fmt.Sprintf(pattern, t.ArtworkID)
				matches, _ := filepath.Glob(filepath.Join(artDir, "*", name))
				if len(matches) > 0 {
					data, err := os.ReadFile(matches[0])
					if err == nil && len(data) > 0 {
						lib.Artwork.AddWithID(t.ArtworkID, "image/jpeg", data)
						artCount++
						break
					}
				}
			}
		}

		if artCount > 0 {
			log.Printf("analysis: loaded %d artworks", artCount)
		}
	}

	// Also load artwork from PIONEER/Artwork/ directory (Rekordbox USB).
	if pdbDB != nil {
		artDir := filepath.Join(cfg.MusicDir, "PIONEER", "Artwork")
		artCount := 0
		for _, t := range pdbDB.Tracks {
			if t.ArtworkID == 0 || lib.Artwork.Get(t.ArtworkID) != nil {
				continue
			}
			for _, pattern := range []string{"a%d_m.jpg", "a%d.jpg"} {
				name := fmt.Sprintf(pattern, t.ArtworkID)
				matches, _ := filepath.Glob(filepath.Join(artDir, "*", name))
				if len(matches) > 0 {
					data, err := os.ReadFile(matches[0])
					if err == nil && len(data) > 0 {
						lib.Artwork.AddWithID(t.ArtworkID, "image/jpeg", data)
						artCount++
						break
					}
				}
			}
		}
		if artCount > 0 {
			log.Printf("loaded %d artworks from PIONEER/Artwork", artCount)
		}
	}
	_ = hasANLZ

	// Start the database server for track metadata queries.
	// Build folder lookup for directory-based playlist browsing.
	var folderLookup *pdb.FolderLookup
	if pdbDB != nil {
		folderLookup = pdb.BuildFolderLookup(pdbDB.Tracks)
		log.Printf("folders: %d directories mapped", len(folderLookup.Nodes))
	}

	cueStore := dbserver.NewCueStore(filepath.Join(cfg.DataDir, "cues"))
	tagStore := api.NewTagStore(filepath.Join(cfg.DataDir, "tags"))
	playlistStore := api.NewPlaylistStore(filepath.Join(cfg.DataDir, "playlists"))
	menuStore := api.NewMenuStore(filepath.Join(cfg.DataDir, "menu"))

	// Apply persisted track colors and ratings from tag store to library tracks.
	for _, t := range lib.Tracks() {
		if c := tagStore.GetTrackColor(t.ID); c != 0 {
			t.ColorID = c
		}
		if r := tagStore.GetTrackRating(t.ID); r != 0 {
			t.Rating = r
		}
	}

	var cdjSettings *device.CDJSettings
	if cfg.SettingsFile != "" {
		cdjSettings = device.NewCDJSettingsAt(cfg.SettingsFile)
	} else {
		cdjSettings = device.NewCDJSettings(cfg.DataDir)
	}

	db := &dbserver.Server{
		Library:      lib,
		PDB:          pdbDB,
		DeviceNumber: cfg.DeviceNumber,
		ExportRoot:   nfsRoot,
		Analysis:     analysisStore,
		Folders:      folderLookup,
		Playlists:    playlistSource{ps: playlistStore, lib: lib, tags: tagStore},
		Menu:         menuSource{ms: menuStore},
		Cues:         cueStore,
		Settings:     cdjSettings,
		ReplayDir:    cfg.ReplayDir,
	}
	// Wired after dev is constructed below; uses a closure so we can
	// reference dev.Peers (which is created inside dev.Start).
	// db.OnPeerTeardown set later.
	// Background services that need to finish cleanup (close listeners,
	// drain in-flight requests) before main exits. Tracked via srvWg.
	var srvWg sync.WaitGroup
	srvWg.Add(1)
	go func() {
		defer srvWg.Done()
		if err := db.Start(ctx); err != nil {
			log.Printf("dbserver error: %v", err)
		}
		log.Printf("dbserver: stopped")
	}()

	// Forward-declare the device so the NFS server's LinkedFn closure
	// can reference it (dev itself is constructed further down). Empty
	// MOUNT EXPORT replies when unlinked are what rekordbox uses
	// to make the CDJ drop its LINK indicator instantly instead of
	// waiting for the keep-alive timeout (~5-6s).
	var dev *device.VirtualDevice
	nfsSrv := nfs.NewServer(nfsRoot)
	nfsSrv.Transcode = cfg.Transcode
	nfsSrv.IP = iface.IP
	nfsSrv.LinkedFn = func() bool { return dev != nil && dev.Linked() }
	if cfg.Transcode {
		log.Printf("nfs: transcoding enabled — FLAC/WAV/AIFF will be converted to MP3")
	}
	srvWg.Add(1)
	go func() {
		defer srvWg.Done()
		if err := nfsSrv.Start(ctx); err != nil {
			log.Printf("nfs server error: %v", err)
		}
		log.Printf("nfs server: stopped")
	}()

	log.Printf("mode: %s (device %d, name %q, slot %d)",
		cfg.DeviceType, cfg.DeviceNumber, cfg.DeviceName, cfg.MediaSlot)

	// Start the virtual device (announcement + keep-alive + status).
	monitor := device.NewPlayerMonitor(pdbDB, lib)
	if analysisStore != nil {
		monitor.AnalysisStatus = analysisStore.Status
	}
	monitor.ExportStatus = export.Status
	monitor.APIAddr = fmt.Sprintf("127.0.0.1:%d", 9443)
	// Every time the PlayerMonitor's 50%-played threshold fires (same
	// trigger as the per-track PlayCount bump), append the track to a
	// dated history playlist. The history folder + per-day playlist are
	// auto-created on first append; they show up on the CDJ's PLAYLIST
	// menu via the existing PlaylistStore wiring without further work.
	monitor.OnPlayed = func(trackID uint32, _ uint8) {
		appendToHistoryPlaylist(playlistStore, trackID, time.Now())
	}

	// Track-history file: a single rolling, crash-safe log — each completed
	// play is appended as it finishes, accumulating across sessions.
	// --history-file overrides the default of <data-dir>/history.<ext>;
	// --history-format picks text/csv/json.
	histPath := cfg.HistoryFile
	if histPath == "" {
		ext := "txt"
		switch cfg.HistoryFormat {
		case "csv":
			ext = "csv"
		case "json":
			ext = "json"
		}
		histPath = filepath.Join(cfg.DataDir, "history."+ext)
	}
	monitor.SetHistoryOutput(histPath, cfg.HistoryFormat)
	log.Printf("track history: %s (%s)", histPath, cfg.HistoryFormat)

	dev = &device.VirtualDevice{
		Name:         cfg.DeviceName,
		DeviceNumber: cfg.DeviceNumber,
		DeviceType:   cfg.DeviceType,
		MediaSlot:    cfg.MediaSlot,
		MAC:          iface.MAC,
		IP:           iface.IP,
		Broadcast:    iface.Broadcast,
		TrackCount:   uint16(trackCount(lib, pdbDB)),
		Monitor:      monitor,
		Settings:     cdjSettings,
	}

	// Now that dev exists, wire the dbserver teardown callback to drop
	// peers from the tracker the moment they send 0x0100.
	db.OnPeerTeardown = func(ip net.IP) {
		if dev.Peers != nil {
			dev.Peers.RemoveByIP(ip)
		}
	}

	// When the user toggles UNLINK in the web UI, fire the rekordbox-
	// authentic disconnect chain in the order rekordbox uses (from
	// pcap analysis):
	//   1) unicast 0x16 "session reset" status to each CDJ peer
	//      (rekordbox sends this before the TCP teardown; without it
	//      the CDJ's Linked indicator waits for the keep-alive timeout)
	//   2) dbserver 0x0100 teardown + TCP close on every active session
	//   3) clear our peer tracker so the web UI reflects reality
	dev.SetLinkChangeCallback(func(linked bool) {
		if linked {
			return
		}
		dev.SendDisconnectSignal()
		db.Unlink()
		if dev.Peers != nil {
			for _, p := range dev.Peers.Peers() {
				dev.Peers.RemoveByIP(p.IP)
			}
		}
	})

	// Default to LINKED so file loading works out of the box. The user
	// can hit UNLINK in the web UI to disconnect. (We start "unlinked"
	// internally so SetLinked(true) actually fires the callback chain,
	// but flip immediately before any CDJ traffic arrives.)
	dev.SetLinked(true)

	// Gate new dbserver connections on linked state — when the user
	// clicks UNLINK we both tear down existing sessions AND refuse
	// new ones, so a CDJ can't re-browse our library after being
	// disconnected.
	db.LinkedFn = func() bool { return dev != nil && dev.Linked() }

	// Start HTTP API server for external application integration.
	apiSrv := &api.Server{
		Device:   dev,
		Library:  lib,
		PDB:      pdbDB,
		Analysis: analysisStore,
		Cues: &api.CueStoreAdapter{
			Store: cueStore,
			OnChange: func(trackID uint32) {
				// Tell connected CDJs to re-fetch cues so colour edits
				// from the web UI show up without a track reload. The
				// CDJ ignores the trigger unless its embedded track ID
				// matches the track currently loaded on the deck.
				dev.BroadcastTrackRefresh(trackID)
			},
		},
		Tags:         tagStore,
		Playlists:    playlistStore,
		Menu:         menuStore,
		Settings:     cdjSettings,
		DBServer:     db,
		LazyAnalysis: cfg.LazyAnalysis,
		MusicDir:     cfg.MusicDir,
		Listen:       cfg.Listen,
		Port:         9443, // legacy fallback if Listen is empty (it isn't, given flag default)
		Web:          cfg.Web,
		CacheDir:     cfg.DataDir, // for rendered waveform PNGs etc.
	}
	if cfg.Web {
		log.Printf("web UI enabled: http://%s/", displayAddr(cfg.Listen))
	}
	srvWg.Add(1)
	go func() {
		defer srvWg.Done()
		if err := apiSrv.Start(ctx); err != nil {
			log.Printf("api server error: %v", err)
		}
		log.Printf("api server: stopped")
	}()

	// Run device in a goroutine; the TUI takes the main thread.
	srvWg.Add(1)
	var devErr error
	go func() {
		defer srvWg.Done()
		devErr = dev.Start(ctx)
		if devErr != nil && ctx.Err() == nil {
			log.Printf("device error: %v", devErr)
			cancel()
		}
		log.Printf("device: stopped")
	}()

	// Periodic resource snapshot so we can spot goroutine / memory / session
	// leaks when the CDJ stops being able to load tracks after extended
	// runtime. Logged at INFO once per minute; cheap to compute, helpful for
	// post-mortem analysis of long-running sessions.
	srvWg.Add(1)
	go func() {
		defer srvWg.Done()
		runStatsLogger(ctx, db, dev, analysisStore)
	}()

	// pprof on a fixed localhost-only port for live introspection while
	// the process leaks: `go tool pprof http://127.0.0.1:6060/debug/pprof/heap`,
	// `/goroutine`, etc. Mounted on its own server so it can't fight the
	// API mux. localhost-only — never expose pprof on the LAN.
	go func() {
		if err := http.ListenAndServe("127.0.0.1:6060", nil); err != nil {
			log.Printf("pprof: %v", err)
		}
	}()

	if cfg.TUI {
		// Launch the TUI on the main goroutine. It owns the terminal until
		// the user presses 'q' or ctx is cancelled from elsewhere (SIGINT,
		// service error). Bridge ctx → program.Quit so SIGINT unwinds cleanly.
		tuiProgram := device.NewTUI(monitor, lib, cdjSettings, dev.Peers, displayAddr(cfg.Listen))
		go func() {
			<-ctx.Done()
			tuiProgram.Quit()
		}()
		if _, err := tuiProgram.Run(); err != nil {
			log.Printf("tui error: %v", err)
		}
		cancel() // user pressed 'q' → tell services to stop

		fmt.Print("\033[H\033[2J") // clear monitor screen
	} else {
		// Headless: no terminal UI. Block the main goroutine until a signal
		// (SIGINT/SIGTERM) or a service error cancels the context.
		log.Printf("running headless (--tui=false); press Ctrl-C to stop")
		<-ctx.Done()
		cancel()
	}
	log.Printf("shutdown: waiting for services to finish (up to 5s)...")

	// Wait for dbserver / nfs / api to finish their teardown (close
	// listeners, drain in-flight requests), with a timeout so a stuck
	// goroutine can't hang the process forever.
	done := make(chan struct{})
	go func() {
		srvWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Printf("shutdown: all services stopped cleanly")
	case <-time.After(5 * time.Second):
		log.Printf("shutdown: timed out — some services did not stop within 5s")
	}

	// Finalize track history: close the still-playing entry and flush.
	monitor.FinalizeHistory()
	if p := monitor.HistoryPath(); p != "" && len(monitor.History()) > 0 {
		fmt.Printf("\nTrack history saved to %s\n", p)
	}
}

// artworkLookup returns an pdb.ArtworkLookup wired to the library's
// in-memory + on-disk artwork cache. nil when the library has no
// cache configured (shouldn't happen but defensive).
func artworkLookup(lib *library.Library) func(uint32) []byte {
	if lib == nil || lib.Artwork == nil {
		return nil
	}
	return func(id uint32) []byte {
		art := lib.Artwork.Get(id)
		if art == nil {
			return nil
		}
		return art.Data
	}
}

// findPlaylistByName looks up a playlist or smart playlist by name
// (case-insensitive). Used by --export-playlist to resolve the CLI
// argument to a PlaylistInfo. Returns nil when no match exists.
func findPlaylistByName(ps *api.PlaylistStore, name string) *api.PlaylistInfo {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, p := range ps.All() {
		if p.IsFolder {
			continue
		}
		if strings.ToLower(p.Name) == target {
			return p
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// displayAddr returns a user-facing form of the API listen address.
// "0.0.0.0:9443" or ":9443" are reachable but uninformative; if the
// caller set --interface we use that interface's IP for the host part
// so the URL we log/show in the TUI is actually clickable. Otherwise
// the configured address is returned as-is.
// runStatsLogger emits a once-per-minute snapshot of resource counters that
// matter for the "CDJ stops loading tracks after a while" symptom. If a
// counter grows monotonically over a long session (e.g. goroutines, active
// dbserver sessions, heap), that's where to look for the leak.
func runStatsLogger(ctx context.Context, db *dbserver.Server, dev *device.VirtualDevice, analysisStore *analysis.Store) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	// Log once at startup so we have a baseline.
	logStatsLine(db, dev, analysisStore)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			logStatsLine(db, dev, analysisStore)
		}
	}
}

func logStatsLine(db *dbserver.Server, dev *device.VirtualDevice, analysisStore *analysis.Store) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	sessions := 0
	if db != nil {
		sessions = db.ActiveSessions()
	}
	peers := 0
	if dev != nil && dev.Peers != nil {
		peers = len(dev.Peers.Peers())
	}
	pending, analyzed, cached := int32(0), 0, 0
	if analysisStore != nil {
		pending = analysisStore.Pending()
		analyzed = analysisStore.Count()
		cached = analysisStore.CachedCount()
	}
	log.Printf("stats: goroutines=%d heap=%dMB sys=%dMB heap-idle=%dMB heap-released=%dMB gc=%d dbserver-sessions=%d peers=%d analysis(pending=%d analyzed=%d cached=%d)",
		runtime.NumGoroutine(),
		ms.HeapAlloc>>20,
		ms.Sys>>20,
		ms.HeapIdle>>20,
		ms.HeapReleased>>20,
		ms.NumGC,
		sessions,
		peers,
		pending, analyzed, cached,
	)
	// Hint the runtime to return unused spans to the OS. After the startup
	// analysis spike (PCM + FFT working memory across the worker pool),
	// HeapIdle can stay multi-GB for a long time on Linux because Go uses
	// MADV_FREE by default — pages remain charged to RSS until something
	// else needs them. FreeOSMemory promotes those to MADV_DONTNEED, which
	// drops RSS immediately. Cheap to call (~ms) and only does work when
	// there is actually idle memory to release.
	debug.FreeOSMemory()
}

// playlistSource adapts api.PlaylistStore to dbserver.PlaylistSource —
// translates between the API's PlaylistInfo struct and the slimmer
// PlaylistEntry the dbserver needs for menu rendering. Lives in main
// (rather than either package) so neither side depends on the other.
//
// The lib + tags fields give the adapter what it needs to resolve
// smart-playlist tracks at read time (the rules need the full library
// to filter and the tag store to evaluate tag conditions). Both can be
// nil for regular playlists — TracksFor handles that case.
type playlistSource struct {
	ps   *api.PlaylistStore
	lib  *library.Library
	tags api.TagLookup
}

func (a playlistSource) Children(parentID uint32) []dbserver.PlaylistEntry {
	src := a.ps.Children(parentID)
	out := make([]dbserver.PlaylistEntry, len(src))
	for i, c := range src {
		out[i] = dbserver.PlaylistEntry{ID: c.ID, Name: c.Name, IsFolder: c.IsFolder}
	}
	return out
}

func (a playlistSource) Tracks(id uint32) []uint32 {
	return a.ps.TracksFor(id, a.lib, a.tags)
}

func (a playlistSource) HistoryFolderID() uint32 {
	for _, p := range a.ps.All() {
		if p.ParentID == 0 && p.IsFolder && p.Name == historyFolderName {
			return p.ID
		}
	}
	return 0
}

// menuSource adapts api.MenuStore to dbserver.MenuSource. Only the
// visible entries reach the CDJ; hidden ones stay in storage so the
// user can flip them back on without losing their order.
type menuSource struct{ ms *api.MenuStore }

func (a menuSource) RootMenu() []dbserver.RootMenuEntry {
	src := a.ms.Visible()
	out := make([]dbserver.RootMenuEntry, len(src))
	for i, m := range src {
		out[i] = dbserver.RootMenuEntry{ID: m.ID, Label: m.Label, ItemType: m.ItemType}
	}
	return out
}

func (a menuSource) TrackDetail() string { return a.ms.TrackDetail() }

// appendToHistoryPlaylist upserts a "History · YYYY-MM-DD" playlist
// inside a top-level "History" folder and appends trackID to it.
// Consecutive duplicates are skipped — repeatedly cueing the same
// track shouldn't add it five times. Errors are logged and swallowed
// since this runs from a status-packet hot path; a missed history
// entry isn't worth crashing analysis over.
const historyFolderName = "History"

func appendToHistoryPlaylist(ps *api.PlaylistStore, trackID uint32, when time.Time) {
	if ps == nil {
		return
	}
	all := ps.All()

	// Find or create the root "History" folder.
	var folderID uint32
	for _, p := range all {
		if p.ParentID == 0 && p.IsFolder && p.Name == historyFolderName {
			folderID = p.ID
			break
		}
	}
	if folderID == 0 {
		f, err := ps.Create(historyFolderName, 0, true)
		if err != nil {
			log.Printf("history: create folder: %v", err)
			return
		}
		folderID = f.ID
	}

	// Find or create today's playlist.
	plName := "History · " + when.Format("2006-01-02")
	var plID uint32
	for _, p := range ps.All() {
		if p.ParentID == folderID && !p.IsFolder && !p.IsSmart && p.Name == plName {
			plID = p.ID
			break
		}
	}
	if plID == 0 {
		p, err := ps.Create(plName, folderID, false)
		if err != nil {
			log.Printf("history: create %q: %v", plName, err)
			return
		}
		plID = p.ID
	}

	// Append (skip consecutive dup).
	current := ps.Tracks(plID)
	if len(current) > 0 && current[len(current)-1] == trackID {
		return
	}
	current = append(current, trackID)
	if err := ps.SetTracks(plID, current); err != nil {
		log.Printf("history: append to %q: %v", plName, err)
	}
}

func displayAddr(listen string) string {
	if listen == "" {
		return "127.0.0.1:9443"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		// Pick the first non-loopback IPv4 we can find.
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if ipNet, ok := a.(*net.IPNet); ok {
					ip4 := ipNet.IP.To4()
					if ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
						return net.JoinHostPort(ip4.String(), port)
					}
				}
			}
		}
		return "127.0.0.1:" + port
	}
	return listen
}

func trackCount(lib *library.Library, pdbDB *pdb.Database) int {
	if pdbDB != nil {
		return len(pdbDB.Tracks)
	}
	return lib.TrackCount()
}

func runAnalyze(audioPath, pdbPath, htmlPath string) {
	// PDB mode: show track data from a rekordbox database.
	var pdbDB *pdb.Database
	if pdbPath != "" {
		var err error
		pdbDB, err = pdb.Open(pdbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pdb error: %v\n", err)
			os.Exit(1)
		}
		printPDB(pdbDB, audioPath)
	}

	if audioPath == "" {
		return
	}

	fmt.Printf("Analyzing: %s\n\n", audioPath)

	samples, err := analysis.DecodePCM(audioPath, 44100)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
		os.Exit(1)
	}
	durationSec := float64(len(samples)) / 44100.0
	fmt.Printf("Duration:  %.1fs (%d samples @ 44100Hz)\n", durationSec, len(samples))

	result := analysis.DetectBeats(samples, 44100)
	fmt.Printf("BPM:       %.2f\n", result.BPM)
	fmt.Printf("Beats:     %d\n", len(result.Beats))
	fmt.Printf("Downbeat:  %.1f ms\n", result.Downbeat)

	if len(result.Beats) == 0 {
		fmt.Println("\nNo beats detected.")
		return
	}

	// Show beat-to-beat intervals to spot inconsistencies.
	fmt.Printf("\n--- Beat positions (first 32) ---\n")
	fmt.Printf("%4s  %10s  %10s  %6s  %s\n", "Beat", "Time (ms)", "Interval", "BPM", "Bar")
	for i := 0; i < len(result.Beats) && i < 32; i++ {
		ms := result.Beats[i]
		interval := 0.0
		localBPM := 0.0
		if i > 0 {
			interval = result.Beats[i] - result.Beats[i-1]
			if interval > 0 {
				localBPM = 60000.0 / interval
			}
		}

		// Determine bar position relative to downbeat.
		barPos := ""
		if result.Downbeat > 0 {
			// Find downbeat index.
			dbIdx := 0
			for j, b := range result.Beats {
				if b >= result.Downbeat-0.5 {
					dbIdx = j
					break
				}
			}
			beatInBar := ((i - dbIdx) % 4)
			if beatInBar < 0 {
				beatInBar += 4
			}
			beatInBar++ // 1-based
			barNum := (i-dbIdx)/4 + 1
			if i < dbIdx {
				barPos = fmt.Sprintf("(pre)")
			} else {
				barPos = fmt.Sprintf("bar %d.%d", barNum, beatInBar)
			}
		}

		if i == 0 {
			fmt.Printf("%4d  %10.1f  %10s  %6.2f  %s\n", i+1, ms, "-", result.BPM, barPos)
		} else {
			fmt.Printf("%4d  %10.1f  %10.1f  %6.2f  %s\n", i+1, ms, interval, localBPM, barPos)
		}
	}
	if len(result.Beats) > 32 {
		fmt.Printf("  ... (%d more beats)\n", len(result.Beats)-32)
	}

	// Show interval statistics.
	if len(result.Beats) > 1 {
		intervals := make([]float64, len(result.Beats)-1)
		for i := 1; i < len(result.Beats); i++ {
			intervals[i-1] = result.Beats[i] - result.Beats[i-1]
		}

		// Median interval.
		sorted := make([]float64, len(intervals))
		copy(sorted, intervals)
		sortFloat64s(sorted)
		median := sorted[len(sorted)/2]

		// Count outliers (>5% off from median).
		outliers := 0
		for _, iv := range intervals {
			if abs64(iv-median)/median > 0.05 {
				outliers++
			}
		}

		fmt.Printf("\n--- Interval stats ---\n")
		fmt.Printf("Median interval: %.1f ms (%.2f BPM)\n", median, 60000.0/median)
		fmt.Printf("Min interval:    %.1f ms (%.2f BPM)\n", sorted[0], 60000.0/sorted[0])
		fmt.Printf("Max interval:    %.1f ms (%.2f BPM)\n", sorted[len(sorted)-1], 60000.0/sorted[len(sorted)-1])
		fmt.Printf("Outliers (>5%%):  %d / %d (%.1f%%)\n", outliers, len(intervals), float64(outliers)/float64(len(intervals))*100)
	}

	// Key detection.
	camelot, standard := analysis.DetectKey(samples, 44100)
	fmt.Printf("\nKey: %s (%s)\n", camelot, standard)

	// Run full analysis to get ANLZ section data.
	fmt.Printf("\n--- Our generated ANLZ sections ---\n")
	fullResult, err := analysis.AnalyzeTrack(audioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "full analysis error: %v\n", err)
		return
	}

	// Write temp ANLZ files and dump them.
	tmpDir, err := os.MkdirTemp("", "rekordbox-anlz-*")
	if err == nil {
		defer os.RemoveAll(tmpDir)
		anlzPath, err := analysis.WriteANLZFiles(tmpDir, 1, audioPath, fullResult)
		if err == nil {
			datPath := tmpDir + strings.Replace(anlzPath, ".DAT", ".DAT", 1)
			extPath := strings.Replace(datPath, ".DAT", ".EXT", 1)
			fmt.Printf("\n.DAT sections:\n")
			dumpANLZFile(datPath)
			fmt.Printf(".EXT sections:\n")
			dumpANLZFile(extPath)
		} else {
			fmt.Fprintf(os.Stderr, "write ANLZ: %v\n", err)
		}
	}

	// HTML report.
	if htmlPath != "" {
		// Find rekordbox ANLZ data if PDB available.
		var rbTrack *pdb.Track
		var rbDATPath, rbEXTPath string
		if pdbDB != nil {
			for _, t := range pdbDB.Tracks {
				filterBase := filepath.Base(audioPath)
				if t.FileName == filterBase || filepath.Base(t.FilePath) == filterBase {
					rbTrack = t
					rbDATPath = pdbDB.ExportRoot + t.AnalyzePath
					rbEXTPath = strings.Replace(rbDATPath, ".DAT", ".EXT", 1)
					break
				}
			}
		}
		rb2EXPath := ""
		if rbDATPath != "" {
			rb2EXPath = strings.Replace(rbDATPath, ".DAT", ".2EX", 1)
		}
		if err := writeAnalysisHTML(htmlPath, audioPath, fullResult, result, rbTrack, rbDATPath, rbEXTPath, rb2EXPath); err != nil {
			fmt.Fprintf(os.Stderr, "html error: %v\n", err)
		} else {
			fmt.Printf("\nHTML report: %s\n", htmlPath)
		}
	}
}

func printPDB(db *pdb.Database, filterPath string) {
	fmt.Printf("=== PDB: %d tracks, %d artists, %d albums ===\n\n", len(db.Tracks), len(db.Artists), len(db.Albums))

	// Derive export root from the PDB path (e.g., /media/usb/PIONEER/rekordbox/export.pdb → /media/usb)
	exportRoot := db.ExportRoot

	for _, t := range db.Tracks {
		// If a filter path is given, match by filename.
		if filterPath != "" {
			filterBase := filepath.Base(filterPath)
			if t.FileName != filterBase && filepath.Base(t.FilePath) != filterBase {
				continue
			}
		}

		fmt.Printf("Track %d (0x%08x): %s\n", t.ID, t.ID, t.Title)
		fmt.Printf("  Artist:      %s (ID %d)\n", t.Artist, t.ArtistID)
		fmt.Printf("  Album:       %s (ID %d)\n", t.Album, t.AlbumID)
		fmt.Printf("  Genre:       %s (ID %d)\n", t.Genre, t.GenreID)
		fmt.Printf("  Key:         %s (ID %d)\n", t.Key, t.KeyID)
		fmt.Printf("  BPM:         %.2f (raw %d)\n", float64(t.Tempo)/100, t.Tempo)
		fmt.Printf("  Duration:    %ds\n", t.Duration)
		fmt.Printf("  Bitrate:     %d kbps\n", t.Bitrate)
		fmt.Printf("  Year:        %d\n", t.Year)
		fmt.Printf("  Track#:      %d\n", t.TrackNum)
		fmt.Printf("  Rating:      %d\n", t.Rating)
		fmt.Printf("  FileSize:    %d\n", t.FileSize)
		fmt.Printf("  FilePath:    %s\n", t.FilePath)
		fmt.Printf("  FileName:    %s\n", t.FileName)
		fmt.Printf("  AnalyzePath: %s\n", t.AnalyzePath)
		fmt.Printf("  ArtworkID:   %d\n", t.ArtworkID)
		fmt.Printf("  DateAdded:   %s\n", t.DateAdded)
		fmt.Printf("  Comment:     %s\n", t.Comment)
		fmt.Printf("  ColorID:     %d\n", t.ColorID)
		fmt.Println()

		// Dump all ANLZ sections from .DAT and .EXT files.
		if t.AnalyzePath != "" {
			datPath := exportRoot + t.AnalyzePath
			extPath := strings.Replace(datPath, ".DAT", ".EXT", 1)
			if fileExists(datPath) {
				fmt.Printf("  --- ANLZ .DAT: %s ---\n", datPath)
				dumpANLZFile(datPath)
			}
			if fileExists(extPath) {
				fmt.Printf("  --- ANLZ .EXT: %s ---\n", extPath)
				dumpANLZFile(extPath)
			}
		}
	}

	if filterPath != "" {
		return // don't list all tracks when filtering
	}
}

func dumpANLZFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  read error: %v\n", err)
		return
	}
	if len(data) < 28 || string(data[0:4]) != "PMAI" {
		fmt.Fprintf(os.Stderr, "  not a valid ANLZ file (no PMAI header)\n")
		return
	}

	fileLen := binary.BigEndian.Uint32(data[8:12])
	fmt.Printf("  PMAI: file_len=%d actual=%d\n\n", fileLen, len(data))

	hdrLen := int(binary.BigEndian.Uint32(data[4:8]))
	pos := hdrLen
	for pos+12 <= len(data) {
		fourcc := string(data[pos : pos+4])
		secHdrLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		bodyLen := secLen - secHdrLen
		fmt.Printf("  %s: offset=0x%04x header=%d total=%d data=%d\n",
			fourcc, pos, secHdrLen, secLen, bodyLen)

		section := data[pos : pos+secLen]
		header := section[:secHdrLen]
		body := section[secHdrLen:]

		switch fourcc {
		case "PPTH":
			dumpPPTH(header, body)
		case "PVBR":
			dumpPVBR(header, body)
		case "PQTZ":
			dumpPQTZ(header, body)
		case "PWAV":
			dumpWaveform(fourcc, header, body, 1)
		case "PWV2":
			dumpWaveform(fourcc, header, body, 1)
		case "PWV3":
			dumpWaveform(fourcc, header, body, 1)
		case "PWV4":
			dumpPWV4(header, body)
		case "PWV5":
			dumpPWV5(header, body)
		case "PCOB", "PCO2":
			dumpCuePoints(fourcc, header, body)
		case "PQT2":
			dumpPQT2(header, body)
		case "PSSI":
			dumpPSSI(header, body)
		case "PVB2":
			dumpPVBR(header, body)
		default:
			fmt.Printf("    (unknown section, %d body bytes)\n", bodyLen)
		}
		fmt.Println()
		pos += secLen
	}
}

func dumpPPTH(header, body []byte) {
	if len(header) >= 16 {
		pathLen := binary.BigEndian.Uint32(header[12:16])
		fmt.Printf("    path_len=%d\n", pathLen)
	}
	// Decode UTF-16BE path.
	var path string
	for i := 0; i+1 < len(body); i += 2 {
		ch := binary.BigEndian.Uint16(body[i:])
		if ch == 0 {
			break
		}
		path += string(rune(ch))
	}
	fmt.Printf("    path: %s\n", path)
}

func dumpPVBR(header, body []byte) {
	numEntries := len(body) / 4
	fmt.Printf("    entries=%d (byte offsets into audio file)\n", numEntries)
	// Show first and last few.
	show := 5
	if numEntries < show*2 {
		show = numEntries
	}
	for i := 0; i < show && i*4+3 < len(body); i++ {
		off := binary.BigEndian.Uint32(body[i*4:])
		fmt.Printf("    [%3d] offset=%d\n", i, off)
	}
	if numEntries > show*2 {
		fmt.Printf("    ...\n")
		for i := numEntries - show; i < numEntries && i*4+3 < len(body); i++ {
			off := binary.BigEndian.Uint32(body[i*4:])
			fmt.Printf("    [%3d] offset=%d\n", i, off)
		}
	}
}

func dumpPQTZ(header, body []byte) {
	if len(header) >= 24 {
		numBeats := binary.BigEndian.Uint32(header[20:24])
		fmt.Printf("    beats=%d\n", numBeats)
	}
	numBeats := len(body) / 8
	limit := numBeats
	if limit > 32 {
		limit = 32
	}
	fmt.Printf("    %4s  %10s  %10s  %6s  %s\n", "Beat", "Time (ms)", "Interval", "BPM", "Bar")
	var prevMs uint32
	for i := 0; i < limit; i++ {
		off := i * 8
		beatNum := binary.BigEndian.Uint16(body[off:])
		tempo := binary.BigEndian.Uint16(body[off+2:])
		timeMs := binary.BigEndian.Uint32(body[off+4:])
		bpmVal := float64(tempo) / 100.0
		interval := ""
		if i > 0 && timeMs > prevMs {
			interval = fmt.Sprintf("%10.1f", float64(timeMs-prevMs))
		} else {
			interval = fmt.Sprintf("%10s", "-")
		}
		bar := (i / 4) + 1
		beatInBar := (i % 4) + 1
		fmt.Printf("    %4d  %10d  %s  %6.2f  bar %d.%d (beat# %d)\n",
			i+1, timeMs, interval, bpmVal, bar, beatInBar, beatNum)
		prevMs = timeMs
	}
	if numBeats > 32 {
		fmt.Printf("    ... (%d more beats)\n", numBeats-32)
	}
}

func dumpWaveform(tag string, header, body []byte, bytesPerEntry int) {
	numEntries := len(body) / bytesPerEntry
	// Compute min/max/avg height.
	var minH, maxH uint8
	var sumH uint64
	minH = 255
	for i := 0; i < len(body); i++ {
		h := body[i] & 0x1f
		if h < minH {
			minH = h
		}
		if h > maxH {
			maxH = h
		}
		sumH += uint64(h)
	}
	avgH := float64(sumH) / float64(max(len(body), 1))
	fmt.Printf("    entries=%d  height: min=%d max=%d avg=%.1f\n", numEntries, minH, maxH, avgH)
}

func dumpPWV4(header, body []byte) {
	entrySize := 6
	numEntries := len(body) / entrySize
	if len(header) >= 24 {
		entrySize = int(binary.BigEndian.Uint32(header[12:16]))
		numEntries = int(binary.BigEndian.Uint32(header[16:20]))
		fmt.Printf("    entry_size=%d entries=%d\n", entrySize, numEntries)
	}
	// Show first few entries decoded.
	limit := 10
	if numEntries < limit {
		limit = numEntries
	}
	fmt.Printf("    %4s  %4s  %4s  %4s  %3s  %3s  %3s\n", "Idx", "b0", "b1", "b2", "R", "G", "B")
	for i := 0; i < limit; i++ {
		off := i * 6
		if off+5 >= len(body) {
			break
		}
		// byte0: (intensity<<5)|(height&0x1f), byte1: back layer, byte2: rms
		// byte3-5: R/G/B (0-127 range)
		fmt.Printf("    %4d  0x%02x  0x%02x  0x%02x  %3d  %3d  %3d\n",
			i, body[off], body[off+1], body[off+2], body[off+3], body[off+4], body[off+5])
	}
	if numEntries > limit {
		fmt.Printf("    ... (%d more entries)\n", numEntries-limit)
	}
}

func dumpPWV5(header, body []byte) {
	entrySize := 2
	numEntries := len(body) / entrySize
	var extFlags uint32
	if len(header) >= 24 {
		entrySize = int(binary.BigEndian.Uint32(header[12:16]))
		numEntries = int(binary.BigEndian.Uint32(header[16:20]))
		extFlags = binary.BigEndian.Uint32(header[20:24])
		fmt.Printf("    entry_size=%d entries=%d ext=0x%08x\n", entrySize, numEntries, extFlags)
	}
	// Decode first few entries: R(3)G(3)B(3)H(5)unused(2)
	limit := 20
	if numEntries < limit {
		limit = numEntries
	}
	fmt.Printf("    %4s  %6s  %1s %1s %1s  %2s\n", "Idx", "Raw", "R", "G", "B", "H")
	padding := 0
	for i := 0; i < limit; i++ {
		off := i * 2
		if off+1 >= len(body) {
			break
		}
		word := uint16(body[off])<<8 | uint16(body[off+1])
		r := (word >> 13) & 7
		g := (word >> 10) & 7
		b := (word >> 7) & 7
		h := (word >> 2) & 0x1f
		pad := ""
		if body[off] == 0xff && body[off+1] == 0x80 {
			pad = " (padding)"
			padding++
		}
		fmt.Printf("    %4d  0x%04x  %d %d %d  %2d%s\n", i, word, r, g, b, h, pad)
	}
	// Count total padding entries.
	for i := limit; i < numEntries; i++ {
		off := i * 2
		if off+1 < len(body) && body[off] == 0xff && body[off+1] == 0x80 {
			padding++
		}
	}
	if numEntries > limit {
		fmt.Printf("    ... (%d more entries)\n", numEntries-limit)
	}
	fmt.Printf("    padding entries: %d / %d (%.1f%%)\n", padding, numEntries, float64(padding)/float64(max(numEntries, 1))*100)
}

func dumpCuePoints(tag string, header, body []byte) {
	if len(header) < 20 {
		fmt.Printf("    (header too short)\n")
		return
	}
	cueType := binary.BigEndian.Uint32(header[12:16])
	numCues := binary.BigEndian.Uint16(header[16:18])
	memSize := binary.BigEndian.Uint16(header[18:20])
	fmt.Printf("    type=%d cues=%d memory_size=%d\n", cueType, numCues, memSize)

	if numCues == 0 || len(body) == 0 {
		fmt.Printf("    (no cue points)\n")
		return
	}

	// Parse cue entries. Each starts with "PCPT" or "PCP2" magic.
	pos := 0
	for i := 0; i < int(numCues) && pos+4 <= len(body); i++ {
		entryTag := string(body[pos : pos+4])
		if pos+8 > len(body) {
			break
		}
		entryHdrLen := int(binary.BigEndian.Uint32(body[pos+4 : pos+8]))
		entryLen := int(binary.BigEndian.Uint32(body[pos+8 : pos+12]))
		if entryLen <= 0 || pos+entryLen > len(body) {
			break
		}
		entry := body[pos : pos+entryLen]

		if entryTag == "PCPT" && len(entry) >= 28 {
			hotCue := binary.BigEndian.Uint32(entry[12:16])
			status := binary.BigEndian.Uint32(entry[16:20])
			_ = binary.BigEndian.Uint32(entry[20:24]) // ordering
			cueNum := entry[24]
			timeMs := binary.BigEndian.Uint32(entry[28:32])
			loopMs := uint32(0)
			if len(entry) >= 36 {
				loopMs = binary.BigEndian.Uint32(entry[32:36])
			}
			cueTypeStr := "memory"
			if hotCue > 0 {
				cueTypeStr = fmt.Sprintf("hot cue %d", hotCue)
			}
			loopStr := ""
			if loopMs > 0 {
				loopStr = fmt.Sprintf(" loop_end=%dms", loopMs)
			}
			fmt.Printf("    cue %d: %s  status=%d  time=%dms%s\n", cueNum, cueTypeStr, status, timeMs, loopStr)
		} else if entryTag == "PCP2" && len(entry) >= 56 {
			hotCue := binary.BigEndian.Uint32(entry[12:16])
			status := entry[17]
			cueNum := binary.BigEndian.Uint32(entry[20:24])
			timeMs := binary.BigEndian.Uint32(entry[28:32])
			loopMs := binary.BigEndian.Uint32(entry[32:36])
			colorR := entry[42]
			colorG := entry[43]
			colorB := entry[44]
			cueTypeStr := "memory"
			if hotCue > 0 {
				cueTypeStr = fmt.Sprintf("hot cue %d", hotCue)
			}
			loopStr := ""
			if loopMs > 0 {
				loopStr = fmt.Sprintf(" loop_end=%dms", loopMs)
			}
			colorStr := ""
			if colorR > 0 || colorG > 0 || colorB > 0 {
				colorStr = fmt.Sprintf(" color=#%02x%02x%02x", colorR, colorG, colorB)
			}
			fmt.Printf("    cue %d: %s  status=%d  time=%dms%s%s\n", cueNum, cueTypeStr, status, timeMs, loopStr, colorStr)
		} else {
			fmt.Printf("    cue entry %s: hdr=%d len=%d\n", entryTag, entryHdrLen, entryLen)
		}
		pos += entryLen
	}
}

func dumpPQT2(header, body []byte) {
	if len(header) < 56 {
		fmt.Printf("    (header too short: %d)\n", len(header))
		return
	}
	firstBeatNum := binary.BigEndian.Uint16(header[24:26])
	firstTempo := binary.BigEndian.Uint16(header[26:28])
	firstTimeMs := binary.BigEndian.Uint32(header[28:32])
	lastBeatNum := binary.BigEndian.Uint16(header[32:34])
	lastTempo := binary.BigEndian.Uint16(header[34:36])
	lastTimeMs := binary.BigEndian.Uint32(header[36:40])
	entryCount := binary.BigEndian.Uint32(header[40:44])

	fmt.Printf("    entries=%d\n", entryCount)
	fmt.Printf("    first: beat#%d  tempo=%.2f  time=%dms\n", firstBeatNum, float64(firstTempo)/100, firstTimeMs)
	fmt.Printf("    last:  beat#%d  tempo=%.2f  time=%dms\n", lastBeatNum, float64(lastTempo)/100, lastTimeMs)

	limit := int(entryCount)
	if limit > 20 {
		limit = 20
	}
	fmt.Printf("    %4s  %6s\n", "Beat", "ms%%1k")
	for i := 0; i < limit && i*2+1 < len(body); i++ {
		frac := binary.BigEndian.Uint16(body[i*2:])
		fmt.Printf("    %4d  %6d\n", i+1, frac)
	}
	if int(entryCount) > 20 {
		fmt.Printf("    ... (%d more entries)\n", int(entryCount)-20)
	}
}

func dumpPSSI(header, body []byte) {
	if len(header) < 24 {
		fmt.Printf("    (header too short)\n")
		return
	}
	entrySize := binary.BigEndian.Uint32(header[12:16])
	numEntries := binary.BigEndian.Uint32(header[16:20])
	fmt.Printf("    entry_size=%d entries=%d (phrase/song structure)\n", entrySize, numEntries)
	fmt.Printf("    body_bytes=%d\n", len(body))
}

func sortFloat64s(s []float64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// dumpANLZToCSV walks the ANLZ file at path and writes one CSV per
// PWV4/PWV5 section to outDir. Used for byte-level comparison between
// rekordbox .EXT files and our encoder output.
func dumpANLZToCSV(path, outDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 28 || string(data[0:4]) != "PMAI" {
		return fmt.Errorf("not a valid ANLZ file")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	hdrLen := int(binary.BigEndian.Uint32(data[4:8]))
	pos := hdrLen
	wrote := 0
	for pos+12 <= len(data) {
		fourcc := string(data[pos : pos+4])
		secHdrLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		body := data[pos+secHdrLen : pos+secLen]
		switch fourcc {
		case "PWV4":
			f := filepath.Join(outDir, "pwv4.csv")
			if err := writePWV4CSV(f, body); err != nil {
				return err
			}
			fmt.Printf("  wrote %s (%d entries)\n", f, len(body)/6)
			wrote++
		case "PWV5":
			f := filepath.Join(outDir, "pwv5.csv")
			if err := writePWV5CSV(f, body); err != nil {
				return err
			}
			fmt.Printf("  wrote %s (%d entries)\n", f, len(body)/2)
			wrote++
		}
		pos += secLen
	}
	if wrote == 0 {
		fmt.Printf("  no PWV4 or PWV5 sections found in %s\n", path)
	}
	return nil
}

func writePWV4CSV(path string, body []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("idx,d0,d1,d2,d3,d4,d5\n"); err != nil {
		return err
	}
	for i := 0; i+6 <= len(body); i += 6 {
		fmt.Fprintf(f, "%d,%d,%d,%d,%d,%d,%d\n",
			i/6, body[i], body[i+1], body[i+2], body[i+3], body[i+4], body[i+5])
	}
	return nil
}

func writePWV5CSV(path string, body []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("idx,hi,lo,r,g,b,h,padding\n"); err != nil {
		return err
	}
	for i := 0; i+2 <= len(body); i += 2 {
		hi, lo := body[i], body[i+1]
		word := uint16(hi)<<8 | uint16(lo)
		r := (word >> 13) & 7
		g := (word >> 10) & 7
		b := (word >> 7) & 7
		h := (word >> 2) & 0x1f
		padding := 0
		if hi == 0xff && lo == 0x80 {
			padding = 1
		}
		fmt.Fprintf(f, "%d,%d,%d,%d,%d,%d,%d,%d\n", i/2, hi, lo, r, g, b, h, padding)
	}
	return nil
}

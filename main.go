// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
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

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/api"
	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/export"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/netutil"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/link/prolink"
	"github.com/vynulldev/vynull/mediadb"
	"github.com/vynulldev/vynull/mpris"
	"github.com/vynulldev/vynull/nfs"
	"github.com/vynulldev/vynull/pdb"
)

func main() {
	cfg := parseFlags()

	// Apply --log-level before anything else so startup messages obey it.
	if lvl, ok := dlog.Parse(cfg.LogLevel); ok {
		dlog.SetLevel(lvl)
	} else {
		fmt.Fprintf(os.Stderr, "warning: unknown --log-level %q; using info\n", cfg.LogLevel)
	}

	// Install the Pro DJ Link wire-format encoder before any analysis runs.
	// analysis.AnalyzeTrack delegates encoding to the installed Encoder, which
	// defaults to nil — so this must happen before AnalyzeAll / AnalyzeTrack.
	analysis.SetEncoder(prolink.NewEncoder())

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

	// Log destination while serving:
	//   --log-file PATH  → append to that file (TUI or headless)
	//   TUI (default)    → an auto temp file, since the TUI owns the terminal
	//   headless         → stdout (no redirect)
	// In TUI mode the last few thousand log lines are also kept in memory
	// for the Logs tab, teed alongside the file writer.
	var logRing *device.LogRing
	if cfg.GenerateDir == "" {
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
			if cfg.TUI {
				logRing = device.NewLogRing(2000)
				log.SetOutput(io.MultiWriter(logFile, logRing))
			} else {
				log.SetOutput(logFile)
			}
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
	// "N tracks" in the analysis status line reports the library size (not just
	// how many analyses are loaded).
	analysisStore.TotalTracksFn = func() int {
		if lib == nil {
			return 0
		}
		return lib.TrackCount()
	}
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
	// MOUNT EXPORT replies when unlinked are what makes
	// the CDJ drop its LINK indicator instantly instead of
	// waiting for the keep-alive timeout (~5-6s).
	var dev *device.VirtualDevice
	nfsSrv := nfs.NewServer(nfsRoot)
	nfsSrv.Transcode = cfg.Transcode
	nfsSrv.IP = iface.IP
	nfsSrv.CDJMode = cfg.CDJMode // only CDJ mode needs privileged port 111
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
	// Let the monitor tell our own tracks apart from ones a deck loaded off a
	// USB/SD or another player (device number is negotiated during the claim,
	// so read it live rather than capturing cfg.DeviceNumber).
	monitor.SelfDevice = func() uint8 { return dev.DeviceNumber }

	// Fetch metadata for tracks a deck plays from its own media (a USB/SD) by
	// downloading that player's rekordbox export.pdb over NFS and reading it
	// locally — asynchronously and cached, so the status hot path never blocks.
	// A CDJ refuses our dbserver metadata requests because we use a
	// rekordbox-range player number (see docs/design/external-metadata.md), so
	// this NFS/PDB route (what beat-link's CrateDigger and prolink-connect use)
	// is the working path. Resolves the source device number to its IP via the
	// peer tracker.
	extMeta := mediadb.NewFetcher(
		func(player uint8) net.IP {
			if dev.Peers != nil {
				if p := dev.Peers.ByNumber(player); p != nil {
					return p.IP
				}
			}
			return nil
		},
	)
	monitor.ExternalMeta = func(player, slot uint8, trackID uint32) (string, string, string, bool) {
		if md := extMeta.Get(player, slot, trackID); md != nil {
			return md.Title, md.Artist, md.Key, md.Title != ""
		}
		return "", "", "", false
	}

	// Now that dev exists, wire the dbserver teardown callback to drop
	// peers from the tracker the moment they send 0x0100.
	db.OnPeerTeardown = func(ip net.IP) {
		if dev.Peers != nil {
			dev.Peers.RemoveByIP(ip)
		}
	}

	// When the user toggles UNLINK in the web UI, fire the rekordbox-
	// authentic disconnect chain in this order:
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
		BrowseRoots:  cfg.BrowseRoots,
		Listen:       cfg.Listen,
		Port:         9443, // legacy fallback if Listen is empty (it isn't, given flag default)
		Web:          cfg.Web,
		CacheDir:     cfg.DataDir, // for rendered waveform PNGs etc.
		ExtArtwork:   extMeta.Artwork,
		ExtAnalysis:  extMeta.Analysis,
		Overlay:      api.NewOverlayStore(filepath.Join(cfg.DataDir, "overlay.json")),
	}
	if cfg.Web {
		log.Printf("web UI enabled: http://%s/", displayAddr(cfg.Listen))
	}
	// MPRIS: mirror the audible deck to the desktop's media surfaces. Missing
	// session bus (headless) is normal — debug-log and move on.
	if cfg.MPRIS {
		base := "http://" + displayAddr(cfg.Listen)
		if _, err := mpris.Start(ctx, func() mpris.NowPlaying {
			np := apiSrv.NowPlayingSnapshot()
			out := mpris.NowPlaying{
				Playing:      np.Playing,
				DeviceNumber: np.DeviceNumber,
				TrackID:      np.TrackID,
				Title:        np.Title,
				Artist:       np.Artist,
				DurationMs:   np.DurationMs,
			}
			if np.ArtworkURL != "" {
				out.ArtURL = base + np.ArtworkURL
			}
			// Position from the beat counter: beats elapsed over tempo.
			if np.BeatInTrack > 0 && np.BPM > 0 {
				out.PositionMs = uint32(float64(np.BeatInTrack-1) / np.BPM * 60000)
			}
			return out
		}, time.Second); err != nil {
			dlog.Debugf("mpris: disabled: %v", err)
		} else {
			log.Printf("mpris: publishing now-playing on the session bus")
		}
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
		tuiProgram := device.NewTUI(monitor, lib, cdjSettings,
			func() *device.PeerTracker { return dev.Peers }, // created inside dev.Start — resolve at render time
			dev.MixerSnapshot, displayAddr(cfg.Listen), logRing)
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

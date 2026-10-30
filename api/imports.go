// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/filetype"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
)

// importProgress is the shared, pollable state of a rekordbox import.
type importProgress struct {
	Active bool   `json:"active"`
	Phase  string `json:"phase"`
	Done   int    `json:"done"`  // items processed in the current counted phase
	Total  int    `json:"total"` // items in the current counted phase (0 = indeterminate)
}

func (s *Server) importBegin(phase string) {
	s.importProgMu.Lock()
	s.importProg = importProgress{Active: true, Phase: phase}
	s.importProgMu.Unlock()
}
func (s *Server) importPhase(phase string) {
	s.importProgMu.Lock()
	s.importProg.Phase, s.importProg.Done, s.importProg.Total = phase, 0, 0
	s.importProgMu.Unlock()
}
func (s *Server) importCount(phase string, done, total int) {
	s.importProgMu.Lock()
	s.importProg.Phase, s.importProg.Done, s.importProg.Total = phase, done, total
	s.importProgMu.Unlock()
}
func (s *Server) importEnd() {
	s.importProgMu.Lock()
	s.importProg = importProgress{}
	s.importProgMu.Unlock()
}
func (s *Server) importBusy() bool {
	s.importProgMu.Lock()
	defer s.importProgMu.Unlock()
	return s.importProg.Active
}

// handleImportStatus reports the progress of an in-flight import (polled by the
// web UI). GET /api/import/status.
func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	s.importProgMu.Lock()
	p := s.importProg
	s.importProgMu.Unlock()
	writeJSON(w, p)
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// handleImportRekordbox imports tracks (and optionally playlists/tags)
// from a rekordbox XML export OR a rekordbox 6 master.db. The body
// chooses which path:
//
//	{"path": "/.../master.db", "key": "<sqlcipher key>"}      // master.db
//	{"path": "/.../rekordbox.xml"}                              // XML
//
// Auto-detected from extension if not obvious.
func (s *Server) handleImportRekordbox(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Path      string `json:"path"`
		Key       string `json:"key"`
		RemapFrom string `json:"remap_from"`
		RemapTo   string `json:"remap_to"`
		// Include lets the caller choose what to import. Each field is a
		// pointer so an omitted field (or an absent "include" object) defaults
		// to true — keeping the legacy "import everything" behaviour for older
		// clients that don't send it.
		Include *struct {
			Tracks    *bool `json:"tracks"`
			Playlists *bool `json:"playlists"`
			Tags      *bool `json:"tags"`
			Analysis  *bool `json:"analysis"`
			Artwork   *bool `json:"artwork"`
			Cues      *bool `json:"cues"`
			Settings  *bool `json:"settings"`
		} `json:"include"`
		// LockAnalysis locks each track whose import brings a beat grid
		// (rekordbox backups and master.db with its ANLZ files), so re-analysis
		// and grid edits cannot replace it until it is unlocked. Omitted means
		// true: an imported grid is usually work the user wants kept.
		LockAnalysis *bool `json:"lock_analysis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		jsonError(w, "path is required", http.StatusBadRequest)
		return
	}

	// Resolve the include flags (nil → true). incTracks gates the core track
	// import; the rest gate the per-category materialization below. Playlists,
	// tags, analysis, artwork, and cues all derive from the track import, so
	// they only take effect when tracks are imported too.
	want := func(p *bool) bool { return p == nil || *p }
	incTracks, incPlaylists, incTags := true, true, true
	incAnalysis, incArtwork, incCues, incSettings := true, true, true, true
	if req.Include != nil {
		incTracks = want(req.Include.Tracks)
		incPlaylists = want(req.Include.Playlists)
		incTags = want(req.Include.Tags)
		incAnalysis = want(req.Include.Analysis)
		incArtwork = want(req.Include.Artwork)
		incCues = want(req.Include.Cues)
		incSettings = want(req.Include.Settings)
	}
	if _, err := os.Stat(req.Path); err != nil {
		jsonError(w, "path not found: "+err.Error(), http.StatusBadRequest)
		return
	}

	// One import at a time — the progress state (and the library writes) assume
	// a single in-flight import. The UI disables its button too, but guard here
	// against a second concurrent request.
	if s.importBusy() {
		jsonError(w, "an import is already running", http.StatusConflict)
		return
	}
	s.importBegin("Reading rekordbox database…")
	defer s.importEnd()

	imp := library.ImporterFor(req.Path)
	if imp == nil {
		jsonError(w, "path must be a .xml, .nml (Traktor), .db, or .zip (rekordbox backup) file", http.StatusBadRequest)
		return
	}
	// Encrypted rekordbox formats (master.db / backup zip) need the user's 64-hex
	// SQLCipher key — we ship none and don't extract one.
	key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.Key), "0x"))
	if imp.RequiresKey(req.Path) && !isHex64(key) {
		jsonError(w, "a 64-hex SQLCipher key is required", http.StatusBadRequest)
		return
	}

	bundle, err := imp.Import(s.Library, library.ImportOptions{
		Path:         req.Path,
		Key:          key,
		WantTracks:   incTracks,
		WantSettings: incSettings && s.Settings != nil,
		Progress:     s.importPhase,
	})
	if err != nil {
		jsonError(w, "import failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if bundle == nil {
		bundle = &library.ImportBundle{}
	}
	if bundle.Cleanup != nil {
		defer bundle.Cleanup() // release the backup-zip temp dir after apply reads it
	}
	s.applyImportBundle(w, bundle, importInclude{
		Tracks: incTracks, Playlists: incPlaylists, Tags: incTags, Analysis: incAnalysis,
		Artwork: incArtwork, Cues: incCues, Settings: incSettings,
		Lock: req.LockAnalysis == nil || *req.LockAnalysis,
	}, req.RemapFrom, req.RemapTo)
}

// importInclude selects which categories of an import to materialize.
type importInclude struct {
	Tracks, Playlists, Tags, Analysis, Artwork, Cues, Settings bool
	Lock                                                       bool // lock tracks whose import brought a beat grid
}

// applyImportBundle materializes an importer's ImportBundle into the stores
// (playlists, tags, colours, cues, analysis, artwork, settings), flags missing
// audio files, and writes the JSON summary. Split out of handleImportRekordbox
// so the dispatch stays small; the body is otherwise unchanged from when it ran
// inline.
func (s *Server) applyImportBundle(w http.ResponseWriter, bundle *library.ImportBundle, inc importInclude, remapFrom, remapTo string) {
	incTracks, incPlaylists, incTags := inc.Tracks, inc.Playlists, inc.Tags
	incAnalysis, incArtwork, incCues, incSettings := inc.Analysis, inc.Artwork, inc.Cues, inc.Settings
	result := bundle.Result
	playlists := bundle.Playlists
	tags := bundle.Tags
	colors := bundle.Colors
	assets := bundle.Assets   // ANLZ + artwork paths (.zip, or .db in its lib folder)
	masterCues := bundle.Cues // cue points from djmdCue (.db/.zip)
	shareRoot := bundle.ShareRoot
	settingsDir := bundle.SettingsDir
	// !incTracks paths skip the importer entirely (e.g. a settings-only zip
	// import), so synthesize an empty result for the materialization + summary.
	if result == nil {
		result = &library.ImportResult{}
	}

	// Optional path-prefix remap (e.g. "Z:/Music/" → "/media/usb/Music/").
	// Applied after import so it covers tracks added in *this* import as
	// well as any already present with the matching prefix.
	if remapFrom != "" && remapTo != "" {
		n, _ := s.Library.RemapPaths(remapFrom, remapTo)
		dlog.Infof("import: remapped %d track paths %q -> %q", n, remapFrom, remapTo)
	}

	s.Library.Save()

	// Materialize the playlist tree (folders and leaf playlists) into
	// the playlist store. Re-importing creates new entries — we don't
	// try to dedupe by name since the user may legitimately want both.
	if s.Playlists != nil && incPlaylists {
		if len(playlists) > 0 {
			s.importPhase("Importing playlists…")
		}
		var created, skipped, smart int
		var walk func(parent uint32, nodes []library.PlaylistImport)
		walk = func(parent uint32, nodes []library.PlaylistImport) {
			for _, n := range nodes {
				var p *PlaylistInfo
				var perr error
				if n.IsSmart {
					// Translate rekordbox rules → SmartRules and create a
					// rule-based playlist (membership computed on read).
					rules, mapped, dropped := smartRulesFromRekordbox(n.Smart)
					if mapped == 0 || rbMatchesNothing(n.Smart) {
						// rekordbox selects nothing for this rule set (none,
						// empty, or an all-of set with a condition it cannot
						// satisfy), while an empty SmartRules would match the
						// whole library: make a plain empty playlist instead.
						dlog.Infof("import: smart playlist %q: matches nothing in rekordbox, creating empty", n.Name)
						p, perr = s.Playlists.Create(n.Name, parent, false)
					} else {
						if dropped > 0 {
							dlog.Infof("import: smart playlist %q: %d condition(s) dropped (unsupported)", n.Name, dropped)
						}
						p, perr = s.Playlists.CreateSmart(n.Name, parent, rules)
						smart++
					}
				} else {
					p, perr = s.Playlists.Create(n.Name, parent, n.IsFolder)
				}
				if perr != nil {
					skipped++
					dlog.Infof("import: create playlist %q failed: %v", n.Name, perr)
					continue
				}
				created++
				if !n.IsFolder && !n.IsSmart && len(n.TrackIDs) > 0 {
					if terr := s.Playlists.SetTracks(p.ID, n.TrackIDs); terr != nil {
						dlog.Infof("import: SetTracks for playlist %q failed: %v", n.Name, terr)
					}
				}
				if len(n.Children) > 0 {
					walk(p.ID, n.Children)
				}
			}
		}
		walk(0, playlists)
		dlog.Infof("import: materialized %d playlist nodes (%d smart, %d skipped)", created, smart, skipped)
	}

	// Materialize MyTags (from the XML's comment encoding). Each tag is
	// created uncategorized if it doesn't already exist by name, then
	// assigned to the imported tracks. Existing per-track tag sets are
	// unioned so a re-import doesn't drop tags applied elsewhere.
	if s.Tags != nil && incTags && (len(tags) > 0 || len(bundle.TagCategories) > 0) {
		s.importPhase("Importing tags…")
		byName := make(map[string]TagInfo)
		for _, t := range s.Tags.GetAllTags() {
			byName[t.Name] = t
		}
		// Resolve MyTag categories by name, creating any that don't exist yet
		// so imported tags land under their rekordbox category (Genre, Mood…).
		catByName := make(map[string]uint32)
		for _, c := range s.Tags.GetAllCategories() {
			catByName[c.Name] = c.ID
		}
		ensureCategory := func(name string) uint32 {
			if name == "" {
				return 0
			}
			if id, ok := catByName[name]; ok {
				return id
			}
			id, err := s.Tags.CreateCategory(name)
			if err != nil {
				dlog.Infof("import: create tag category %q failed: %v", name, err)
				return 0
			}
			catByName[name] = id
			return id
		}
		// Columns first, in rekordbox's order, so empty ones exist too.
		for _, name := range bundle.TagCategories {
			ensureCategory(name)
		}
		var tagged int
		for _, ti := range tags {
			catID := ensureCategory(ti.Category)
			cur, ok := byName[ti.Name]
			var id uint32
			if !ok {
				newID, err := s.Tags.CreateTag(ti.Name, catID)
				if err != nil {
					dlog.Infof("import: create tag %q failed: %v", ti.Name, err)
					continue
				}
				id = newID
				byName[ti.Name] = TagInfo{ID: id, Name: ti.Name, CategoryID: catID}
			} else {
				id = cur.ID
				// Adopt the imported category only if the tag is currently
				// uncategorized — don't clobber a category the user set.
				if catID != 0 && cur.CategoryID == 0 {
					s.Tags.SetTagCategory(id, catID)
					cur.CategoryID = catID
					byName[ti.Name] = cur
				}
			}
			for _, trackID := range ti.TrackIDs {
				existing := s.Tags.GetTagsForTrack(trackID)
				ids := []uint32{id}
				has := false
				for _, e := range existing {
					if e.ID == id {
						has = true
					}
					ids = append(ids, e.ID)
				}
				if has {
					continue
				}
				s.Tags.SetTagsForTrack(trackID, ids)
				tagged++
			}
		}
		dlog.Infof("import: materialized %d MyTag(s), %d track assignments", len(tags), tagged)
	}

	// Track colour labels (from each TRACK's Colour attribute) go to the
	// library, which owns them; this also covers tracks the import merged
	// into existing ones.
	if len(colors) > 0 {
		n := s.Library.SetColors(colors)
		dlog.Infof("import: materialized %d track colour label(s)", n)
	}

	// The tracks given cues, so the decks holding one re-fetch its cues once
	// the import is done.
	var cueTracks []uint32

	// Import ANLZ analysis (waveforms, beat grids, phrases) and cover art from
	// the share/ tree, so we reuse rekordbox's exact data instead of
	// re-analysing. Populated by a .zip (extracted share/) or by a .db that
	// still sits in its rekordbox library folder (share/ beside it); a .xml or
	// a copied-out .db has no accompanying file tree, so this is skipped.
	if shareRoot != "" && len(assets) > 0 && (incArtwork || incAnalysis || incCues) {
		var artN, anlzN, cueN, lockN, keptN int
		for i, a := range assets {
			if i%8 == 0 {
				s.importCount("Importing waveforms & artwork…", i, len(assets))
			}
			t := s.Library.Track(a.TrackID)
			if t == nil {
				continue
			}
			if incArtwork && a.ImagePath != "" && s.Library.Artwork != nil {
				p := filepath.Join(shareRoot, filepath.FromSlash(a.ImagePath))
				if data, e := os.ReadFile(p); e == nil && len(data) > 0 {
					// SetArtwork also marks the track checked, so the lazy
					// ffmpeg probe doesn't replace this curated rekordbox art.
					s.Library.SetArtwork(a.TrackID, s.Library.Artwork.Add("image/jpeg", data))
					artN++
				}
			}
			if a.AnalyzePath == "" {
				continue
			}
			dat := filepath.Join(shareRoot, filepath.FromSlash(a.AnalyzePath))
			base := strings.TrimSuffix(dat, filepath.Ext(dat))
			extPath := base + ".EXT"
			if incAnalysis && s.Analysis != nil && t.AnalysisLocked {
				keptN++ // a locked track keeps its analysis; unlock it to re-import
			} else if incAnalysis && s.Analysis != nil {
				twoEXPath := base + ".2EX" // CDJ-3000 3-band waveforms (absent in older libraries)
				if res := analysis.ParseANLZ(dat, extPath, twoEXPath, t.BPM, int(t.Duration.Seconds())); res != nil {
					// The ANLZ files don't carry the musical key, so ParseANLZ
					// leaves it blank. Backfill from the DB key (rekordbox
					// stores Camelot or standard notation) so imported tracks
					// report a key like our own analysis does.
					if res.KeyCamelot == "" && t.Key != "" {
						res.KeyCamelot, res.KeyStandard = analysis.KeyNamesFrom(t.Key)
					}
					s.Analysis.SetPath(a.TrackID, t.FilePath)
					s.Analysis.Set(a.TrackID, res)
					anlzN++
					if inc.Lock && len(res.Beats) > 0 {
						s.Library.UpdateBulk(a.TrackID, func(tr *library.Track) error { tr.AnalysisLocked = true; return nil })
						lockN++
					}
				}
			}
			// Cue points (hot + memory) from the ANLZ PCO2/PCOB lists. Hot cues
			// keep their pad number (A=1..H=8); memory cues are numbered 9.. in
			// order, matching the app's cue-number convention.
			if incCues && s.Cues != nil {
				if cues := importedCuesToInfo(analysis.ParseANLZCues(extPath, dat)); len(cues) > 0 {
					s.Cues.SaveCues(a.TrackID, cues)
					cueTracks = append(cueTracks, a.TrackID)
					cueN += len(cues)
				}
			}
		}
		if artN > 0 || lockN > 0 {
			s.Library.FinalizeBulk() // persist the new Track.ArtID references and locks
		}
		result.ArtworkImported = artN
		result.AnalysisImported = anlzN
		result.AnalysisLocked = lockN
		result.CuesImported = cueN
		dlog.Infof("import: imported %d artwork image(s), %d ANLZ analysis set(s) (%d locked, %d already-locked kept), %d cue point(s)", artN, anlzN, lockN, keptN, cueN)
	}

	// Cue points from the master.db djmdCue table — for imports without an
	// ANLZ tree (bare .db) or whose ANLZ files carried no cues. Skipped when
	// the ANLZ pass above already imported cues, so a zip doesn't double up.
	if incCues && s.Cues != nil && result.CuesImported == 0 && len(masterCues) > 0 {
		mems := map[uint32]int{} // per-track memory-cue counter (numbered 9..)
		byTrack := map[uint32][]CueInfo{}
		for _, c := range masterCues {
			num := uint16(c.HotCue)
			if c.HotCue < 1 || c.HotCue > 8 {
				mems[c.TrackID]++
				num = uint16(8 + mems[c.TrackID])
			}
			ci := CueInfo{Number: num, Type: "cue", TimeMs: c.TimeMs, LoopMs: -1, ColorID: c.ColorID, Label: c.Comment}
			if c.LoopMs > 0 {
				ci.Type = "loop"
				ci.LoopMs = c.LoopMs
			}
			if byTrack[c.TrackID] == nil {
				cueTracks = append(cueTracks, c.TrackID)
			}
			byTrack[c.TrackID] = append(byTrack[c.TrackID], ci)
		}
		for id, cues := range byTrack {
			s.Cues.SaveCues(id, cues)
		}
		result.CuesImported = len(masterCues)
		dlog.Infof("import: imported %d cue point(s) from master.db", len(masterCues))
	}
	s.refreshLoaded(cueTracks, (*device.VirtualDevice).BroadcastTrackRefresh)

	// Flag tracks whose audio file isn't present on this machine. A
	// rekordbox DB faithfully carries every track's path, including files on
	// drives we don't have here or paths the remap didn't cover — those are
	// unplayable (the CDJ rejects a load it can't fetch). Mark them so the UI
	// can show it, and report a count. Existence is cheap (os.Stat); scan the
	// whole library so re-imports re-evaluate previously-flagged tracks too.
	if incTracks {
		s.importPhase("Checking files…")
		// Standardize key notation to Camelot. rekordbox's ScaleName is a mix
		// of Camelot ("8A") and classic ("Am") depending on the track; our
		// analyzed tracks store Camelot, so normalize imports to match —
		// otherwise the KEY column shows both notations.
		keyFixed := 0
		for _, t := range s.Library.Tracks() {
			err := s.Library.UpdateBulk(t.ID, func(tr *library.Track) error {
				cam, _ := analysis.KeyNamesFrom(tr.Key)
				if tr.Key == "" || cam == "" || cam == tr.Key {
					return errUnchanged
				}
				tr.Key = cam
				return nil
			})
			if err == nil {
				keyFixed++
			}
		}
		if keyFixed > 0 {
			s.Library.FinalizeBulk()
			dlog.Infof("import: standardized %d track key(s) to Camelot notation", keyFixed)
		}
		if missing := s.Library.RefreshMissing(); missing > 0 {
			var sample []string
			for _, t := range s.Library.Tracks() {
				if t.FileMissing && len(sample) < 5 {
					sample = append(sample, t.FilePath)
				}
			}
			result.FilesMissing = missing
			dlog.Infof("import: WARNING — %d track(s) reference files not present on this machine (unplayable). Examples: %v", missing, sample)
		}
	}

	// Player/mixer settings (DEVSETTING/MYSETTING/MYSETTING2/DJMMYSETTING)
	// from the backup zip's root *SETTING.DAT blobs. settingsDir is only
	// populated for a .zip when the caller opted in (incSettings); a bare
	// .db/.xml carries no settings files. Merged into the live settings store
	// and persisted, exactly like the --import-settings CLI flag.
	if incSettings && settingsDir != "" && s.Settings != nil {
		cur := s.Settings.Config()
		imported, serr := device.ImportSettingsDir(settingsDir, &cur)
		if serr != nil {
			dlog.Infof("import: settings import failed: %v", serr)
			result.Errors = append(result.Errors, "settings: "+serr.Error())
		} else if len(imported) > 0 {
			s.Settings.SetConfig(cur)
			result.SettingsImported = len(imported)
			dlog.Infof("import: imported %d settings file(s): %v", len(imported), imported)
		}
	}
	// The deck's SORT menu and track-list column (master.db's djmdSort) are
	// browse settings too: adopted when the import includes settings.
	if incSettings && s.Menu != nil && len(bundle.Sorts) > 0 {
		s.Menu.ImportSorts(bundle.Sorts)
	}

	writeJSON(w, result)
}

// handleRemapPaths rewrites every track whose FilePath starts with
// req.From so the prefix becomes req.To. Renames the on-disk analysis
// cache (keyed by hash(file_path)) so the user doesn't have to
// re-analyze after the move. Other per-track caches (artwork, cues,
// waveform-png) are keyed by track ID or art ID and survive untouched.
func (s *Server) handleRemapPaths(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.From == "" || req.To == "" {
		jsonError(w, "both 'from' and 'to' are required", http.StatusBadRequest)
		return
	}
	n, changes := s.Library.RemapPaths(req.From, req.To)
	for _, c := range changes {
		s.Analysis.RenameCachedPath(c[0], c[1])
	}
	// Rewritten paths may now resolve (or stop resolving) on disk — re-check the
	// FileMissing flags so remapped tracks stop showing as missing.
	missing := s.Library.RefreshMissing()
	s.Library.Save()
	dlog.Infof("remap-paths: rewrote %d tracks %q -> %q (%d still missing)", n, req.From, req.To, missing)
	writeJSON(w, map[string]any{"changed": n, "missing": missing})
}

func (s *Server) handleAddTracks(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}

	var req struct {
		Paths []string `json:"paths"` // file paths to add
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Paths) == 0 {
		jsonError(w, "paths array is required", http.StatusBadRequest)
		return
	}

	// Expand any directories into their supported audio files up front so we
	// know the batch size and can choose sync vs. background adding.
	var files []string
	for _, path := range req.Paths {
		files = append(files, expandAudioPaths(path)...)
	}

	// Small batch: add inline and return the count immediately.
	if len(files) <= addSyncThreshold {
		added := s.addFiles(files, nil)
		s.Library.FinalizeBulk()
		writeJSON(w, map[string]interface{}{
			"ok":    true,
			"added": added,
			"total": s.Library.TrackCount(),
		})
		return
	}

	// Large batch: add in the background so the request/UI doesn't block on the
	// per-file decode check (ffmpeg per file). The client polls the status
	// endpoint below.
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	job := &addJob{total: len(files)}
	s.addJobs.Store(id, job)
	go func() {
		s.addFiles(files, job)
		s.Library.FinalizeBulk()
		job.finish()
		dlog.Infof("api: add job %s done: +%d (%d failed, %d already present) of %d",
			id, job.added, job.failed, job.skipped, job.total)
		time.AfterFunc(2*time.Minute, func() { s.addJobs.Delete(id) })
	}()
	writeJSON(w, map[string]interface{}{
		"ok":     true,
		"async":  true,
		"job_id": id,
		"total":  len(files),
	})
}

const addSyncThreshold = 25 // files at or under this are added synchronously

// addJob tracks the progress of a background bulk add.
type addJob struct {
	mu      sync.Mutex
	total   int
	added   int
	failed  int
	skipped int
	done    bool
}

func (j *addJob) bumpAdded()   { j.mu.Lock(); j.added++; j.mu.Unlock() }
func (j *addJob) bumpFailed()  { j.mu.Lock(); j.failed++; j.mu.Unlock() }
func (j *addJob) bumpSkipped() { j.mu.Lock(); j.skipped++; j.mu.Unlock() }
func (j *addJob) finish()      { j.mu.Lock(); j.done = true; j.mu.Unlock() }
func (j *addJob) snapshot() map[string]interface{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]interface{}{
		"total": j.total, "added": j.added, "failed": j.failed,
		"skipped": j.skipped, "done": j.done,
	}
}

// addFiles bulk-adds each supported file, skipping ones already in the library.
// Updates job progress when job is non-nil. The caller must call
// Library.FinalizeBulk() once after this returns.
func (s *Server) addFiles(files []string, job *addJob) int {
	added := 0
	for _, f := range files {
		if s.Library.TrackByPath(f) != nil {
			if job != nil {
				job.bumpSkipped()
			}
			continue
		}
		if _, err := s.addTrackByPath(f, true); err != nil {
			dlog.Infof("api: skipping %s: %v", f, err)
			if job != nil {
				job.bumpFailed()
			}
			continue
		}
		added++
		if job != nil {
			job.bumpAdded()
		}
	}
	return added
}

// handleAddStatus reports progress of a background add job started by
// handleAddTracks. GET /api/tracks/add/status?id=<job_id>.
func (s *Server) handleAddStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	v, ok := s.addJobs.Load(id)
	if !ok {
		jsonError(w, "unknown or expired job", http.StatusNotFound)
		return
	}
	writeJSON(w, v.(*addJob).snapshot())
}

// expandAudioPaths returns the path itself when it's a file, or — when it's a
// directory — every supported audio file beneath it (recursively). Lets the
// "add files/folders" UI accept a folder and pull in its whole tree.
func expandAudioPaths(path string) []string {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return []string{path} // a file (or missing — addTrackByPath reports it)
	}
	var files []string
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if _, ok := filetype.ForPath(p); ok {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// addTrackByPath adds a single track to the library by file path. When bulk is
// true it uses AddTrackBulk (no per-track list rebuild/save) — the caller must
// call Library.FinalizeBulk() once after the batch.
// Returns the new track ID. If the file doesn't exist or is unsupported, returns an error.
func (s *Server) addTrackByPath(path string, bulk bool) (uint32, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("file not found: %s", path)
	}

	track := &library.Track{
		FilePath: path,
	}

	ft, ok := filetype.ForPath(path)
	if !ok {
		return 0, fmt.Errorf("unsupported file type: %s", filepath.Ext(path))
	}
	track.FileType = ft.Name

	if tags, err := library.ReadFileTags(path); err == nil {
		tags.ApplyTo(track)
		if len(tags.Picture) > 0 {
			track.ArtID = s.Library.Artwork.Add(tags.PictureMIME, tags.Picture)
		}
	}

	// Fallback title (for a file whose tags can't be read)
	if track.Title == "" {
		track.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	track.DateAdded = time.Now()

	// File size
	if info, err := os.Stat(path); err == nil {
		track.FileSize = info.Size()
	}

	var id uint32
	if bulk {
		id = s.Library.AddTrackBulk(track)
	} else {
		id = s.Library.AddTrack(track)
	}

	// Also add to PDB if available
	if s.PDB != nil {
		pdbTrack := &pdb.Track{
			ID:          id,
			Title:       track.Title,
			Artist:      track.Artist,
			Album:       track.Album,
			Genre:       track.Genre,
			Label:       track.Label,
			FilePath:    path,
			FileName:    filepath.Base(path),
			Duration:    uint16(track.Duration.Seconds()),
			FileSize:    uint32(track.FileSize),
			Year:        uint16(track.Year),
			TrackNum:    uint32(track.TrackNum),
			DiscNumber:  uint16(track.DiscNum),
			Comment:     track.Comment,
			SampleRate:  uint32(track.SampleRate),
			SampleDepth: uint16(track.SampleDepth),
			PlayCount:   uint16(track.PlayCount),
		}
		s.PDB.AddTrack(pdbTrack)
	}

	dlog.Infof("api: added track %d: %s — %s", id, track.Artist, track.Title)

	// Register path for cache lookups. In lazy mode, analysis happens
	// when the CDJ requests the track. Otherwise, queue for analysis.
	if s.Analysis != nil {
		s.Analysis.SetPath(id, path)
	}
	if s.Analysis != nil && !s.LazyAnalysis {
		s.tryQueueAnalysis(id, path)
	}

	// Update device track count
	if s.Device != nil {
		s.Device.TrackCount = uint16(s.Library.TrackCount())
	}

	return id, nil
}

// handleReimportTracks re-reads metadata tags from disk for tracks in the library.
// POST /api/tracks/reimport — reimport all tracks
// POST /api/tracks/reimport {"track_ids": [1, 2, 3]} — reimport specific tracks
func (s *Server) handleReimportTracks(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}

	if s.Library == nil {
		jsonError(w, "library not available", http.StatusServiceUnavailable)
		return
	}
	// An empty body (or no track_ids) reimports every track; a body that
	// doesn't parse is refused rather than taken as "everything", which
	// would overwrite tag edits across the whole library.
	var req struct {
		TrackIDs []uint32 `json:"track_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	want := make(map[uint32]bool, len(req.TrackIDs))
	for _, id := range req.TrackIDs {
		want[id] = true
	}

	updated := 0
	for _, t := range s.Library.Tracks() {
		if len(want) > 0 && !want[t.ID] {
			continue
		}
		updated++
		// Read the file outside the library's lock. A file that can't be read
		// leaves its track as it is.
		tags, err := library.ReadFileTags(t.FilePath)
		if err != nil {
			continue
		}
		var artID uint32
		if len(tags.Picture) > 0 {
			artID = s.Library.Artwork.Add(tags.PictureMIME, tags.Picture)
		}
		s.Library.UpdateBulk(t.ID, func(tr *library.Track) error {
			tags.ApplyTo(tr)
			if artID != 0 {
				tr.ArtID = artID
			}
			return nil
		})
	}

	// One save for the batch, with the artist, album and genre lists rebuilt.
	s.Library.FinalizeBulk()
	dlog.Infof("api: reimported tags for %d tracks", updated)

	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"updated": updated,
	})
}

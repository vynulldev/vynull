// SPDX-License-Identifier: GPL-3.0-or-later

// Package export wraps the rekordbox USB-generation pipeline that
// used to live inline in main.go's --generate block. It can produce
// either a full-library export (the original behaviour) or a subset
// export for a single playlist / smart playlist via Options.
//
// The pipeline:
//  1. Convert library tracks → pdb tracks (or accept a pre-built list)
//  2. Analyse audio (BPM, key, waveform, beat grid…) — uses any cached
//     results in opts.Analysis; new ones go into the same store.
//  3. Lay out audio files under <dest>/Contents (copy or symlink).
//  4. Write per-track ANLZ0000.DAT/.EXT files.
//  5. Write export.pdb with track / artist / album / etc. tables.
//  6. Write MYSETTING.DAT / MYSETTING2.DAT / DJMMYSETTING.DAT.
//
// Callers from the CLI (main.go --generate) and HTTP API (POST
// /api/export) both go through Run so we keep one canonical pipeline.
package export

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
)

// Status is the live progress line set by Run while an export is
// happening (empty string when idle). Status access is goroutine-safe;
// the TUI / API can poll Status() at any cadence.
var (
	statusMu sync.RWMutex
	status   string
)

// Status returns the current export progress line (empty when no
// export is in flight).
func Status() string {
	statusMu.RLock()
	defer statusMu.RUnlock()
	return status
}

func setStatus(s string) {
	statusMu.Lock()
	status = s
	statusMu.Unlock()
}

// Options configures a single USB export run.
type Options struct {
	// Tracks is the set of tracks to include. If nil and Library is
	// non-nil, the full library is exported.
	Tracks []*pdb.Track

	// Library is the source of truth when Tracks is nil; the helper
	// LibraryToTracks builds pdb.Tracks from it.
	Library *library.Library

	// SrcDir is the music source directory the tracks were scanned from.
	// Used by pdb.PrepareUSBLayout to resolve relative paths.
	SrcDir string

	// DestDir is the USB destination root (will contain PIONEER/ and
	// Contents/).
	DestDir string

	// CopyFiles selects copy (true) vs symlink (false) when laying out
	// the Contents directory.
	CopyFiles bool

	// Settings is the CDJ settings to write into PIONEER/MYSETTING*.DAT.
	// Required.
	Settings pdb.SettingsBodies

	// Analysis is the shared analysis cache. New analyses are stored
	// here; existing entries are reused (no re-decode). If nil a fresh
	// in-memory store is used for this run.
	Analysis *analysis.Store

	// Playlists, when non-nil, replaces the default filesystem-mirror
	// playlist tree with an explicit one. Use this for subset exports so
	// the USB shows only the selected playlist(s).
	Playlists []*pdb.FolderNode

	// ArtworkLookup resolves an artwork ID to its raw JPEG bytes. When
	// non-nil the export writes each referenced JPEG under
	// PIONEER/Artwork/ so the CDJ can render album art. Typically
	// wraps library.ArtworkCache.Get.
	ArtworkLookup pdb.ArtworkLookup

	// Menu is the user's CDJ root-menu configuration (visible categories
	// in display order + hidden categories). nil = use defaults baked
	// into pdb/defaults.go.
	Menu *pdb.MenuConfig

	// Merge, when true, reads any existing export.pdb at DestDir and
	// appends to it: new tracks are added (dedup by FilePath), playlists
	// are added alongside existing ones, and artist/album/genre/key/label
	// IDs are preserved so re-exports stay stable. The deck shows BOTH
	// the existing library and the newly-added content.
	Merge bool

	// Concurrency for analysis. 0 = runtime.NumCPU().
	Concurrency int
}

// Run executes the full export pipeline. Blocks until done.
func Run(opts Options) error {
	tracks := opts.Tracks
	if tracks == nil {
		if opts.Library == nil {
			return fmt.Errorf("export: Tracks or Library must be set")
		}
		tracks = LibraryToTracks(opts.Library)
	}
	if len(tracks) == 0 {
		return fmt.Errorf("export: no tracks to write")
	}
	if opts.DestDir == "" {
		return fmt.Errorf("export: DestDir is required")
	}

	workers := opts.Concurrency
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Reset the status line on the way out so the TUI stops showing
	// the export hint once Run returns.
	defer setStatus("")

	// Overall percentage is weighted across phases — analysis is by
	// far the slowest (decoding + BPM/key + waveforms), then ANLZ
	// writing, then everything else is near-instant.
	const (
		wAnalyse = 0.75
		wAnlz    = 0.20
		wOther   = 0.05
	)
	phase := func(label string, pct float64) {
		setStatus(fmt.Sprintf("Exporting %d tracks → %s · %s · %3.0f%%",
			len(tracks), opts.DestDir, label, pct*100))
	}

	phase("analysing", 0)
	log.Printf("export: analysing %d tracks (workers=%d)", len(tracks), workers)
	store := analysis.AnalyzeAll(tracks, workers, opts.Analysis,
		func(done, total int) {
			phase("analysing", wAnalyse*float64(done)/float64(total))
		})

	phase("laying out files", wAnalyse)
	if err := pdb.PrepareUSBLayout(tracks, opts.SrcDir, opts.DestDir, opts.CopyFiles); err != nil {
		return fmt.Errorf("export: preparing USB layout: %w", err)
	}

	// Per-track ANLZ files (.DAT / .EXT) for waveforms, beat grids,
	// cue points. Stash the produced path on each track so the PDB row
	// can point at it.
	phase("writing ANLZ", wAnalyse)
	for i, t := range tracks {
		r := store.Get(t.ID)
		if r != nil {
			anlzPath, err := analysis.WriteANLZFiles(opts.DestDir, t.ID, t.FilePath, r)
			if err != nil {
				log.Printf("export: anlz track %d: %v", t.ID, err)
			} else {
				t.AnalyzePath = anlzPath
			}
		}
		phase("writing ANLZ", wAnalyse+wAnlz*float64(i+1)/float64(len(tracks)))
	}

	base := wAnalyse + wAnlz
	if opts.ArtworkLookup != nil {
		phase("copying artwork", base)
		ids := uniqueArtworkIDs(tracks)
		n, err := pdb.WriteArtworkFiles(opts.DestDir, ids, opts.ArtworkLookup)
		if err != nil {
			return fmt.Errorf("export: writing artwork: %w", err)
		}
		log.Printf("export: wrote %d artworks (%d referenced)", n, len(ids))
	}

	// Merge with existing PDB if requested. mergeBase carries existing
	// artist/album/genre/key/label IDs (so they stay stable across
	// re-exports), and we extend `tracks` with any pre-existing entries
	// not already in the new export (deduplicated by FilePath), and
	// `opts.Playlists` with any pre-existing playlists.
	var mergeBase *pdb.MergeBase
	if opts.Merge {
		existingPdbPath := filepath.Join(opts.DestDir, "PIONEER", "rekordbox", "export.pdb")
		mb, err := pdb.LoadForMerge(existingPdbPath)
		if err != nil {
			log.Printf("export: merge requested but could not load %s: %v (proceeding as fresh export)", existingPdbPath, err)
		} else if mb != nil {
			tracks, opts.Playlists = mergeWithExisting(existingPdbPath, tracks, opts.Playlists)
			mergeBase = mb
			log.Printf("export: merging with existing PDB — %d total tracks after dedup", len(tracks))
		}
	}

	phase("writing PDB", base+wOther*0.5)
	if err := pdb.GenerateWithOptions(tracks, opts.Playlists, opts.Menu, mergeBase, opts.DestDir); err != nil {
		return fmt.Errorf("export: generating PDB: %w", err)
	}

	// Skeleton PIONEER subdirs that are created but left
	// empty on a fresh export (CDJ + MPJ). We don't know what these
	// are used for — they're empty in every export —
	// but creating them matches the layout the CDJ probably expects.
	for _, sub := range []string{"PIONEER/CDJ", "PIONEER/MPJ"} {
		if err := os.MkdirAll(filepath.Join(opts.DestDir, sub), 0o755); err != nil {
			return fmt.Errorf("export: create %s: %w", sub, err)
		}
	}

	// Companion exportExt.pdb skeleton. Empty tables for now — the
	// CDJ accepts a present-but-empty file the same as a fully
	// populated one for browse / playback. Tag and hot-cue-bank
	// content will be plumbed through here as those table formats
	// are supported.
	if err := pdb.GenerateExt(opts.DestDir); err != nil {
		return fmt.Errorf("export: generating exportExt.pdb: %w", err)
	}

	if err := pdb.WriteSettingsFiles(opts.DestDir, opts.Settings); err != nil {
		return fmt.Errorf("export: generating settings: %w", err)
	}

	log.Printf("export: wrote %d tracks to %s", len(tracks), opts.DestDir)
	return nil
}

// mergeWithExisting reads the existing PDB at `existingPath` and combines:
//   - Tracks: keeps all existing tracks; appends new ones whose FilePath
//     isn't already present. For new tracks, assigns IDs starting at
//     (max existing ID) + 1. Existing tracks keep their stored metadata —
//     we don't overwrite (so the CDJ's history/play counts stay valid).
//   - Playlists: keeps existing playlist tree; appends new playlists. If
//     a new playlist's name matches an existing one, the new one wins
//     (re-export of the same playlist replaces it).
//
// Returns the merged tracks slice and playlist list. If existingPath
// can't be loaded, returns the inputs unchanged.
func mergeWithExisting(existingPath string, newTracks []*pdb.Track, newPlaylists []*pdb.FolderNode) ([]*pdb.Track, []*pdb.FolderNode) {
	existing, err := pdb.Open(existingPath)
	if err != nil {
		log.Printf("export: merge: cannot load %s: %v", existingPath, err)
		return newTracks, newPlaylists
	}

	// Index new tracks by FilePath so we know which existing entries to skip.
	newByPath := make(map[string]*pdb.Track, len(newTracks))
	for _, t := range newTracks {
		if t.FilePath != "" {
			newByPath[t.FilePath] = t
		}
	}

	// Start the merged set with all existing tracks not being re-exported,
	// plus the new ones (which override on FilePath match).
	merged := make([]*pdb.Track, 0, len(existing.Tracks)+len(newTracks))
	usedID := make(map[uint32]bool, len(existing.Tracks))
	var maxID uint32
	for _, t := range existing.Tracks {
		if t.ID > maxID {
			maxID = t.ID
		}
		usedID[t.ID] = true
		if _, replaced := newByPath[t.FilePath]; replaced {
			continue // new export has a fresh version; let the new one through
		}
		merged = append(merged, t)
	}

	// Add new tracks, reusing existing IDs where FilePath matches, else
	// allocating a fresh ID above the existing max.
	existingByPath := make(map[string]uint32, len(existing.Tracks))
	for _, t := range existing.Tracks {
		if t.FilePath != "" {
			existingByPath[t.FilePath] = t.ID
		}
	}
	nextID := maxID + 1
	for _, t := range newTracks {
		if id, ok := existingByPath[t.FilePath]; ok {
			t.ID = id // preserve existing ID for the same file
		} else if t.ID == 0 || usedID[t.ID] {
			t.ID = nextID
			nextID++
		}
		usedID[t.ID] = true
		merged = append(merged, t)
	}

	// Merge playlists: existing tree first, then new playlists (replacing
	// by name when collision).
	newByName := make(map[string]bool, len(newPlaylists))
	for _, p := range newPlaylists {
		newByName[p.Name] = true
	}
	mergedPlaylists := make([]*pdb.FolderNode, 0, len(existing.PlaylistTree)+len(newPlaylists))
	var maxPLID uint32
	for _, p := range existing.PlaylistTree {
		if p.ID > maxPLID {
			maxPLID = p.ID
		}
		if newByName[p.Name] {
			continue // new playlist with same name supersedes
		}
		mergedPlaylists = append(mergedPlaylists, p)
	}
	// Bump new playlist IDs above max existing to avoid collision.
	for _, p := range newPlaylists {
		if p.ID <= maxPLID {
			maxPLID++
			p.ID = maxPLID
		}
		mergedPlaylists = append(mergedPlaylists, p)
	}

	return merged, mergedPlaylists
}

func uniqueArtworkIDs(tracks []*pdb.Track) []uint32 {
	seen := make(map[uint32]bool, len(tracks))
	out := make([]uint32, 0, len(tracks))
	for _, t := range tracks {
		if t.ArtworkID == 0 || seen[t.ArtworkID] {
			continue
		}
		seen[t.ArtworkID] = true
		out = append(out, t.ArtworkID)
	}
	return out
}

// LibraryToTracks converts every library track to a pdb.Track, carrying
// through the tag/metadata fields the PDB writer encodes. Moved from
// main.go so subset exports use the same conversion.
func LibraryToTracks(lib *library.Library) []*pdb.Track {
	src := lib.Tracks()
	out := make([]*pdb.Track, len(src))
	for i, t := range src {
		out[i] = &pdb.Track{
			ID:       t.ID,
			Title:    t.Title,
			Artist:   t.Artist,
			Album:    t.Album,
			Genre:    t.Genre,
			Key:      t.Key,
			Label:    t.Label,
			FilePath: t.FilePath,
			FileName: filepath.Base(t.FilePath),
			Comment:  t.Comment,
			Tempo:    uint32(t.BPM * 100),
			Duration: uint16(t.Duration.Seconds()),
			Bitrate:  uint32(safeBitrate(t.FileSize, t.Duration.Seconds())),
			Year:     uint16(t.Year),
			// Re-encode currently disabled — the CDJ freezes we were
			// chasing turned out to be PDB-encoding bugs (long-ASCII paths,
			// stale FileSize), not the source audio itself. The re-encode
			// path in pdb.PrepareUSBLayout stays available but nothing sets
			// this true today.
			NeedsReencode: false,
			TrackNum:      uint32(t.TrackNum),
			DiscNumber:    uint16(t.DiscNum),
			FileSize:      uint32(t.FileSize),
			Rating:        t.Rating,
			ColorID:       t.ColorID,
			ArtworkID:     t.ArtID,
			SampleRate:    uint32(t.SampleRate),
			SampleDepth:   uint16(t.SampleDepth),
			PlayCount:     uint16(t.PlayCount),
		}
	}
	return out
}

func safeBitrate(sizeBytes int64, durSec float64) int64 {
	if durSec < 1 {
		durSec = 1
	}
	return sizeBytes * 8 / int64(durSec)
}

// FilterTracks returns the subset of tracks whose ID appears in ids.
// Result order matches ids (so a playlist export keeps the user's
// curated order).
func FilterTracks(tracks []*pdb.Track, ids []uint32) []*pdb.Track {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[uint32]*pdb.Track, len(tracks))
	for _, t := range tracks {
		byID[t.ID] = t
	}
	out := make([]*pdb.Track, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out
}

// SinglePlaylist returns a one-entry playlist tree suitable for a
// subset export: one leaf playlist named `name` containing trackIDs in
// the supplied order. Use a small fixed ID (1) since the tree has no
// other nodes to collide with.
func SinglePlaylist(name string, trackIDs []uint32) []*pdb.FolderNode {
	return []*pdb.FolderNode{
		{
			ID:       1,
			ParentID: 0,
			Name:     name,
			IsFolder: false,
			TrackIDs: append([]uint32(nil), trackIDs...),
		},
	}
}

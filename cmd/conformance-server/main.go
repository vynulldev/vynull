// SPDX-License-Identifier: GPL-3.0-or-later

// Command conformance-server stands Vynull's dbserver up on loopback against a
// fixture library, for the evanpurkhiser/link-spec conformance harness.
//
// It implements the CLI + readiness contract the harness's replay_vynull.py
// adapter drives:
//
//		conformance-server --fixture-json <dump> --data-dir <dir> --second-column <col>
//
//	  - --fixture-json is a tools/rekordbox_dump.py JSON dump (the same format our
//	    master.db importer consumes); we load it into a library and serve it.
//	  - --data-dir is a writable per-run scratch directory.
//	  - --second-column selects the track-list detail column.
//
// On startup it prints exactly one line of JSON to stdout —
// {"status":"ready","listen_address":"127.0.0.1","query_port":N} — then serves
// the dbserver on loopback until signalled. All logging goes to stderr so
// stdout carries only the readiness line. See conformance/BACKENDS.md in
// link-spec and docs/design/foh-manager.md's sibling note.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/vynulldev/vynull/api"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/library"
)

// confMenu supplies the built-in root menu plus the harness-selected second
// (detail) column.
type confMenu struct{ second string }

func (m confMenu) RootMenu() []dbserver.RootMenuEntry { return dbserver.DefaultRootMenu() }
func (m confMenu) TrackDetail() string                { return m.second }

// confPlaylists serves the fixture's playlist tree straight from the dump
// (which keeps rekordbox's playlist IDs, parent IDs, and membership order,
// in Seq order). A ParentID of "root" maps to folder ID 0, which is what the
// deck sends for the top level. Smart playlists are evaluated against the
// library with the daemon's own rule engine (api.RekordboxSmartTracks), so
// the suites measure what the daemon serves for an imported smart playlist.
type confPlaylists struct {
	nodes []library.MasterDBPlaylist
	lib   *library.Library
	tags  api.TagLookup
	now   time.Time // the clock smart-playlist date rules are evaluated at
}

// conformanceNow is the clock for relative-date smart rules ("added in the
// last N days"). rekordbox's goldens for those were recorded with the guest
// clock pinned (link-spec record_smart_relative_date.sh), so the harness can
// pass that instant in VYNULL_CONFORMANCE_NOW (local time,
// 2006-01-02T15:04:05); otherwise the real clock is used.
func conformanceNow() time.Time {
	if v := os.Getenv("VYNULL_CONFORMANCE_NOW"); v != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", v, time.Local)
		if err != nil {
			fatalf("VYNULL_CONFORMANCE_NOW: %v", err)
		}
		return t
	}
	return time.Now()
}

// confTags answers MyTag lookups for smart-playlist rules from the dump's
// tag assignments. Rules name the tag, so only names are needed.
type confTags map[uint32][]api.TagInfo

func (c confTags) GetTagsForTrack(trackID uint32) []api.TagInfo { return c[trackID] }

func tagsFromDump(tags []library.MasterDBTag) confTags {
	out := confTags{}
	for _, tag := range tags {
		for _, t := range tag.TrackIDs {
			id := playlistID(t)
			out[id] = append(out[id], api.TagInfo{ID: playlistID(tag.ID), Name: tag.Name})
		}
	}
	return out
}

func playlistID(s string) uint32 {
	if s == "" || s == "root" {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return uint32(n)
}

func (p confPlaylists) Children(parentID uint32) []dbserver.PlaylistEntry {
	var out []dbserver.PlaylistEntry
	for _, n := range p.nodes {
		if playlistID(n.ParentID) == parentID {
			out = append(out, dbserver.PlaylistEntry{
				ID: playlistID(n.ID), Name: n.Name, IsFolder: n.IsFolder, Seq: uint32(n.Seq),
			})
		}
	}
	// rekordbox lists folders before playlists, each group in Seq order (the
	// stored Seq still rides along in arg 9).
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsFolder != out[j].IsFolder {
			return out[i].IsFolder
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

func (p confPlaylists) Tracks(playlistID_ uint32) []uint32 {
	for _, n := range p.nodes {
		if playlistID(n.ID) != playlistID_ {
			continue
		}
		if n.IsSmart {
			return api.RekordboxSmartTracks(n.Smart, p.lib, p.tags, p.now)
		}
		ids := make([]uint32, 0, len(n.TrackIDs))
		for _, t := range n.TrackIDs {
			ids = append(ids, playlistID(t))
		}
		return ids
	}
	return nil
}

func (p confPlaylists) IsSmart(id uint32) bool {
	for _, n := range p.nodes {
		if playlistID(n.ID) == id {
			return n.IsSmart
		}
	}
	return false
}

func (confPlaylists) HistoryFolderID() uint32 { return 0 }

func main() {
	// stdout is reserved for the single readiness line; everything else (ours
	// and the dbserver's) goes to stderr.
	log.SetOutput(os.Stderr)

	fixtureJSON := flag.String("fixture-json", "", "path to a tools/rekordbox_dump.py JSON dump to serve")
	dataDir := flag.String("data-dir", "", "writable directory for per-run state")
	secondColumn := flag.String("second-column", "bpm", "track-list detail (second) column")
	flag.Parse()

	if *fixtureJSON == "" {
		fatalf("--fixture-json is required")
	}

	data, err := os.ReadFile(*fixtureJSON)
	if err != nil {
		fatalf("read fixture: %v", err)
	}
	var dump library.MasterDBDump
	if err := json.Unmarshal(data, &dump); err != nil {
		fatalf("parse fixture JSON %s: %v", *fixtureJSON, err)
	}
	lib := library.New()
	if _, err := library.ImportMasterDBDump(lib, &dump); err != nil {
		fatalf("import fixture: %v", err)
	}
	log.Printf("conformance: loaded %d tracks from %s", len(dump.Tracks), *fixtureJSON)

	analysisStore := analysis.NewStore()
	if *dataDir != "" {
		analysisStore = analysis.NewStoreWithCache(filepath.Join(*dataDir, "analysis"))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db := &dbserver.Server{
		Library:      lib,
		DeviceNumber: 17, // rekordbox device number, matching the oracle
		Analysis:     analysisStore,
		Menu:         confMenu{second: *secondColumn},
		Playlists:    confPlaylists{nodes: dump.Playlists, lib: lib, tags: tagsFromDump(dump.Tags), now: conformanceNow()},
		BindHost:     "127.0.0.1", // loopback only
		Ready:        make(chan struct{}),
		// LinkedFn left nil so the server accepts connections (no UNLINK gate).
	}

	errCh := make(chan error, 1)
	go func() { errCh <- db.Start(ctx) }()

	select {
	case <-db.Ready:
	case err := <-errCh:
		fatalf("dbserver failed to start: %v", err)
	}

	// The harness client feeds query_port into its port-query (RemoteDBServer
	// discovery) to learn the dynamic port, so query_port is the discovery port
	// (12523), not the dynamic one. dynamic_port is included for diagnostics.
	readiness, _ := json.Marshal(map[string]any{
		"status":         "ready",
		"listen_address": "127.0.0.1",
		"query_port":     12523,
		"dynamic_port":   int(db.QueryPort()),
	})
	fmt.Println(string(readiness))
	os.Stdout.Sync()

	if err := <-errCh; err != nil {
		fatalf("dbserver: %v", err)
	}
}

func fatalf(format string, args ...any) {
	log.Printf(format, args...)
	os.Exit(1)
}

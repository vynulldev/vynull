// SPDX-License-Identifier: GPL-3.0-or-later

// Package mediadb resolves metadata for tracks a deck plays from its own media
// (a USB/SD) by downloading that player's rekordbox export.pdb over NFS and
// reading it locally. It is the working alternative to the dbserver client,
// which a CDJ refuses because we use a rekordbox-range player number rather than
// a standard 1-4 (see docs/design/external-metadata.md). Beat-link's
// CrateDigger and prolink-connect take this same route.
//
// The whole export.pdb is fetched once per (player, slot) and cached, so any
// track from that media resolves from memory afterward. Get never blocks: on a
// miss it kicks off a background download and returns nil, and the next Get for
// that media (after the download lands) hits the cache.
package mediadb

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/vynulldev/vynull/nfs"
	"github.com/vynulldev/vynull/pdb"
)

const (
	// successTTL re-downloads a media's database occasionally so a swapped USB
	// eventually refreshes; the stale copy is served meanwhile (no flicker).
	successTTL = 10 * time.Minute
	// failCooldown is how long a failed download is remembered before retrying,
	// so an unreachable player isn't re-dialled on every status packet.
	failCooldown = 30 * time.Second
)

// Metadata is the subset of a track's fields the monitor/overlay display.
type Metadata struct {
	Title  string
	Artist string
	Album  string
	Genre  string
	Key    string
	BPM    float64
}

// Fetcher downloads and caches players' export databases.
type Fetcher struct {
	// ResolveIP maps a source player's device number to its IP (wired to the
	// peer tracker). Required.
	ResolveIP func(player uint8) net.IP
	// Timeout bounds each NFS RPC; zero uses the client default.
	Timeout time.Duration

	mu          sync.Mutex
	cache       map[string]*entry
	inflight    map[string]bool
	art         map[string][]byte // (player:slot:trackID) -> JPEG (nil = none/failed)
	artInflight map[string]bool
}

type entry struct {
	db     *pdb.Database // nil = the last download failed
	export string        // the export root the db came from (e.g. "/C/")
	at     time.Time
}

// NewFetcher builds a Fetcher. resolveIP maps a device number to its IP.
func NewFetcher(resolveIP func(uint8) net.IP) *Fetcher {
	return &Fetcher{
		ResolveIP:   resolveIP,
		cache:       map[string]*entry{},
		inflight:    map[string]bool{},
		art:         map[string][]byte{},
		artInflight: map[string]bool{},
	}
}

func key(player, slot uint8) string {
	return fmt.Sprintf("%d:%d", player, slot)
}

// Get returns metadata for a track on the given player's media, or nil when the
// media's database isn't downloaded yet (a background fetch starts on the first
// miss) or the track isn't in it.
func (f *Fetcher) Get(player, slot uint8, trackID uint32) *Metadata {
	k := key(player, slot)

	f.mu.Lock()
	e, ok := f.cache[k]
	stale := ok && e.db != nil && time.Since(e.at) >= successTTL
	failedRecently := ok && e.db == nil && time.Since(e.at) < failCooldown
	needFetch := !ok || stale || (e.db == nil && !failedRecently)
	if needFetch && !f.inflight[k] {
		f.inflight[k] = true
		go f.fetch(k, player, slot)
	}
	var db *pdb.Database
	if ok {
		db = e.db // serve the cached copy immediately, even while refreshing
	}
	f.mu.Unlock()

	if db == nil {
		return nil
	}
	return metaFromDB(db, trackID)
}

func metaFromDB(db *pdb.Database, trackID uint32) *Metadata {
	t := db.TrackByID(trackID)
	if t == nil {
		return nil
	}
	return &Metadata{
		Title:  t.Title,
		Artist: t.Artist,
		Album:  t.Album,
		Genre:  t.Genre,
		Key:    t.Key,
		BPM:    float64(t.Tempo) / 100.0,
	}
}

func (f *Fetcher) fetch(k string, player, slot uint8) {
	db, export := f.doFetch(player, slot)
	f.mu.Lock()
	// Keep an earlier successful copy if this refresh failed.
	if db == nil {
		if e, ok := f.cache[k]; ok && e.db != nil {
			e.at = time.Now() // reset the TTL so we retry later, not every packet
		} else {
			f.cache[k] = &entry{db: nil, at: time.Now()}
		}
	} else {
		f.cache[k] = &entry{db: db, export: export, at: time.Now()}
	}
	delete(f.inflight, k)
	f.mu.Unlock()
}

func (f *Fetcher) doFetch(player, slot uint8) (*pdb.Database, string) {
	if f.ResolveIP == nil {
		return nil, ""
	}
	ip := f.ResolveIP(player)
	if ip == nil {
		return nil, ""
	}
	c, err := nfs.Dial(ip, f.Timeout)
	if err != nil {
		log.Printf("mediadb: dial player %d (%s): %v", player, ip, err)
		return nil, ""
	}
	defer c.Close()

	data, export, err := c.FetchExportPDB()
	if err != nil {
		log.Printf("mediadb: player %d (%s) export.pdb: %v", player, ip, err)
		return nil, ""
	}
	db, err := pdb.OpenBytes(data, "")
	if err != nil {
		log.Printf("mediadb: player %d parse export.pdb: %v", player, err)
		return nil, ""
	}
	log.Printf("mediadb: player %d slot %d: %d tracks from %s%s", player, slot, len(db.Tracks), ip, export)
	return db, export
}

// Artwork returns the cover-art JPEG for a track on the given player's media, or
// nil when the media isn't downloaded yet, the track has no art, or the fetch is
// still in flight. Like Get, it never blocks: a miss starts a background
// download and returns nil, and the art appears on a later call.
func (f *Fetcher) Artwork(player, slot uint8, trackID uint32) []byte {
	ak := artKey(player, slot, trackID)

	f.mu.Lock()
	if b, ok := f.art[ak]; ok {
		f.mu.Unlock()
		return b // cached (nil = known-none or failed; positive = the JPEG)
	}
	e, ok := f.cache[key(player, slot)]
	if !ok || e.db == nil {
		// Need the database first (for the ArtworkID and export root); kicking a
		// metadata fetch also primes the art fetch for a later call.
		f.mu.Unlock()
		f.Get(player, slot, trackID)
		return nil
	}
	if f.artInflight[ak] {
		f.mu.Unlock()
		return nil
	}
	f.artInflight[ak] = true
	db, export := e.db, e.export
	f.mu.Unlock()

	go func() {
		data := f.doFetchArt(player, export, db, trackID)
		f.mu.Lock()
		f.art[ak] = data // may be nil (no art / failed) — negative-cached so we don't refetch
		delete(f.artInflight, ak)
		f.mu.Unlock()
	}()
	return nil
}

func (f *Fetcher) doFetchArt(player uint8, export string, db *pdb.Database, trackID uint32) []byte {
	if f.ResolveIP == nil {
		return nil
	}
	t := db.TrackByID(trackID)
	if t == nil || t.ArtworkID == 0 {
		return nil
	}
	// Prefer the real path from the media's artwork table; fall back to the
	// standard rekordbox scheme if that table wasn't present.
	path := db.Artwork[t.ArtworkID]
	if path == "" {
		path = pdb.ArtworkPath(t.ArtworkID)
	}
	ip := f.ResolveIP(player)
	if ip == nil {
		return nil
	}
	c, err := nfs.Dial(ip, f.Timeout)
	if err != nil {
		log.Printf("mediadb: art dial player %d (%s): %v", player, ip, err)
		return nil
	}
	defer c.Close()

	data, err := c.ReadFile(export, path)
	if err != nil {
		log.Printf("mediadb: player %d art %s: %v", player, path, err)
		return nil
	}
	log.Printf("mediadb: player %d track %d art %s (%d bytes)", player, trackID, path, len(data))
	return data
}

func artKey(player, slot uint8, trackID uint32) string {
	return fmt.Sprintf("%d:%d:%d", player, slot, trackID)
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dbclient

import (
	"fmt"
	"log"
	"net"
	"sync"
)

// Fetcher fetches metadata for externally-sourced tracks and caches it. Get is
// non-blocking — it returns the cached result or nil, kicking off a background
// fetch on a miss — so a status handler on the hot path never waits on the
// network. Once a fetch lands, the next Get for that track hits the cache.
type Fetcher struct {
	// ResolveIP maps a source player's device number to its IP (wired to the
	// peer tracker); OurPlayer returns our own device number. Both required.
	ResolveIP func(player uint8) net.IP
	OurPlayer func() uint8

	mu       sync.Mutex
	cache    map[string]*Metadata
	inflight map[string]bool
}

// NewFetcher builds a Fetcher. resolveIP maps a device number to its IP;
// ourPlayer returns our own device number.
func NewFetcher(resolveIP func(uint8) net.IP, ourPlayer func() uint8) *Fetcher {
	return &Fetcher{
		ResolveIP: resolveIP,
		OurPlayer: ourPlayer,
		cache:     map[string]*Metadata{},
		inflight:  map[string]bool{},
	}
}

func cacheKey(player, slot uint8, trackID uint32) string {
	return fmt.Sprintf("%d:%d:%d", player, slot, trackID)
}

// Get returns cached metadata for (player, slot, trackID), or nil when it isn't
// cached yet (a background fetch is started on the first miss).
func (f *Fetcher) Get(player, slot uint8, trackID uint32) *Metadata {
	k := cacheKey(player, slot, trackID)
	f.mu.Lock()
	if m, ok := f.cache[k]; ok {
		f.mu.Unlock()
		return m
	}
	if f.inflight[k] {
		f.mu.Unlock()
		return nil
	}
	f.inflight[k] = true
	f.mu.Unlock()
	go f.fetch(k, player, slot, trackID)
	return nil
}

func (f *Fetcher) fetch(k string, player, slot uint8, trackID uint32) {
	m := f.doFetch(player, slot, trackID)
	f.mu.Lock()
	if m != nil {
		f.cache[k] = m
	}
	delete(f.inflight, k)
	f.mu.Unlock()
}

func (f *Fetcher) doFetch(player, slot uint8, trackID uint32) *Metadata {
	if f.ResolveIP == nil || f.OurPlayer == nil {
		return nil
	}
	ip := f.ResolveIP(player)
	if ip == nil {
		return nil
	}
	port, err := DBServerPort(ip)
	if err != nil {
		port = DefaultDBPort // lookup unavailable — try the common default
	}
	c, err := Dial(ip, port, f.OurPlayer())
	if err != nil {
		log.Printf("dbclient: dial player %d (%s:%d): %v", player, ip, port, err)
		return nil
	}
	defer c.Close()
	m, err := c.FetchMetadata(slot, trackID)
	if err != nil {
		log.Printf("dbclient: metadata player %d track %d: %v", player, trackID, err)
		return nil
	}
	log.Printf("dbclient: external track %d from player %d = %q — %q", trackID, player, m.Title, m.Artist)
	return m
}

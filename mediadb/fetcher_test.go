// SPDX-License-Identifier: GPL-3.0-or-later

package mediadb

import (
	"net"
	"testing"
	"time"

	"github.com/vynulldev/vynull/pdb"
)

func testDB() *pdb.Database {
	db := &pdb.Database{}
	db.AddTrack(&pdb.Track{ID: 5, Title: "Strobe", Artist: "deadmau5", Key: "4A", Tempo: 12800})
	return db
}

func TestArtworkCache(t *testing.T) {
	f := NewFetcher(nil)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}

	// A positive cache entry is returned as-is.
	f.art[artKey(1, 3, 5)] = jpeg
	if got := f.Artwork(1, 3, 5); string(got) != string(jpeg) {
		t.Fatalf("Artwork = % x, want % x", got, jpeg)
	}

	// A negative cache entry (track has no art / prior fetch failed) returns nil
	// and does not start another fetch.
	f.art[artKey(1, 3, 6)] = nil
	if got := f.Artwork(1, 3, 6); got != nil {
		t.Fatalf("expected nil for negative-cached art, got % x", got)
	}
	f.mu.Lock()
	inflight := f.artInflight[artKey(1, 3, 6)]
	f.mu.Unlock()
	if inflight {
		t.Fatal("negative-cached art should not trigger a fetch")
	}
}

func TestGetCacheHit(t *testing.T) {
	f := NewFetcher(nil)
	f.cache[key(1, 3)] = &entry{db: testDB(), at: time.Now()}

	m := f.Get(1, 3, 5)
	if m == nil {
		t.Fatal("expected metadata for a cached track")
	}
	if m.Title != "Strobe" || m.Artist != "deadmau5" || m.Key != "4A" || m.BPM != 128 {
		t.Fatalf("got %+v", m)
	}

	// A track id not in that media's database resolves to nil.
	if got := f.Get(1, 3, 999); got != nil {
		t.Fatalf("expected nil for unknown track, got %+v", got)
	}
}

func TestSlotExport(t *testing.T) {
	cases := map[uint8]string{3: "/C/", 2: "/B/", 1: "", 0: "", 9: ""}
	for slot, want := range cases {
		if got := slotExport(slot); got != want {
			t.Errorf("slotExport(%d) = %q, want %q", slot, got, want)
		}
	}
}

func TestDoFetchSkipsCD(t *testing.T) {
	// A CD (slot 1) carries no rekordbox export; the fetch must short-circuit
	// before touching the network even with a resolvable IP.
	f := NewFetcher(func(uint8) net.IP { return net.IPv4(169, 254, 1, 2) })
	if db, ex := f.doFetch(1, slotCD); db != nil || ex != "" {
		t.Fatalf("doFetch for CD = (%v, %q), want (nil, \"\")", db, ex)
	}
}

func TestAnalysisCache(t *testing.T) {
	f := NewFetcher(nil)
	a := &Analysis{DurationMs: 180000, BPM: 128, WaveDetail: []byte{1, 2, 3, 4}}

	// A positive cache entry is returned as-is.
	f.anz[artKey(1, 3, 5)] = a
	if got := f.Analysis(1, 3, 5); got != a {
		t.Fatalf("Analysis = %+v, want cached entry", got)
	}

	// A negative cache entry returns nil and starts no fetch.
	f.anz[artKey(1, 3, 6)] = nil
	if got := f.Analysis(1, 3, 6); got != nil {
		t.Fatalf("expected nil for negative-cached analysis, got %+v", got)
	}
	f.mu.Lock()
	inflight := f.anzInflight[artKey(1, 3, 6)]
	f.mu.Unlock()
	if inflight {
		t.Fatal("negative-cached analysis should not trigger a fetch")
	}
}

func TestGetFailureCooldown(t *testing.T) {
	f := NewFetcher(nil) // ResolveIP nil -> any fetch fails
	// A recent failure is remembered: Get returns nil without spawning a fetch.
	f.cache[key(2, 2)] = &entry{db: nil, at: time.Now()}
	if got := f.Get(2, 2, 1); got != nil {
		t.Fatalf("expected nil during failure cooldown, got %+v", got)
	}
	f.mu.Lock()
	inflight := f.inflight[key(2, 2)]
	f.mu.Unlock()
	if inflight {
		t.Fatal("a fetch was started during the failure cooldown")
	}
}

func TestGetServesStaleWhileRefreshing(t *testing.T) {
	f := NewFetcher(nil)
	// A success older than successTTL is stale: still served, but a refresh is
	// triggered.
	f.cache[key(1, 3)] = &entry{db: testDB(), at: time.Now().Add(-2 * successTTL)}

	m := f.Get(1, 3, 5)
	if m == nil || m.Title != "Strobe" {
		t.Fatalf("stale copy should still be served, got %+v", m)
	}
	f.mu.Lock()
	inflight := f.inflight[key(1, 3)]
	f.mu.Unlock()
	if !inflight {
		t.Fatal("a refresh should have been triggered for the stale entry")
	}
}

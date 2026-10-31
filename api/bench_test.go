// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/dbserver"
	"github.com/vynulldev/vynull/internal/benchlib"
)

// BenchmarkSmartPlaylist measures resolving a smart playlist's tracks on a
// 20k-track library, which happens on every open and sort, for one rule of
// each kind and for a typical combination. Rules are decoded from JSON, as
// they arrive from the web UI or an import. "open" resolves it again with
// nothing changed; "changed" after every library change (a play count, a
// rating, an analysis result), which the next open must reflect.
func BenchmarkSmartPlaylist(b *testing.B) {
	// The store logs each playlist it creates, which would split the result
	// lines benchstat reads.
	defer log.SetOutput(log.Writer())
	log.SetOutput(io.Discard)
	lib := benchlib.Library(benchlib.Size)
	for _, bc := range []struct{ name, rules string }{
		{"numeric", `{"conditions":[{"field":"bpm","operator":"between","value":[120,130]}]}`},
		{"text", `{"conditions":[{"field":"genre","operator":"contains","value":"house"}]}`},
		{"key", `{"conditions":[{"field":"key","operator":"compatible","value":"Am"}]}`},
		{"date", `{"conditions":[{"field":"date_added","operator":"in_last_months","value":12}]}`},
		{"combined", `{"combinator":"all","conditions":[
			{"field":"bpm","operator":"between","value":[118,128]},
			{"field":"rating","operator":"gte","value":3},
			{"group":{"combinator":"any","conditions":[
				{"field":"genre","operator":"contains","value":"house"},
				{"field":"genre","operator":"contains","value":"techno"}]}},
			{"field":"date_added","operator":"in_last_months","value":24}]}`},
	} {
		var rules SmartRules
		if err := json.Unmarshal([]byte(bc.rules), &rules); err != nil {
			b.Fatal(err)
		}
		ps := NewPlaylistStore(b.TempDir())
		pl, err := ps.CreateSmart(bc.name, 0, &rules)
		if err != nil {
			b.Fatal(err)
		}
		if n := len(ps.TracksFor(pl.ID, lib, nil)); n == 0 || n == benchlib.Size {
			b.Fatalf("rules matched %d of %d tracks; the benchmark wants a selective rule", n, benchlib.Size)
		}
		for _, changed := range []bool{false, true} {
			b.Run(bc.name+map[bool]string{false: "/open", true: "/changed"}[changed], func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if changed {
						lib.Touch()
					}
					ps.TracksFor(pl.ID, lib, nil)
				}
			})
		}
	}
}

// BenchmarkAPITracks measures GET /api/tracks on a 20k-track library, gzip
// accepted as a browser does: a request for the current revision, one right
// after a change (the encode a revision costs), and the revalidation when the
// ETag still matches.
func BenchmarkAPITracks(b *testing.B) {
	lib := benchlib.Library(benchlib.Size)
	s := &Server{Library: lib}
	h := s.Handler()
	get := func(etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/tracks", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := get("")
	if first.Code != http.StatusOK {
		b.Fatalf("GET /api/tracks: %d", first.Code)
	}
	b.Run("full", func(b *testing.B) {
		b.SetBytes(int64(first.Body.Len()))
		b.ReportAllocs()
		for b.Loop() {
			get("")
		}
	})
	b.Run("after-change", func(b *testing.B) {
		b.SetBytes(int64(first.Body.Len()))
		b.ReportAllocs()
		for b.Loop() {
			lib.Touch()
			get("")
		}
	})
	b.Run("not-modified", func(b *testing.B) {
		etag := get("").Header().Get("ETag")
		if w := get(etag); w.Code != http.StatusNotModified {
			b.Fatalf("revalidation returned %d", w.Code)
		}
		b.ReportAllocs()
		for b.Loop() {
			get(etag)
		}
	})
}

// BenchmarkAPICues measures GET /api/cues, every cue in the library, which
// the web UI fetches each second. The library has about six cues per track
// (see benchlib.Cues), loaded the way the daemon loads them at startup.
func BenchmarkAPICues(b *testing.B) {
	dir := b.TempDir()
	for _, t := range benchlib.Tracks(benchlib.Size) {
		var cues []dbserver.CuePoint
		for _, c := range benchlib.Cues(t) {
			typ := uint16(1)
			if c.LoopMs >= 0 {
				typ = 2
			}
			cues = append(cues, dbserver.CuePoint{Number: c.Number, Type: typ, TimeMs: c.TimeMs,
				LoopMs: c.LoopMs, Status: 1, ColorID: c.ColorID})
		}
		if cues == nil {
			continue
		}
		data, err := json.Marshal(cues)
		if err != nil {
			b.Fatal(err)
		}
		// The cue store's file layout: one cues_<track ID>.json per track.
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("cues_%d.json", t.ID)), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	s := &Server{Cues: &CueStoreAdapter{Store: dbserver.NewCueStore(dir)}}
	h := s.Handler()
	get := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := get("/api/cues", "")
	if first.Code != http.StatusOK || first.Body.Len() < 1<<20 {
		b.Fatalf("GET /api/cues: %d, %d bytes", first.Code, first.Body.Len())
	}
	b.Run("full", func(b *testing.B) {
		b.SetBytes(int64(first.Body.Len()))
		b.ReportAllocs()
		for b.Loop() {
			get("/api/cues", "")
		}
	})
	// What the web UI now asks each second: the revision, and the cues only
	// when it moved, revalidated against their ETag.
	b.Run("not-modified", func(b *testing.B) {
		etag := first.Header().Get("ETag")
		if w := get("/api/cues", etag); w.Code != http.StatusNotModified {
			b.Fatalf("revalidation returned %d", w.Code)
		}
		b.ReportAllocs()
		for b.Loop() {
			get("/api/cues", etag)
		}
	})
	b.Run("rev", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			get("/api/cues/rev", "")
		}
	})
}

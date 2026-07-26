// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// TestAnalysisPipeline covers the fresh-library flow: tracks added over
// the API analyze in the background, rows fill
// with BPM/duration, detected BPM lands EXACTLY on the known ground-truth
// integer (the snap), and the waveform PNG endpoint serves an image.
func TestAnalysisPipeline(t *testing.T) {
	s := startServer(t, "")
	media := t.TempDir()
	p124 := kickWAV(t, media, "kick124.wav", 124)
	p128 := kickWAV(t, media, "kick128.wav", 128)
	// FLAC exercises the lossless decode path (no encoder-delay shift).
	p126 := flacKick(t, media, "kick126.flac", 126)
	s.addTracks(p124, p128, p126)

	want := map[string]float64{"kick124": 124, "kick128": 128, "kick126": 126}
	s.waitFor("all three tracks analyzed", 2*time.Minute, func() bool {
		n := 0
		for _, tr := range s.tracks() {
			if tr.BPM > 0 && tr.Duration > 0 {
				n++
			}
		}
		return n == 3
	})
	for _, tr := range s.tracks() {
		wantBPM, ok := want[tr.Title]
		if !ok {
			t.Errorf("unexpected title %q", tr.Title)
			continue
		}
		if tr.BPM != wantBPM {
			t.Errorf("%s: BPM %v, want exactly %v (integer snap)", tr.Title, tr.BPM, wantBPM)
		}
		if tr.Duration < 29 || tr.Duration > 31 {
			t.Errorf("%s: duration %v, want ~30s", tr.Title, tr.Duration)
		}

		resp, err := http.Get(fmt.Sprintf("%s/api/analysis/waveform-png/%d?type=detail&w=192&h=44", s.baseURL, tr.ID))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) < 8 || string(body[1:4]) != "PNG" {
			t.Errorf("%s: waveform-png status %d, %d bytes (want a PNG)", tr.Title, resp.StatusCode, len(body))
		}
	}
}

// TestArtworkLazyExtraction covers lazy artwork extraction: a track
// with embedded cover art serves it from /api/artwork on first request (no
// deck involved), the probe result persists as art_checked, and an artless
// track 404s and is marked checked so the UI stops re-requesting.
func TestArtworkLazyExtraction(t *testing.T) {
	s := startServer(t, "")
	// Separate directories: ExtractArtwork falls back to a directory scan
	// for cover images, so an artless file next to the MP3's cover.jpg
	// would legitimately inherit it (asserted below as folderart).
	withArt := mp3WithArt(t, t.TempDir(), 124)
	noArt := kickWAV(t, t.TempDir(), "noart.wav", 126)
	folderDir := t.TempDir()
	folderArt := kickWAV(t, folderDir, "folderart.wav", 122)
	writeCoverJPEG(t, filepath.Join(folderDir, "cover.jpg"))
	s.addTracks(withArt, noArt, folderArt)

	byTitle := func(title string) track {
		for _, tr := range s.tracks() {
			if tr.Title == title {
				return tr
			}
		}
		t.Fatalf("track %q not found", title)
		return track{}
	}

	// Request art for both — first request triggers the lazy probe.
	fetch := func(id uint32) (int, string, int) {
		resp, err := http.Get(fmt.Sprintf("%s/api/artwork/%d", s.baseURL, id))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Content-Type"), len(body)
	}

	art := byTitle("with-art")
	s.waitFor("artwork served for with-art", 30*time.Second, func() bool {
		code, _, n := fetch(art.ID)
		return code == http.StatusOK && n > 0
	})
	code, ctype, n := fetch(art.ID)
	if code != http.StatusOK || n == 0 {
		t.Errorf("with-art: artwork %d, %d bytes", code, n)
	}
	if ctype != "image/jpeg" {
		t.Errorf("with-art: content-type %q", ctype)
	}

	plain := byTitle("noart")
	fetch(plain.ID) // trigger the probe
	s.waitFor("noart probe recorded", 30*time.Second, func() bool {
		return byTitle("noart").ArtChecked
	})
	if tr := byTitle("noart"); tr.ArtID != 0 {
		t.Errorf("noart: art_id %d, want 0", tr.ArtID)
	}
	if code, _, _ := fetch(plain.ID); code != http.StatusNotFound {
		t.Errorf("noart: artwork status %d, want 404", code)
	}

	// Directory-scan fallback: an artless file with a cover.jpg beside it
	// serves the folder art.
	fa := byTitle("folderart")
	s.waitFor("folder art served", 30*time.Second, func() bool {
		code, _, n := fetch(fa.ID)
		return code == http.StatusOK && n > 0
	})
}

// TestBulkAddResponsive is a light stability pass: a batch of
// tracks analyzes to completion while the API stays responsive throughout.
func TestBulkAddResponsive(t *testing.T) {
	s := startServer(t, "")
	media := t.TempDir()
	var paths []string
	for i := 0; i < 12; i++ {
		paths = append(paths, kickWAV(t, media, fmt.Sprintf("bulk%02d.wav", i), float64(120+i)))
	}
	s.addTracks(paths...)

	s.waitFor("all 12 analyzed", 5*time.Minute, func() bool {
		// The poll itself is the responsiveness check: a wedged server
		// fails here, not silently.
		start := time.Now()
		ts := s.tracks()
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("GET /api/tracks took %v mid-analysis", d)
		}
		n := 0
		for _, tr := range ts {
			if tr.BPM > 0 {
				n++
			}
		}
		return n == len(paths)
	})
	for _, tr := range s.tracks() {
		if tr.BPM != float64(int(tr.BPM)) {
			t.Errorf("%s: fractional BPM %v survived on a synthetic integer tempo", tr.Title, tr.BPM)
		}
	}
}

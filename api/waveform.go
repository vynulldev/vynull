// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/link/prolink"
)

// handleWaveform returns waveform data for a track.
// GET /api/analysis/waveform/{trackID}?type=detail|preview|color_preview&style=spectral|3band
//
// For type=detail, the optional style parameter selects the encoding:
//   - "spectral" (default): per-entry r/g/b/h from PWV5 — rekordbox's stored colors
//     where r/g/b indicate which band dominates and h is overall amplitude
//   - "3band": per-band bass/mid/high amplitudes, each normalized to that band's
//     global peak across the track. For CDJ/Rekordbox-style additive 3-band rendering
//     where each band drives its own bar height
func (s *Server) handleWaveform(w http.ResponseWriter, r *http.Request) {
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/waveform/")
	if trackID == 0 {
		jsonError(w, "track ID required", http.StatusBadRequest)
		return
	}

	result := s.getOrAnalyze(r.Context(), trackID)
	if result == nil {
		jsonError(w, "analysis not found", http.StatusNotFound)
		return
	}

	waveType := r.URL.Query().Get("type")
	if waveType == "" {
		waveType = "detail"
	}
	style := r.URL.Query().Get("style")
	if style == "" {
		style = "spectral"
	}

	// HTTP cache: ETag derived from the underlying analysis bytes + cacheVersion
	// + waveType lets the browser revalidate cheaply via If-None-Match. A
	// cacheVersion bump → different ETag → fresh fetch. Otherwise the browser
	// reuses its cached response (no JSON parsing on our side either).
	var sourceBytes []byte
	switch waveType {
	case "detail":
		if style == "3band" {
			sourceBytes = result.WaveDetail3Band
		} else {
			sourceBytes = result.WaveDetail
		}
	case "preview":
		sourceBytes = prolink.DeckPreview(result)
	case "color_preview":
		sourceBytes = result.WaveColorPreview
	}
	if len(sourceBytes) > 0 {
		sum := sha256.Sum256(sourceBytes)
		etag := fmt.Sprintf(`"v%d-%s-%s-%s"`, result.CacheVersion, waveType, style, hex.EncodeToString(sum[:8]))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400, must-revalidate")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	switch waveType {
	case "detail":
		if style == "3band" {
			// 3-band format: 3 bytes per entry (bass, mid, treble; 0-255 each),
			// per-band global-normalized for additive 3-band rendering.
			data := result.WaveDetail3Band
			n := len(data) / 3
			type Entry struct {
				Bass int `json:"bass"` // 0-255
				Mid  int `json:"mid"`  // 0-255
				High int `json:"high"` // 0-255
			}
			entries := make([]Entry, n)
			for i := 0; i < n; i++ {
				entries[i] = Entry{
					Bass: int(data[i*3]),
					Mid:  int(data[i*3+1]),
					High: int(data[i*3+2]),
				}
			}
			writeJSON(w, struct {
				TrackID    uint32  `json:"track_id"`
				Type       string  `json:"type"`
				Style      string  `json:"style"`
				SampleRate int     `json:"sample_rate"`
				Entries    []Entry `json:"entries"`
			}{trackID, "detail", "3band", 150, entries})
			return
		}

		// Default "spectral" style — PWV5: 2 bytes per entry → JSON {r,g,b,h}.
		writeJSON(w, struct {
			TrackID    uint32              `json:"track_id"`
			Type       string              `json:"type"`
			Style      string              `json:"style"`
			SampleRate int                 `json:"sample_rate"` // entries per second
			Entries    []WaveEntrySpectral `json:"entries"`
		}{trackID, "detail", "spectral", 150, decodePWV5(result.WaveDetail)})

	case "preview":
		// The 904-byte 0x2004 preview the decks get.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(prolink.DeckPreview(result))

	case "color_preview":
		// PWV4: 6 bytes per entry, decode to JSON.
		data := result.WaveColorPreview
		n := len(data) / 6
		type Entry struct {
			Bass   int `json:"bass"`   // 0-255
			Mid    int `json:"mid"`    // 0-255
			Treble int `json:"treble"` // 0-255
		}
		entries := make([]Entry, n)
		for i := 0; i < n; i++ {
			entries[i] = Entry{
				Bass:   int(data[i*6+3]),
				Mid:    int(data[i*6+4]),
				Treble: int(data[i*6+5]),
			}
		}
		writeJSON(w, struct {
			TrackID uint32  `json:"track_id"`
			Type    string  `json:"type"`
			Entries []Entry `json:"entries"`
		}{trackID, "color_preview", entries})

	default:
		jsonError(w, "type must be 'detail', 'preview', or 'color_preview'", http.StatusBadRequest)
	}
}

// WaveEntrySpectral is one PWV5 colour-detail waveform entry (r/g/b 0-7, h 0-31).
type WaveEntrySpectral struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
	H int `json:"h"`
}

// decodePWV5 decodes a raw PWV5 colour-detail waveform (2 bytes/entry, 16-bit
// big-endian RRRGGGBBBHHHHH00) into per-entry r/g/b/height values.
func decodePWV5(data []byte) []WaveEntrySpectral {
	n := len(data) / 2
	out := make([]WaveEntrySpectral, n)
	for i := 0; i < n; i++ {
		word := uint16(data[i*2])<<8 | uint16(data[i*2+1])
		out[i] = WaveEntrySpectral{
			R: int((word >> 13) & 7),
			G: int((word >> 10) & 7),
			B: int((word >> 7) & 7),
			H: int((word >> 2) & 0x1f),
		}
	}
	return out
}

// handleWaveformPNG returns a pre-rendered waveform image, disk-cached
// across server restarts. The browser also gets a long Cache-Control +
// ETag so client reloads come from local browser cache too.
//
// GET /api/analysis/waveform-png/{trackID}?type=detail|color_preview&w=280&h=56
//
// Default type=detail (PWV5) since that's what the web UI uses for thumbnails.
// Width/height are clamped to sane bounds; the cache key includes them so
// requests at different sizes coexist.
func (s *Server) handleWaveformPNG(w http.ResponseWriter, r *http.Request) {
	trackID := parseTrackIDFromPath(r.URL.Path, "/api/analysis/waveform-png/")
	if trackID == 0 {
		jsonError(w, "track ID required", http.StatusBadRequest)
		return
	}
	waveType := r.URL.Query().Get("type")
	if waveType == "" {
		waveType = "detail"
	}
	if waveType != "detail" && waveType != "color_preview" {
		jsonError(w, "type must be detail or color_preview", http.StatusBadRequest)
		return
	}
	// Colour mode mirrors the CDJ "waveform color" DEVSETTING: "rgb" (the
	// spectral PWV5 colours, default), "blue" (monochrome blue, treble-tinted
	// caps), or "3band" (CDJ-3000 additive bass/mid/treble). Only meaningful
	// for the "detail" type.
	colorMode := r.URL.Query().Get("color")
	if colorMode != "blue" && colorMode != "3band" {
		colorMode = "rgb"
	}
	// Overview height mirrors the CDJ "overview" DEVSETTING: "full" (default)
	// or "half" (waveform drawn at half height).
	overview := r.URL.Query().Get("overview")
	if overview != "half" {
		overview = "full"
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	height, _ := strconv.Atoi(r.URL.Query().Get("h"))
	if width <= 0 {
		width = 280
	}
	if height <= 0 {
		height = 56
	}
	if width > 2400 {
		width = 2400
	}
	if height > 240 {
		height = 240
	}

	// Non-blocking lookup. We must NOT call getOrAnalyze here: a fresh
	// cacheVersion bump invalidates every track's disk cache, and the
	// library page's <img loading="lazy"> tags can fan out to dozens of
	// concurrent requests on layout — each AnalyzeTrack decodes the full
	// PCM into memory and pegs CPU on FFT/beat detection, so running them
	// inline on per-request goroutines starves the host (with Swap: 0
	// it's enough to lock up the desktop). Instead, queue the work onto
	// the bounded worker pool and return a placeholder PNG. Once the
	// library row's BPM/Duration/Key change from the worker's writeback,
	// /api/tracks differs and the front-end re-renders the row, which
	// re-fetches this URL and gets the real PNG.
	//
	// Set the file path first so Get can find the disk cache — without
	// this, every page reload sees pathMap empty, Get returns nil for
	// every track, and tryQueueAnalysis runs a full fresh AnalyzeTrack
	// even when the .gob is sitting on disk.
	var filePath string
	if s.Library != nil {
		if t := s.Library.Track(trackID); t != nil && t.FilePath != "" {
			filePath = t.FilePath
			s.Analysis.SetPath(trackID, filePath)
		}
	}
	result := s.Analysis.Get(trackID)
	// Heal the library row from the result (noop once filled): thumbnails are
	// often the first thing to touch a re-added track's surviving cache entry,
	// and the row's BPM/duration must follow for the rest of the UI to work.
	s.applyAnalysisToTrack(trackID, result)
	if result == nil {
		s.tryQueueAnalysis(trackID, "")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(placeholderWaveformPNG)
		return
	}
	cacheKey := waveformCacheKey(trackID, filePath)
	variant := fmt.Sprintf("r%d_%s_%s_%s", waveformPNGRenderVersion, waveType, colorMode, overview)
	cachePath := s.waveformPNGPath(cacheKey, variant, width, height, result.CacheVersion)
	etag := fmt.Sprintf(`"png-r%d-v%d-%s-%s-%s-%s-%dx%d"`, waveformPNGRenderVersion, result.CacheVersion, waveType, colorMode, overview, cacheKey, width, height)

	// Browser ETag revalidation.
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=604800, must-revalidate") // 7 days
	w.Header().Set("Content-Type", "image/png")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Serve from disk if present (created on a prior request).
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			w.Write(data)
			return
		}
	}

	// Cache miss → render now.
	imgBytes, err := renderWaveformPNG(result, waveType, colorMode, overview, width, height)
	if err != nil {
		jsonError(w, "render failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Best-effort write to disk for next time. Write-to-temp + rename so a
	// concurrent request reading cachePath can never observe a partial file:
	// lazy-loading <img> tags fan out many thumbnail requests at once, and a
	// truncated PNG served here would be cached by the browser for a week.
	if cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		tmp := fmt.Sprintf("%s.tmp%d", cachePath, os.Getpid())
		if err := os.WriteFile(tmp, imgBytes, 0o644); err == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	w.Write(imgBytes)
}

// waveformCacheKey identifies a track's waveform by a hash of its file path
// (matching the analysis cache), NOT its track ID. Track IDs are positional —
// deleting a track renumbers the rest on the next load, so an ID-keyed cache
// would serve the deleted track's waveform to whoever inherits its ID. The
// waveform belongs to the audio file, so key it by path. Empty path (a
// missing-file track) falls back to the ID — those have no waveform anyway.
func waveformCacheKey(trackID uint32, filePath string) string {
	if filePath != "" {
		h := sha256.Sum256([]byte(filePath))
		return hex.EncodeToString(h[:8])
	}
	return fmt.Sprintf("id%d", trackID)
}

func (s *Server) waveformPNGPath(key, waveType string, width, height, cacheVersion int) string {
	if s.CacheDir == "" {
		return ""
	}
	shard := key[len(key)-2:]
	name := fmt.Sprintf("v%d_%s_%s_%dx%d.png", cacheVersion, key, waveType, width, height)
	return filepath.Join(s.CacheDir, "waveform-png", shard, name)
}

// placeholderWaveformPNG is a tiny 1×1 dark PNG served while a track's
// analysis is still in flight. The browser stretches it to the <img>'s
// width/height attributes so layout doesn't shift; Cache-Control:no-store
// on the response means the next <img> element created for the same URL
// re-fetches and gets the real waveform once analysis completes.
var placeholderWaveformPNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 3, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}()

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// blueColor maps PWV5 per-bucket band weights to the CDJ "blue" waveform:
// a medium blue body that whitens toward the caps where treble (b) dominates.
// mr/mg/mb are summed 0-7 weights over the bucket; only their ratio matters.
func blueColor(mr, mg, mb int) color.RGBA {
	sum := float64(mr + mg + mb)
	tf := 0.0
	if sum > 0 {
		tf = float64(mb) / sum // treble fraction → whitening amount
	}
	k := tf * 0.85
	return color.RGBA{
		R: clamp8(40 + (255-40)*k),
		G: clamp8(120 + (255-120)*k),
		B: clamp8(235 + (255-235)*k),
		A: 255,
	}
}

// waveformPNGRenderVersion invalidates disk-cached thumbnails + browser ETags
// when the PNG rendering changes (independently of the analysis CacheVersion).
// Bump on any renderWaveformPNG change. 2: 3-band drawn as a stacked half wave.
const waveformPNGRenderVersion = 2

// renderWaveformPNG draws a waveform to an RGBA image and PNG-encodes it.
// PWV5 (detail): per-entry r/g/b weights ∈ [0,7] + h ∈ [0,31]. Subsamples
// to canvas width, max-h per bucket, averaged colour weights — matches the
// drawWaveformPWV5 routine in the web UI's js/waveforms.js.
// PWV4 (color_preview): per-entry bass/mid/treble bytes 0-255, normalised
// to track peak.
func renderWaveformPNG(result *analysis.Result, waveType, colorMode, overview string, width, height int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Solid near-black background so the PNG looks the same as the canvas.
	bg := color.RGBA{R: 3, G: 2, B: 3, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	clampBar := func(barH int) int {
		if barH < 1 {
			barH = 1
		}
		return barH
	}
	// "half" overview shows only the lower half of the waveform, anchored to
	// the bottom of the box (same amplitude, not squished); "full" centres it.
	// Matches barTop in web/js/waveform-thumbs.js.
	barTop := func(barH int) int {
		if overview == "half" {
			return height - barH
		}
		return (height - barH) / 2
	}
	// entryRange maps pixel column x to the [start,end) detail-entry range on
	// the waveform's true time base (entry i = i/150 s), scaled to the track
	// duration so features align with the time-positioned cue overlay instead
	// of drifting toward the end. Falls back to a proportional index split when
	// the duration is unknown. Only meaningful for the fixed-rate detail series.
	const detailRate = 150.0
	durMs := float64(result.Duration) * 1000
	entryRange := func(x, n int) (int, int) {
		var s, e int
		if durMs <= 0 {
			s = int(float64(x) * float64(n) / float64(width))
			e = int(float64(x+1) * float64(n) / float64(width))
		} else {
			s = int(float64(x) / float64(width) * durMs * detailRate / 1000)
			e = int(float64(x+1) / float64(width) * durMs * detailRate / 1000)
		}
		if e <= s {
			e = s + 1
		}
		if s > n {
			s = n
		}
		if e > n {
			e = n
		}
		return s, e
	}

	// 3-band (CDJ-3000): 3 bytes/entry (bass,mid,treble; 0-255), absolute
	// amplitudes. Prefer PWV6 (the 1200-entry whole-track overview rekordbox uses
	// for its top minimap), falling back to PWV7 detail then PWV5. PWV6 is a
	// FIXED-count overview, not 150/sec, so it must be index-mapped across the
	// width — using entryRange's detail-rate mapping would cram it into the first
	// few percent of the thumbnail.
	band3, overview3 := result.WavePreview3Band, true
	if len(band3) < 3 {
		band3, overview3 = result.WaveDetail3Band, false
	}
	if waveType == "detail" && colorMode == "3band" && len(band3) >= 3 {
		data := band3
		n := len(data) / 3
		// The 3-band PREVIEW is a bottom-anchored VERTICAL STACK (rekordbox
		// convention, matching drawStacked3Band in web/js/waveform-thumbs.js): blue bass on the
		// bottom, orange mid stacked above, white treble on top — each segment's
		// height is that band's amplitude, not overlaid bars. Pass 1: per-column
		// per-band peak.
		bassC := make([]int, width)
		midC := make([]int, width)
		trebC := make([]int, width)
		for x := 0; x < width; x++ {
			var start, end int
			if overview3 {
				start = x * n / width
				end = (x + 1) * n / width
				if end <= start {
					end = start + 1
				}
				if end > n {
					end = n
				}
			} else {
				start, end = entryRange(x, n)
			}
			var mb, mm, mt int
			for i := start; i < end; i++ {
				if b := int(data[i*3]); b > mb {
					mb = b
				}
				if m := int(data[i*3+1]); m > mm {
					mm = m
				}
				if t := int(data[i*3+2]); t > mt {
					mt = t
				}
			}
			bassC[x], midC[x], trebC[x] = mb, mm, mt
		}
		// Light column-space smoothing (rekordbox's preview is very smooth).
		smoothCol := func(a []int) {
			src := append([]int(nil), a...)
			for x := range a {
				sum, cnt := 0, 0
				for j := x - 1; j <= x+1; j++ {
					if j >= 0 && j < len(a) {
						sum += src[j]
						cnt++
					}
				}
				a[x] = sum / cnt
			}
		}
		smoothCol(bassC)
		smoothCol(midC)
		smoothCol(trebC)
		// Pass 2: stack bottom-up, normalised so the loudest column fills the box.
		// Full vivid colours (segments don't overlap). Always bottom-anchored —
		// the 3-band preview is a half waveform regardless of the overview setting.
		maxTot := 1
		for x := 0; x < width; x++ {
			if t := bassC[x] + midC[x] + trebC[x]; t > maxTot {
				maxTot = t
			}
		}
		stackCols := [3]color.RGBA{{R: 30, G: 120, B: 255, A: 255}, {R: 255, G: 170, B: 30, A: 255}, {R: 235, G: 245, B: 255, A: 255}}
		bands := [3][]int{bassC, midC, trebC}
		for x := 0; x < width; x++ {
			y := height
			for bi := 0; bi < 3; bi++ {
				seg := (bands[bi][x] * height) / maxTot
				for k := 0; k < seg; k++ {
					y--
					if y >= 0 {
						img.SetRGBA(x, y, stackCols[bi])
					}
				}
			}
		}
	} else if waveType == "detail" {
		data := result.WaveDetail
		n := len(data) / 2
		if n > 0 {
			for x := 0; x < width; x++ {
				start, end := entryRange(x, n)
				var mh, mr, mg, mb int
				count := 0
				for i := start; i < end; i++ {
					word := uint16(data[i*2])<<8 | uint16(data[i*2+1])
					r := int((word >> 13) & 7)
					g := int((word >> 10) & 7)
					b := int((word >> 7) & 7)
					h := int((word >> 2) & 0x1f)
					if h > mh {
						mh = h
					}
					mr += r
					mg += g
					mb += b
					count++
				}
				if mh == 0 || count == 0 {
					continue
				}
				var col color.RGBA
				if colorMode == "blue" {
					col = blueColor(mr, mg, mb)
				} else {
					col = color.RGBA{
						R: uint8(min(255, (mr*38)/count)),
						G: uint8(min(255, (mg*38)/count)),
						B: uint8(min(255, (mb*38)/count)),
						A: 255,
					}
				}
				barH := clampBar((mh * height) / 31)
				top := barTop(barH)
				for y := top; y < top+barH; y++ {
					img.SetRGBA(x, y, col)
				}
			}
		}
	} else {
		// color_preview (PWV4)
		data := result.WaveColorPreview
		n := len(data) / 6
		if n > 0 {
			var maxV int = 1
			for i := 0; i < n; i++ {
				peak := int(data[i*6+3])
				if int(data[i*6+4]) > peak {
					peak = int(data[i*6+4])
				}
				if int(data[i*6+5]) > peak {
					peak = int(data[i*6+5])
				}
				if peak > maxV {
					maxV = peak
				}
			}
			step := float64(n) / float64(width)
			for x := 0; x < width; x++ {
				idx := int(float64(x) * step)
				if idx >= n {
					idx = n - 1
				}
				b := int(data[idx*6+3])
				m := int(data[idx*6+4])
				t := int(data[idx*6+5])
				peak := b
				if m > peak {
					peak = m
				}
				if t > peak {
					peak = t
				}
				if peak == 0 {
					continue
				}
				ar := uint8(min(255, b*3/2))
				ag := uint8(min(255, m*3/2))
				ab := uint8(min(255, t*3/2))
				barH := clampBar((peak * height) / maxV)
				top := barTop(barH)
				col := color.RGBA{R: ar, G: ag, B: ab, A: 255}
				for y := top; y < top+barH; y++ {
					img.SetRGBA(x, y, col)
				}
			}
		}
	}

	var buf strings.Builder
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	bw := &writerToBuilder{b: &buf}
	if err := enc.Encode(bw, img); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// writerToBuilder is a tiny io.Writer over a strings.Builder so we can
// PNG-encode without allocating a separate bytes.Buffer.
type writerToBuilder struct{ b *strings.Builder }

func (w *writerToBuilder) Write(p []byte) (int, error) {
	w.b.Write(p)
	return len(p), nil
}

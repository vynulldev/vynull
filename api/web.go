// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/index.html
var webIndexHTML []byte

//go:embed web/overlay.html
var webOverlayHTML []byte

//go:embed web/favicon.svg
var webFaviconSVG []byte

// webFontsFS holds the self-hosted woff2 fonts (Geist Mono + VT323), served at
// /fonts/. Bundled into the binary so the UI needs no external font CDN —
// important since it usually runs offline on the CDJ link-local network.
//
//go:embed web/fonts/*.woff2 web/fonts/OFL.txt
var webFontsFS embed.FS

// RegisterWebUI adds GET / serving the bundled single-page HTML UI on
// the given mux, plus /fonts/ for the embedded fonts. Off by default; opt in
// via the --web CLI flag in main. The page polls the existing JSON endpoints
// (/api/players, /api/tracks, /api/settings) every second for live updates.
func RegisterWebUI(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(webIndexHTML)
	})

	// Now-playing overlay for streaming (OBS browser source, transparent bg).
	mux.HandleFunc("/overlay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(webOverlayHTML)
	})

	// App icon (vinyl + the null ∅), used as the browser-tab favicon.
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(webFaviconSVG)
	})

	fonts, _ := fs.Sub(webFontsFS, "web/fonts")
	mux.HandleFunc("/fonts/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/fonts/")
		data, err := fs.ReadFile(fonts, name) // invalid/.. paths fail fs.ValidPath
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := "font/woff2"
		if strings.HasSuffix(name, ".txt") {
			ct = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(data)
	})
}

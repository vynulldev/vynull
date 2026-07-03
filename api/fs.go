// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fsEntry is one item the file browser can show: a directory to navigate into,
// or a supported audio file.
type fsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Audio bool   `json:"audio"`
}

// fsBrowseRoots returns the directories the file browser may start from and
// must stay within. This keeps the browser (which is reachable by anyone who
// can reach the unauthenticated API) from exposing the whole server
// filesystem, while still covering where music usually lives. Typing a path
// into the add-tracks field still works for anything outside these.
func (s *Server) fsBrowseRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		// Resolve symlinks so confinement (which resolves the requested path)
		// compares like-for-like.
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() && !seen[abs] {
			seen[abs] = true
			roots = append(roots, abs)
		}
	}
	add(s.MusicDir)
	for _, r := range s.BrowseRoots { // operator-configured extra roots
		add(r)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	for _, m := range []string{"/media", "/mnt", "/run/media"} {
		add(m)
	}
	return roots
}

// withinRoots reports whether resolved is one of, or nested under, a root.
func withinRoots(resolved string, roots []string) bool {
	for _, r := range roots {
		if resolved == r || strings.HasPrefix(resolved, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// handleFSList backs the "add files/folders" directory browser.
//
//	GET /api/fs/list            -> the browse roots
//	GET /api/fs/list?path=<dir> -> that dir's subfolders + supported audio files
//
// Requests are confined to fsBrowseRoots(); anything outside is rejected.
func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	roots := s.fsBrowseRoots()
	q := strings.TrimSpace(r.URL.Query().Get("path"))

	// No path: hand back the roots themselves as the starting points.
	if q == "" {
		entries := make([]fsEntry, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, fsEntry{Name: root, Path: root, IsDir: true})
		}
		writeJSON(w, map[string]interface{}{"path": "", "parent": "", "roots": true, "entries": entries})
		return
	}

	abs, err := filepath.Abs(q)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	// Resolve symlinks before the confinement check so a symlink can't escape
	// a browse root. Fall back to the cleaned abs path if it doesn't resolve.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = filepath.Clean(abs)
	}
	if !withinRoots(resolved, roots) {
		http.Error(w, "path is outside the allowed browse roots", http.StatusForbidden)
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	entries := make([]fsEntry, 0, len(infos))
	for _, fi := range infos {
		name := fi.Name()
		if strings.HasPrefix(name, ".") {
			continue // hide dotfiles/dotdirs
		}
		isDir := fi.IsDir()
		audio := !isDir && audioExts[strings.ToLower(filepath.Ext(name))]
		if !isDir && !audio {
			continue // only directories and supported audio files
		}
		entries = append(entries, fsEntry{
			Name:  name,
			Path:  filepath.Join(resolved, name),
			IsDir: isDir,
			Audio: audio,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // directories first
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	// Offer a parent only while it stays within a browse root.
	parent := filepath.Dir(resolved)
	if parent == resolved || !withinRoots(parent, roots) {
		parent = ""
	}
	writeJSON(w, map[string]interface{}{"path": resolved, "parent": parent, "entries": entries})
}

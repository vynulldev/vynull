// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleFSList(t *testing.T) {
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	mustWrite(t, filepath.Join(dir, "Album"), "") // dir marker handled below
	os.MkdirAll(filepath.Join(dir, "Album"), 0o755)
	mustWrite(t, filepath.Join(dir, "Album", "a.flac"), "x")
	mustWrite(t, filepath.Join(dir, "b.mp3"), "x")
	mustWrite(t, filepath.Join(dir, "notes.txt"), "x")
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)

	s := &Server{MusicDir: dir}

	list := func(path string) (int, map[string]any) {
		u := "/api/fs/list"
		if path != "" {
			u += "?path=" + url.QueryEscape(path)
		}
		req := httptest.NewRequest("GET", u, nil)
		w := httptest.NewRecorder()
		s.handleFSList(w, req)
		var body map[string]any
		json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	// Roots: MusicDir should be one of them.
	code, body := list("")
	if code != 200 {
		t.Fatalf("roots: code %d", code)
	}
	roots, _ := body["entries"].([]any)
	foundRoot := false
	for _, e := range roots {
		if m, ok := e.(map[string]any); ok && m["path"] == dir {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Errorf("MusicDir %q not among browse roots: %v", dir, roots)
	}

	// Listing the dir: Album (dir) + b.mp3 (audio); NOT notes.txt or .hidden.
	code, body = list(dir)
	if code != 200 {
		t.Fatalf("list: code %d", code)
	}
	names := map[string]bool{}
	for _, e := range body["entries"].([]any) {
		m := e.(map[string]any)
		names[m["name"].(string)] = m["is_dir"].(bool)
	}
	if d, ok := names["Album"]; !ok || !d {
		t.Errorf("Album dir missing/not-dir: %v", names)
	}
	if a, ok := names["b.mp3"]; !ok || a {
		t.Errorf("b.mp3 missing/marked-dir: %v", names)
	}
	if _, ok := names["notes.txt"]; ok {
		t.Errorf("notes.txt should be filtered out: %v", names)
	}
	if _, ok := names[".hidden"]; ok {
		t.Errorf(".hidden should be filtered out: %v", names)
	}

	// Confinement: a path outside all browse roots is rejected.
	code, _ = list("/etc")
	if code != 403 {
		t.Errorf("/etc: expected 403, got %d", code)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if content == "" {
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestImporterRegistry(t *testing.T) {
	cases := []struct {
		path     string
		label    string
		needsKey bool
	}{
		{"/music/collection.nml", "Traktor NML", false},
		{"/music/rekordbox.xml", "rekordbox XML", false},
		{"/music/master.db", "rekordbox master.db", true},
		{"/music/rekordbox_bak.zip", "rekordbox backup", true},
		{"/music/RB.XML", "rekordbox XML", false}, // extension match is case-insensitive
	}
	for _, c := range cases {
		imp := ImporterFor(c.path)
		if imp == nil {
			t.Errorf("%s: no importer", c.path)
			continue
		}
		if imp.Label() != c.label {
			t.Errorf("%s: label %q, want %q", c.path, imp.Label(), c.label)
		}
		if imp.RequiresKey(c.path) != c.needsKey {
			t.Errorf("%s: RequiresKey=%v, want %v", c.path, imp.RequiresKey(c.path), c.needsKey)
		}
	}
	if imp := ImporterFor("/music/song.mp3"); imp != nil {
		t.Errorf("mp3 should have no importer, got %q", imp.Label())
	}
}

// writeTestZip creates a zip at path whose entries are name→content.
func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRekordboxBackup(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "backup.zip")
	writeTestZip(t, zipPath, map[string]string{
		"master.db":                          "db",
		"share/PIONEER/USBANLZ/ANLZ0000.DAT": "anlz",
		"share/PIONEER/Artwork/art.jpg":      "art",
		"MYSETTING.DAT":                      "set",
		"masterPlaylists6.xml":               "junk", // not one of the kept prefixes
	})

	dest := filepath.Join(dir, "out")
	dbPath, shareRoot, settingsDir, err := extractRekordboxBackup(zipPath, dest, true)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if dbPath != filepath.Join(dest, "master.db") {
		t.Errorf("dbPath=%q", dbPath)
	}
	if shareRoot != filepath.Join(dest, "share") {
		t.Errorf("shareRoot=%q", shareRoot)
	}
	if settingsDir != dest {
		t.Errorf("settingsDir=%q, want %q", settingsDir, dest)
	}
	// Kept entries exist; the junk entry was skipped.
	for _, rel := range []string{"master.db", "share/PIONEER/USBANLZ/ANLZ0000.DAT", "MYSETTING.DAT"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %s extracted: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "masterPlaylists6.xml")); !os.IsNotExist(err) {
		t.Errorf("junk entry should have been skipped")
	}

	// wantSettings=false leaves settingsDir empty and skips the *SETTING.DAT.
	dest2 := filepath.Join(dir, "out2")
	_, _, settingsDir2, err := extractRekordboxBackup(zipPath, dest2, false)
	if err != nil {
		t.Fatalf("extract2: %v", err)
	}
	if settingsDir2 != "" {
		t.Errorf("settingsDir2=%q, want empty", settingsDir2)
	}
	if _, err := os.Stat(filepath.Join(dest2, "MYSETTING.DAT")); !os.IsNotExist(err) {
		t.Errorf("MYSETTING.DAT should not be extracted when wantSettings=false")
	}
}

func TestExtractRekordboxBackupNoDB(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "nodb.zip")
	writeTestZip(t, zipPath, map[string]string{"share/PIONEER/USBANLZ/x.DAT": "a"})
	if _, _, _, err := extractRekordboxBackup(zipPath, filepath.Join(dir, "out"), true); err == nil {
		t.Error("expected error when master.db is absent")
	}
}

// The backup importer with WantTracks=false extracts assets/settings without
// touching the (SQLCipher, python-only) db, and Cleanup releases the temp dir.
func TestBackupImporterSettingsOnly(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "backup.zip")
	writeTestZip(t, zipPath, map[string]string{
		"master.db":                          "db",
		"share/PIONEER/USBANLZ/ANLZ0000.DAT": "anlz",
		"MYSETTING.DAT":                      "set",
	})

	b, err := rekordboxBackupImporter{}.Import(nil, ImportOptions{
		Path: zipPath, WantTracks: false, WantSettings: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if b.Result != nil {
		t.Errorf("WantTracks=false should leave Result nil, got %+v", b.Result)
	}
	if filepath.Base(b.ShareRoot) != "share" || b.SettingsDir == "" {
		t.Errorf("ShareRoot=%q SettingsDir=%q", b.ShareRoot, b.SettingsDir)
	}
	if b.Cleanup == nil {
		t.Fatal("expected a Cleanup func")
	}
	if _, err := os.Stat(b.ShareRoot); err != nil {
		t.Errorf("share root should exist before cleanup: %v", err)
	}
	b.Cleanup()
	if _, err := os.Stat(b.ShareRoot); !os.IsNotExist(err) {
		t.Errorf("Cleanup should have removed the temp dir")
	}
}

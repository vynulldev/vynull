// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The rekordbox importers wrap the format-specific parse functions
// (ImportRekordboxXML / ImportRekordboxMasterDB) with the Importer interface and
// the shared asset/settings location logic. XML carries only tracks; the
// master.db and backup-zip forms also surface the analysis, artwork, and
// player/mixer settings that sit beside (or inside) them.

// --- rekordbox XML ---

type rekordboxXMLImporter struct{}

func (rekordboxXMLImporter) Label() string           { return "rekordbox XML" }
func (rekordboxXMLImporter) Handles(p string) bool   { return hasExt(p, ".xml") }
func (rekordboxXMLImporter) RequiresKey(string) bool { return false }

func (rekordboxXMLImporter) Import(lib *Library, o ImportOptions) (*ImportBundle, error) {
	if !o.WantTracks {
		return &ImportBundle{}, nil
	}
	return ImportRekordboxXML(lib, o.Path)
}

// --- rekordbox master.db ---

type rekordboxDBImporter struct{}

func (rekordboxDBImporter) Label() string           { return "rekordbox master.db" }
func (rekordboxDBImporter) Handles(p string) bool   { return hasExt(p, ".db") }
func (rekordboxDBImporter) RequiresKey(string) bool { return true }

func (rekordboxDBImporter) Import(lib *Library, o ImportOptions) (*ImportBundle, error) {
	b := &ImportBundle{}
	if o.WantTracks {
		var err error
		b, err = ImportRekordboxMasterDB(lib, o.Path, o.Key)
		if err != nil {
			return nil, err
		}
	}
	// A master.db still in its rekordbox library folder has its analysis
	// (share/PIONEER/USBANLZ), artwork (share/PIONEER/Artwork), and *SETTING.DAT
	// blobs right beside it — the same layout a backup zip mirrors — so surface
	// them for the asset/settings import, making a bare-.db import nearly as
	// complete as a .zip. (A db copied out on its own has no neighbours, so these
	// stay empty and nothing extra is imported.)
	dbDir := filepath.Dir(o.Path)
	if fi, e := os.Stat(filepath.Join(dbDir, "share")); e == nil && fi.IsDir() {
		b.ShareRoot = filepath.Join(dbDir, "share")
	}
	if o.WantSettings {
		b.SettingsDir = findRekordboxSettingsDir(dbDir)
	}
	return b, nil
}

// --- rekordbox library backup (.zip) ---

type rekordboxBackupImporter struct{}

func (rekordboxBackupImporter) Label() string           { return "rekordbox backup" }
func (rekordboxBackupImporter) Handles(p string) bool   { return hasExt(p, ".zip") }
func (rekordboxBackupImporter) RequiresKey(string) bool { return true }

func (rekordboxBackupImporter) Import(lib *Library, o ImportOptions) (*ImportBundle, error) {
	// A rekordbox library backup bundles master.db, the share/PIONEER analysis &
	// artwork trees, and the *SETTING.DAT blobs in one zip. Extract the parts we
	// need to a temp dir, import the db, and hand the roots back for the apply
	// pipeline; the temp dir is released via Cleanup once that has run.
	tmp, err := os.MkdirTemp("", "rb-backup-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	o.note("Extracting backup…")
	dbPath, shareRoot, settingsDir, err := extractRekordboxBackup(o.Path, tmp, o.WantSettings)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("extract backup: %w", err)
	}
	b := &ImportBundle{}
	if o.WantTracks {
		o.note("Reading rekordbox database…")
		b, err = ImportRekordboxMasterDB(lib, dbPath, o.Key)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	b.ShareRoot = shareRoot
	b.SettingsDir = settingsDir
	b.Cleanup = func() { os.RemoveAll(tmp) }
	return b, nil
}

// rekordboxSettingsFiles are the player/mixer setting blobs rekordbox writes to
// the root of a library-backup zip (and to /PIONEER on a USB export).
var rekordboxSettingsFiles = []string{
	"MYSETTING.DAT", "MYSETTING2.DAT", "DJMMYSETTING.DAT", "DEVSETTING.DAT",
}

// findRekordboxSettingsDir returns the directory holding *SETTING.DAT blobs for
// a master.db in dbDir, or "" if none has them. rekordbox 6 keeps them in a
// sibling "rekordbox6" folder (the db lives in "rekordbox"); a backup zip puts
// them beside the db, so the sibling is checked first, then the db's own dir.
func findRekordboxSettingsDir(dbDir string) string {
	has := func(dir string) bool {
		for _, sf := range rekordboxSettingsFiles {
			if _, e := os.Stat(filepath.Join(dir, sf)); e == nil {
				return true
			}
		}
		return false
	}
	for _, cand := range []string{filepath.Join(filepath.Dir(dbDir), "rekordbox6"), dbDir} {
		if has(cand) {
			return cand
		}
	}
	return ""
}

// extractRekordboxBackup unpacks the parts of a rekordbox library-backup zip we
// care about — master.db, the share/PIONEER/{USBANLZ,Artwork} trees, and (when
// wantSettings) the *SETTING.DAT blobs at the zip root — into dest. It returns
// the extracted master.db path, the share/ root, and the directory holding the
// settings files (empty if none were extracted). Other entries (lighting DBs,
// XML prefs, etc.) are skipped. Guards against zip-slip.
func extractRekordboxBackup(zipPath, dest string, wantSettings bool) (dbPath, shareRoot, settingsDir string, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", "", err
	}
	defer zr.Close()

	isSettingsFile := func(name string) bool {
		for _, s := range rekordboxSettingsFiles {
			if name == s {
				return true
			}
		}
		return false
	}

	cleanDest := filepath.Clean(dest)
	gotSettings := false
	for _, f := range zr.File {
		name := f.Name // zip paths use forward slashes
		settings := wantSettings && isSettingsFile(name)
		keep := name == "master.db" ||
			strings.HasPrefix(name, "share/PIONEER/USBANLZ/") ||
			strings.HasPrefix(name, "share/PIONEER/Artwork/") ||
			settings
		if !keep || f.FileInfo().IsDir() {
			continue
		}
		target := filepath.Join(cleanDest, filepath.FromSlash(name))
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			continue // zip-slip: entry escapes dest
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", "", err
		}
		if err := extractZipEntry(f, target); err != nil {
			return "", "", "", err
		}
		if settings {
			gotSettings = true
		}
	}

	dbPath = filepath.Join(cleanDest, "master.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "", "", "", fmt.Errorf("master.db not found in backup zip")
	}
	if gotSettings {
		settingsDir = cleanDest
	}
	return dbPath, filepath.Join(cleanDest, "share"), settingsDir, nil
}

// extractZipEntry copies one zip file entry to target on disk.
func extractZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

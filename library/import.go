// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

// ImportBundle is the neutral result every library importer returns. The tracks
// themselves are added straight into the Library during the import; this carries
// the side-data the api-side applier materializes afterwards: the playlist tree,
// MyTags, track colours, cues, and — for rekordbox master.db / backup imports —
// the ANLZ + artwork asset paths plus the roots they live under.
type ImportBundle struct {
	Result    *ImportResult
	Playlists []PlaylistImport
	Tags      []TagImport
	Colors    []ColorImport
	Cues      []ImportedCue
	Assets    []ImportedAsset
	// ShareRoot is the share/ root for ANLZ + artwork (a backup-zip extract, or
	// a master.db's own library folder). SettingsDir holds the *SETTING.DAT
	// blobs. Both are empty when the format carries no such assets.
	ShareRoot   string
	SettingsDir string
	// Cleanup releases any transient resources the import created (the backup-zip
	// temp dir). The caller MUST defer it, after the apply pipeline has read
	// ShareRoot/SettingsDir. Nil when there is nothing to release.
	Cleanup func()
}

// ImportOptions parameterizes an import. WantTracks gates the track import; a
// false value still lets an importer surface ShareRoot/SettingsDir for a
// settings/analysis-only import. WantSettings gates locating *SETTING.DAT (the
// caller ANDs the user's include flag with its own settings-store capability).
// Progress, if set, receives human-readable phase updates.
type ImportOptions struct {
	Path         string
	Key          string
	WantTracks   bool
	WantSettings bool
	Progress     func(msg string)
}

func (o ImportOptions) note(msg string) {
	if o.Progress != nil {
		o.Progress(msg)
	}
}

// Importer reads one source library format into the Library.
type Importer interface {
	// Label is a short human name, e.g. "rekordbox XML", "Traktor NML".
	Label() string
	// Handles reports whether this importer claims path (by extension).
	Handles(path string) bool
	// RequiresKey reports whether path needs a decryption key.
	RequiresKey(path string) bool
	// Import reads path and populates lib, returning the side-data bundle.
	Import(lib *Library, opt ImportOptions) (*ImportBundle, error)
}

// Importers is the importer registry, in resolution order.
func Importers() []Importer {
	return []Importer{
		rekordboxXMLImporter{},
		rekordboxBackupImporter{},
		rekordboxDBImporter{},
		traktorImporter{},
		virtualdjImporter{},
	}
}

// ImporterFor returns the importer that handles path, or nil if none does.
func ImporterFor(path string) Importer {
	for _, imp := range Importers() {
		if imp.Handles(path) {
			return imp
		}
	}
	return nil
}

func hasExt(path, ext string) bool {
	return strings.EqualFold(filepath.Ext(path), ext)
}

// xmlRootIs reports whether the XML file at path has the given root element
// local name. It reads only up to the first start element, so it is cheap.
// Used to tell same-extension formats apart — a rekordbox export
// (DJ_PLAYLISTS) and a VirtualDJ database (VirtualDJ_Database) are both .xml.
func xmlRootIs(path, name string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == name
		}
	}
}

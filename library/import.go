// SPDX-License-Identifier: GPL-3.0-or-later

package library

// ImportBundle is the neutral result every library importer returns. The tracks
// themselves are added straight into the Library during the import; this carries
// the side-data the api-side applier materializes afterwards: the playlist tree,
// MyTags, track colours, cues, and — for rekordbox master.db / backup imports —
// the ANLZ + artwork asset paths.
//
// (import-layer phase 1: unifies the previously divergent importer return
// signatures. ShareRoot / SettingsDir join here in phase 2, once the backup-zip
// container becomes an Importer; today the api handler still manages those.)
type ImportBundle struct {
	Result    *ImportResult
	Playlists []PlaylistImport
	Tags      []TagImport
	Colors    []ColorImport
	Cues      []ImportedCue
	Assets    []ImportedAsset
}

// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MasterDBDump matches tools/rekordbox_dump.py's output schema.
type MasterDBDump struct {
	Tracks    []masterDBTrack    `json:"tracks"`
	Playlists []MasterDBPlaylist `json:"playlists"`
	Tags      []MasterDBTag      `json:"tags"`
	Cues      []masterDBCue      `json:"cues"`
}

type masterDBCue struct {
	ContentID string `json:"content_id"`
	InMsec    int    `json:"in_msec"`
	OutMsec   int    `json:"out_msec"`
	Kind      int    `json:"kind"`
	Color     int    `json:"color"`
	Comment   string `json:"comment"`
}

// ImportedCue is a djmdCue row with its rekordbox ContentID resolved to a
// library Track.ID. HotCue 0 = memory cue, 1..8 = hot cue A..H.
type ImportedCue struct {
	TrackID uint32
	HotCue  int
	TimeMs  uint32
	LoopMs  int32  // -1 if not a loop
	ColorID uint32 // rekordbox colour code (0x00-0x3e)
	Comment string
}

// defaultHotCueColorID is the palette index used for a hot cue that has no
// colour assigned in rekordbox (djmdCue.ColorTableIndex NULL). rekordbox
// shows those in its default hot-cue green; 0x16 (#28e214) is that green in
// the hot-cue palette.
const defaultHotCueColorID = 0x16

// hotCueSlot maps rekordbox's djmdCue.Kind to the contiguous 1..8 hot-cue
// slot (A..H) the CDJ expects. rekordbox's Kind numbering skips 4 — hot
// cues are stored as A=1, B=2, C=3, D=5, E=6, F=7, G=8, H=9 (Kind 4 is
// reserved and never used) — so without this remap an imported "D" shifts
// up to "E". Memory cues (Kind 0) are returned unchanged.
func hotCueSlot(kind int) int {
	if kind >= 5 {
		return kind - 1
	}
	return kind
}

type masterDBTrack struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	Genre       string  `json:"genre"`
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	FilePath    string  `json:"file_path"`
	FileName    string  `json:"file_name"`
	FileSize    int64   `json:"file_size"`
	BPM         float64 `json:"bpm"`
	DurationSec int     `json:"duration_sec"`
	Bitrate     int     `json:"bitrate"`
	SampleRate  int     `json:"sample_rate"`
	Rating      int     `json:"rating"`
	Year        int     `json:"year"`
	TrackNum    int     `json:"track_num"`
	DiscNum     int     `json:"disc_num"`
	Comment     string  `json:"comment"`
	PlayCount   int     `json:"play_count"`
	DateAdded   string  `json:"date_added"`
	ColorID     int     `json:"color_id"`     // rekordbox track-colour palette ID (1-8)
	AnalyzePath string  `json:"analyze_path"` // /PIONEER/USBANLZ/.../ANLZ0000.DAT, relative to share/
	ImagePath   string  `json:"image_path"`   // /PIONEER/Artwork/.../artwork.jpg, relative to share/
}

// ImportedAsset pairs a library Track.ID with the rekordbox-relative paths
// to its analysis (ANLZ) and artwork files inside a backup's share/ tree.
// The caller resolves these against the extracted backup directory to import
// the existing waveforms/beat grids and cover art.
type ImportedAsset struct {
	TrackID     uint32
	AnalyzePath string // e.g. /PIONEER/USBANLZ/<bucket>/<uuid>/ANLZ0000.DAT
	ImagePath   string // e.g. /PIONEER/Artwork/<bucket>/<uuid>/artwork.jpg
}

// MasterDBPlaylist mirrors a djmdPlaylist row plus its track membership.
type MasterDBPlaylist struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	ParentID string       `json:"parent_id"`
	IsFolder bool         `json:"is_folder"`
	IsSmart  bool         `json:"is_smart"`
	TrackIDs []string     `json:"track_ids"`
	Smart    *RBSmartList `json:"smart"` // present for smart playlists
}

// RBSmartList is a rekordbox smart-playlist rule set parsed from the
// djmdPlaylist.SmartList XML by the Python helper. Logical is 1=AND, 2=OR.
// The api package translates this into its own SmartRules. myTag condition
// values arrive already resolved from tag ID to tag name.
type RBSmartList struct {
	Logical    int           `json:"logical"`
	Conditions []RBSmartCond `json:"conditions"`
}

// RBSmartCond is one rekordbox condition. Operator is the rekordbox numeric
// code (1=equal, 2=not-equal, 3=greater, 4=less, 5=in-range, 6=in-last,
// 7=not-in-last, 8=contains, 9=not-contains, 10=starts-with, 11=ends-with).
type RBSmartCond struct {
	Property string `json:"property"`
	Operator int    `json:"operator"`
	Unit     string `json:"unit"`
	Left     string `json:"left"`
	Right    string `json:"right"`
}

// MasterDBTag is a rekordbox MyTag with its track assignments.
type MasterDBTag struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ParentID string   `json:"parent_id"`
	TrackIDs []string `json:"track_ids"`
}

// ImportRekordboxMasterDB shells out to tools/rekordbox_dump.py to read
// the encrypted master.db and then merges the result into the library.
// Requires Python 3 + the `sqlcipher3` package. The key is the SQLCipher
// decryption key for the user's own database, supplied by the caller — this
// project ships no key and does not extract one.
//
// Returns an ImportBundle like the other importers: the resolved playlist tree
// (track IDs already mapped to library Track.IDs), the MyTags, track colours,
// and cues, plus Assets carrying each track's ANLZ + artwork paths (relative to
// the backup's share/ root) for the caller to import.
func ImportRekordboxMasterDB(lib *Library, dbPath, key string) (*ImportBundle, error) {
	dump, err := runMasterDBDump(dbPath, key)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{TracksTotal: len(dump.Tracks)}
	idMap := make(map[string]uint32, len(dump.Tracks)) // rekordbox ContentID → library Track.ID
	var colors []ColorImport
	var assets []ImportedAsset
	for _, t := range dump.Tracks {
		path := t.FilePath
		if path != "" && t.FileName != "" && !strings.HasSuffix(path, t.FileName) {
			path = filepath.Join(path, t.FileName)
		}
		if path == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("track %q: empty file_path", t.Title))
			res.TracksSkipped++
			continue
		}
		track := masterDBToLibrary(&t, path)
		existing := lib.TrackByPath(path)
		var id uint32
		if existing != nil {
			mergeTrack(existing, track)
			id = existing.ID
			res.TracksUpdated++
		} else {
			id = lib.AddTrackBulk(track)
			res.TracksAdded++
		}
		if t.ID != "" {
			idMap[t.ID] = id
		}
		if track.ColorID > 0 {
			colors = append(colors, ColorImport{TrackID: id, ColorID: track.ColorID})
		}
		if t.AnalyzePath != "" || t.ImagePath != "" {
			assets = append(assets, ImportedAsset{TrackID: id, AnalyzePath: t.AnalyzePath, ImagePath: t.ImagePath})
		}
	}

	// Resolve each MyTag's rekordbox ContentIDs to library Track.IDs.
	// djmdMyTag is a 2-level tree: category rows (Genre, Components, Mood, …)
	// are parents, and the actual tags are children whose ParentID points at
	// their category. Category rows carry no track assignments, so they drop
	// out of the leaf list below; we use them only to label each tag's
	// category. tagsByID lets a tag look up its parent category's name.
	tagsByID := make(map[string]MasterDBTag, len(dump.Tags))
	for _, mt := range dump.Tags {
		tagsByID[mt.ID] = mt
	}
	tags := make([]TagImport, 0, len(dump.Tags))
	for _, mt := range dump.Tags {
		var ids []uint32
		for _, cid := range mt.TrackIDs {
			if id, ok := idMap[cid]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue // category/parent row, or an unused tag — nothing to apply
		}
		category := ""
		if parent, ok := tagsByID[mt.ParentID]; ok {
			category = parent.Name
		}
		tags = append(tags, TagImport{Name: mt.Name, Category: category, TrackIDs: ids})
	}
	res.TagsTotal = len(tags)

	playlists := masterDBPlaylistTree(dump.Playlists, idMap)
	res.PlaylistsTotal = countPlaylists2(playlists)

	// Resolve cue points to library track IDs. OutMsec -1 means "not a loop".
	cues := make([]ImportedCue, 0, len(dump.Cues))
	for _, c := range dump.Cues {
		id, ok := idMap[c.ContentID]
		if !ok {
			continue
		}
		mc := ImportedCue{TrackID: id, HotCue: hotCueSlot(c.Kind), TimeMs: uint32(c.InMsec), LoopMs: -1, Comment: c.Comment}
		if c.OutMsec > 0 {
			mc.LoopMs = int32(c.OutMsec)
		}
		if c.Color >= 0 {
			mc.ColorID = uint32(c.Color)
		} else if c.Kind >= 1 {
			// A hot cue with no colour set in rekordbox (ColorTableIndex
			// NULL). rekordbox renders these with its default hot-cue
			// colour, green — so do the same rather than falling back to
			// the "no colour" orange. Memory cues (Kind 0) keep no colour.
			mc.ColorID = defaultHotCueColorID
		}
		cues = append(cues, mc)
	}

	lib.FinalizeBulk()
	return &ImportBundle{Result: res, Playlists: playlists, Tags: tags, Colors: colors, Assets: assets, Cues: cues}, nil
}

// masterDBPlaylistTree turns the flat djmdPlaylist rows into a nested
// PlaylistImport tree with track IDs resolved to library Track.IDs. rekordbox
// links children to parents by ParentID; top-level nodes use a sentinel
// ("root") that isn't itself a playlist ID — so any node whose parent isn't a
// known playlist is treated as a root.
func masterDBPlaylistTree(flat []MasterDBPlaylist, idMap map[string]uint32) []PlaylistImport {
	ids := make(map[string]bool, len(flat))
	childrenOf := make(map[string][]MasterDBPlaylist)
	for _, p := range flat {
		ids[p.ID] = true
		childrenOf[p.ParentID] = append(childrenOf[p.ParentID], p)
	}
	var build func(p MasterDBPlaylist) PlaylistImport
	build = func(p MasterDBPlaylist) PlaylistImport {
		pi := PlaylistImport{Name: p.Name, IsFolder: p.IsFolder}
		if p.IsFolder {
			for _, c := range childrenOf[p.ID] {
				pi.Children = append(pi.Children, build(c))
			}
		} else if p.IsSmart {
			// Rule-based: no static membership; carry the rules for the
			// caller to create a smart playlist.
			pi.IsSmart = true
			pi.Smart = p.Smart
		} else {
			for _, cid := range p.TrackIDs {
				if id, ok := idMap[cid]; ok {
					pi.TrackIDs = append(pi.TrackIDs, id)
				}
			}
		}
		return pi
	}
	var roots []PlaylistImport
	for _, p := range flat {
		if !ids[p.ParentID] { // parent is the "root" sentinel
			roots = append(roots, build(p))
		}
	}
	return roots
}

// countPlaylists2 counts leaf playlists (not folders) in a PlaylistImport tree.
func countPlaylists2(nodes []PlaylistImport) int {
	n := 0
	for _, p := range nodes {
		if p.IsFolder {
			n += countPlaylists2(p.Children)
		} else {
			n++
		}
	}
	return n
}

// runMasterDBDump invokes the Python helper and parses its JSON output.
func runMasterDBDump(dbPath, key string) (*MasterDBDump, error) {
	if _, err := exec.LookPath("python3"); err != nil {
		return nil, fmt.Errorf("python3 not found in PATH (required for master.db import)")
	}
	// tools/rekordbox_dump.py lives next to the binary's source — find it
	// either by walking up from the executable or in the source tree.
	script := findDumpScript()
	if script == "" {
		return nil, fmt.Errorf("rekordbox_dump.py helper not found")
	}
	cmd := exec.Command("python3", script, dbPath, key)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("python helper failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	var dump MasterDBDump
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, fmt.Errorf("parse helper output: %w", err)
	}
	log.Printf("import: master.db: %d tracks, %d playlists, %d tags",
		len(dump.Tracks), len(dump.Playlists), len(dump.Tags))
	return &dump, nil
}

func findDumpScript() string {
	var candidates []string
	if env := os.Getenv("VYNULL_DUMP_SCRIPT"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "tools/rekordbox_dump.py")
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "tools/rekordbox_dump.py"))
		candidates = append(candidates, filepath.Join(dir, "../tools/rekordbox_dump.py"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func masterDBToLibrary(t *masterDBTrack, path string) *Track {
	ext := strings.ToLower(filepath.Ext(path))
	ft := strings.TrimPrefix(ext, ".")
	if ft == "aif" {
		ft = "aiff"
	}
	dateAdded, _ := time.Parse("2006-01-02", t.DateAdded)
	if dateAdded.IsZero() {
		// rekordbox dates may include time; try a longer layout
		dateAdded, _ = time.Parse("2006-01-02 15:04:05", t.DateAdded)
	}
	if dateAdded.IsZero() {
		dateAdded = time.Now()
	}
	return &Track{
		Title:      t.Title,
		Artist:     t.Artist,
		Album:      t.Album,
		Genre:      t.Genre,
		Duration:   DurationSec(time.Duration(t.DurationSec) * time.Second),
		BPM:        t.BPM,
		Key:        t.Key,
		Rating:     uint8(t.Rating),
		Year:       t.Year,
		TrackNum:   t.TrackNum,
		DiscNum:    t.DiscNum,
		FilePath:   path,
		FileType:   ft,
		FileSize:   t.FileSize,
		Comment:    t.Comment,
		Label:      t.Label,
		DateAdded:  dateAdded,
		Bitrate:    t.Bitrate,
		SampleRate: t.SampleRate,
		PlayCount:  t.PlayCount,
		ColorID:    uint8(t.ColorID),
	}
}

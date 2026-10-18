// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MasterDBDump matches tools/rekordbox_dump.py's output schema.
type MasterDBDump struct {
	Tracks    []masterDBTrack    `json:"tracks"`
	Playlists []MasterDBPlaylist `json:"playlists"`
	Tags      []MasterDBTag      `json:"tags"`
	Cues      []masterDBCue      `json:"cues"`
	// TagList holds the rekordbox ContentIDs in djmdSongTagList (the "Tag
	// List"). Distinct from per-track MyTag assignments; drives the song-tag
	// flag bit in served menu rows.
	TagList []string `json:"tag_list"`
	// Recommendations are djmdRecommendLike related-track pairs backing the
	// MATCHING browse.
	Recommendations []masterDBRecommend `json:"recommendations"`
	// HotCueBanks and HotCueBankMembers are the Hot Cue Bank tree
	// (djmdHotCueBanklist) and memberships (djmdSongHotCueBanklist).
	HotCueBanks       []masterDBHotCueBank       `json:"hot_cue_banks"`
	HotCueBankMembers []masterDBHotCueBankMember `json:"hot_cue_bank_members"`
	// Sorts are the deck's SORT menu and track-list column (djmdSort).
	Sorts []masterDBSort `json:"sorts"`
}

// masterDBHotCueBank is one djmdHotCueBanklist node; top-level nodes have
// ParentID "root".
type masterDBHotCueBank struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Seq      int    `json:"seq"`
	Name     string `json:"name"`
	Folder   bool   `json:"folder"`
	Deleted  bool   `json:"deleted"`
}

// masterDBHotCueBankMember is one djmdSongHotCueBanklist membership.
type masterDBHotCueBankMember struct {
	BankID    string `json:"bank_id"`
	ContentID string `json:"content_id"`
	TrackNo   int64  `json:"track_no"`
	// Numeric cue fields are decoded wide: rekordbox stores sentinels like -1
	// in columns the wire carries as unsigned 32-bit words.
	InMsec          int64  `json:"in_msec"`
	OutMsec         int64  `json:"out_msec"`
	InMpegFrame     int64  `json:"in_mpeg_frame"`
	OutMpegFrame    int64  `json:"out_mpeg_frame"`
	InMpegAbs       int64  `json:"in_mpeg_abs"`
	OutMpegAbs      int64  `json:"out_mpeg_abs"`
	Color           int    `json:"color"`
	ColorTableIndex int    `json:"color_table_index"`
	Comment         string `json:"comment"`
	BeatLoopSize    int64  `json:"beat_loop_size"`
	CueMicrosec     int64  `json:"cue_microsec"`
	Deleted         bool   `json:"deleted"`
}

// masterDBRecommend is one djmdRecommendLike related-track pair (ContentIDs).
type masterDBRecommend struct {
	ID1 string `json:"id1"`
	ID2 string `json:"id2"`
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
	DateCreated string  `json:"date_created"`
	ReleaseDate string  `json:"release_date"`
	ColorID     int     `json:"color_id"`     // rekordbox track-colour palette ID (1-8)
	AnalyzePath string  `json:"analyze_path"` // /PIONEER/USBANLZ/.../ANLZ0000.DAT, relative to share/
	ImagePath   string  `json:"image_path"`   // /PIONEER/Artwork/.../artwork.jpg, relative to share/
	// rekordbox lookup-table row IDs (djmd*), so served category rows carry
	// rekordbox's IDs rather than hashed names. Strings, like ID.
	GenreID  string `json:"genre_id"`
	ArtistID string `json:"artist_id"`
	AlbumID  string `json:"album_id"`
	KeyID    string `json:"key_id"`
	LabelID  string `json:"label_id"`
	// FileTypeID is rekordbox's FileType enum (1=mp3, 4=m4a, 5=flac, 11=wav,
	// 12=aiff), authoritative over the file extension. HotCueAutoLoad is
	// non-empty when the deck should auto-load this track's hot cues.
	FileTypeID     int    `json:"file_type_id"`
	HotCueAutoLoad string `json:"hot_cue_auto_load"`
	// Remixer and original artist (both djmdArtist rows), with their IDs, so
	// the REMIXER and ORIGINAL ARTIST category drills resolve.
	Remixer        string `json:"remixer"`
	OriginalArtist string `json:"original_artist"`
	MixName        string `json:"mix_name"`
	AlbumArtist    string `json:"album_artist"`
	Composer       string `json:"composer"`
	RemixerID      string `json:"remixer_id"`
	OrigArtistID   string `json:"orig_artist_id"`
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
	Seq      int          `json:"seq"` // rekordbox display sequence within the parent
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
	// Seq orders tags within a column, and columns among themselves.
	Seq int `json:"seq"`
	// IsCategory marks a column (rekordbox Attribute 1) rather than a tag.
	IsCategory bool `json:"is_category"`
}

// isCategory reports whether a djmdMyTag row is a column (Genre,
// Components, ...) rather than a tag: its Attribute flag, or, for dumps
// without it, a top-level row.
func (t MasterDBTag) isCategory() bool { return t.IsCategory || t.ParentID == "root" }

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
	return ImportMasterDBDump(lib, dump)
}

// ImportMasterDBDump builds the library from an already-decoded master.db dump
// (the JSON that tools/rekordbox_dump.py emits). Split out of
// ImportRekordboxMasterDB so a caller that already holds the dump — e.g. the
// conformance server loading a --fixture-json — can reuse it without re-running
// the Python helper.
func ImportMasterDBDump(lib *Library, dump *MasterDBDump) (*ImportBundle, error) {
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
		// Record each category value's rekordbox lookup-table ID so the dbserver
		// can serve rekordbox's real IDs (not hashed names) in category rows.
		lib.RegisterCategoryID("genre", t.Genre, parseRBID(t.GenreID))
		lib.RegisterCategoryID("artist", t.Artist, parseRBID(t.ArtistID))
		lib.RegisterCategoryID("album", t.Album, parseRBID(t.AlbumID))
		lib.RegisterCategoryID("key", t.Key, parseRBID(t.KeyID))
		lib.RegisterCategoryID("label", t.Label, parseRBID(t.LabelID))
		lib.RegisterCategoryID("remixer", t.Remixer, parseRBID(t.RemixerID))
		lib.RegisterCategoryID("origartist", t.OriginalArtist, parseRBID(t.OrigArtistID))
		if track.ColorID > 0 {
			colors = append(colors, ColorImport{TrackID: id, ColorID: track.ColorID})
		}
		if t.AnalyzePath != "" || t.ImagePath != "" {
			assets = append(assets, ImportedAsset{TrackID: id, AnalyzePath: t.AnalyzePath, ImagePath: t.ImagePath})
		}
	}

	// Record related-track recommendations (both directions) for MATCHING.
	for _, r := range dump.Recommendations {
		a, ok1 := idMap[r.ID1]
		b, ok2 := idMap[r.ID2]
		if ok1 && ok2 {
			lib.AddRelated(a, b)
		}
	}

	// Hot Cue Banks replace the library's when the dump has any. Memberships
	// keep their rekordbox ContentID; TrackID is the library track it
	// resolved to (0 for content that isn't in the library).
	if len(dump.HotCueBanks) > 0 {
		banks := make([]HotCueBank, 0, len(dump.HotCueBanks))
		for _, b := range dump.HotCueBanks {
			banks = append(banks, HotCueBank{
				ID: parseRBID(b.ID), ParentID: parseRBID(b.ParentID), // "root" → 0
				Seq: b.Seq, Name: b.Name, Folder: b.Folder, Deleted: b.Deleted,
			})
		}
		members := make([]HotCueBankMember, 0, len(dump.HotCueBankMembers))
		for _, m := range dump.HotCueBankMembers {
			members = append(members, HotCueBankMember{
				BankID: parseRBID(m.BankID), TrackID: idMap[m.ContentID], ContentID: parseRBID(m.ContentID),
				TrackNo: int32(m.TrackNo), InMsec: int32(m.InMsec), OutMsec: int32(m.OutMsec),
				InMpegFrame: uint32(m.InMpegFrame), OutMpegFrame: uint32(m.OutMpegFrame),
				InMpegAbs: uint32(m.InMpegAbs), OutMpegAbs: uint32(m.OutMpegAbs),
				Color: m.Color, ColorTableIndex: m.ColorTableIndex, Comment: m.Comment, BeatLoopSize: uint32(m.BeatLoopSize),
				CueMicrosec: uint32(m.CueMicrosec), Deleted: m.Deleted,
			})
		}
		lib.SetHotCueBanks(banks, members)
	}

	// Flag the tracks in the "Tag List" (djmdSongTagList) so their served menu
	// rows carry rekordbox's song-tag bit (argument 7). This is distinct from
	// per-track MyTag assignments below.
	for _, cid := range dump.TagList {
		if id, ok := idMap[cid]; ok {
			lib.MarkSongTag(id)
		}
	}

	// MyTags. djmdMyTag is a 2-level tree: columns (Genre, Components, …) at
	// the top and the tags under them. Every live column and tag is imported,
	// in rekordbox's order (columns by Seq, then each column's tags by Seq),
	// whether or not any track carries it: rekordbox keeps unused tags and
	// empty columns, and smart playlists can name them.
	tagsByID := make(map[string]MasterDBTag, len(dump.Tags))
	var cats, leaves []MasterDBTag
	for _, mt := range dump.Tags {
		tagsByID[mt.ID] = mt
		if mt.isCategory() {
			cats = append(cats, mt)
		} else {
			leaves = append(leaves, mt)
		}
	}
	sort.SliceStable(cats, func(i, j int) bool { return cats[i].Seq < cats[j].Seq })
	catSeq := func(mt MasterDBTag) int {
		if p, ok := tagsByID[mt.ParentID]; ok {
			return p.Seq
		}
		return 1 << 30 // tags outside any column last
	}
	sort.SliceStable(leaves, func(i, j int) bool {
		if a, b := catSeq(leaves[i]), catSeq(leaves[j]); a != b {
			return a < b
		}
		return leaves[i].Seq < leaves[j].Seq
	})
	tagCategories := make([]string, 0, len(cats))
	for _, c := range cats {
		tagCategories = append(tagCategories, c.Name)
	}
	tags := make([]TagImport, 0, len(leaves))
	for _, mt := range leaves {
		var ids []uint32
		for _, cid := range mt.TrackIDs {
			if id, ok := idMap[cid]; ok {
				ids = append(ids, id)
			}
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
	return &ImportBundle{Result: res, Playlists: playlists, Tags: tags, TagCategories: tagCategories, Colors: colors, Assets: assets, Cues: cues,
		Sorts: importSorts(dump.Sorts)}, nil
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

// defaultHelperVenv is the per-user location of the isolated python
// environment runMasterDBDump provisions on demand (sqlcipher3 is pip-only
// and must not pollute the system python).

// helperPython returns a python3 interpreter able to run tools/rekordbox_dump.py.
//
// Resolution order:
//  1. VYNULL_PYTHON env var (explicit override, used verbatim)
//  2. system python3, if it can already import sqlcipher3
//  3. a dedicated venv under the state dir, created on demand with sqlcipher3
//     installed (provisioning is skipped when the marker file exists)
func helperPython(venvDir string) (string, error) {
	if override := os.Getenv("VYNULL_PYTHON"); override != "" {
		return override, nil
	}
	if _, err := exec.LookPath("python3"); err != nil {
		return "", fmt.Errorf("python3 not found in PATH (required for master.db import)")
	}
	const probe = "import sqlcipher3"
	if err := exec.Command("python3", "-c", probe).Run(); err == nil {
		return "python3", nil
	}
	if err := ensureHelperVenv(venvDir); err != nil {
		return "", err
	}
	return filepath.Join(venvDir, "bin", "python3"), nil
}

// ensureHelperVenv creates venvDir (a fresh python3 venv) and pip-installs
// sqlcipher3 into it, unless a previous run already left the marker file.
// The marker is written only after the install is verified by an import probe.
func ensureHelperVenv(venvDir string) error {
	marker := filepath.Join(venvDir, ".deps-ok")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	py, err := exec.Command("python3", "-c", "import sys; print(sys.executable)").Output()
	if err != nil {
		return fmt.Errorf("locate python3: %w", err)
	}
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		return fmt.Errorf("create helper venv dir: %w", err)
	}
	cmd := exec.Command(strings.TrimSpace(string(py)), "-m", "venv", venvDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create helper venv %s: %w (stderr: %s)", venvDir, err, strings.TrimSpace(stderr.String()))
	}
	venvPy := filepath.Join(venvDir, "bin", "python3")
	cmd = exec.Command(venvPy, "-m", "pip", "--quiet", "install", "sqlcipher3")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install sqlcipher3 into helper venv: %w (stderr: %s) — "+
			"or point VYNULL_PYTHON at a python that already has sqlcipher3", err, strings.TrimSpace(stderr.String()))
	}
	if err := exec.Command(venvPy, "-c", "import sqlcipher3").Run(); err != nil {
		return fmt.Errorf("sqlcipher3 unusable in helper venv: %w", err)
	}
	return os.WriteFile(marker, []byte("ok\n"), 0o644)
}

// runMasterDBDump invokes the Python helper and parses its JSON output.
func runMasterDBDump(dbPath, key string) (*MasterDBDump, error) {
	// tools/rekordbox_dump.py lives next to the binary's source — find it
	// either by walking up from the executable or in the source tree.
	script := findDumpScript()
	if script == "" {
		return nil, fmt.Errorf("rekordbox_dump.py helper not found")
	}
	// Keep the helper's isolated python venv next to the rest of vynull's
	// state (analysis cache, settings…). Tests pass an explicit absolute dir
	// via VYNULL_HELPER_VENV; the plain run uses ~/.vynull/python-venv.
	venvDir := os.Getenv("VYNULL_HELPER_VENV")
	if venvDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home for helper venv: %w", err)
		}
		venvDir = filepath.Join(home, ".vynull", "python-venv")
	}
	python, err := helperPython(venvDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(python, script, dbPath, key)
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

// parseRBID parses a rekordbox lookup-table ID (stored as a decimal string)
// into a uint32, returning 0 when the string is empty or not a valid uint32.
func parseRBID(s string) uint32 {
	if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32); err == nil {
		return uint32(n)
	}
	return 0
}

func masterDBToLibrary(t *masterDBTrack, path string) *Track {
	ext := strings.ToLower(filepath.Ext(path))
	ft := strings.TrimPrefix(ext, ".")
	if ft == "aif" {
		ft = "aiff"
	}
	// rekordbox's FileType enum is authoritative over the extension (it stores
	// .wav-named files as FileType 1, say); fall back to the extension when the
	// dump carries no FileType.
	if s := fileTypeString(t.FileTypeID); s != "" {
		ft = s
	}
	// rekordbox reads its dates leniently by position and treats a blank or
	// malformed one as no date (smart playlists skip such tracks).
	dateAdded := ParseRekordboxDate(t.DateAdded)
	dateCreated := ParseRekordboxDate(t.DateCreated)
	releaseDate := ParseRekordboxDate(t.ReleaseDate)
	// Preserve the rekordbox djmdContent.ID as the library track ID so served
	// rows carry the same IDs rekordbox does (addTrackLocked respects a preset
	// ID; idMap then resolves to it identically). Fall back to auto-assign (0)
	// if the ID is missing or not a uint32.
	trackID := parseRBID(t.ID)
	return &Track{
		ID:             trackID,
		Title:          t.Title,
		Artist:         t.Artist,
		Album:          t.Album,
		Genre:          t.Genre,
		Duration:       DurationSec(time.Duration(t.DurationSec) * time.Second),
		BPM:            t.BPM,
		Key:            t.Key,
		Rating:         uint8(t.Rating),
		Year:           t.Year,
		TrackNum:       t.TrackNum,
		DiscNum:        t.DiscNum,
		FilePath:       path,
		FileType:       ft,
		FileSize:       t.FileSize,
		Comment:        t.Comment,
		Label:          t.Label,
		DateAdded:      dateAdded,
		DateCreated:    dateCreated,
		ReleaseDate:    releaseDate,
		Bitrate:        t.Bitrate,
		SampleRate:     t.SampleRate,
		PlayCount:      t.PlayCount,
		ColorID:        uint8(t.ColorID),
		FileTypeID:     t.FileTypeID,
		AutoLoadHotCue: t.HotCueAutoLoad != "",
		Remixer:        t.Remixer,
		OriginalArtist: t.OriginalArtist,
		MixName:        t.MixName,
		KeyID:          parseRBID(t.KeyID),
		AlbumArtist:    t.AlbumArtist,
		Composer:       t.Composer,
	}
}

// fileTypeString maps a rekordbox FileType enum value to the library's codec
// string. Returns "" for 0/unknown so callers keep the extension-derived type.
func fileTypeString(id int) string {
	switch id {
	case 1:
		return "mp3"
	case 4:
		return "m4a"
	case 5:
		return "flac"
	case 11:
		return "wav"
	case 12:
		return "aiff"
	}
	return ""
}

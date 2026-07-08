// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Traktor stores its whole library as a single XML file (collection.nml): a
// COLLECTION of ENTRY elements plus a nested PLAYLISTS tree. This importer
// mirrors ImportRekordboxXML — parse entries into library.Track, then walk the
// playlist node tree — so both importers share the same result shape and the
// api.go dispatch can treat them uniformly.

type traktorNML struct {
	XMLName    xml.Name          `xml:"NML"`
	Version    int               `xml:"VERSION,attr"`
	Collection traktorCollection `xml:"COLLECTION"`
	Playlists  traktorPlaylists  `xml:"PLAYLISTS"`
}

type traktorCollection struct {
	Entries int            `xml:"ENTRIES,attr"`
	Tracks  []traktorEntry `xml:"ENTRY"`
}

type traktorEntry struct {
	Title      string          `xml:"TITLE,attr"`
	Artist     string          `xml:"ARTIST,attr"`
	Location   traktorLocation `xml:"LOCATION"`
	Album      traktorAlbum    `xml:"ALBUM"`
	Info       traktorInfo     `xml:"INFO"`
	Tempo      traktorTempo    `xml:"TEMPO"`
	MusicalKey *traktorKey     `xml:"MUSICAL_KEY"`
	Cues       []traktorCue    `xml:"CUE_V2"`
}

type traktorCue struct {
	Name   string  `xml:"NAME,attr"`
	Type   int     `xml:"TYPE,attr"`   // 0=cue 1=fade-in 2=fade-out 3=load 4=grid 5=loop
	Start  float64 `xml:"START,attr"`  // milliseconds
	Len    float64 `xml:"LEN,attr"`    // milliseconds (loop length)
	Hotcue int     `xml:"HOTCUE,attr"` // -1 = memory cue, else 0-7 hot-cue slot
}

const (
	traktorCueTypeCue  = 0
	traktorCueTypeGrid = 4
	traktorCueTypeLoop = 5
)

type traktorLocation struct {
	Dir    string `xml:"DIR,attr"`
	File   string `xml:"FILE,attr"`
	Volume string `xml:"VOLUME,attr"`
}

type traktorAlbum struct {
	Title string `xml:"TITLE,attr"`
	Track int    `xml:"TRACK,attr"`
}

type traktorInfo struct {
	Bitrate     int    `xml:"BITRATE,attr"` // bits/sec
	Genre       string `xml:"GENRE,attr"`
	Comment     string `xml:"COMMENT,attr"`
	Label       string `xml:"LABEL,attr"`
	Remixer     string `xml:"REMIXER,attr"`
	Key         string `xml:"KEY,attr"` // text key, e.g. "Am" (may be empty)
	Playtime    int    `xml:"PLAYTIME,attr"`
	ImportDate  string `xml:"IMPORT_DATE,attr"`
	ReleaseDate string `xml:"RELEASE_DATE,attr"`
	Filesize    int64  `xml:"FILESIZE,attr"` // kilobytes
	Ranking     int    `xml:"RANKING,attr"`  // 0-255
	Playcount   int    `xml:"PLAYCOUNT,attr"`
}

type traktorTempo struct {
	BPM float64 `xml:"BPM,attr"`
}

type traktorKey struct {
	Value int `xml:"VALUE,attr"` // 0-23
}

type traktorPlaylists struct {
	Root traktorNode `xml:"NODE"`
}

type traktorNode struct {
	Type     string           `xml:"TYPE,attr"` // "FOLDER" or "PLAYLIST"
	Name     string           `xml:"NAME,attr"`
	Subnodes []traktorNode    `xml:"SUBNODES>NODE"`
	Playlist *traktorPlaylist `xml:"PLAYLIST"`
}

type traktorPlaylist struct {
	Entries int                    `xml:"ENTRIES,attr"`
	Rows    []traktorPlaylistEntry `xml:"ENTRY"`
}

type traktorPlaylistEntry struct {
	PrimaryKey traktorPrimaryKey `xml:"PRIMARYKEY"`
}

type traktorPrimaryKey struct {
	Type string `xml:"TYPE,attr"` // "TRACK"
	Key  string `xml:"KEY,attr"`  // VOLUME+DIR+FILE location key
}

// ImportTraktorNML imports a Traktor collection.nml. It returns cues (hot cues,
// memory cues, and loops from CUE_V2) in the same MasterDBCue carrier the
// api.go applier already consumes; Traktor has no MyTags or per-track colours,
// so those two slices are always nil.
func ImportTraktorNML(lib *Library, nmlPath string) (*ImportResult, []PlaylistImport, []TagImport, []ColorImport, []MasterDBCue, error) {
	data, err := os.ReadFile(nmlPath)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("read nml: %w", err)
	}
	var doc traktorNML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("parse nml: %w", err)
	}
	log.Printf("import: Traktor NML v%d, %d entries declared",
		doc.Version, doc.Collection.Entries)

	res := &ImportResult{TracksTotal: len(doc.Collection.Tracks)}
	// Traktor playlists reference tracks by their location key (VOLUME+DIR+FILE
	// with Traktor's "/:" separators) rather than a numeric ID, so index by it.
	idMap := make(map[string]uint32, len(doc.Collection.Tracks))
	var cues []MasterDBCue
	for i := range doc.Collection.Tracks {
		t := &doc.Collection.Tracks[i]
		path := traktorLocationToPath(t.Location)
		if path == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("entry %q: empty LOCATION", t.Title))
			res.TracksSkipped++
			continue
		}
		track := traktorEntryToLibrary(t, path)

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
		idMap[traktorLocationKey(t.Location)] = id
		cues = append(cues, traktorCues(id, t.Cues)...)
	}

	// The root node is Traktor's "$ROOT" wrapper; import its children directly.
	imports := make([]PlaylistImport, 0, len(doc.Playlists.Root.Subnodes))
	for i := range doc.Playlists.Root.Subnodes {
		imports = append(imports, traktorNodeToImport(&doc.Playlists.Root.Subnodes[i], idMap))
	}
	res.PlaylistsTotal = countTraktorPlaylists(doc.Playlists.Root.Subnodes)

	lib.FinalizeBulk()
	return res, imports, nil, nil, cues, nil
}

// traktorCues converts a track's CUE_V2 markers to the neutral MasterDBCue
// carrier. Grid anchors (TYPE 4, the beat grid) are dropped; hot cues (HOTCUE
// 0-7) map to slots 1-8 to match the applier's convention, and memory markers
// are kept only when they are plain cues or loops (fade-in/out/load markers are
// skipped as clutter). Traktor CUE_V2 carries no per-cue colour.
func traktorCues(trackID uint32, cs []traktorCue) []MasterDBCue {
	var out []MasterDBCue
	for _, c := range cs {
		if c.Type == traktorCueTypeGrid {
			continue
		}
		isHot := c.Hotcue >= 0 && c.Hotcue <= 7
		if !isHot && c.Type != traktorCueTypeCue && c.Type != traktorCueTypeLoop {
			continue
		}
		mc := MasterDBCue{
			TrackID: trackID,
			HotCue:  -1,
			TimeMs:  uint32(c.Start + 0.5),
			LoopMs:  -1,
			Comment: c.Name,
		}
		if isHot {
			mc.HotCue = c.Hotcue + 1
		}
		if c.Type == traktorCueTypeLoop && c.Len > 0 {
			mc.LoopMs = int32(c.Len + 0.5)
		}
		out = append(out, mc)
	}
	return out
}

// traktorLocationKey rebuilds the collection key Traktor uses in playlist
// PRIMARYKEY entries: VOLUME + DIR + FILE, keeping the raw "/:" separators.
func traktorLocationKey(loc traktorLocation) string {
	return loc.Volume + loc.Dir + loc.File
}

var winDriveRe = regexp.MustCompile(`^[A-Za-z]:$`)

// traktorLocationToPath converts a Traktor LOCATION to a filesystem path.
// Traktor encodes the directory with "/:" as the separator (e.g.
// "/:Users/:me/:Music/:"); the real path is that with "/:" → "/" plus FILE. A
// Windows drive volume ("C:") is prepended; a macOS/Linux boot volume is left
// off (its paths are already absolute under "/"). Returns "" if there is no
// file component.
func traktorLocationToPath(loc traktorLocation) string {
	if loc.File == "" {
		return ""
	}
	path := strings.ReplaceAll(loc.Dir, "/:", "/") + loc.File
	if winDriveRe.MatchString(loc.Volume) {
		path = loc.Volume + path
	}
	return path
}

func traktorEntryToLibrary(t *traktorEntry, path string) *Track {
	ext := strings.ToLower(filepath.Ext(path))
	ft := strings.TrimPrefix(ext, ".")
	if ft == "aif" {
		ft = "aiff"
	}
	dateAdded, _ := time.Parse("2006/1/2", t.Info.ImportDate)
	if dateAdded.IsZero() {
		dateAdded = time.Now()
	}
	year := 0
	if rd, err := time.Parse("2006/1/2", t.Info.ReleaseDate); err == nil {
		year = rd.Year()
	}
	key := t.Info.Key
	if key == "" && t.MusicalKey != nil {
		key = traktorKeyName(t.MusicalKey.Value)
	}
	return &Track{
		Title:     t.Title,
		Artist:    t.Artist,
		Album:     t.Album.Title,
		Genre:     t.Info.Genre,
		Duration:  DurationSec(time.Duration(t.Info.Playtime) * time.Second),
		BPM:       t.Tempo.BPM,
		Key:       key,
		Rating:    uint8(t.Info.Ranking / 51), // Traktor 0-255 → 0-5 stars
		Year:      year,
		TrackNum:  t.Album.Track,
		FilePath:  path,
		FileType:  ft,
		FileSize:  t.Info.Filesize * 1024, // Traktor stores kilobytes
		Comment:   t.Info.Comment,
		Label:     t.Info.Label,
		Remixer:   t.Info.Remixer,
		DateAdded: dateAdded,
		Bitrate:   t.Info.Bitrate / 1000, // Traktor stores bits/sec → kbps
		PlayCount: t.Info.Playcount,
	}
}

// traktorKeyNames maps Traktor's MUSICAL_KEY VALUE (0-23) to standard notation:
// 0-11 are the chromatic major keys from C, 12-23 the chromatic minor keys.
var traktorKeyNames = [24]string{
	"C", "Db", "D", "Eb", "E", "F", "Gb", "G", "Ab", "A", "Bb", "B",
	"Cm", "Dbm", "Dm", "Ebm", "Em", "Fm", "Gbm", "Gm", "Abm", "Am", "Bbm", "Bm",
}

func traktorKeyName(v int) string {
	if v < 0 || v >= len(traktorKeyNames) {
		return ""
	}
	return traktorKeyNames[v]
}

func traktorNodeToImport(n *traktorNode, idMap map[string]uint32) PlaylistImport {
	if strings.EqualFold(n.Type, "FOLDER") {
		pi := PlaylistImport{Name: n.Name, IsFolder: true}
		for i := range n.Subnodes {
			pi.Children = append(pi.Children, traktorNodeToImport(&n.Subnodes[i], idMap))
		}
		return pi
	}
	pi := PlaylistImport{Name: n.Name}
	if n.Playlist != nil {
		for _, e := range n.Playlist.Rows {
			if id, ok := idMap[e.PrimaryKey.Key]; ok {
				pi.TrackIDs = append(pi.TrackIDs, id)
			}
		}
	}
	return pi
}

func countTraktorPlaylists(nodes []traktorNode) int {
	n := 0
	for i := range nodes {
		if strings.EqualFold(nodes[i].Type, "FOLDER") {
			n += countTraktorPlaylists(nodes[i].Subnodes)
		} else {
			n++
		}
	}
	return n
}

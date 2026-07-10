// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VirtualDJ keeps its library in a single database.xml (one per drive): a flat
// list of <Song> elements with metadata, analysis, and POIs (cues/loops).
// Playlists live in separate .vdjfolder files, so a database.xml import brings
// tracks and cues only. database.xml shares the .xml extension with a rekordbox
// export, so this importer is selected by sniffing the root element rather than
// the extension (see Handles / xmlRootIs).

type virtualDJDatabase struct {
	XMLName xml.Name  `xml:"VirtualDJ_Database"`
	Version string    `xml:"Version,attr"`
	Songs   []vdjSong `xml:"Song"`
}

type vdjSong struct {
	FilePath string   `xml:"FilePath,attr"`
	FileSize int64    `xml:"FileSize,attr"`
	Tags     vdjTags  `xml:"Tags"`
	Infos    vdjInfos `xml:"Infos"`
	Scan     vdjScan  `xml:"Scan"`
	Pois     []vdjPoi `xml:"Poi"`
	Comment  string   `xml:"Comment"`
}

type vdjTags struct {
	Author      string  `xml:"Author,attr"`
	Title       string  `xml:"Title,attr"`
	Album       string  `xml:"Album,attr"`
	Genre       string  `xml:"Genre,attr"`
	Label       string  `xml:"Label,attr"`
	Remix       string  `xml:"Remix,attr"`
	Composer    string  `xml:"Composer,attr"`
	Key         string  `xml:"Key,attr"`
	TrackNumber int     `xml:"TrackNumber,attr"`
	Year        int     `xml:"Year,attr"`
	Bpm         float64 `xml:"Bpm,attr"`
}

type vdjInfos struct {
	SongLength float64 `xml:"SongLength,attr"` // seconds
	Bitrate    int     `xml:"Bitrate,attr"`    // kbps
	PlayCount  int     `xml:"PlayCount,attr"`
	FirstSeen  int64   `xml:"FirstSeen,attr"` // unix seconds
}

type vdjScan struct {
	Bpm float64 `xml:"Bpm,attr"`
	Key string  `xml:"Key,attr"`
}

type vdjPoi struct {
	Pos  float64 `xml:"Pos,attr"`  // seconds
	Type string  `xml:"Type,attr"` // cue, loop, beatgrid, automix, ...
	Num  int     `xml:"Num,attr"`  // hot-cue pad number (1-8); 0/absent = unset
	Name string  `xml:"Name,attr"`
	Size float64 `xml:"Size,attr"` // loop length in beats
}

// ImportVirtualDJ imports a VirtualDJ database.xml (tracks + cues). Playlists
// live in separate .vdjfolder files and are not imported here.
func ImportVirtualDJ(lib *Library, xmlPath string) (*ImportBundle, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("read database.xml: %w", err)
	}
	var doc virtualDJDatabase
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse database.xml: %w", err)
	}
	log.Printf("import: VirtualDJ database v%s, %d songs", doc.Version, len(doc.Songs))

	res := &ImportResult{TracksTotal: len(doc.Songs)}
	var cues []ImportedCue
	for i := range doc.Songs {
		s := &doc.Songs[i]
		if s.FilePath == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("song %q: empty FilePath", s.Tags.Title))
			res.TracksSkipped++
			continue
		}
		track := vdjSongToLibrary(s)
		existing := lib.TrackByPath(s.FilePath)
		var id uint32
		if existing != nil {
			mergeTrack(existing, track)
			id = existing.ID
			res.TracksUpdated++
		} else {
			id = lib.AddTrackBulk(track)
			res.TracksAdded++
		}
		cues = append(cues, vdjCues(id, s.Pois, track.BPM)...)
	}
	lib.FinalizeBulk()
	return &ImportBundle{Result: res, Cues: cues}, nil
}

func vdjSongToLibrary(s *vdjSong) *Track {
	ext := strings.ToLower(filepath.Ext(s.FilePath))
	ft := strings.TrimPrefix(ext, ".")
	if ft == "aif" {
		ft = "aiff"
	}
	key := s.Tags.Key
	if key == "" {
		key = s.Scan.Key
	}
	bpm := vdjBPM(s.Scan.Bpm)
	if bpm == 0 {
		bpm = vdjBPM(s.Tags.Bpm)
	}
	dateAdded := time.Now()
	if s.Infos.FirstSeen > 0 {
		dateAdded = time.Unix(s.Infos.FirstSeen, 0)
	}
	return &Track{
		Title:     s.Tags.Title,
		Artist:    s.Tags.Author,
		Album:     s.Tags.Album,
		Genre:     s.Tags.Genre,
		Label:     s.Tags.Label,
		Remixer:   s.Tags.Remix,
		Key:       key,
		BPM:       bpm,
		Year:      s.Tags.Year,
		TrackNum:  s.Tags.TrackNumber,
		Duration:  DurationSec(time.Duration(s.Infos.SongLength * float64(time.Second))),
		FilePath:  s.FilePath,
		FileType:  ft,
		FileSize:  s.FileSize,
		Comment:   s.Comment,
		Bitrate:   s.Infos.Bitrate,
		PlayCount: s.Infos.PlayCount,
		DateAdded: dateAdded,
	}
}

// vdjBPM converts VirtualDJ's beat-period value (seconds per beat, e.g.
// 0.46875 = 128 BPM) to BPM. A value >= 10 is treated as an already-computed
// BPM; 0 or negative yields 0 (unknown).
func vdjBPM(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v < 10 {
		return 60.0 / v
	}
	return v
}

// vdjCues converts VirtualDJ POIs to cues: cue/loop POIs become cues (hot cues
// for Num 1-8, memory cues otherwise); beatgrid/automix and other markers are
// skipped. A loop's length (Size, in beats) becomes the loop end when the BPM
// is known. VirtualDJ POIs carry no colour we map, so hot cues take the default
// green like the other importers.
func vdjCues(trackID uint32, pois []vdjPoi, bpm float64) []ImportedCue {
	var out []ImportedCue
	for _, p := range pois {
		if p.Type != "cue" && p.Type != "loop" {
			continue
		}
		mc := ImportedCue{
			TrackID: trackID,
			HotCue:  -1,
			TimeMs:  uint32(p.Pos*1000 + 0.5),
			LoopMs:  -1,
			Comment: p.Name,
		}
		if p.Num >= 1 && p.Num <= 8 {
			mc.HotCue = p.Num
			mc.ColorID = defaultHotCueColorID // hot cues default to green
		}
		if p.Type == "loop" && p.Size > 0 && bpm > 0 {
			mc.LoopMs = int32(float64(mc.TimeMs) + p.Size*60000.0/bpm + 0.5)
		}
		out = append(out, mc)
	}
	return out
}

// --- Importer registration ---

type virtualdjImporter struct{}

func (virtualdjImporter) Label() string { return "VirtualDJ" }
func (virtualdjImporter) Handles(p string) bool {
	return hasExt(p, ".xml") && xmlRootIs(p, "VirtualDJ_Database")
}
func (virtualdjImporter) RequiresKey(string) bool { return false }

func (virtualdjImporter) Import(lib *Library, o ImportOptions) (*ImportBundle, error) {
	if !o.WantTracks {
		return &ImportBundle{}, nil
	}
	return ImportVirtualDJ(lib, o.Path)
}

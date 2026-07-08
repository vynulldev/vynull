// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/xml"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RekordboxXML is the schema for a `File > Export collection in XML` from
// rekordbox 4-6+. Documents tracks, playlists, and a few other bits in
// a deterministic format we can read without any encryption keys.
type RekordboxXML struct {
	XMLName    xml.Name     `xml:"DJ_PLAYLISTS"`
	Product    rbxmlProduct `xml:"PRODUCT"`
	Collection rbxmlColl    `xml:"COLLECTION"`
	Playlists  rbxmlPLRoot  `xml:"PLAYLISTS"`
}

type rbxmlProduct struct {
	Name    string `xml:"Name,attr"`
	Version string `xml:"Version,attr"`
}

type rbxmlColl struct {
	Entries int          `xml:"Entries,attr"`
	Tracks  []rbxmlTrack `xml:"TRACK"`
}

type rbxmlTrack struct {
	TrackID     string `xml:"TrackID,attr"`
	Name        string `xml:"Name,attr"`
	Artist      string `xml:"Artist,attr"`
	Composer    string `xml:"Composer,attr"`
	Album       string `xml:"Album,attr"`
	Grouping    string `xml:"Grouping,attr"`
	Genre       string `xml:"Genre,attr"`
	Kind        string `xml:"Kind,attr"`
	Size        int64  `xml:"Size,attr"`
	TotalTime   int    `xml:"TotalTime,attr"`
	DiscNumber  int    `xml:"DiscNumber,attr"`
	TrackNumber int    `xml:"TrackNumber,attr"`
	Year        int    `xml:"Year,attr"`
	AverageBpm  string `xml:"AverageBpm,attr"`
	DateAdded   string `xml:"DateAdded,attr"`
	BitRate     int    `xml:"BitRate,attr"`
	SampleRate  int    `xml:"SampleRate,attr"`
	Comments    string `xml:"Comments,attr"`
	PlayCount   int    `xml:"PlayCount,attr"`
	Rating      int    `xml:"Rating,attr"`
	Location    string `xml:"Location,attr"`
	Remixer     string `xml:"Remixer,attr"`
	Tonality    string `xml:"Tonality,attr"`
	Label       string `xml:"Label,attr"`
	Mix         string `xml:"Mix,attr"`
	Colour      string `xml:"Colour,attr"` // track colour label, e.g. "0xFF007F"
}

// rbColorIDs maps the rekordbox XML Colour hex values to the app's 8-entry
// track-colour palette IDs (1=Pink … 8=Purple; see trackColorNames in the
// api package). These eight hex codes are the fixed set rekordbox emits for
// its track colour labels. Anything else (or "") maps to 0 = no colour.
var rbColorIDs = map[string]uint8{
	"0XFF007F": 1, // pink
	"0XFF0000": 2, // red
	"0XFFA500": 3, // orange
	"0XFFFF00": 4, // yellow
	"0X00FF00": 5, // green
	"0X25FDE9": 6, // aqua
	"0X0000FF": 7, // blue
	"0X660099": 8, // purple
}

// colorIDFromHex resolves a rekordbox XML Colour attribute to a palette ID.
func colorIDFromHex(hex string) uint8 {
	return rbColorIDs[strings.ToUpper(strings.TrimSpace(hex))]
}

// ColorImport is a track colour label resolved from the rekordbox XML's
// Colour attribute, paired with the library Track.ID it applies to. The
// caller persists these to the tag store (the app's authoritative colour
// source). Only tracks with a recognised colour appear here.
type ColorImport struct {
	TrackID uint32
	ColorID uint8
}

type rbxmlPLRoot struct {
	Node rbxmlNode `xml:"NODE"`
}

type rbxmlNode struct {
	Type    int            `xml:"Type,attr"` // 0 = folder, 1 = playlist
	Name    string         `xml:"Name,attr"`
	KeyType int            `xml:"KeyType,attr"`
	Entries int            `xml:"Entries,attr"`
	Count   int            `xml:"Count,attr"`
	Tracks  []rbxmlPLTrack `xml:"TRACK"`
	Nodes   []rbxmlNode    `xml:"NODE"`
}

type rbxmlPLTrack struct {
	Key string `xml:"Key,attr"` // references rbxmlTrack.TrackID
}

// ImportResult summarizes what an import did.
type ImportResult struct {
	TracksTotal      int
	TracksAdded      int
	TracksUpdated    int
	TracksSkipped    int
	PlaylistsTotal   int
	TagsTotal        int
	ArtworkImported  int // cover-art images imported from a backup's share/ tree
	AnalysisImported int // ANLZ analysis sets (waveforms/beat grids) imported
	CuesImported     int // hot/memory cue points imported from ANLZ cue lists
	SettingsImported int // rekordbox MYSETTING/MYSETTING2/DJMMYSETTING/DEVSETTING files merged (zip only)
	FilesMissing     int // imported tracks whose audio file doesn't exist locally (unplayable)
	Errors           []string
}

// PlaylistImport is the public, resolved form of a rekordbox playlist
// or folder node — track IDs are already mapped to library Track.IDs
// so the caller can materialize directly without touching XML types.
type PlaylistImport struct {
	Name     string
	IsFolder bool
	TrackIDs []uint32
	Children []PlaylistImport
	// Smart-playlist rules (master.db imports only). When IsSmart is true the
	// caller should create a smart playlist from Smart's rules rather than
	// assigning TrackIDs (which are empty — membership is rule-based).
	IsSmart bool
	Smart   *RBSmartList
}

// TagImport is a MyTag extracted from the rekordbox XML's Comments
// field plus the library Track.IDs it applies to. The caller
// materializes these into the tag store.
type TagImport struct {
	Name     string
	Category string // owning MyTag category name; "" = uncategorized
	TrackIDs []uint32
}

// myTagComment matches rekordbox's "Add MyTag to Comments" encoding:
// the comment is (or starts with) "/* Tag1 / Tag2 / Tag3 */". Captures
// the inner tag list. Anything outside the markers is the real comment.
var myTagComment = regexp.MustCompile(`/\*\s*(.*?)\s*\*/`)

// extractMyTags pulls MyTag names out of a rekordbox comment string.
// Returns the tag names and the comment with the "/* … */" block
// removed (so the track's stored comment doesn't carry the raw tag
// markup). When the comment has no MyTag block, returns (nil, comment).
func extractMyTags(comment string) ([]string, string) {
	m := myTagComment.FindStringSubmatch(comment)
	if m == nil {
		return nil, comment
	}
	var tags []string
	for _, t := range strings.Split(m[1], "/") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	cleaned := strings.TrimSpace(myTagComment.ReplaceAllString(comment, ""))
	return tags, cleaned
}

func nodeToImport(n *rbxmlNode, idMap map[string]uint32) PlaylistImport {
	pi := PlaylistImport{
		Name:     n.Name,
		IsFolder: n.Type == 0,
	}
	if !pi.IsFolder {
		for _, t := range n.Tracks {
			if id, ok := idMap[t.Key]; ok {
				pi.TrackIDs = append(pi.TrackIDs, id)
			}
		}
	}
	for _, c := range n.Nodes {
		pi.Children = append(pi.Children, nodeToImport(&c, idMap))
	}
	return pi
}

// ImportRekordboxXML reads a rekordbox-exported XML file and merges its
// tracks + playlists into the given library. Existing tracks (matched by
// file path) are UPDATED with the XML's metadata (per the user's chosen
// merge policy "imported metadata wins"); new tracks are added. Returns
// counts for UI display.
//
// The second return is the resolved playlist tree (children of the
// root NODE) with track IDs already mapped to library Track.IDs; the
// third is the MyTags extracted from track comments (empty unless the
// export had the "Add MyTag to Comments" preference enabled); the fourth
// is the track colour labels resolved from each TRACK's Colour attribute.
func ImportRekordboxXML(lib *Library, xmlPath string) (*ImportBundle, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("read xml: %w", err)
	}
	var doc RekordboxXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}
	log.Printf("import: %s v%s, %d tracks declared",
		doc.Product.Name, doc.Product.Version, doc.Collection.Entries)

	res := &ImportResult{TracksTotal: len(doc.Collection.Tracks)}
	idMap := make(map[string]uint32, len(doc.Collection.Tracks))
	tagTracks := map[string][]uint32{} // tag name → library track IDs
	var colors []ColorImport
	for _, t := range doc.Collection.Tracks {
		path, err := locationToPath(t.Location)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("track %s: bad Location: %v", t.Name, err))
			res.TracksSkipped++
			continue
		}
		track := xmlTrackToLibrary(&t, path)

		// MyTags ride inside the Comments field when the user enabled
		// "Add MyTag to Comments" in rekordbox. Pull them out, and
		// strip the markup so the stored comment is the real comment.
		tags, cleaned := extractMyTags(track.Comment)
		track.Comment = cleaned

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
		if t.TrackID != "" {
			idMap[t.TrackID] = id
		}
		for _, name := range tags {
			tagTracks[name] = append(tagTracks[name], id)
		}
		if track.ColorID > 0 {
			colors = append(colors, ColorImport{TrackID: id, ColorID: track.ColorID})
		}
	}

	res.PlaylistsTotal = countPlaylists(&doc.Playlists.Node)
	imports := make([]PlaylistImport, 0, len(doc.Playlists.Node.Nodes))
	for _, n := range doc.Playlists.Node.Nodes {
		imports = append(imports, nodeToImport(&n, idMap))
	}

	tags := make([]TagImport, 0, len(tagTracks))
	for name, ids := range tagTracks {
		tags = append(tags, TagImport{Name: name, TrackIDs: ids})
	}
	res.TagsTotal = len(tags)
	lib.FinalizeBulk()
	return &ImportBundle{Result: res, Playlists: imports, Tags: tags, Colors: colors}, nil
}

// locationToPath converts a rekordbox XML Location ("file://localhost/...")
// to an absolute filesystem path. URL-decodes percent escapes; strips the
// Windows drive-letter prefix when needed so paths land in the user's
// actual /Contents/ tree.
func locationToPath(loc string) (string, error) {
	if loc == "" {
		return "", fmt.Errorf("empty Location")
	}
	// rekordbox always emits file://localhost/...
	loc = strings.TrimPrefix(loc, "file://localhost")
	loc = strings.TrimPrefix(loc, "file://")
	p, err := url.PathUnescape(loc)
	if err != nil {
		return "", err
	}
	// Windows-style paths come back as "/C:/Users/..." — strip the leading
	// slash so it's a valid path on the host side (we leave drive-letter
	// resolution to the caller).
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p, nil
}

func xmlTrackToLibrary(t *rbxmlTrack, path string) *Track {
	ext := strings.ToLower(filepath.Ext(path))
	ft := strings.TrimPrefix(ext, ".")
	if ft == "aif" {
		ft = "aiff"
	}
	bpm, _ := strconv.ParseFloat(t.AverageBpm, 64)
	dateAdded, _ := time.Parse("2006-01-02", t.DateAdded)
	if dateAdded.IsZero() {
		dateAdded = time.Now()
	}
	tr := &Track{
		Title:      t.Name,
		Artist:     t.Artist,
		Album:      t.Album,
		Genre:      t.Genre,
		Duration:   DurationSec(time.Duration(t.TotalTime) * time.Second),
		BPM:        bpm,
		Key:        t.Tonality,
		Rating:     uint8(t.Rating / 20), // rekordbox XML 0-100 → 0-5 stars
		Year:       t.Year,
		TrackNum:   t.TrackNumber,
		DiscNum:    t.DiscNumber,
		FilePath:   path,
		FileType:   ft,
		FileSize:   t.Size,
		Comment:    t.Comments,
		Label:      t.Label,
		MixName:    t.Mix,
		Remixer:    t.Remixer,
		DateAdded:  dateAdded,
		Bitrate:    t.BitRate,
		SampleRate: t.SampleRate,
		PlayCount:  t.PlayCount,
		ColorID:    colorIDFromHex(t.Colour),
	}
	return tr
}

// mergeTrack copies non-zero fields from src onto dst. The user's chosen
// policy is "imported metadata wins", so we overwrite even when dst has
// existing values, except for fields that the library populates itself
// (ID, ArtID, DateAdded if dst is older).
func mergeTrack(dst, src *Track) {
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.Artist != "" {
		dst.Artist = src.Artist
	}
	if src.Album != "" {
		dst.Album = src.Album
	}
	if src.Genre != "" {
		dst.Genre = src.Genre
	}
	if src.BPM > 0 {
		dst.BPM = src.BPM
	}
	if src.Key != "" {
		dst.Key = src.Key
	}
	if src.Rating > 0 {
		dst.Rating = src.Rating
	}
	if src.Year > 0 {
		dst.Year = src.Year
	}
	if src.TrackNum > 0 {
		dst.TrackNum = src.TrackNum
	}
	if src.DiscNum > 0 {
		dst.DiscNum = src.DiscNum
	}
	if src.Comment != "" {
		dst.Comment = src.Comment
	}
	if src.Label != "" {
		dst.Label = src.Label
	}
	if src.MixName != "" {
		dst.MixName = src.MixName
	}
	if src.Remixer != "" {
		dst.Remixer = src.Remixer
	}
	if src.Bitrate > 0 {
		dst.Bitrate = src.Bitrate
	}
	if src.SampleRate > 0 {
		dst.SampleRate = src.SampleRate
	}
	if src.PlayCount > 0 {
		dst.PlayCount = src.PlayCount
	}
	if src.ColorID > 0 {
		dst.ColorID = src.ColorID
	}
	if src.Duration > 0 {
		dst.Duration = src.Duration
	}
	if src.FileSize > 0 {
		dst.FileSize = src.FileSize
	}
}

func countPlaylists(n *rbxmlNode) int {
	c := 0
	if n.Type == 1 {
		c++
	}
	for _, child := range n.Nodes {
		c += countPlaylists(&child)
	}
	return c
}

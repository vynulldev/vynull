// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
)

// FileTags is the metadata read from an audio file's tags. Scanning a folder,
// adding a file and reimporting a track's tags all read it the same way.
type FileTags struct {
	Title, Artist, Album, Genre, Comment string
	Year, TrackNum, DiscNum              int
	// Label, Remixer, OriginalArtist and MixName are nil when the file has no
	// such tag, so applying the tags leaves the track's value alone.
	Label, Remixer, OriginalArtist, MixName *string
	// Picture is the embedded artwork, if any.
	PictureMIME string
	Picture     []byte
}

// ReadFileTags reads the tags of the audio file at path.
func ReadFileTags(path string) (FileTags, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileTags{}, err
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return FileTags{}, err
	}
	ft := FileTags{
		Title:   m.Title(),
		Artist:  m.Artist(),
		Album:   m.Album(),
		Genre:   m.Genre(),
		Comment: m.Comment(),
		Year:    m.Year(),
	}
	ft.TrackNum, _ = m.Track()
	ft.DiscNum, _ = m.Disc()
	// The first key present wins: the names differ between ID3 frames,
	// Vorbis comments and MP4 atoms.
	raw := m.Raw()
	first := func(keys ...string) *string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				s := fmt.Sprintf("%v", v)
				return &s
			}
		}
		return nil
	}
	ft.Label = first("label", "LABEL", "publisher", "TPUB", "ORGANIZATION", "organization")
	ft.Remixer = first("remixer", "TPE4", "MIXARTIST")
	ft.OriginalArtist = first("original artist", "TOPE")
	ft.MixName = first("mix", "TSST")
	if pic := m.Picture(); pic != nil && len(pic.Data) > 0 {
		ft.PictureMIME, ft.Picture = pic.MIMEType, pic.Data
	}
	return ft, nil
}

// ApplyTo copies the tags onto t, as reading a file's tags does: the main
// fields are replaced (empty when the file has no such tag), the optional ones
// only when present, and a track left without a title takes its file name.
// Artwork isn't applied: the caller adds Picture to the artwork cache.
func (ft FileTags) ApplyTo(t *Track) {
	t.Title, t.Artist, t.Album, t.Genre, t.Comment = ft.Title, ft.Artist, ft.Album, ft.Genre, ft.Comment
	t.Year, t.TrackNum, t.DiscNum = ft.Year, ft.TrackNum, ft.DiscNum
	for _, f := range []struct {
		dst *string
		v   *string
	}{{&t.Label, ft.Label}, {&t.Remixer, ft.Remixer}, {&t.OriginalArtist, ft.OriginalArtist}, {&t.MixName, ft.MixName}} {
		if f.v != nil {
			*f.dst = *f.v
		}
	}
	if t.Title == "" {
		t.Title = strings.TrimSuffix(filepath.Base(t.FilePath), filepath.Ext(t.FilePath))
	}
}

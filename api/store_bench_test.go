// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/internal/benchlib"
)

// BenchmarkPlaylistUpdate measures what the history append waits for on the
// deck status goroutine: replacing one playlist's tracks in a store of 40
// playlists of 250 tracks each.
func BenchmarkPlaylistUpdate(b *testing.B) {
	defer log.SetOutput(log.Writer()) // keep the store's log out of benchstat's lines
	log.SetOutput(io.Discard)
	ps := NewPlaylistStore(b.TempDir())
	var last uint32
	ids := make([]uint32, 250)
	for i := range 40 {
		p, err := ps.Create(fmt.Sprintf("Playlist %d", i), 0, false)
		if err != nil {
			b.Fatal(err)
		}
		for j := range ids {
			ids[j] = uint32(i*250 + j + 1)
		}
		ps.SetTracks(p.ID, ids)
		last = p.ID
	}
	b.ResetTimer()
	for i := range b.N {
		ids[0] = uint32(i)
		ps.SetTracks(last, ids)
	}
}

// BenchmarkTagAssign measures setting one track's tags from the web UI, with
// a quarter of a 20k-track library tagged (three tags each).
func BenchmarkTagAssign(b *testing.B) {
	defer log.SetOutput(log.Writer()) // keep the store's log out of benchstat's lines
	log.SetOutput(io.Discard)
	dir := b.TempDir()
	tags := storeFileData{NextCatID: 1, NextTagID: 48}
	var ids []uint32
	for i := range 47 {
		ids = append(ids, uint32(i+1))
		tags.Tags = append(tags.Tags, TagInfo{ID: uint32(i + 1), Name: fmt.Sprintf("Tag %d", i)})
	}
	assigned := map[uint32][]uint32{}
	for i := range benchlib.Size / 4 {
		assigned[uint32(i+1)] = []uint32{ids[i%47], ids[(i+7)%47], ids[(i+19)%47]}
	}
	for name, v := range map[string]any{"tags.json": tags, "track_tags.json": assigned} {
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	ts := NewTagStore(dir)
	b.ResetTimer()
	for i := range b.N {
		ts.SetTagsForTrack(1, []uint32{ids[i%47]})
	}
}

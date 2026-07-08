// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleNML = `<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<NML VERSION="19">
  <COLLECTION ENTRIES="2">
    <ENTRY MODIFIED_DATE="2024/1/2" TITLE="First Track" ARTIST="Alpha">
      <LOCATION DIR="/:Users/:me/:Music/:" FILE="first.mp3" VOLUME="Macintosh HD"></LOCATION>
      <ALBUM TITLE="Debut" TRACK="3"></ALBUM>
      <INFO BITRATE="320000" GENRE="Techno" COMMENT="hello" LABEL="Lbl" RANKING="204"
            PLAYTIME="360" IMPORT_DATE="2024/1/2" RELEASE_DATE="2020/5/1"
            FILESIZE="8192" PLAYCOUNT="7" KEY="Am"></INFO>
      <TEMPO BPM="128.000000" BPM_QUALITY="100.000000"></TEMPO>
      <MUSICAL_KEY VALUE="21"></MUSICAL_KEY>
      <CUE_V2 NAME="AutoGrid" TYPE="4" START="0.0" LEN="0.0" HOTCUE="-1"></CUE_V2>
      <CUE_V2 NAME="Intro" TYPE="0" START="1000.0" LEN="0.0" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="Drop" TYPE="0" START="2000.4" LEN="0.0" HOTCUE="2"></CUE_V2>
      <CUE_V2 NAME="Mem" TYPE="0" START="5000.0" LEN="0.0" HOTCUE="-1"></CUE_V2>
      <CUE_V2 NAME="TheLoop" TYPE="5" START="8000.0" LEN="2000.0" HOTCUE="1"></CUE_V2>
      <CUE_V2 NAME="FadeIn" TYPE="1" START="9000.0" LEN="0.0" HOTCUE="-1"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="Second Track" ARTIST="Beta">
      <LOCATION DIR="/:Users/:me/:Music/:" FILE="second.flac" VOLUME="Macintosh HD"></LOCATION>
      <INFO GENRE="House" PLAYTIME="200"></INFO>
      <TEMPO BPM="124.500000"></TEMPO>
      <MUSICAL_KEY VALUE="0"></MUSICAL_KEY>
    </ENTRY>
  </COLLECTION>
  <PLAYLISTS>
    <NODE TYPE="FOLDER" NAME="$ROOT">
      <SUBNODES COUNT="2">
        <NODE TYPE="PLAYLIST" NAME="Warmup">
          <PLAYLIST ENTRIES="2" TYPE="LIST">
            <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="Macintosh HD/:Users/:me/:Music/:first.mp3"></PRIMARYKEY></ENTRY>
            <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="Macintosh HD/:Users/:me/:Music/:second.flac"></PRIMARYKEY></ENTRY>
          </PLAYLIST>
        </NODE>
        <NODE TYPE="FOLDER" NAME="Gigs">
          <SUBNODES COUNT="1">
            <NODE TYPE="PLAYLIST" NAME="Peak">
              <PLAYLIST ENTRIES="1" TYPE="LIST">
                <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="Macintosh HD/:Users/:me/:Music/:first.mp3"></PRIMARYKEY></ENTRY>
              </PLAYLIST>
            </NODE>
          </SUBNODES>
        </NODE>
      </SUBNODES>
    </NODE>
  </PLAYLISTS>
</NML>`

func TestImportTraktorNML(t *testing.T) {
	dir := t.TempDir()
	nml := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(nml, []byte(sampleNML), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := NewLibrary(nil, nil)
	res, playlists, tags, colors, cues, err := ImportTraktorNML(lib, nml)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if tags != nil || colors != nil {
		t.Errorf("expected nil tags/colors, got %v / %v", tags, colors)
	}
	if res.TracksTotal != 2 || res.TracksAdded != 2 {
		t.Errorf("counts: total=%d added=%d, want 2/2", res.TracksTotal, res.TracksAdded)
	}

	tr := lib.TrackByPath("/Users/me/Music/first.mp3")
	if tr == nil {
		t.Fatal("first.mp3 not imported at reconstructed path")
	}
	if tr.Title != "First Track" || tr.Artist != "Alpha" || tr.Album != "Debut" {
		t.Errorf("metadata: %q / %q / %q", tr.Title, tr.Artist, tr.Album)
	}
	if tr.BPM != 128.0 {
		t.Errorf("bpm=%v want 128", tr.BPM)
	}
	if tr.Key != "Am" { // INFO KEY wins over MUSICAL_KEY
		t.Errorf("key=%q want Am", tr.Key)
	}
	if tr.Rating != 4 { // 204/51
		t.Errorf("rating=%d want 4", tr.Rating)
	}
	if tr.Genre != "Techno" || tr.Label != "Lbl" || tr.PlayCount != 7 {
		t.Errorf("genre/label/playcount: %q/%q/%d", tr.Genre, tr.Label, tr.PlayCount)
	}
	if tr.Bitrate != 320 {
		t.Errorf("bitrate=%d want 320 kbps", tr.Bitrate)
	}
	if tr.Year != 2020 {
		t.Errorf("year=%d want 2020", tr.Year)
	}
	if tr.TrackNum != 3 {
		t.Errorf("tracknum=%d want 3", tr.TrackNum)
	}

	// Cues: the grid anchor (TYPE 4) and fade-in (TYPE 1 memory) are dropped;
	// the two hot cues, one memory cue, and one hot loop remain, in order.
	if len(cues) != 4 {
		t.Fatalf("cues=%d want 4: %+v", len(cues), cues)
	}
	want := []MasterDBCue{
		{TrackID: tr.ID, HotCue: 1, TimeMs: 1000, LoopMs: -1, Comment: "Intro"},     // HOTCUE 0 → slot 1
		{TrackID: tr.ID, HotCue: 3, TimeMs: 2000, LoopMs: -1, Comment: "Drop"},      // 2000.4 rounds down
		{TrackID: tr.ID, HotCue: -1, TimeMs: 5000, LoopMs: -1, Comment: "Mem"},      // memory cue
		{TrackID: tr.ID, HotCue: 2, TimeMs: 8000, LoopMs: 2000, Comment: "TheLoop"}, // hot loop
	}
	for i, w := range want {
		if cues[i] != w {
			t.Errorf("cue[%d] = %+v, want %+v", i, cues[i], w)
		}
	}

	// Second track: no INFO KEY, so MUSICAL_KEY VALUE=0 → "C".
	tr2 := lib.TrackByPath("/Users/me/Music/second.flac")
	if tr2 == nil || tr2.Key != "C" || tr2.FileType != "flac" {
		t.Errorf("second track: %+v", tr2)
	}

	// Playlist tree: root has [Warmup(2 tracks), Gigs(folder → Peak(1 track))].
	if res.PlaylistsTotal != 2 {
		t.Errorf("playlists total=%d want 2", res.PlaylistsTotal)
	}
	if len(playlists) != 2 {
		t.Fatalf("top-level nodes=%d want 2", len(playlists))
	}
	warmup := playlists[0]
	if warmup.Name != "Warmup" || warmup.IsFolder || len(warmup.TrackIDs) != 2 {
		t.Errorf("warmup: %+v", warmup)
	}
	gigs := playlists[1]
	if gigs.Name != "Gigs" || !gigs.IsFolder || len(gigs.Children) != 1 {
		t.Fatalf("gigs: %+v", gigs)
	}
	peak := gigs.Children[0]
	if peak.Name != "Peak" || len(peak.TrackIDs) != 1 || peak.TrackIDs[0] != tr.ID {
		t.Errorf("peak: %+v (want 1 track = %d)", peak, tr.ID)
	}
}

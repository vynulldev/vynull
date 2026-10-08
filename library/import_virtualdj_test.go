// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleVDJ = `<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="2023">
  <Song FilePath="/Users/me/Music/first.mp3" FileSize="8388608">
    <Tags Author="Alpha" Title="First" Album="Debut" Genre="Techno" Label="Lbl" Year="2020" TrackNumber="3" Key="Am" Bpm="0.468750"></Tags>
    <Infos SongLength="360.5" Bitrate="320" PlayCount="7" FirstSeen="1600000000"></Infos>
    <Scan Bpm="0.468750" Key="Am"></Scan>
    <Poi Pos="1.5" Type="cue" Num="1" Name="Intro"></Poi>
    <Poi Pos="30.0" Type="cue" Num="3" Name="Drop"></Poi>
    <Poi Pos="60.0" Type="loop" Num="2" Size="4" Name="Loop"></Poi>
    <Poi Pos="0.0" Type="beatgrid"></Poi>
    <Poi Pos="90.0" Type="cue" Num="0" Name="Mem"></Poi>
  </Song>
  <Song FilePath="/Users/me/Music/second.flac" FileSize="20000000">
    <Tags Author="Beta" Title="Second" Genre="House" Bpm="0.5"></Tags>
    <Infos SongLength="200" Bitrate="1000"></Infos>
  </Song>
</VirtualDJ_Database>`

func TestImportVirtualDJ(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "database.xml")
	if err := os.WriteFile(dbPath, []byte(sampleVDJ), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := NewLibrary(nil, nil)
	bundle, err := ImportVirtualDJ(lib, dbPath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if bundle.Result.TracksTotal != 2 || bundle.Result.TracksAdded != 2 {
		t.Errorf("counts: total=%d added=%d, want 2/2", bundle.Result.TracksTotal, bundle.Result.TracksAdded)
	}

	tr := lib.TrackByPath("/Users/me/Music/first.mp3")
	if tr == nil {
		t.Fatal("first.mp3 not imported")
	}
	if tr.Title != "First" || tr.Artist != "Alpha" || tr.Album != "Debut" || tr.Genre != "Techno" {
		t.Errorf("metadata: %q/%q/%q/%q", tr.Title, tr.Artist, tr.Album, tr.Genre)
	}
	if tr.BPM != 128 { // 60 / 0.46875
		t.Errorf("bpm=%v want 128 (period 0.46875)", tr.BPM)
	}
	if tr.Key != "Am" || tr.Label != "Lbl" || tr.Year != 2020 || tr.TrackNum != 3 {
		t.Errorf("key/label/year/tracknum: %q/%q/%d/%d", tr.Key, tr.Label, tr.Year, tr.TrackNum)
	}
	if tr.Bitrate != 320 || tr.PlayCount != 7 || tr.FileType != "mp3" {
		t.Errorf("bitrate/playcount/filetype: %d/%d/%q", tr.Bitrate, tr.PlayCount, tr.FileType)
	}
	if int(time.Duration(tr.Duration).Seconds()) != 360 {
		t.Errorf("duration=%v want ~360s", time.Duration(tr.Duration))
	}

	// Second track: BPM from Tags (Scan absent) → 60/0.5 = 120; flac.
	if tr2 := lib.TrackByPath("/Users/me/Music/second.flac"); tr2 == nil || tr2.BPM != 120 || tr2.FileType != "flac" {
		t.Errorf("second track: %+v", tr2)
	}

	// Cues: beatgrid POI dropped; two hot cues, one hot loop, one memory cue.
	want := []ImportedCue{
		{TrackID: tr.ID, HotCue: 1, TimeMs: 1500, LoopMs: -1, ColorID: 0x16, Comment: "Intro"},
		{TrackID: tr.ID, HotCue: 3, TimeMs: 30000, LoopMs: -1, ColorID: 0x16, Comment: "Drop"},
		{TrackID: tr.ID, HotCue: 2, TimeMs: 60000, LoopMs: 61875, ColorID: 0x16, Comment: "Loop"}, // 4 beats @128bpm = 1875ms
		{TrackID: tr.ID, HotCue: -1, TimeMs: 90000, LoopMs: -1, ColorID: 0, Comment: "Mem"},
	}
	if len(bundle.Cues) != len(want) {
		t.Fatalf("cues=%d want %d: %+v", len(bundle.Cues), len(want), bundle.Cues)
	}
	for i, w := range want {
		if bundle.Cues[i] != w {
			t.Errorf("cue[%d] = %+v, want %+v", i, bundle.Cues[i], w)
		}
	}
}

func TestImportVirtualDJPlaylists(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "database.xml"), []byte(sampleVDJ), 0o644)
	// A Folders/ tree next to database.xml: a leaf playlist, a nested folder,
	// and a smart/empty folder that should be dropped.
	folders := filepath.Join(dir, "Folders")
	os.MkdirAll(filepath.Join(folders, "Sets"), 0o755)
	os.WriteFile(filepath.Join(folders, "Warmup.vdjfolder"),
		[]byte(`<VirtualFolder><song path="/Users/me/Music/first.mp3"></song><song path="/Users/me/Music/second.flac"></song></VirtualFolder>`), 0o644)
	os.WriteFile(filepath.Join(folders, "Sets", "Peak.vdjfolder"),
		[]byte(`<VirtualFolder><song path="/Users/me/Music/first.mp3"></song></VirtualFolder>`), 0o644)
	os.WriteFile(filepath.Join(folders, "Empty.vdjfolder"),
		[]byte(`<VirtualFolder><filter>bpm&gt;120</filter></VirtualFolder>`), 0o644)

	lib := NewLibrary(nil, nil)
	bundle, err := ImportVirtualDJ(lib, filepath.Join(dir, "database.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Result.PlaylistsTotal != 2 { // Warmup + Sets/Peak; Empty dropped
		t.Errorf("PlaylistsTotal=%d want 2", bundle.Result.PlaylistsTotal)
	}
	byName := map[string]PlaylistImport{}
	for _, p := range bundle.Playlists {
		byName[p.Name] = p
	}
	if w := byName["Warmup"]; w.IsFolder || len(w.TrackIDs) != 2 {
		t.Errorf("Warmup: %+v", w)
	}
	sets, ok := byName["Sets"]
	if !ok || !sets.IsFolder || len(sets.Children) != 1 {
		t.Fatalf("Sets: %+v", sets)
	}
	if peak := sets.Children[0]; peak.Name != "Peak" || len(peak.TrackIDs) != 1 {
		t.Errorf("Peak: %+v", peak)
	}
	if _, present := byName["Empty"]; present {
		t.Error("Empty smart folder (no resolved songs) should have been dropped")
	}
}

// TestImporterForXMLDispatch checks that .xml files route to the right importer
// by root element — VirtualDJ and rekordbox both use the .xml extension.
func TestImporterForXMLDispatch(t *testing.T) {
	dir := t.TempDir()
	vdj := filepath.Join(dir, "database.xml")
	rbx := filepath.Join(dir, "collection.xml")
	os.WriteFile(vdj, []byte(sampleVDJ), 0o644)
	os.WriteFile(rbx, []byte(`<?xml version="1.0"?><DJ_PLAYLISTS Version="1.0.0"><COLLECTION Entries="0"></COLLECTION></DJ_PLAYLISTS>`), 0o644)

	if imp := ImporterFor(vdj); imp == nil || imp.Label() != "VirtualDJ" {
		t.Errorf("database.xml → %v, want VirtualDJ", imp)
	}
	if imp := ImporterFor(rbx); imp == nil || imp.Label() != "rekordbox XML" {
		t.Errorf("rekordbox .xml → %v, want rekordbox XML", imp)
	}
}

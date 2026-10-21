// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// files lists the cue store's files in dir.
func cueFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A track's files follow its cues: a deck cue keeps its blob, a cue without
// one has no .bin, a deleted cue's blob goes, and a track with no cues left
// has no files.
func TestCueStoreFilesFollowCues(t *testing.T) {
	dir := t.TempDir()
	cs := NewCueStore(dir)
	deck := makeNXS2CueBlob(1, 0x2a, 0, 0, 0)
	cs.SaveCue(7, &CuePoint{Number: 1, Type: 1, LoopMs: -1, Status: 1, ColorID: 0x2a}, deck)
	cs.SaveCue(7, &CuePoint{Number: 2, Type: 1, TimeMs: 500, LoopMs: -1, Status: 1}, nil)
	if err := cs.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := cueFiles(t, dir), []string{"cue_7_1.bin", "cues_7.json"}; !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	re := NewCueStore(dir)
	if n := len(re.GetCues(7)); n != 2 {
		t.Fatalf("reloaded %d cues, want 2", n)
	}
	if got := re.GetCombinedBlob(7); !bytes.HasPrefix(got, deck) || len(got) != 2*124 {
		t.Errorf("reloaded blob: %d bytes, want the deck's blob then a synthesized one", len(got))
	}

	cs.DeleteCue(7, 1)
	cs.Flush()
	if got, want := cueFiles(t, dir), []string{"cues_7.json"}; !slices.Equal(got, want) {
		t.Fatalf("after deleting the deck cue: files %v, want %v", got, want)
	}

	cs.DeleteAllForTrack(7)
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if got := cueFiles(t, dir); len(got) != 0 {
		t.Fatalf("after deleting the track: files %v, want none", got)
	}
}

// A write rewrites only the blobs that changed, and removes the blob of a
// cue deleted by an older version, which left the file behind.
func TestCueStoreWriteSkipsUnchangedBlobs(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cues_7.json", []byte(`[{"number":1,"type":1,"time_ms":0,"loop_ms":-1,"status":1,"color_id":42}]`))
	write("cue_7_1.bin", makeNXS2CueBlob(1, 0x2a, 0, 0, 0))
	write("cue_7_3.bin", makeNXS2CueBlob(3, 0x2a, 0, 0, 0)) // its cue was deleted
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "cue_7_1.bin"), old, old)

	cs := NewCueStore(dir)
	if got := cs.GetCombinedBlob(7); len(got) != 124 {
		t.Fatalf("served %d bytes, want only cue 1's blob", len(got))
	}
	cs.SaveCue(7, &CuePoint{Number: 2, Type: 1, LoopMs: -1, Status: 1}, nil)
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := cueFiles(t, dir), []string{"cue_7_1.bin", "cues_7.json"}; !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "cue_7_1.bin")); !fi.ModTime().Equal(old) {
		t.Error("the unchanged blob was rewritten")
	}
}

// Cues handed out stay as they were: changes replace a track's list instead
// of editing it, since readers and the writer hold it without the lock.
func TestCueStoreChangesDontEditHandedOutCues(t *testing.T) {
	cs := NewCueStore(t.TempDir())
	defer cs.Close()
	cs.SaveCue(7, &CuePoint{Number: 1, TimeMs: 100}, nil)
	cs.SaveCue(7, &CuePoint{Number: 2, TimeMs: 200}, nil)
	before := cs.GetCues(7)
	cs.SaveCue(7, &CuePoint{Number: 1, TimeMs: 999}, nil)
	cs.DeleteCue(7, 2)
	if before[0].TimeMs != 100 || before[1].Number != 2 {
		t.Errorf("handed-out cues changed: %+v", before)
	}
}

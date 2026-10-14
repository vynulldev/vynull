// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// TestPVB2FLACOnly checks that a generated PVB2 seek index is served only for
// FLAC, as rekordbox does (it writes PVB2 only into FLAC .EXT files and
// serves the atom from there); an MP3 gets the not-found reply.
func TestPVB2FLACOnly(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	encode := func(name string, args ...string) string {
		path := filepath.Join(dir, name)
		all := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=5:sample_rate=44100"}, args...)
		if out, err := exec.Command("ffmpeg", append(all, path)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, out)
		}
		return path
	}
	flac := encode("tone.flac")
	mp3 := encode("tone.mp3", "-c:a", "libmp3lame", "-q:a", "2")

	lib := library.NewLibrary([]*library.Track{
		{ID: 1, FilePath: flac},
		{ID: 2, FilePath: mp3},
	}, nil)
	store := analysis.NewStore()
	store.Set(1, &analysis.Result{BPM: 120})
	store.Set(2, &analysis.Result{BPM: 120})
	h := &Handler{lib: lib, analysis: store}

	pvb2 := uint32('2')<<24 | uint32('B')<<16 | uint32('V')<<8 | uint32('P') // "PVB2" reversed
	ask := func(trackID uint32) *proto.DBMessage {
		replies := h.handleGetExtAnalysis(&proto.DBMessage{
			TxID: 1, Type: 0x2c04,
			Args: []proto.DBArg{proto.ArgI32(0x01020401), proto.ArgI32(trackID), proto.ArgI32(pvb2), proto.ArgI32(0x00545845)},
		})
		if len(replies) != 1 {
			t.Fatalf("track %d: %d replies, want 1", trackID, len(replies))
		}
		return replies[0]
	}

	if r := ask(1); r.Args[1].Int() != 0 || len(r.Args) < 4 || len(r.Args[3].Bytes) == 0 {
		t.Errorf("FLAC: want a PVB2 blob with status 0, got status %d", r.Args[1].Int())
	}
	if r := ask(2); r.Args[1].Int() != 0x32 {
		t.Errorf("MP3: want not-found status 0x32, got %d", r.Args[1].Int())
	}
}

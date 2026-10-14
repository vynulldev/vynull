// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"os/exec"
	"path/filepath"
	"testing"
)

// encodeTestAudio writes a 10-second 44.1 kHz tone in the given codec.
func encodeTestAudio(t *testing.T, name string, codecArgs ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), name)
	args := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=10:sample_rate=44100"}, codecArgs...)
	if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return path
}

// TestVBRSeekIndexSamplePositions checks that entries carry sample positions,
// not raw packet timestamps: MP3 timestamps are in a 1/14112000 time base, so
// unscaled they ran 320× past the end of the track and the deck showed the
// wrong length.
func TestVBRSeekIndexSamplePositions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"tone.flac", nil},
		{"tone.mp3", []string{"-c:a", "libmp3lame", "-q:a", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := generateVBRSeekIndex(encodeTestAudio(t, tc.name, tc.args...))
			if err != nil {
				t.Fatal(err)
			}
			section := blob[4:] // after the LE length prefix
			extra := section[12:32]
			total := int64(binary.BigEndian.Uint32(extra[8:12]))
			n := int(binary.BigEndian.Uint32(extra[12:16]))
			if total < 44100*10-4096 || total > 44100*10+4096 {
				t.Fatalf("total_samples = %d, want ≈ %d", total, 44100*10)
			}
			last := section[32+(n-1)*20:]
			pos := int64(binary.BigEndian.Uint64(last[0:8]))
			if pos <= 0 || pos >= total {
				t.Fatalf("last entry sample position = %d, want within (0, %d)", pos, total)
			}
			if pos < total*9/10 {
				t.Fatalf("last entry sample position = %d, want near the end (%d)", pos, total)
			}
		})
	}
}

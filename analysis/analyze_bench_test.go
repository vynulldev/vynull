// SPDX-License-Identifier: GPL-3.0-or-later

package analysis_test

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/link/prolink"
)

// clickTrack writes a mono 16-bit WAV of the given length: a 60 Hz kick every
// half second (120 BPM) over quiet noise, from a fixed seed, so it is the same
// file every run.
func clickTrack(b *testing.B, seconds int) string {
	b.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		b.Skip("ffmpeg not installed")
	}
	const rate = 44100
	rng := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, seconds*rate*2)
	for i := range seconds * rate {
		t := float64(i%(rate/2)) / rate // time since the last kick
		v := 0.8*math.Sin(2*math.Pi*60*t)*math.Exp(-t*20) + 0.02*(rng.Float64()*2-1)
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(v*32767)))
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(data)))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1) // mono
	binary.LittleEndian.PutUint32(hdr[24:], rate)
	binary.LittleEndian.PutUint32(hdr[28:], rate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(data)))
	path := filepath.Join(b.TempDir(), "clicks.wav")
	if err := os.WriteFile(path, append(hdr, data...), 0o644); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkAnalyzeTrack measures one analysis of a one-minute track: decode,
// beat grid, key, phrases, vocals and every waveform. Its B/op is the memory
// one analysis allocates, which grows with the track's length.
func BenchmarkAnalyzeTrack(b *testing.B) {
	path := clickTrack(b, 60)
	analysis.SetEncoder(prolink.NewEncoder())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := analysis.AnalyzeTrack(path); err != nil {
			b.Fatal(err)
		}
	}
}

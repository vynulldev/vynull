// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// silentWAV writes a mono 16-bit WAV of the given length, long enough that
// analyzing it takes seconds: decoding it runs ffmpeg like any track.
func silentWAV(t *testing.T, seconds int) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	const rate = 44100
	data := make([]byte, seconds*rate*2)
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
	path := filepath.Join(t.TempDir(), "silence.wav")
	if err := os.WriteFile(path, append(hdr, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// An analysis stops when its context ends, with the context's error.
func TestAnalyzeTrackStopsOnCancel(t *testing.T) {
	path := silentWAV(t, 600)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := AnalyzeTrackContext(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's", err)
	}
}

// The queue cancels a running analysis whose result is no longer wanted: the
// track was deleted (nil for its requests), or given another result (which
// its requests get instead).
func TestQueueCancelsRealAnalysis(t *testing.T) {
	path := silentWAV(t, 600)
	for _, tc := range []struct {
		name string
		stop func(s *Store)
		want func(s *Store) *Result
	}{
		{"removed", func(s *Store) { s.Remove(1) }, func(*Store) *Result { return nil }},
		{"replaced", func(s *Store) { s.Set(1, &Result{BPM: 128}) }, func(s *Store) *Result { return s.Get(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			done := make(chan *Result, 1)
			s.Request(1, path, Foreground, func(r *Result) { done <- r })
			// Wait until the analysis is running, then stop it partway.
			deadline := time.Now().Add(time.Second)
			for s.activity() == "" {
				if time.Now().After(deadline) {
					t.Fatal("the analysis never started")
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond)
			tc.stop(s)
			select {
			case r := <-done:
				if want := tc.want(s); r != want {
					t.Errorf("request got %+v, want %+v", r, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the analysis was not cancelled")
			}
			if tc.name == "removed" && s.Get(1) != nil {
				t.Error("the removed track's analysis was stored")
			}
		})
	}
}

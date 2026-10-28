// SPDX-License-Identifier: GPL-3.0-or-later

package ffmpeg

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func needTools(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
}

// Run returns ffmpeg's output, and Probe reads the file it made.
func TestRunAndProbe(t *testing.T) {
	needTools(t)
	wav := filepath.Join(t.TempDir(), "tone.wav")
	if _, err := Run(context.Background(), "-f", "lavfi", "-i", "sine=frequency=440:duration=1:sample_rate=44100", wav); err != nil {
		t.Fatal(err)
	}
	out, err := Probe(context.Background(), wav, "stream=sample_rate", "csv=p=0")
	if err != nil || strings.TrimSpace(string(out)) != "44100" {
		t.Fatalf("Probe = %q, %v; want 44100", out, err)
	}
}

// A failing run's error carries what the tool printed.
func TestRunError(t *testing.T) {
	needTools(t)
	missing := filepath.Join(t.TempDir(), "missing.flac")
	_, err := Run(context.Background(), "-i", missing, "-f", "null", "-")
	if err == nil || !strings.HasPrefix(err.Error(), "ffmpeg: ") || !strings.Contains(err.Error(), "missing.flac") {
		t.Errorf("Run error %v, want ffmpeg's message", err)
	}
	if _, err := Probe(context.Background(), missing, "stream=sample_rate", "csv=p=0"); err == nil || !strings.HasPrefix(err.Error(), "ffprobe: ") {
		t.Errorf("Probe error %v, want ffprobe's message", err)
	}
}

// Stream returns ffmpeg's output as it writes it, and Close reports how it
// ended: nil for a run that worked, ffmpeg's message for one that failed.
func TestStream(t *testing.T) {
	needTools(t)
	ctx := context.Background()
	want, err := Run(ctx, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-f", "wav", "-")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Stream(ctx, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-f", "wav", "-")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("streamed %d bytes, %v; want Run's %d", len(got), err, len(want))
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing.flac")
	r, err = Stream(ctx, "-i", missing, "-f", "wav", "-")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r)
	if err := r.Close(); err == nil || !strings.Contains(err.Error(), "missing.flac") {
		t.Errorf("Close = %v, want ffmpeg's message", err)
	}
}

// Closing a stream before the end stops ffmpeg rather than waiting for it.
func TestStreamClosedEarly(t *testing.T) {
	needTools(t)
	r, err := Stream(context.Background(), "-f", "lavfi", "-i", "sine=frequency=440", "-f", "wav", "-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close of an endless stream didn't return")
	}
}

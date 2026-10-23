// SPDX-License-Identifier: GPL-3.0-or-later

package ffmpeg

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

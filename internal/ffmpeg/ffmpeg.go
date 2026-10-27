// SPDX-License-Identifier: GPL-3.0-or-later

// Package ffmpeg runs ffmpeg and ffprobe the same way everywhere Vynull
// decodes, transcodes or probes audio and artwork: reading nothing from
// stdin, printing only errors, and failing with what the tool printed.
package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Command returns the ffmpeg command for args, run with -nostdin and
// -loglevel error. ctx ending kills it.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "ffmpeg", append([]string{"-nostdin", "-loglevel", "error"}, args...)...)
}

// Run runs ffmpeg with args and returns what it wrote to stdout. Its error
// carries what ffmpeg printed.
func Run(ctx context.Context, args ...string) ([]byte, error) {
	return run(Command(ctx, args...))
}

// Probe runs ffprobe on the first audio stream of the file at path, showing
// entries ("stream=start_time") in format ("csv=p=0"), and returns what it
// printed. Its error carries ffprobe's message.
func Probe(ctx context.Context, path, entries, format string) ([]byte, error) {
	return run(exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", entries, "-of", format, path))
}

func run(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		name := cmd.Args[0]
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s: %w", name, msg, err)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

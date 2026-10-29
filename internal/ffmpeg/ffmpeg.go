// SPDX-License-Identifier: GPL-3.0-or-later

// Package ffmpeg runs ffmpeg and ffprobe the same way everywhere Vynull
// decodes, transcodes or probes audio and artwork: reading nothing from
// stdin, printing only errors, and failing with what the tool printed.
package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// Stream starts ffmpeg with args and returns what it writes to stdout, as it
// writes it. Close waits for ffmpeg to exit and returns its error, carrying
// what ffmpeg printed; closing before the end stops ffmpeg.
func Stream(ctx context.Context, args ...string) (io.ReadCloser, error) {
	cmd := Command(ctx, args...)
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, toolError(cmd, stderr, err)
	}
	return &stream{ReadCloser: stdout, cmd: cmd, stderr: stderr}, nil
}

// stream is ffmpeg's stdout, from Stream.
type stream struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (s *stream) Close() error {
	s.ReadCloser.Close() // a write to it now fails, so ffmpeg can't block on it
	if err := s.cmd.Wait(); err != nil {
		return toolError(s.cmd, s.stderr, err)
	}
	return nil
}

func run(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, toolError(cmd, &stderr, err)
	}
	return stdout.Bytes(), nil
}

// toolError is err from running cmd, carrying what the tool printed.
func toolError(cmd *exec.Cmd, stderr *bytes.Buffer, err error) error {
	name := cmd.Args[0]
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("%s: %s: %w", name, msg, err)
	}
	return fmt.Errorf("%s: %w", name, err)
}

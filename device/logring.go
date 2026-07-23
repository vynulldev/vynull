// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"bytes"
	"sync"
)

// LogRing is an io.Writer holding the most recent log lines in a fixed-size
// ring, so the TUI's Logs tab can show live output while the full stream
// still goes to the log file (tee both via io.MultiWriter). Safe for
// concurrent writers — the stdlib log package serializes its own writes, but
// nothing stops other code from writing to the same tee.
type LogRing struct {
	mu      sync.Mutex
	lines   []string
	head    int // next slot to overwrite
	full    bool
	partial bytes.Buffer // trailing bytes of an unterminated line
}

// NewLogRing returns a ring holding up to capacity lines.
func NewLogRing(capacity int) *LogRing {
	if capacity < 1 {
		capacity = 1
	}
	return &LogRing{lines: make([]string, capacity)}
}

// Write splits p into lines and appends each to the ring. A write that does
// not end in a newline leaves its tail buffered until the newline arrives,
// so interleaved fmt-style writes still land as whole lines. Always returns
// len(p), nil — a log tee must never fail the other writer.
func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.partial.Write(p)
			break
		}
		r.partial.Write(p[:i])
		r.push(r.partial.String())
		r.partial.Reset()
		p = p[i+1:]
	}
	return n, nil
}

func (r *LogRing) push(line string) {
	r.lines[r.head] = line
	r.head++
	if r.head == len(r.lines) {
		r.head = 0
		r.full = true
	}
}

// Lines returns the buffered lines, oldest first.
func (r *LogRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]string, r.head)
		copy(out, r.lines[:r.head])
		return out
	}
	out := make([]string, 0, len(r.lines))
	out = append(out, r.lines[r.head:]...)
	out = append(out, r.lines[:r.head]...)
	return out
}

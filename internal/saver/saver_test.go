// SPDX-License-Identifier: GPL-3.0-or-later

package saver

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// counter is a write func that counts its calls.
type counter struct{ n atomic.Int32 }

func (c *counter) write() error { c.n.Add(1); return nil }

// waitFor polls until cond holds or a second has passed.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBurstIsOneBackgroundWrite(t *testing.T) {
	var c counter
	s := New("test", 100*time.Millisecond, c.write)
	for range 100 {
		s.Mark()
	}
	waitFor(t, func() bool { return c.n.Load() > 0 })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.n.Load(); got != 1 {
		t.Fatalf("100 changes in one interval: %d writes, want 1", got)
	}
}

func TestFlushWritesOnlyChanges(t *testing.T) {
	var c counter
	s := New("test", time.Hour, c.write)
	defer s.Close()
	s.Flush()
	if c.n.Load() != 0 {
		t.Fatal("Flush with nothing changed wrote")
	}
	s.Mark()
	s.Flush()
	s.Flush()
	if got := c.n.Load(); got != 1 {
		t.Fatalf("one change, two Flushes: %d writes, want 1", got)
	}
}

func TestCloseWritesPending(t *testing.T) {
	var c counter
	s := New("test", time.Hour, c.write)
	s.Mark()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.n.Load(); got != 1 {
		t.Fatalf("Close with a change pending: %d writes, want 1", got)
	}
	if err := s.Close(); err != nil || c.n.Load() != 1 {
		t.Fatalf("second Close: err %v, %d writes", err, c.n.Load())
	}
}

// A store that never changed has no goroutine to stop; after Close, a change
// is written only by Flush.
func TestCloseBeforeAnyChange(t *testing.T) {
	var c counter
	s := New("test", time.Millisecond, c.write)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.Mark()
	time.Sleep(20 * time.Millisecond)
	if c.n.Load() != 0 {
		t.Fatal("a change after Close was written in the background")
	}
	s.Flush()
	if c.n.Load() != 1 {
		t.Fatal("Flush after Close didn't write")
	}
}

func TestFailedWriteIsRetried(t *testing.T) {
	fail := true
	var n int
	s := New("test", time.Hour, func() error {
		n++
		if fail {
			return errors.New("disk full")
		}
		return nil
	})
	defer s.Close()
	s.Mark()
	if err := s.Flush(); err == nil {
		t.Fatal("failed write not reported")
	}
	fail = false
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Flush()
	if n != 2 {
		t.Fatalf("%d writes, want 2 (the failure and its retry)", n)
	}
}

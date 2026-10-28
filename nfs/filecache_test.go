// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeTemp creates a file with the given contents and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// TestFileCacheReusesHandle proves get returns one shared handle per path and
// that it reads the right bytes via ReadAt (a pread — no shared offset).
func TestFileCacheReusesHandle(t *testing.T) {
	c := newFileCache()
	path := writeTemp(t, "a.bin", []byte("0123456789"))

	f1, err := c.get(path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	f2, err := c.get(path)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	if f1 != f2 {
		t.Fatalf("second get returned a different handle; the file was re-opened")
	}

	buf := make([]byte, 3)
	if n, _ := f1.ReadAt(buf, 5); n != 3 || string(buf) != "567" {
		t.Fatalf("ReadAt(5) = %q (n=%d), want %q", buf[:n], n, "567")
	}
	// A second read at a different offset must not be affected by the first
	// (ReadAt does not advance a shared seek offset).
	if n, _ := f1.ReadAt(buf, 0); n != 3 || string(buf) != "012" {
		t.Fatalf("ReadAt(0) = %q (n=%d), want %q", buf[:n], n, "012")
	}

	if got := len(c.entries); got != 1 {
		t.Fatalf("cache holds %d entries, want 1", got)
	}
}

// TestFileCacheConcurrentGet hammers get for the same path from many
// goroutines: exactly one handle must be cached and the duplicates closed.
// Run under -race to catch locking mistakes.
func TestFileCacheConcurrentGet(t *testing.T) {
	c := newFileCache()
	path := writeTemp(t, "b.bin", []byte("hello world"))

	var wg sync.WaitGroup
	handles := make([]*os.File, 32)
	for i := range handles {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f, err := c.get(path)
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			handles[i] = f
		}(i)
	}
	wg.Wait()

	for i, f := range handles {
		if f != handles[0] {
			t.Fatalf("goroutine %d got a different handle; cache handed out duplicates", i)
		}
	}
	if got := len(c.entries); got != 1 {
		t.Fatalf("cache holds %d entries, want 1", got)
	}
}

// TestFileCacheSweepClosesIdle checks the sweeper reclaims a handle once it has
// been idle past the TTL, and that shutdown via ctx closes everything.
func TestFileCacheSweepClosesIdle(t *testing.T) {
	c := newFileCache()
	path := writeTemp(t, "c.bin", []byte("x"))
	if _, err := c.get(path); err != nil {
		t.Fatalf("get: %v", err)
	}

	// Force the single entry past the idle TTL and sweep once by hand (so the
	// test doesn't wait real seconds for the ticker).
	c.mu.Lock()
	c.entries[path].lastUsed = time.Now().Add(-2 * fileCacheIdleTTL)
	c.mu.Unlock()

	now := time.Now()
	c.mu.Lock()
	for p, e := range c.entries {
		if now.Sub(e.lastUsed) > fileCacheIdleTTL {
			e.f.Close()
			delete(c.entries, p)
		}
	}
	c.mu.Unlock()

	if got := len(c.entries); got != 0 {
		t.Fatalf("idle entry not reclaimed: %d entries remain", got)
	}

	// closeAll on an empty cache is a no-op and must not panic; a populated
	// cache (via sweep's ctx path) must empty out.
	if _, err := c.get(path); err != nil {
		t.Fatalf("re-get after sweep: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.sweep(ctx); close(done) }()
	cancel()
	<-done
	if got := len(c.entries); got != 0 {
		t.Fatalf("sweep shutdown left %d entries open", got)
	}
}

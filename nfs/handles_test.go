// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHandlesFoundByPrefix checks that a handle a CDJ sends back, with bytes
// 12-31 rewritten, still finds its path, as the root handle does.
func TestHandlesFoundByPrefix(t *testing.T) {
	m := newFileHandleMap()
	m.Register([fhSize]byte{}, "/export")
	fh := m.GetOrCreate("/export/Music/track.mp3")
	m.GetOrCreate("/export/Music")

	sent := fh
	for i := fhPrefix; i < fhSize; i++ {
		sent[i] = byte(i * 7)
	}
	if _, ok := m.Resolve(sent); ok {
		t.Fatal("a rewritten handle resolved exactly; the test needs it not to")
	}
	if path, ok := m.ResolveByPrefix(sent); !ok || path != "/export/Music/track.mp3" {
		t.Errorf("rewritten handle resolved to %q, %v; want /export/Music/track.mp3", path, ok)
	}
	root := [fhSize]byte{}
	root[20] = 0xff
	if path, ok := m.ResolveByPrefix(root); !ok || path != "/export" {
		t.Errorf("rewritten root handle resolved to %q, %v; want /export", path, ok)
	}
	if _, ok := m.ResolveByPrefix([fhSize]byte{1, 2, 3}); ok {
		t.Error("an unknown handle resolved")
	}
}

// TestReadAfterFileReplaced reads a file through the server, replaces it on
// disk, and reads it again: the second read must see the new file, not the
// open handle the first read left in the cache.
func TestReadAfterFileReplaced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.mp3")
	first := bytes.Repeat([]byte("first "), 5000)
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(root)
	c := newClient(net.IPv4(127, 0, 0, 1), time.Second)
	c.transport = serverTransport(t, srv, &Portmapper{mountPort: mountPortNum, nfsPort: nfsPortNum})
	if err := c.resolvePorts(); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ReadFile("/C/", "track.mp3"); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("first read: %d bytes, %v; want the first file", len(got), err)
	}

	// Replace it the way an editor or re-encode does: a new file renamed over.
	second := bytes.Repeat([]byte("second file "), 4000)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, second, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ReadFile("/C/", "track.mp3"); err != nil || !bytes.Equal(got, second) {
		t.Errorf("read after replacing: %d bytes, %v; want the new file's %d", len(got), err, len(second))
	}
}

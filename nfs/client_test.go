// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// serverTransport routes a client's RPC call straight to the matching server
// handler, so the whole client<->server wire (encode + decode, both ways) is
// exercised with no sockets. This is the same dispatch the UDP listeners do.
func serverTransport(t *testing.T, srv *Server, pm *Portmapper) func(rpcKind, []byte) ([]byte, error) {
	t.Helper()
	return func(_ rpcKind, packet []byte) ([]byte, error) {
		hdr, err := parseRPCCall(packet)
		if err != nil {
			return nil, err
		}
		var resp []byte
		switch hdr.Program {
		case progPortmap:
			if hdr.Proc == pmapGetPort {
				resp = pm.handleGetPort(hdr)
			}
		case progMount:
			resp = srv.handleMount(hdr)
		case progNFS:
			resp = srv.handleNFS(hdr)
		}
		if resp == nil {
			return nil, fmt.Errorf("no server response for prog=%d proc=%d", hdr.Program, hdr.Proc)
		}
		return resp, nil
	}
}

func TestClientFetchExportPDB(t *testing.T) {
	// A media export with a fake export.pdb larger than one NFS read, so the
	// download exercises multi-chunk reassembly.
	root := t.TempDir()
	want := make([]byte, maxReadSize*2+123)
	for i := range want {
		want[i] = byte(i*31 + 7)
	}
	pdbPath := filepath.Join(root, "PIONEER", "rekordbox", "export.pdb")
	if err := os.MkdirAll(filepath.Dir(pdbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdbPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(root)
	pm := &Portmapper{mountPort: mountPortNum, nfsPort: nfsPortNum}

	c := newClient(net.IPv4(127, 0, 0, 1), time.Second)
	c.transport = serverTransport(t, srv, pm)

	// Port resolution round-trips through the portmapper.
	if err := c.resolvePorts(); err != nil {
		t.Fatalf("resolvePorts: %v", err)
	}
	if c.mountAddr.Port != mountPortNum || c.nfsAddr.Port != nfsPortNum {
		t.Fatalf("resolved ports mount=%d nfs=%d, want %d/%d",
			c.mountAddr.Port, c.nfsAddr.Port, mountPortNum, nfsPortNum)
	}

	// EXPORT lists our single export.
	exports, err := c.Exports()
	if err != nil {
		t.Fatalf("Exports: %v", err)
	}
	if len(exports) != 1 || exports[0] != "/C/" {
		t.Fatalf("exports = %v, want [/C/]", exports)
	}

	// The full mount -> lookup chain -> chunked read must return the file byte
	// for byte.
	got, ex, err := c.FetchExportPDB("")
	if err != nil {
		t.Fatalf("FetchExportPDB: %v", err)
	}
	if ex != "/C/" {
		t.Fatalf("export used = %q, want /C/", ex)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes, want %d (equal=%v)", len(got), len(want), bytes.Equal(got, want))
	}
}

func TestUTF16LEPathEncoding(t *testing.T) {
	// The exact "/C/" export bytes a real CDJ returned (UTF-16LE).
	cdj := []byte{0x2f, 0x00, 0x43, 0x00, 0x2f, 0x00}
	if got := encodeUTF16LE("/C/"); !bytes.Equal(got, cdj) {
		t.Fatalf("encodeUTF16LE(/C/) = % x, want % x", got, cdj)
	}
	// cleanExportName turns the CDJ's UTF-16LE name back into a plain string,
	// and leaves a plain name (our own server's form) untouched.
	if got := cleanExportName(string(cdj)); got != "/C/" {
		t.Fatalf("cleanExportName = %q, want /C/", got)
	}
	if got := cleanExportName("/C/"); got != "/C/" {
		t.Fatalf("cleanExportName(plain) = %q, want /C/", got)
	}
	// LOOKUP names round-trip through the UTF-16LE codec.
	for _, name := range []string{"PIONEER", "rekordbox", "export.pdb"} {
		if got := decodeUTF16LE(encodeUTF16LE(name)); got != name {
			t.Fatalf("round-trip %q -> %q", name, got)
		}
	}
}

func TestClientReadFileMissing(t *testing.T) {
	root := t.TempDir()
	srv := NewServer(root)
	pm := &Portmapper{mountPort: mountPortNum, nfsPort: nfsPortNum}
	c := newClient(net.IPv4(127, 0, 0, 1), time.Second)
	c.transport = serverTransport(t, srv, pm)

	_, err := c.ReadFile("/C/", ExportPDBPath)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !notFound(err) {
		t.Fatalf("err = %v, want NFS not-found", err)
	}
}

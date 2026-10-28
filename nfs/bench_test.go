// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// readCall builds an NFS v2 READ call for fh at offset.
func readCall(fh [fhSize]byte, offset, count uint32) []byte {
	var b bytes.Buffer
	for _, v := range []uint32{0x12345678, 0 /* CALL */, 2, progNFS, 2, 6 /* READ */} {
		binary.Write(&b, binary.BigEndian, v)
	}
	b.Write(make([]byte, 16)) // null credential + verifier
	b.Write(fh[:])
	for _, v := range []uint32{offset, count, count} {
		binary.Write(&b, binary.BigEndian, v)
	}
	return b.Bytes()
}

// BenchmarkNFSRead measures serving a deck's 16 KB READs of a 10 MB track,
// the handle sent back with bytes 12-31 rewritten as a CDJ does, with 2,000
// other paths looked up before it (a browsing session's worth).
func BenchmarkNFSRead(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(path, make([]byte, 10<<20), 0o644); err != nil {
		b.Fatal(err)
	}
	srv := NewServer(root)
	for i := range 2000 {
		srv.handles.GetOrCreate(filepath.Join(root, fmt.Sprintf("Artist %d", i/10), fmt.Sprintf("track %d.mp3", i)))
	}
	fh := srv.handles.GetOrCreate(path)
	for i := fhPrefix; i < fhSize; i++ {
		fh[i] ^= 0xa5
	}
	var calls [][]byte
	for off := uint32(0); off < 10<<20; off += maxReadSize {
		calls = append(calls, readCall(fh, off, maxReadSize))
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		hdr, err := parseRPCCall(calls[i%len(calls)])
		if err != nil {
			b.Fatal(err)
		}
		if reply := srv.handleNFS(hdr); len(reply) < maxReadSize {
			b.Fatalf("reply of %d bytes", len(reply))
		}
		i++
	}
}

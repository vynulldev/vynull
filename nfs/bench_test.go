// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
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

// BenchmarkNFSReadUDP measures a deck's 16 KB READs of a 10 MB track over a
// loopback UDP socket, one at a time, answered inline and in a goroutine.
func BenchmarkNFSReadUDP(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(path, make([]byte, 10<<20), 0o644); err != nil {
		b.Fatal(err)
	}
	srv := NewServer(root)
	fh := srv.handles.GetOrCreate(path)
	var calls [][]byte
	for off := uint32(0); off < 10<<20; off += maxReadSize {
		calls = append(calls, readCall(fh, off, maxReadSize))
	}
	for _, concurrent := range []bool{false, true} {
		name := map[bool]string{false: "inline", true: "goroutine"}[concurrent]
		b.Run(name, func(b *testing.B) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				b.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.serveUDP(ctx, conn, srv.handleNFS, false, concurrent)
			}()
			defer func() { cancel(); wg.Wait(); conn.Close() }()
			deck, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
			if err != nil {
				b.Fatal(err)
			}
			defer deck.Close()
			reply := make([]byte, 65536)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if _, err := deck.Write(calls[i%len(calls)]); err != nil {
					b.Fatal(err)
				}
				if n, err := deck.Read(reply); err != nil || n < maxReadSize {
					b.Fatalf("reply of %d bytes, %v", n, err)
				}
				i++
			}
		})
	}
}

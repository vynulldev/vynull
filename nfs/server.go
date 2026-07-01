// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vynull/internal/dlog"
)

const (
	// Separate ports for mount and NFS (like real CDJ).
	mountPortNum = 38251
	nfsPortNum   = 2049
	maxReadSize  = 16384 // NFS v2 max read size (matches real CDJ)
)

// flac handles transparent FLAC→WAV transcoding for NFS serving.
var flac = newFlacTranscoder()

// fileHandleMap maps opaque 32-byte handles to filesystem paths.
type fileHandleMap struct {
	mu       sync.RWMutex
	toPath   map[[fhSize]byte]string
	fromPath map[string][fhSize]byte
}

func newFileHandleMap() *fileHandleMap {
	return &fileHandleMap{
		toPath:   make(map[[fhSize]byte]string),
		fromPath: make(map[string][fhSize]byte),
	}
}

func (m *fileHandleMap) GetOrCreate(path string) [fhSize]byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	if fh, ok := m.fromPath[path]; ok {
		return fh
	}

	fh := sha256.Sum256([]byte(path))
	m.toPath[fh] = path
	m.fromPath[path] = fh
	return fh
}

// Register maps a specific file handle to a path (e.g., all-zeros for root).
func (m *fileHandleMap) Register(fh [fhSize]byte, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toPath[fh] = path
	m.fromPath[path] = fh
}

func (m *fileHandleMap) Resolve(fh [fhSize]byte) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	path, ok := m.toPath[fh]
	return path, ok
}

// ResolveByPrefix matches a file handle by its first 12 bytes.
// CDJ modifies bytes 12-31 of file handles with its own data.
func (m *fileHandleMap) ResolveByPrefix(fh [fhSize]byte) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for storedFH, path := range m.toPath {
		match := true
		for i := 0; i < 12; i++ {
			if storedFH[i] != fh[i] {
				match = false
				break
			}
		}
		if match {
			return path, true
		}
	}
	return "", false
}

// Server implements a minimal read-only NFS v2 server.
type Server struct {
	exportRoot string
	handles    *fileHandleMap
	Transcode  bool   // if true, transcode FLAC/WAV/AIFF to MP3
	IP         net.IP // server's IP for EXPORT response

	// LinkedFn, if set, gates MOUNT EXPORT replies on the device's
	// link-state. rekordbox starts returning an empty export
	// list the moment the user clicks UNLINK — the CDJ's next
	// poll (~200ms) sees no shares and immediately drops its LINK
	// indicator and sends UMNT. Without this gate the CDJ would
	// keep treating us as a valid partner until the keep-alive
	// timeout (~5-6s) fires.
	LinkedFn func() bool
}

// NewServer creates an NFS server exporting the given directory.
func NewServer(exportRoot string) *Server {
	return &Server{
		exportRoot: exportRoot,
		handles:    newFileHandleMap(),
	}
}

// Start launches the portmapper, mount, and NFS listeners.
func (s *Server) Start(ctx context.Context) error {
	// Start portmapper.
	pm := &Portmapper{
		mountPort: mountPortNum,
		nfsPort:   nfsPortNum,
	}
	go func() {
		if err := pm.Start(ctx); err != nil {
			log.Printf("portmapper error: %v", err)
		}
	}()

	var wg sync.WaitGroup

	// Mount on separate port. Replies are sent twice (matching real
	// rekordbox's wire pattern observed in pcaps) — the CDJ apparently
	// treats a single MOUNT reply as packet loss and retries 200ms
	// later, which is what was causing our UNLINK to look "unclean"
	// even after the empty-EXPORT fix.
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("nfs: mount listener starting on UDP port %d", mountPortNum)
		s.listenUDP(ctx, mountPortNum, s.handleMount, true)
	}()

	// NFS on port 2049 (also accept mount program here as fallback).
	combinedHandler := func(hdr *rpcHeader) []byte {
		switch hdr.Program {
		case progMount:
			return s.handleMount(hdr)
		case progNFS:
			return s.handleNFS(hdr)
		default:
			log.Printf("nfs: unknown program %d", hdr.Program)
			return nil
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("nfs: NFS listener starting on UDP port %d", nfsPortNum)
		// Single-reply: don't duplicate NFS READ payloads (would double
		// the bandwidth for every track byte during playback).
		s.listenUDP(ctx, nfsPortNum, combinedHandler, false)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.listenTCP(ctx, nfsPortNum, combinedHandler)
	}()

	log.Printf("nfs server: export=%s port=:%d portmap=%v",
		s.exportRoot, nfsPortNum, portmapPorts)

	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s *Server) listenUDP(ctx context.Context, port int, handler func(*rpcHeader) []byte, duplicate bool) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		log.Printf("nfs udp bind %d: %v", port, err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 65536)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("nfs udp %d read error: %v", port, err)
			return
		}

		if dlog.Enabled(dlog.Trace) {
			dlog.Tracef("nfs udp %d: recv %d bytes from %s\n%s", port, n, addr, hex.Dump(buf[:min(n, 80)]))
		}
		resp := s.dispatchRPC(buf[:n], handler)
		if resp != nil {
			if dlog.Enabled(dlog.Trace) {
				dlog.Tracef("nfs udp %d: send %d bytes to %s\n%s", port, len(resp), addr, hex.Dump(resp[:min(len(resp), 80)]))
			}
			conn.WriteToUDP(resp, addr)
			if duplicate {
				// rekordbox sends each MOUNT/Portmap reply twice
				// ~400µs apart. Without the duplicate the CDJ treats
				// it as packet loss and retries 200ms later, which
				// makes UNLINK look unclean.
				time.Sleep(400 * time.Microsecond)
				conn.WriteToUDP(resp, addr)
			}
		}
	}
}

func (s *Server) listenTCP(ctx context.Context, port int, handler func(*rpcHeader) []byte) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Printf("nfs tcp bind %d: %v", port, err)
		return
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("nfs tcp accept %d: %v", port, err)
				return
			}
		}
		go s.handleTCPConn(ctx, conn, handler)
	}
}

func (s *Server) handleTCPConn(ctx context.Context, conn net.Conn, handler func(*rpcHeader) []byte) {
	defer conn.Close()

	buf := make([]byte, 65536)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// TCP RPC: 4-byte record marker (MSB=last fragment, lower 31 bits=length).
		var marker [4]byte
		if _, err := readFull(conn, marker[:]); err != nil {
			return
		}
		fragLen := int(be32(marker[:]) & 0x7FFFFFFF)
		if fragLen > len(buf) {
			log.Printf("nfs tcp: fragment too large: %d", fragLen)
			return
		}
		if _, err := readFull(conn, buf[:fragLen]); err != nil {
			return
		}

		resp := s.dispatchRPC(buf[:fragLen], handler)
		if resp != nil {
			// Write record marker + response.
			var respMarker [4]byte
			putBE32(respMarker[:], uint32(len(resp))|0x80000000)
			conn.Write(respMarker[:])
			conn.Write(resp)
		}
	}
}

func (s *Server) dispatchRPC(data []byte, handler func(*rpcHeader) []byte) []byte {
	hdr, err := parseRPCCall(data)
	if err != nil {
		log.Printf("nfs: RPC parse error: %v (data len=%d)", err, len(data))
		return nil
	}
	log.Printf("nfs: RPC call prog=%d vers=%d proc=%d xid=%08x", hdr.Program, hdr.Version, hdr.Proc, hdr.XID)
	return handler(hdr)
}

// handleNFS dispatches NFS v2 procedures.
func (s *Server) handleNFS(hdr *rpcHeader) []byte {
	switch hdr.Proc {
	case nfsNull:
		log.Printf("nfs: NULL")
		return buildRPCReply(hdr.XID).bytes()
	case nfsGetAttr:
		log.Printf("nfs: GETATTR")
		return s.nfsGetAttr(hdr)
	case nfsLookup:
		log.Printf("nfs: LOOKUP")
		return s.nfsLookup(hdr)
	case nfsRead:
		log.Printf("nfs: READ")
		return s.nfsRead(hdr)
	case nfsReadDir:
		log.Printf("nfs: READDIR")
		return s.nfsReadDir(hdr)
	case nfsStatFS:
		log.Printf("nfs: STATFS")
		return s.nfsStatFS(hdr)
	case nfsSetAttr:
		log.Printf("nfs: SETATTR (write attempt)")
		return s.nfsWriteStub(hdr, "SETATTR")
	case nfsWrite:
		log.Printf("nfs: WRITE (write attempt)")
		return s.nfsWriteStub(hdr, "WRITE")
	case nfsCreate:
		log.Printf("nfs: CREATE (write attempt)")
		return s.nfsWriteStub(hdr, "CREATE")
	case nfsRemove:
		log.Printf("nfs: REMOVE (write attempt)")
		return s.nfsWriteStub(hdr, "REMOVE")
	case nfsMkdir:
		log.Printf("nfs: MKDIR (write attempt)")
		return s.nfsWriteStub(hdr, "MKDIR")
	default:
		log.Printf("nfs: unhandled proc %d", hdr.Proc)
		return buildRPCReply(hdr.XID).bytes()
	}
}

func (s *Server) nfsGetAttr(hdr *rpcHeader) []byte {
	r := newXDRReader(hdr.body)
	fh, err := r.fh()
	if err != nil {
		return nil
	}

	path, ok := s.handles.Resolve(fh)
	if !ok {
		path, ok = s.handles.ResolveByPrefix(fh)
	}
	if !ok {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}

	info, err := os.Stat(path)
	if err != nil {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}

	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)
	putFAttr(w, info, path, !s.Transcode)
	return w.bytes()
}

func (s *Server) nfsLookup(hdr *rpcHeader) []byte {
	r := newXDRReader(hdr.body)
	dirFH, err := r.fh()
	if err != nil {
		log.Printf("nfs: LOOKUP bad fh: %v", err)
		return nil
	}
	rawName, err := r.str()
	if err != nil {
		log.Printf("nfs: LOOKUP bad name: %v", err)
		return nil
	}

	log.Printf("nfs: LOOKUP raw body (%d bytes): %x", len(hdr.body), hdr.body[:min(len(hdr.body), 80)])

	// Detect UTF-16LE vs ASCII. CDJ sends UTF-16LE (every other byte is 0x00
	// for ASCII chars). Linux sends plain ASCII.
	name := rawName
	if len(rawName) >= 4 && rawName[1] == 0x00 {
		// UTF-16LE: second byte of first char is 0x00
		runes := make([]rune, 0, len(rawName)/2)
		for i := 0; i < len(rawName)-1; i += 2 {
			c := uint16(rawName[i+1])<<8 | uint16(rawName[i])
			if c == 0 {
				continue
			}
			runes = append(runes, rune(c))
		}
		if len(runes) > 0 {
			name = string(runes)
		}
	}
	// else: treat as ASCII (already correct)

	dirPath, ok := s.handles.Resolve(dirFH)
	if !ok {
		// CDJ modifies bytes 12-31 of file handles. Try matching by prefix.
		dirPath, ok = s.handles.ResolveByPrefix(dirFH)
		if !ok {
			log.Printf("nfs: LOOKUP %q in unknown dir handle %x", name, dirFH[:12])
			w := buildRPCReply(hdr.XID)
			w.putU32(nfsNoEnt)
			return w.bytes()
		}
	}

	childPath := filepath.Clean(filepath.Join(dirPath, name))
	// Prevent path traversal outside the export root.
	if !strings.HasPrefix(childPath, s.exportRoot) {
		log.Printf("nfs: LOOKUP %q in %q: path traversal blocked (resolved to %q)", name, dirPath, childPath)
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}
	log.Printf("nfs: LOOKUP %q in %q -> %q", name, dirPath, childPath)
	info, err := os.Stat(childPath)
	if err != nil {
		log.Printf("nfs: LOOKUP %q: %v", childPath, err)
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}

	childFH := s.handles.GetOrCreate(childPath)

	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)
	w.putFH(childFH)
	putFAttr(w, info, childPath, !s.Transcode)
	resp := w.bytes()

	log.Printf("nfs: LOOKUP OK %q size=%d isdir=%v fh=%x resp_hex=%x",
		name, info.Size(), info.IsDir(), childFH[:8], resp)

	return resp
}

func (s *Server) nfsRead(hdr *rpcHeader) []byte {
	if len(hdr.body) >= 8 {
		log.Printf("nfs: READ body (%d bytes) first 48: %x", len(hdr.body), hdr.body[:min(len(hdr.body), 48)])
	}
	r := newXDRReader(hdr.body)
	fh, err := r.fh()
	if err != nil {
		return nil
	}
	offset, _ := r.u32()
	count, _ := r.u32()
	_, _ = r.u32() // totalcount (unused in v2)
	log.Printf("nfs: READ fh=%x... offset=%d count=%d", fh[:8], offset, count)

	path, ok := s.handles.Resolve(fh)
	if !ok {
		path, ok = s.handles.ResolveByPrefix(fh)
	}
	if !ok {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}

	if count > maxReadSize {
		count = maxReadSize
	}

	// For non-MP3 formats, serve transcoded MP3 data instead (unless disabled).
	if s.Transcode && NeedsTranscode(path) {
		wavBytes, err := flac.ReadAt(path, offset, count)
		if err != nil {
			log.Printf("nfs: READ transcode %s error: %v", filepath.Base(path), err)
			w := buildRPCReply(hdr.XID)
			w.putU32(nfsIO)
			return w.bytes()
		}
		if offset == 0 {
			log.Printf("nfs: READ transcode %s offset=0 count=%d read=%d",
				filepath.Base(path), count, len(wavBytes))
		}
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsOK)
		putFAttr(w, nil, path, !s.Transcode)
		w.putBytes(wavBytes)
		return w.bytes()
	}

	f, err := os.Open(path)
	if err != nil {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsIO)
		return w.bytes()
	}
	defer f.Close()

	data := make([]byte, count)
	n, err := f.ReadAt(data, int64(offset))
	if n == 0 && err != nil {
		log.Printf("nfs: READ %s offset=%d count=%d error=%v", path, offset, count, err)
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsIO)
		return w.bytes()
	}
	data = data[:n]
	info, _ := f.Stat()

	if offset == 0 {
		log.Printf("nfs: READ %s offset=0 count=%d read=%d first_bytes=%x",
			filepath.Base(path), count, n, data[:min(n, 16)])
	}

	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)
	putFAttr(w, info, path, !s.Transcode)
	w.putBytes(data)
	return w.bytes()
}

func (s *Server) nfsReadDir(hdr *rpcHeader) []byte {
	r := newXDRReader(hdr.body)
	fh, err := r.fh()
	if err != nil {
		return nil
	}
	cookie, _ := r.u32()
	count, _ := r.u32()

	path, ok := s.handles.Resolve(fh)
	if !ok {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNoEnt)
		return w.bytes()
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		w := buildRPCReply(hdr.XID)
		w.putU32(nfsNotDir)
		return w.bytes()
	}

	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)

	// NFSv2 READDIR pagination. The client passes `count` (max bytes of
	// directory data it will accept) and `cookie` (resume point — the cookie
	// value from the last entry it received, which we emit as position+1).
	// We must honor both: a directory with more entries than fit in `count`
	// is read across several READDIR calls. Ignoring this (the old behavior)
	// overflowed a single reply for large directories — fine for a small
	// scanned library, but it broke the deck's mount once an imported
	// library produced a directory too big for one reply. os.ReadDir returns
	// a stable name-sorted order, so position-based cookies are consistent
	// across calls.
	maxBytes := int(count)
	if maxBytes < 1024 || maxBytes > 8000 {
		maxBytes = 8000 // CDJ-friendly default; leaves UDP headroom
	}
	start := int(cookie) // first call: cookie==0 → start at entry 0
	size := 0
	eof := true
	for i := start; i < len(entries); i++ {
		name := entries[i].Name()
		// Per-entry XDR size: value-follows(4) + fileid(4) + string(4 + padded
		// name) + cookie(4).
		entrySize := 4 + 4 + 4 + ((len(name) + 3) &^ 3) + 4
		if i > start && size+entrySize > maxBytes {
			eof = false // more entries remain; client will re-READDIR
			break
		}
		s.handles.GetOrCreate(filepath.Join(path, name))
		w.putU32(1)             // value-follows = true
		w.putU32(uint32(i + 1)) // fileID
		w.putString(name)
		w.putU32(uint32(i + 1)) // cookie = resume point for the next call
		size += entrySize
	}

	w.putU32(0) // value-follows = false (end of this batch)
	if eof {
		w.putU32(1)
	} else {
		w.putU32(0)
	}
	return w.bytes()
}

func (s *Server) nfsStatFS(hdr *rpcHeader) []byte {
	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)    // status
	w.putU32(16384)    // tsize: transfer size (matches real CDJ)
	w.putU32(512)      // bsize: block size (matches real CDJ)
	w.putU32(32000000) // blocks: total (~16GB at 512 byte blocks)
	w.putU32(16000000) // bfree: free (~8GB)
	w.putU32(16000000) // bavail: available (~8GB)
	return w.bytes()
}

// putFAttr writes NFS v2 file attributes.
func putFAttr(w *xdrWriter, info os.FileInfo, path string, noTranscode bool) {
	var ftype uint32
	var mode uint32
	var fileSize int64
	var mtime uint32

	if info == nil && !noTranscode {
		// Transcoded file: no os.FileInfo, get size from transcoder.
		ftype = nfTypeReg
		mode = 0x8000
		if wavSize, err := flac.Size(path); err == nil {
			fileSize = wavSize
		}
	} else if info == nil {
		// No info and no transcode — try stat directly.
		ftype = nfTypeReg
		mode = 0x8000
		if fi, err := os.Stat(path); err == nil {
			fileSize = fi.Size()
			mtime = uint32(fi.ModTime().Unix())
		}
	} else if info.IsDir() {
		ftype = nfTypeDir
		mode = 0o40666
		fileSize = info.Size()
		mtime = uint32(info.ModTime().Unix())
	} else {
		ftype = nfTypeReg
		mode = 0x8000
		fileSize = info.Size()
		mtime = uint32(info.ModTime().Unix())
		if !noTranscode && NeedsTranscode(path) {
			if wavSize, err := flac.Size(path); err == nil {
				fileSize = wavSize
			}
		}
	}

	w.putU32(ftype)
	w.putU32(mode)
	w.putU32(1) // nlink
	w.putU32(0) // uid
	w.putU32(0) // gid
	w.putU32(uint32(fileSize))
	w.putU32(512) // blocksize
	w.putU32(1)   // rdev
	blocks := (uint32(fileSize) + 511) / 512
	w.putU32(blocks)
	w.putU32(2) // fsid
	h := sha256.Sum256([]byte(path))
	fileid := be32(h[:4])
	w.putU32(fileid)
	w.putU32(mtime)
	w.putU32(0) // atime usec
	w.putU32(mtime)
	w.putU32(0) // mtime usec
	w.putU32(mtime)
	w.putU32(0) // ctime usec
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func putBE32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

// nfsWriteStub logs and rejects write operations with EROFS.
// Used to detect whether CDJs attempt NFS writes for cue points, ANLZ, etc.
func (s *Server) nfsWriteStub(hdr *rpcHeader, op string) []byte {
	// Try to extract the file handle to log which file the CDJ is targeting.
	if len(hdr.body) >= fhSize {
		var fh [fhSize]byte
		copy(fh[:], hdr.body[:fhSize])
		s.handles.mu.RLock()
		path := s.handles.toPath[fh]
		s.handles.mu.RUnlock()
		log.Printf("nfs: %s target path: %s (body %d bytes)", op, path, len(hdr.body))
	}
	w := buildRPCReply(hdr.XID)
	w.putU32(nfsROFS) // read-only filesystem
	return w.bytes()
}

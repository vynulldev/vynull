// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/vynulldev/vynull/internal/dlog"
)

// A CDJ encodes every path string on the wire (export names, LOOKUP filenames)
// in UTF-16LE, exactly as it does when it is the client reading from us (see the
// server's LOOKUP name decode). We keep clean UTF-8 strings internally and
// convert at the wire.

func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return b
}

func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		return string(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// cleanExportName turns a possibly-UTF-16LE export name (the CDJ's form) into a
// plain string; an already-plain name (our own server's form) is unchanged.
func cleanExportName(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		return decodeUTF16LE([]byte(s))
	}
	return s
}

// traceReply hex-dumps a low-volume control reply (EXPORT/MNT/LOOKUP) when trace
// logging is on, to diagnose a real CDJ's wire format. Never used for READ.
func traceReply(label string, data []byte) {
	if dlog.Enabled(dlog.Trace) {
		n := len(data)
		if n > 128 {
			n = 128
		}
		dlog.Tracef("nfs client: %s reply (%d bytes):\n%s", label, len(data), hex.Dump(data[:n]))
	}
}

// This file implements the client side of the same protocols the rest of the
// package serves: a minimal, read-only ONC-RPC / MOUNT / NFSv2 client used to
// download a player's rekordbox export (its USB/SD `export.pdb`) so we can
// resolve metadata for tracks a deck loads from its own media. Beat-link's
// CrateDigger and prolink-connect take the same route, because a CDJ refuses
// dbserver metadata requests from our rekordbox-range player number but still
// serves its media over NFS to anyone. See docs/design/external-metadata.md.
//
// It reuses the package's xdrReader/xdrWriter, the RPC/NFS constants, and
// maxReadSize, so the client and server share one definition of the wire.

const (
	protoUDP       uint32 = 17 // IPPROTO_UDP, for portmap GETPORT
	authUnixFlavor uint32 = 1  // AUTH_UNIX / AUTH_SYS credential flavor
)

// ExportPDBPath is the rekordbox database's location within a media export,
// relative to the export root.
const ExportPDBPath = "PIONEER/rekordbox/export.pdb"

// rpcKind selects which of the player's RPC services a call targets. The client
// resolves a concrete UDP address for each via the portmapper.
type rpcKind int

const (
	kindPortmap rpcKind = iota
	kindMount
	kindNFS
)

// fattr holds the NFSv2 file attributes we care about (the rest are skipped).
type fattr struct {
	Type uint32 // nfTypeReg / nfTypeDir
	Mode uint32
	Size uint32
}

// nfsError is a non-OK NFS/MOUNT status from the server.
type nfsError struct {
	proc   string
	name   string
	status uint32
}

func (e *nfsError) Error() string {
	if e.name != "" {
		return fmt.Sprintf("nfs %s %q: status %d", e.proc, e.name, e.status)
	}
	return fmt.Sprintf("nfs %s: status %d", e.proc, e.status)
}

// notFound reports whether err is an NFS "no such entry" status (a missing
// file/export), as opposed to a transport or protocol failure.
func notFound(err error) bool {
	var ne *nfsError
	if e, ok := err.(*nfsError); ok {
		ne = e
	}
	return ne != nil && ne.status == nfsNoEnt
}

// Client is a read-only NFSv2 client for one player. Not safe for concurrent
// use by multiple goroutines. Each RPC is a fresh connected UDP socket, so a
// stale reply from a previous call can't be mistaken for the current one.
type Client struct {
	ip          net.IP
	hostname    string
	timeout     time.Duration
	portmapPort int
	mountAddr   *net.UDPAddr
	nfsAddr     *net.UDPAddr

	mu  sync.Mutex
	xid uint32

	// transport sends one RPC call packet to the given service and returns the
	// raw reply datagram. Defaults to udpTransport; overridden in tests to route
	// calls straight to the server handlers with no sockets.
	transport func(kind rpcKind, packet []byte) ([]byte, error)
}

func newClient(ip net.IP, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	c := &Client{ip: ip, hostname: "vynull", timeout: timeout, xid: 1}
	c.transport = c.udpTransport
	return c
}

// Dial resolves the player's mount and NFS ports via its portmapper and returns
// a ready client. It tries the standard RPC port 111 (a CDJ-USB source) and
// Pioneer's non-standard 50111 (a rekordbox source).
func Dial(ip net.IP, timeout time.Duration) (*Client, error) {
	c := newClient(ip, timeout)
	if err := c.resolvePorts(); err != nil {
		return nil, err
	}
	return c, nil
}

// Close releases any resources. UDP sockets are per-call, so this is a no-op
// kept for API symmetry.
func (c *Client) Close() error { return nil }

func (c *Client) resolvePorts() error {
	var lastErr error
	for _, pmp := range []int{111, 50111} {
		c.portmapPort = pmp
		mp, err := c.getPort(progMount, versMount)
		if err != nil {
			lastErr = fmt.Errorf("portmap :%d mount: %w", pmp, err)
			continue
		}
		np, err := c.getPort(progNFS, versNFS)
		if err != nil {
			lastErr = fmt.Errorf("portmap :%d nfs: %w", pmp, err)
			continue
		}
		if mp <= 0 || np <= 0 {
			lastErr = fmt.Errorf("portmap :%d returned mount=%d nfs=%d", pmp, mp, np)
			continue
		}
		c.mountAddr = &net.UDPAddr{IP: c.ip, Port: mp}
		c.nfsAddr = &net.UDPAddr{IP: c.ip, Port: np}
		log.Printf("nfs client: %s portmap :%d -> mount=%d nfs=%d", c.ip, pmp, mp, np)
		return nil
	}
	return fmt.Errorf("nfs client: %s: could not resolve mount/nfs ports: %w", c.ip, lastErr)
}

func (c *Client) addrFor(kind rpcKind) *net.UDPAddr {
	switch kind {
	case kindPortmap:
		return &net.UDPAddr{IP: c.ip, Port: c.portmapPort}
	case kindMount:
		return c.mountAddr
	default:
		return c.nfsAddr
	}
}

func (c *Client) udpTransport(kind rpcKind, packet []byte) ([]byte, error) {
	addr := c.addrFor(kind)
	if addr == nil {
		return nil, fmt.Errorf("nfs client: no address for service %d", kind)
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	buf := make([]byte, 65536)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		conn.SetDeadline(time.Now().Add(c.timeout))
		if _, err := conn.Write(packet); err != nil {
			lastErr = err
			continue
		}
		n, err := conn.Read(buf)
		if err != nil {
			lastErr = err
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out, nil
	}
	return nil, lastErr
}

func (c *Client) nextXID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.xid++
	return c.xid
}

// buildCall marshals a Sun RPC v2 call with AUTH_UNIX credentials (uid/gid 0)
// and a null verifier, followed by the procedure arguments.
func (c *Client) buildCall(xid, prog, vers, proc uint32, args []byte) []byte {
	w := newXDRWriter(128 + len(args))
	w.putU32(xid)
	w.putU32(rpcCall)
	w.putU32(2) // RPC version
	w.putU32(prog)
	w.putU32(vers)
	w.putU32(proc)
	// Credential: AUTH_UNIX.
	w.putU32(authUnixFlavor)
	w.putBytes(c.authUnixBody())
	// Verifier: AUTH_NULL.
	w.putU32(rpcAuthNull)
	w.putU32(0)
	w.buf = append(w.buf, args...)
	return w.bytes()
}

func (c *Client) authUnixBody() []byte {
	w := newXDRWriter(32)
	w.putU32(0) // stamp
	w.putString(c.hostname)
	w.putU32(0) // uid
	w.putU32(0) // gid
	w.putU32(0) // auxiliary gid count
	return w.bytes()
}

// call sends an RPC and returns an xdrReader positioned at the procedure result
// (after verifying the reply was accepted). It rejects a reply whose XID does
// not match the call, so a stale datagram can't be misread.
func (c *Client) call(kind rpcKind, prog, vers, proc uint32, args []byte) (*xdrReader, error) {
	xid := c.nextXID()
	reply, err := c.transport(kind, c.buildCall(xid, prog, vers, proc, args))
	if err != nil {
		return nil, err
	}
	r := newXDRReader(reply)
	rxid, err := r.u32()
	if err != nil {
		return nil, err
	}
	if rxid != xid {
		return nil, fmt.Errorf("rpc xid mismatch: got %08x want %08x", rxid, xid)
	}
	mtype, err := r.u32()
	if err != nil {
		return nil, err
	}
	if mtype != rpcReply {
		return nil, fmt.Errorf("rpc: not a reply (msg_type=%d)", mtype)
	}
	replyStat, err := r.u32()
	if err != nil {
		return nil, err
	}
	if replyStat != rpcMsgAccepted {
		return nil, fmt.Errorf("rpc denied (reply_stat=%d)", replyStat)
	}
	// Verifier (flavor + opaque body).
	if _, err := r.u32(); err != nil {
		return nil, err
	}
	vlen, err := r.u32()
	if err != nil {
		return nil, err
	}
	if _, err := r.opaque(int(vlen)); err != nil {
		return nil, err
	}
	acceptStat, err := r.u32()
	if err != nil {
		return nil, err
	}
	if acceptStat != rpcAcceptOK {
		return nil, fmt.Errorf("rpc not accepted (accept_stat=%d)", acceptStat)
	}
	return r, nil
}

func (c *Client) getPort(prog, vers uint32) (int, error) {
	a := newXDRWriter(16)
	a.putU32(prog)
	a.putU32(vers)
	a.putU32(protoUDP)
	a.putU32(0)
	r, err := c.call(kindPortmap, progPortmap, versPortmap, pmapGetPort, a.bytes())
	if err != nil {
		return 0, err
	}
	port, err := r.u32()
	return int(port), err
}

// Exports lists the server's exported filesystems (MOUNT EXPORT). Some servers
// return an empty list or reject it; the caller falls back to common roots.
func (c *Client) Exports() ([]string, error) {
	r, err := c.call(kindMount, progMount, versMount, mountExport, nil)
	if err != nil {
		return nil, err
	}
	traceReply("EXPORT", r.data)
	var out []string
	for {
		more, err := r.u32()
		if err != nil || more == 0 {
			break
		}
		dir, err := r.str()
		if err != nil {
			return out, err
		}
		out = append(out, cleanExportName(dir))
		// Skip the access-group list for this export.
		for {
			g, err := r.u32()
			if err != nil {
				return out, err
			}
			if g == 0 {
				break
			}
			if _, err := r.str(); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// mount performs MOUNT MNT and returns the export's root file handle.
func (c *Client) mount(path string) ([fhSize]byte, error) {
	var fh [fhSize]byte
	a := newXDRWriter(64)
	a.putBytes(encodeUTF16LE(path)) // CDJ expects the export path in UTF-16LE
	r, err := c.call(kindMount, progMount, versMount, mountMnt, a.bytes())
	if err != nil {
		return fh, err
	}
	traceReply("MNT "+path, r.data)
	status, err := r.u32()
	if err != nil {
		return fh, err
	}
	if status != nfsOK {
		return fh, &nfsError{proc: "MNT", name: path, status: status}
	}
	return r.fh()
}

// lookup performs NFS LOOKUP of name within the directory handle dir.
func (c *Client) lookup(dir [fhSize]byte, name string) ([fhSize]byte, fattr, error) {
	var fh [fhSize]byte
	var at fattr
	a := newXDRWriter(fhSize + 32)
	a.putFH(dir)
	a.putBytes(encodeUTF16LE(name)) // CDJ expects LOOKUP filenames in UTF-16LE
	r, err := c.call(kindNFS, progNFS, versNFS, nfsLookup, a.bytes())
	if err != nil {
		return fh, at, err
	}
	traceReply("LOOKUP "+name, r.data)
	status, err := r.u32()
	if err != nil {
		return fh, at, err
	}
	if status != nfsOK {
		return fh, at, &nfsError{proc: "LOOKUP", name: name, status: status}
	}
	if fh, err = r.fh(); err != nil {
		return fh, at, err
	}
	at, err = readFAttr(r)
	return fh, at, err
}

// read performs NFS READ of up to count bytes at offset from the file handle.
func (c *Client) read(fh [fhSize]byte, offset, count uint32) ([]byte, error) {
	a := newXDRWriter(fhSize + 16)
	a.putFH(fh)
	a.putU32(offset)
	a.putU32(count)
	a.putU32(0) // totalcount (unused in v2)
	r, err := c.call(kindNFS, progNFS, versNFS, nfsRead, a.bytes())
	if err != nil {
		return nil, err
	}
	status, err := r.u32()
	if err != nil {
		return nil, err
	}
	if status != nfsOK {
		return nil, &nfsError{proc: "READ", status: status}
	}
	if _, err := readFAttr(r); err != nil { // fattr precedes the data in a v2 READ reply
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	return r.opaque(int(n))
}

func readFAttr(r *xdrReader) (fattr, error) {
	var vals [17]uint32
	for i := range vals {
		v, err := r.u32()
		if err != nil {
			return fattr{}, fmt.Errorf("fattr field %d: %w", i, err)
		}
		vals[i] = v
	}
	return fattr{Type: vals[0], Mode: vals[1], Size: vals[5]}, nil
}

// ReadFile mounts export and downloads the file at path (relative to the export
// root, slash-separated) fully into memory, in maxReadSize chunks.
func (c *Client) ReadFile(export, path string) ([]byte, error) {
	root, err := c.mount(export)
	if err != nil {
		return nil, err
	}
	fh := root
	var at fattr
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" {
			continue
		}
		if fh, at, err = c.lookup(fh, part); err != nil {
			return nil, err
		}
	}
	if at.Type == nfTypeDir {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	buf := make([]byte, 0, at.Size)
	for off := uint32(0); off < at.Size; {
		count := at.Size - off
		if count > maxReadSize {
			count = maxReadSize
		}
		chunk, err := c.read(fh, off, count)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break // short read; server has no more data
		}
		buf = append(buf, chunk...)
		off += uint32(len(chunk))
	}
	return buf, nil
}

// FetchExportPDB downloads the rekordbox export.pdb, trying the server's
// advertised export list first and then common CDJ roots. It returns the bytes
// and the export they came from.
// FetchExportPDB downloads the rekordbox export.pdb. preferred is the export the
// caller believes matches the media it wants (e.g. the letter for a slot); it is
// tried first, then the server's advertised export list, then common CDJ roots.
// The preferred hint matters only when a player has more than one media mounted
// (USB + SD), where every export otherwise resolves to whichever comes first.
func (c *Client) FetchExportPDB(preferred string) ([]byte, string, error) {
	var candidates []string
	if preferred != "" {
		candidates = append(candidates, preferred)
	}
	if exports, err := c.Exports(); err == nil {
		log.Printf("nfs client: %s exports: %v", c.ip, exports)
		for _, e := range exports {
			if !containsStr(candidates, e) {
				candidates = append(candidates, e)
			}
		}
	} else {
		log.Printf("nfs client: %s EXPORT failed (%v); trying common roots", c.ip, err)
	}
	for _, root := range []string{"/C/", "/B/", "/"} {
		if !containsStr(candidates, root) {
			candidates = append(candidates, root)
		}
	}

	var lastErr error
	for _, ex := range candidates {
		data, err := c.ReadFile(ex, ExportPDBPath)
		if err == nil && len(data) > 0 {
			log.Printf("nfs client: %s%s -> export.pdb (%d bytes)", c.ip, ex, len(data))
			return data, ex, nil
		}
		if err != nil && !notFound(err) {
			log.Printf("nfs client: %s%s export.pdb: %v", c.ip, ex, err)
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no export.pdb found in %v", candidates)
	}
	return nil, "", lastErr
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

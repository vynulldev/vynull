// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"encoding/binary"
	"fmt"
)

// Sun RPC constants.
const (
	rpcCall  uint32 = 0
	rpcReply uint32 = 1

	rpcMsgAccepted uint32 = 0
	rpcAcceptOK    uint32 = 0

	rpcAuthNull uint32 = 0
)

// RPC program/version numbers.
const (
	progPortmap uint32 = 100000
	progMount   uint32 = 100005
	progNFS     uint32 = 100003

	versPortmap uint32 = 2
	versMount   uint32 = 1
	versNFS     uint32 = 2
)

// Portmap procedures.
const (
	pmapGetPort uint32 = 3
)

// Mount procedures.
const (
	mountNull   uint32 = 0
	mountMnt    uint32 = 1
	mountExport uint32 = 5
)

// NFS v2 procedures.
const (
	nfsNull     uint32 = 0
	nfsGetAttr  uint32 = 1
	nfsSetAttr  uint32 = 2
	nfsLookup   uint32 = 4
	nfsRead     uint32 = 6
	nfsWrite    uint32 = 8
	nfsCreate   uint32 = 9
	nfsRemove   uint32 = 10
	nfsMkdir    uint32 = 14
	nfsReadDir  uint32 = 16
	nfsStatFS   uint32 = 17
)

// NFS v2 status codes.
const (
	nfsOK      uint32 = 0
	nfsNoEnt   uint32 = 2
	nfsIO      uint32 = 5
	nfsAccess  uint32 = 13
	nfsNotDir  uint32 = 20
	nfsIsDir   uint32 = 21
	nfsROFS    uint32 = 30 // read-only filesystem
)

// NFS v2 file types.
const (
	nfTypeReg uint32 = 1
	nfTypeDir uint32 = 2
)

// NFS v2 file handle size.
const fhSize = 32

// rpcHeader represents a parsed RPC call header.
type rpcHeader struct {
	XID     uint32
	Program uint32
	Version uint32
	Proc    uint32
	body    []byte // remaining bytes after header
}

// parseRPCCall parses a Sun RPC call message.
func parseRPCCall(data []byte) (*rpcHeader, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("rpc call too short: %d", len(data))
	}

	msgType := binary.BigEndian.Uint32(data[4:8])
	if msgType != rpcCall {
		return nil, fmt.Errorf("not an RPC call: type=%d", msgType)
	}

	h := &rpcHeader{
		XID:     binary.BigEndian.Uint32(data[0:4]),
		Program: binary.BigEndian.Uint32(data[12:16]),
		Version: binary.BigEndian.Uint32(data[16:20]),
		Proc:    binary.BigEndian.Uint32(data[20:24]),
	}

	// Skip auth credentials and verifier.
	pos := 24
	// Credential: flavor(4) + length(4) + data(length)
	if pos+8 > len(data) {
		return nil, fmt.Errorf("truncated credentials")
	}
	credLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
	if credLen < 0 || pos+8+credLen > len(data) {
		return nil, fmt.Errorf("invalid credential length %d", credLen)
	}
	pos += 8 + credLen

	// Verifier: flavor(4) + length(4) + data(length)
	if pos+8 > len(data) {
		return nil, fmt.Errorf("truncated verifier")
	}
	verifLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
	if verifLen < 0 || pos+8+verifLen > len(data) {
		return nil, fmt.Errorf("invalid verifier length %d", verifLen)
	}
	pos += 8 + verifLen

	if pos <= len(data) {
		h.body = data[pos:]
	}

	return h, nil
}

// xdrWriter helps build XDR-encoded RPC responses.
type xdrWriter struct {
	buf []byte
}

func newXDRWriter(capacity int) *xdrWriter {
	return &xdrWriter{buf: make([]byte, 0, capacity)}
}

func (w *xdrWriter) putU32(v uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	w.buf = append(w.buf, b...)
}

func (w *xdrWriter) putBytes(data []byte) {
	w.putU32(uint32(len(data)))
	w.buf = append(w.buf, data...)
	// XDR pads to 4-byte boundary.
	if pad := (4 - len(data)%4) % 4; pad > 0 {
		w.buf = append(w.buf, make([]byte, pad)...)
	}
}

func (w *xdrWriter) putString(s string) {
	w.putBytes([]byte(s))
}

func (w *xdrWriter) putFH(fh [fhSize]byte) {
	w.buf = append(w.buf, fh[:]...)
}

func (w *xdrWriter) bytes() []byte {
	return w.buf
}

// buildRPCReply creates the RPC reply header for a successful response.
func buildRPCReply(xid uint32) *xdrWriter {
	w := newXDRWriter(256)
	w.putU32(xid)
	w.putU32(rpcReply)
	w.putU32(rpcMsgAccepted)
	// Auth verifier: NULL auth.
	w.putU32(rpcAuthNull)
	w.putU32(0) // verifier length
	// Accept status: OK.
	w.putU32(rpcAcceptOK)
	return w
}

// xdrReader helps parse XDR-encoded RPC bodies.
type xdrReader struct {
	data []byte
	pos  int
}

func newXDRReader(data []byte) *xdrReader {
	return &xdrReader{data: data}
}

func (r *xdrReader) u32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, fmt.Errorf("xdr: truncated u32 at %d", r.pos)
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *xdrReader) opaque(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, fmt.Errorf("xdr: truncated opaque(%d) at %d", n, r.pos)
	}
	data := make([]byte, n)
	copy(data, r.data[r.pos:r.pos+n])
	r.pos += n
	// Skip XDR padding.
	if pad := (4 - n%4) % 4; pad > 0 {
		r.pos += pad
	}
	return data, nil
}

func (r *xdrReader) fh() ([fhSize]byte, error) {
	var fh [fhSize]byte
	if r.pos+fhSize > len(r.data) {
		return fh, fmt.Errorf("xdr: truncated file handle at %d", r.pos)
	}
	copy(fh[:], r.data[r.pos:r.pos+fhSize])
	r.pos += fhSize
	return fh, nil
}

func (r *xdrReader) str() (string, error) {
	length, err := r.u32()
	if err != nil {
		return "", err
	}
	data, err := r.opaque(int(length))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

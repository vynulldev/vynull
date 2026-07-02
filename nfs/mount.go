// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/vynulldev/vynull/internal/dlog"
)

const (
	mountUmnt    uint32 = 3
	mountUmntAll uint32 = 4
)

// handleMount processes Mount protocol RPCs.
func (s *Server) handleMount(hdr *rpcHeader) []byte {
	switch hdr.Proc {
	case mountNull:
		return s.mountNull(hdr)
	case mountMnt:
		return s.mountMnt(hdr)
	case mountUmnt, mountUmntAll:
		log.Printf("mount: UMOUNT")
		return buildRPCReply(hdr.XID).bytes()
	case mountExport:
		return s.mountExport(hdr)
	default:
		log.Printf("mount: unhandled proc %d", hdr.Proc)
		return nil
	}
}

func (s *Server) mountNull(hdr *rpcHeader) []byte {
	w := buildRPCReply(hdr.XID)
	return w.bytes()
}

// mountMnt handles the MNT procedure — returns a file handle for the export root.
// We accept ANY mount path and map it to our export root.
// Use an all-zeros file handle (matching rekordbox behavior).
func (s *Server) mountMnt(hdr *rpcHeader) []byte {
	if dlog.Enabled(dlog.Debug) {
		dlog.Debugf("mount: MNT raw body (%d bytes):\n%s", len(hdr.body), hex.Dump(hdr.body))
	}
	r := newXDRReader(hdr.body)
	path, _ := r.str()
	log.Printf("mount: MNT %q -> %s", path, s.exportRoot)

	// Use all-zeros file handle matching rekordbox behavior.
	var rootFH [fhSize]byte
	s.handles.Register(rootFH, s.exportRoot)

	w := buildRPCReply(hdr.XID)
	w.putU32(nfsOK)
	w.putFH(rootFH)
	resp := w.bytes()
	if dlog.Enabled(dlog.Debug) {
		dlog.Debugf("mount: MNT response (%d bytes):\n%s", len(resp), hex.Dump(resp))
	}
	return resp
}

// mountExport handles the EXPORT procedure — returns the export list,
// or an empty list when the device is unlinked. Returning an empty
// list is rekordbox's "I'm not available" signal that makes the
// CDJ drop its LINK indicator within one poll cycle (~200ms).
func (s *Server) mountExport(hdr *rpcHeader) []byte {
	w := buildRPCReply(hdr.XID)
	if s.LinkedFn != nil && !s.LinkedFn() {
		log.Printf("mount: EXPORT (unlinked — empty list)")
		w.putU32(0) // value-follows = false, no exports
		return w.bytes()
	}
	log.Printf("mount: EXPORT")

	w.putU32(1) // value-follows = true
	w.putBytes([]byte("/C/"))

	// Access group.
	group := "0.0.0.0/0.0.0.0"
	if s.IP != nil {
		group = fmt.Sprintf("%s/255.255.0.0", s.IP.String())
	}
	w.putU32(1) // group value-follows = true
	w.putBytes([]byte(group))
	w.putU32(0) // end groups

	w.putU32(0) // no more exports
	return w.bytes()
}

// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/vynulldev/vynull/internal/dlog"
)

// Portmapper responds to GETPORT queries on Pioneer's non-standard port 50111
// (and, in CDJ mode, the standard RPC port 111).
type Portmapper struct {
	mountPort uint32
	nfsPort   uint32
	cdjMode   bool // if true, also bind the privileged port 111 (CDJ-USB source)
}

func (pm *Portmapper) Start(ctx context.Context) error {
	// A deck linking to us as a CDJ-USB source discovers our mount/NFS ports by
	// querying the standard RPC portmapper on the privileged port 111; a
	// rekordbox source queries Pioneer's non-standard 50111 instead. We only
	// bind 111 in CDJ mode — attempting it in rekordbox mode just fails with a
	// permission error (it's privileged) and is never used there.
	ports := []int{50111}
	if pm.cdjMode {
		ports = []int{111, 50111}
	}
	var conns []*net.UDPConn
	var bound []int
	for _, port := range ports {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			log.Printf("portmapper: cannot bind port %d: %v", port, err)
			continue
		}
		conns = append(conns, conn)
		bound = append(bound, port)
	}
	if len(conns) == 0 {
		return fmt.Errorf("portmapper: could not bind any port")
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	log.Printf("portmapper listening on UDP ports %v (mount=%d, nfs=%d)",
		bound, pm.mountPort, pm.nfsPort)

	// Port 111 is privileged (<1024). Without it, a deck using us as a CDJ-USB
	// source can't find our NFS mount, so track LOADS fail (browsing still
	// works — that's dbserver-only over TCP). Warn only in CDJ mode, where 111
	// is actually needed; rekordbox mode never attempts it (it uses 50111).
	has111 := false
	for _, p := range bound {
		if p == 111 {
			has111 = true
		}
	}
	if pm.cdjMode && !has111 {
		log.Printf("portmapper: WARNING — port 111 is NOT bound; CDJ-mode track loading WILL FAIL " +
			"(the deck can't locate the NFS mount). Grant the port with ONE of: " +
			"`sudo sysctl -w net.ipv4.ip_unprivileged_port_start=111` (system-wide, survives rebuilds), " +
			"`sudo setcap 'cap_net_bind_service=+ep' <binary>` (re-run after each build), or run with sudo. " +
			"rekordbox mode is unaffected.")
	}

	// Run a listener for each port.
	for _, c := range conns[1:] {
		go pm.listenLoop(ctx, c)
	}
	pm.listenLoop(ctx, conns[0])
	return nil
}

func (pm *Portmapper) listenLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("portmap read: %v", err)
			continue
		}

		resp := pm.handlePacket(buf[:n])
		if resp != nil {
			conn.WriteToUDP(resp, remoteAddr)
			// Duplicate matches rekordbox's wire pattern — the
			// CDJ retries 200ms later if it only sees one reply,
			// which makes UNLINK look like multiple polling rounds.
			time.Sleep(400 * time.Microsecond)
			conn.WriteToUDP(resp, remoteAddr)
		}
	}
}

func (pm *Portmapper) handlePacket(data []byte) []byte {
	hdr, err := parseRPCCall(data)
	if err != nil {
		log.Printf("portmap: %v", err)
		return nil
	}
	dlog.Debugf("portmap: request xid=%08x prog=%d vers=%d proc=%d", hdr.XID, hdr.Program, hdr.Version, hdr.Proc)

	if hdr.Program != progPortmap || hdr.Version != versPortmap {
		return nil
	}

	if hdr.Proc != pmapGetPort {
		log.Printf("portmap: unhandled proc %d", hdr.Proc)
		return nil
	}

	return pm.handleGetPort(hdr)
}

func (pm *Portmapper) handleGetPort(hdr *rpcHeader) []byte {
	// Body contains: program(4) + version(4) + proto(4) + port(4)
	r := newXDRReader(hdr.body)
	prog, _ := r.u32()
	vers, _ := r.u32()
	proto, _ := r.u32()
	_, _ = r.u32() // port (ignored)

	var port uint32
	switch prog {
	case progMount:
		port = pm.mountPort
		log.Printf("portmap: GETPORT mount vers=%d proto=%d -> %d", vers, proto, port)
	case progNFS:
		port = pm.nfsPort
		log.Printf("portmap: GETPORT nfs vers=%d proto=%d -> %d", vers, proto, port)
	default:
		log.Printf("portmap: GETPORT unknown program %d", prog)
	}

	w := buildRPCReply(hdr.XID)
	w.putU32(port)
	resp := w.bytes()
	dlog.Debugf("portmap: response (%d bytes): %x", len(resp), resp)
	return resp
}

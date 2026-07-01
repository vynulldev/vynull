// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"vynull/internal/dlog"
)

// Listen on both standard (111) and Pioneer (50111) portmapper ports.
var portmapPorts = []int{111, 50111}

// Portmapper responds to GETPORT queries on Pioneer's non-standard port 50111.
type Portmapper struct {
	mountPort uint32
	nfsPort   uint32
}

func (pm *Portmapper) Start(ctx context.Context) error {
	// Listen on the standard RPC portmapper port (111) and Pioneer's
	// non-standard 50111. A deck linking to us as a CDJ-USB source queries
	// 111 to discover our mount/NFS ports; a rekordbox source queries 50111.
	// We can't move this — the deck chooses where to ask.
	var conns []*net.UDPConn
	var bound []int
	for _, port := range portmapPorts {
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
	// works — that's dbserver-only over TCP). Make the consequence explicit and
	// point at the fixes; rekordbox mode is unaffected (it uses 50111).
	has111 := false
	for _, p := range bound {
		if p == 111 {
			has111 = true
		}
	}
	if !has111 {
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

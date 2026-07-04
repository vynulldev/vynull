// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/proto"
)

const peerTimeout = 5 * time.Second

// Peer represents a device seen on the Pro DJ Link network.
type Peer struct {
	DeviceNumber uint8
	Name         string
	DeviceType   proto.DeviceType
	MAC          net.HardwareAddr
	IP           net.IP
	LastSeen     time.Time
}

// PeerTracker maintains a list of active devices on the network.
type PeerTracker struct {
	mu      sync.RWMutex
	peers   map[uint8]*Peer
	ownIP   net.IP
	ownType proto.DeviceType
	// warnedCollisions records peers we've already logged a collision
	// warning for, keyed by DeviceNumber. Keeps the log clean when the
	// same peer keeps sending keep-alive packets.
	warnedCollisions map[uint8]bool
}

// NewPeerTracker creates a tracker that filters out packets from ownIP.
// ownType lets the tracker emit a clear warning when another peer on the
// LAN announces the same device type as us (e.g. another rekordbox while
// we're emulating rekordbox), which causes CDJs to cache conflicting
// (slot, trackID) → metadata entries and silently refuse track loads.
func NewPeerTracker(ownIP net.IP, ownType proto.DeviceType) *PeerTracker {
	return &PeerTracker{
		peers:            make(map[uint8]*Peer),
		ownIP:            ownIP.To4(),
		ownType:          ownType,
		warnedCollisions: make(map[uint8]bool),
	}
}

// HandlePacket processes an incoming keep-alive packet. Returns true
// if the packet was successfully processed as a keep-alive.
func (pt *PeerTracker) HandlePacket(data []byte) bool {
	typ, err := proto.ValidatePacket(data)
	if err != nil || typ != proto.TypeKeepAlive {
		return false
	}

	ka, err := proto.ParseKeepAlive(data)
	if err != nil {
		return false
	}

	// Ignore our own packets.
	if ka.IP.Equal(pt.ownIP) {
		return false
	}

	// Override the wire-declared device type for newer Pioneer mixers
	// — some advertise with type byte 0x02 (CDJ), not 0x01 (Mixer) as
	// the older convention. Without this, mixers appear as CDJs in
	// /api/peers + the players grid. Name-prefix is the most reliable
	// discriminator across models.
	devType := ka.DeviceType
	upperName := strings.ToUpper(ka.Name)
	if strings.HasPrefix(upperName, "DJM") || strings.HasPrefix(upperName, "RMX") {
		devType = proto.DeviceMixer
	}

	pt.mu.Lock()
	pt.peers[ka.DeviceNumber] = &Peer{
		DeviceNumber: ka.DeviceNumber,
		Name:         ka.Name,
		DeviceType:   devType,
		MAC:          ka.MAC,
		IP:           ka.IP,
		LastSeen:     time.Now(),
	}
	// Warn once if another peer is also advertising as rekordbox while we
	// are. The CDJ caches metadata per (slot, trackID); two rekordbox
	// sources both claim slot 4 (rekordbox), and once enough conflicting
	// entries accumulate the CDJ silently rejects load commands.
	shouldWarn := pt.ownType == proto.DeviceRekordbox &&
		ka.DeviceType == proto.DeviceRekordbox &&
		!pt.warnedCollisions[ka.DeviceNumber]
	if shouldWarn {
		pt.warnedCollisions[ka.DeviceNumber] = true
	}
	pt.mu.Unlock()

	if shouldWarn {
		log.Printf("WARNING: rekordbox slot collision — peer %q (device %d at %s) is also advertising as rekordbox (slot 4). CDJs may silently reject track loads after a few minutes of mixed activity. Close the other rekordbox or change one source's mode.",
			ka.Name, ka.DeviceNumber, ka.IP)
	}
	return true
}

// Peers returns a snapshot of all peers seen within the timeout window.
func (pt *PeerTracker) Peers() []Peer {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	now := time.Now()
	var result []Peer
	for _, p := range pt.peers {
		if now.Sub(p.LastSeen) < peerTimeout {
			result = append(result, *p)
		}
	}
	return result
}

// Count returns the number of active peers.
func (pt *PeerTracker) Count() uint8 {
	peers := pt.Peers()
	if len(peers) > 255 {
		return 255
	}
	return uint8(len(peers))
}

// RemoveByIP drops any peer whose IP matches the given address. Used
// when a peer explicitly signals it's going away (e.g. via a dbserver
// 0x0100 Teardown message) so we don't keep them in the active peer
// list until the 5s keep-alive timeout fires.
func (pt *PeerTracker) RemoveByIP(ip net.IP) {
	if ip == nil {
		return
	}
	target := ip.To4()
	if target == nil {
		target = ip
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	for n, p := range pt.peers {
		if p.IP.Equal(target) {
			delete(pt.peers, n)
		}
	}
}

// HasDeviceNumber returns true if any active peer is using the given device number.
func (pt *PeerTracker) HasDeviceNumber(n uint8) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	p, ok := pt.peers[n]
	if !ok {
		return false
	}
	return time.Since(p.LastSeen) < peerTimeout
}

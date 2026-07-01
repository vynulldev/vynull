// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"vynull/proto"
)

const (
	announcePort      = 50000
	beatPort          = 50001 // beat broadcasts + DJM channel state
	statusPort        = 50002
	claimInterval     = 300 * time.Millisecond
	claimRepeat       = 3
	keepAliveInterval = 1500 * time.Millisecond
	statusInterval    = 100 * time.Millisecond // rekordbox sends at ~10Hz in pairs
)

// VirtualDevice emulates a Rekordbox instance or CDJ on the Pro DJ Link network.
type VirtualDevice struct {
	Name         string
	DeviceNumber uint8
	DeviceType   proto.DeviceType
	MediaSlot    uint8 // proto.SlotUSB or proto.SlotRekordbox
	MAC          net.HardwareAddr
	IP           net.IP
	Broadcast    net.IP
	TrackCount   uint16 // number of tracks in library

	Peers    *PeerTracker
	Monitor  *PlayerMonitor
	Settings *CDJSettings

	announceConn    *net.UDPConn
	statusConn      *net.UDPConn
	beatConn        *net.UDPConn
	statusMu        sync.Mutex
	linkAcked       map[string]int  // track 0x46 count per peer for link activation sequence
	settingsPending map[string]bool // peers that need a 0x47 on next 0x46
	claimDone       chan struct{}   // closed when first 0x46 received, stops 0x02 cycling
	keepAliveCount  int

	// pendingLoad tracks the most recent load command sent to each CDJ
	// IP so we can recover when the CDJ replies with 0x1c "media
	// unavailable" — typically happens after a long idle period when
	// the deck has timed out our media-slot registration. Recovery:
	// re-send MarshalMediaResponse to reassert the slot, then re-send
	// the original 0x19 once. Limited to one retry per load to avoid
	// loops.
	pendingLoad map[string]*loadAttempt

	// mixerStatuses holds the latest parsed 0x29 status broadcast per
	// mixer (keyed by device number). Populated by listenStatus when
	// a mixer peer sends 0x29; consumed by getPeers via MixerSnapshot.
	mixerStatuses   map[uint8]*proto.MixerStatus
	mixerStatusesMu sync.RWMutex

	// Linked-state machine: `linked` controls whether the NFS server
	// returns a populated MOUNT EXPORT (via Server.LinkedFn) and
	// whether dbserver accepts menu sessions. Keep-alives + status
	// broadcasts flow regardless — matches rekordbox's wire
	// behavior, and is what makes UNLINK look instant on the CDJ:
	// empty EXPORT response causes the CDJ to drop its LINK indicator
	// within one poll cycle (~200ms) instead of waiting for the
	// keep-alive timeout (~5-6s).
	linked       atomic.Bool
	linkOnChange func(bool) // fired on each state transition (true→false fires before the flag flips, false→true fires after)
}

type loadAttempt struct {
	TrackID      uint32
	TargetDevice uint8
	SentAt       time.Time
	Retried      bool
}

// LoadTrackOnCDJ sends a remote track load command (type 0x19) to a CDJ.
// First ensures the CDJ has received Link activation + media info so it
// has a proper NFS mount context for file access.
func (d *VirtualDevice) LoadTrackOnCDJ(trackID uint32, targetDevice uint8, targetIP net.IP) error {
	if d.statusConn == nil {
		return fmt.Errorf("status connection not ready")
	}
	addr := &net.UDPAddr{IP: targetIP, Port: statusPort}

	// Just the load command. rekordbox sends nothing else on a
	// load (verified in a capture: 5-min capture with
	// two loads 245s apart, only two 0x19 pairs and zero
	// announce/link/media re-sends). The deck's NFS/link state is
	// maintained by the 0x46 (link keepalive) handshake every ~5s
	// handled in the status read loop — not by anything we resend on
	// load. Re-sending the announce/link/media trio here was being
	// interpreted by the deck as a fresh device announce, which dropped
	// its NFS mount and led to 0x1c "media unavailable" rejection of
	// the subsequent load command (especially common after a track
	// played out to the ENDED state).
	pkt := proto.MarshalLoadTrackCommand(d.Name, d.DeviceNumber, d.MediaSlot, targetDevice, trackID)
	// Duplicate pair matches rekordbox's 0x19 send pattern (two
	// copies <1ms apart).
	if _, err := d.sendStatus(pkt, addr); err != nil {
		return fmt.Errorf("send load command: %w", err)
	}
	if _, err := d.sendStatus(pkt, addr); err != nil {
		return fmt.Errorf("send load command (duplicate): %w", err)
	}
	// Track this load so the 0x1c "media unavailable" handler in listenStatus
	// can log which track the deck rejected. We deliberately do NOT retry or
	// re-assert media on 0x1c — re-announcing media drops the deck's NFS mount
	// and cascades (see the note in the 0x1c handler); the deck self-recovers.
	//
	// There is intentionally NO load watchdog. An earlier version resent the
	// 0x19 (and, worse, re-asserted the media source) when the deck didn't
	// adopt a track within a window. The media re-assert dropped the deck's
	// NFS mount and made failures spread ("freezes / stops loading more and
	// more"), so we send exactly what rekordbox sends: the 0x19 pair,
	// once, and nothing else unsolicited.
	d.statusMu.Lock()
	if d.pendingLoad == nil {
		d.pendingLoad = make(map[string]*loadAttempt)
	}
	d.pendingLoad[targetIP.String()] = &loadAttempt{
		TrackID:      trackID,
		TargetDevice: targetDevice,
		SentAt:       time.Now(),
	}
	d.statusMu.Unlock()
	log.Printf("sent load track command (x2): track=%d -> device %d (%s)", trackID, targetDevice, targetIP)
	return nil
}

// BroadcastTrackRefresh sends the type 0x1d "track data invalidated"
// trigger (twice, ephemeral source port — matching rekordbox) to
// the link-local broadcast. Connected CDJs re-fetch the track's data,
// which is how a cue/colour/rating edit from the web UI reaches the
// deck without a track reload. CDJ only acts if its loaded track ID
// matches trackID.
func (d *VirtualDevice) BroadcastTrackRefresh(trackID uint32) {
	pkt := proto.MarshalTrackRefreshTrigger(d.Name, d.DeviceNumber, trackID)
	d.sendEphemeralBroadcastPair(pkt, fmt.Sprintf("track refresh track=%d", trackID))
}

// BroadcastRatingRefresh sends the type 0x1b "rating changed" trigger
// (twice, ephemeral source port). CDJs re-fetch the track's rating.
func (d *VirtualDevice) BroadcastRatingRefresh(trackID uint32) {
	pkt := proto.MarshalRatingRefreshTrigger(d.Name, d.DeviceNumber, trackID)
	d.sendEphemeralBroadcastPair(pkt, fmt.Sprintf("rating refresh track=%d", trackID))
}

// sendEphemeralBroadcastPair opens a fresh UDP socket bound to an
// ephemeral source port and sends pkt twice to the link-local broadcast
// on port 50002. rekordbox uses a different ephemeral source port
// for each 50002 broadcast (51725, 51726, …), which CDJs apparently use
// to distinguish "live rekordbox events" from packets emitted by their
// own bound-to-50002 listener.
func (d *VirtualDevice) sendEphemeralBroadcastPair(pkt []byte, label string) {
	dst := &net.UDPAddr{IP: d.Broadcast, Port: statusPort}
	src := &net.UDPAddr{IP: d.IP, Port: 0}
	conn, err := net.ListenUDP("udp4", src)
	if err != nil {
		log.Printf("%s trigger: ephemeral socket: %v", label, err)
		return
	}
	defer conn.Close()
	n1, err1 := conn.WriteToUDP(pkt, dst)
	n2, err2 := conn.WriteToUDP(pkt, dst)
	log.Printf("%s sent from %s to %s: %d+%d bytes err=%v,%v",
		label, conn.LocalAddr(), dst, n1, n2, err1, err2)
}

// Start brings up the device's network side: binds the announce/status
// ports, starts the listener and keep-alive loops, and blocks until ctx
// is cancelled. The device starts UNLINKED — keep-alives and status
// broadcasts flow unconditionally (matching rekordbox), but the
// NFS server returns an empty MOUNT EXPORT list and the dbserver
// rejects menu sessions until the user flips the link on via
// SetLinked(true). The CDJ's LINK indicator follows the EXPORT
// response, not the keep-alive presence.
func (d *VirtualDevice) Start(ctx context.Context) error {
	d.Peers = NewPeerTracker(d.IP, d.DeviceType)

	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", announcePort))
	if err != nil {
		return fmt.Errorf("bind announce port %d: %w", announcePort, err)
	}
	d.announceConn = conn.(*net.UDPConn)
	defer d.announceConn.Close()

	// Bind status port 50002 for media queries and status broadcasting.
	statusConn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", statusPort))
	if err != nil {
		return fmt.Errorf("bind status port %d: %w", statusPort, err)
	}
	d.statusConn = statusConn.(*net.UDPConn)
	defer d.statusConn.Close()

	// Bind beat port 50001 — primarily for beat broadcasts (0x28),
	// but also where DJM-class mixers broadcast their channel-
	// on-air state (type 0x03 at ~3 Hz). We only listen; nothing
	// proactive on this port today.
	beatConn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", beatPort))
	if err != nil {
		log.Printf("bind beat port %d: %v (DJM channel state will be unavailable)", beatPort, err)
	} else {
		d.beatConn = beatConn.(*net.UDPConn)
		defer d.beatConn.Close()
	}

	// Initialize claim completion channel before starting goroutines.
	d.claimDone = make(chan struct{})

	// Start background listeners + loops. These run regardless of link
	// state — outbound sends are gated inside send()/sendKeepAlive so
	// each tick is a no-op while unlinked.
	go d.listenAnnouncements(ctx)
	go d.listenStatus(ctx)
	if d.beatConn != nil {
		go d.listenBeats(ctx)
	}
	go d.statusBroadcastLoop(ctx)

	// Log one keep-alive packet for debugging (purely informational).
	sample := proto.MarshalKeepAlive(d.Name, d.DeviceNumber, d.DeviceType, d.MAC, d.IP, 0)
	log.Printf("keep-alive packet (%d bytes):\n%s", len(sample), hex.Dump(sample))
	log.Printf("starting claim sequence on %s (device %d, type %s)", d.IP, d.DeviceNumber, d.DeviceType)

	// Claim sequence is synchronous — keep-alive loop must NOT start
	// until the CDJ has acknowledged our device number. Running them
	// concurrently confuses the CDJ's protocol state machine and the
	// device never gets registered.
	if err := d.claimSequence(ctx); err != nil {
		return fmt.Errorf("claim sequence: %w", err)
	}
	log.Printf("device ready on %s (device %d, type %s) — broadcasting; UNLINKED (MOUNT EXPORT empty until user clicks LINK)", d.IP, d.DeviceNumber, d.DeviceType)

	return d.keepAliveLoop(ctx)
}

// Linked reports whether outbound advertising/transmits are enabled.
func (d *VirtualDevice) Linked() bool { return d.linked.Load() }

// MixerSnapshot returns a copy of the latest parsed mixer status for
// each known mixer peer, keyed by device number. Empty when no mixer
// has broadcast since startup.
func (d *VirtualDevice) MixerSnapshot() map[uint8]proto.MixerStatus {
	d.mixerStatusesMu.RLock()
	defer d.mixerStatusesMu.RUnlock()
	out := make(map[uint8]proto.MixerStatus, len(d.mixerStatuses))
	for k, v := range d.mixerStatuses {
		out[k] = *v
	}
	return out
}

// SendDisconnectSignal sends the unicast 0x16 "session reset" status
// packet (× 2, ~1ms apart) to every tracked CDJ peer — matching what
// rekordbox emits at UNLINK time. The actual fast-disconnect
// mechanism lives elsewhere: the NFS MOUNT EXPORT handler returns an
// empty list when unlinked, which causes the CDJ to drop its LINK
// indicator on its next ~200ms poll. This 0x16 is kept for parity
// with rekordbox's wire behavior.
//
// Bypasses the linked-state gate intentionally — this is meant to be
// called from inside the unlink callback, which fires while the gate
// is still open, but the bypass keeps it safe regardless.
func (d *VirtualDevice) SendDisconnectSignal() {
	if d.statusConn == nil || d.Peers == nil {
		return
	}
	pkt := proto.MarshalStatusRekordbox(d.Name, d.DeviceNumber)
	for _, p := range d.Peers.Peers() {
		if p.DeviceType != proto.DeviceCDJ {
			continue
		}
		dst := &net.UDPAddr{IP: p.IP, Port: statusPort}
		d.statusConn.WriteToUDP(pkt, dst)
		time.Sleep(time.Millisecond)
		d.statusConn.WriteToUDP(pkt, dst)
		log.Printf("device: sent unicast 0x16 disconnect signal to %s (device %d)", p.IP, p.DeviceNumber)
	}
}

// SetLinkChangeCallback registers a one-shot callback fired on every
// state transition (true→false or false→true). Used by api/main to
// tear down dbserver sessions when the user clicks UNLINK.
func (d *VirtualDevice) SetLinkChangeCallback(cb func(bool)) {
	d.linkOnChange = cb
}

// SetLinked turns library availability on or off. Keep-alives and
// status broadcasts flow regardless (we stay visible to the CDJ);
// `linked` only gates the MOUNT EXPORT response (empty when unlinked,
// via Server.LinkedFn) and triggers dbserver session teardown via the
// link-change callback. The CDJ's LINK indicator follows the EXPORT
// response, so UNLINK looks instant from the CDJ's POV.
func (d *VirtualDevice) SetLinked(on bool) {
	if d.linked.Load() == on {
		return
	}
	if on {
		d.linked.Store(true)
		log.Printf("device: LINK on — NFS exports now non-empty, dbserver accepting sessions")
		if d.linkOnChange != nil {
			d.linkOnChange(true)
		}
		return
	}
	// Unlink: callback first so it can tear down any in-flight TCP
	// menu sessions while the device is still considered "up".
	log.Printf("device: LINK off — NFS exports now empty, dbserver sessions torn down")
	if d.linkOnChange != nil {
		d.linkOnChange(false)
	}
	d.linked.Store(false)
	d.statusMu.Lock()
	d.linkAcked = nil
	d.statusMu.Unlock()
}

// claimSequence performs the device number claim.
// CDJ mode: 4-stage (0x0a → 0x00 → 0x02 → 0x04).
// Rekordbox mode: 2-stage (0x00 → 0x02) matching rekordbox.
func (d *VirtualDevice) claimSequence(ctx context.Context) error {
	dst := &net.UDPAddr{IP: d.Broadcast, Port: announcePort}

	if d.DeviceType != proto.DeviceRekordbox {
		// CDJ mode: Stage 1 — initial announcement (0x0a).
		pkt := proto.MarshalInitialAnnounce(d.Name, d.DeviceType, d.MAC)
		if err := d.sendRepeat(ctx, pkt, dst, claimRepeat); err != nil {
			return err
		}
	}

	// Stage 2: First claim (0x00, MAC). Sent in pairs like rekordbox.
	for i := uint8(1); i <= claimRepeat; i++ {
		pkt := proto.MarshalFirstClaim(d.Name, i, d.MAC)
		d.send(pkt, dst)
		d.send(pkt, dst) // sent twice per iteration
		if err := d.sleepCtx(ctx, claimInterval); err != nil {
			return err
		}
	}

	// Stage 3: Second claim (0x02, IP + MAC + device number).
	if d.DeviceType == proto.DeviceRekordbox {
		// Rekordbox claims multiple device slots: {17, 18, 41, 42, 43, 44}
		// each sent twice per counter iteration, for 6 iterations.
		// This matches rekordbox behavior from pcap analysis.
		rbSlots := []uint8{17, 18, 41, 42, 43, 44}
		for counter := uint8(1); counter <= 6; counter++ {
			for _, slot := range rbSlots {
				pkt := proto.MarshalSecondClaim(d.Name, slot, counter, d.MAC, d.IP)
				d.send(pkt, dst)
				d.send(pkt, dst) // sent twice per rekordbox
			}
			if err := d.sleepCtx(ctx, claimInterval); err != nil {
				return err
			}
		}
	} else {
		for i := uint8(1); i <= claimRepeat; i++ {
			pkt := proto.MarshalSecondClaim(d.Name, d.DeviceNumber, i, d.MAC, d.IP)
			if err := d.send(pkt, dst); err != nil {
				return err
			}
			if err := d.sleepCtx(ctx, claimInterval); err != nil {
				return err
			}
		}
	}

	if d.DeviceType != proto.DeviceRekordbox {
		// CDJ mode: Stage 4 — final claim (0x04).
		for i := uint8(1); i <= claimRepeat; i++ {
			pkt := proto.MarshalFinalClaim(d.Name, d.DeviceNumber, i)
			if err := d.send(pkt, dst); err != nil {
				return err
			}
			if err := d.sleepCtx(ctx, claimInterval); err != nil {
				return err
			}
		}
	}

	return nil
}

// keepAliveLoop sends keep-alive packets.
// CDJ mode: type 0x06 every 1.5 seconds.
// Rekordbox mode: type 0x06 every 1.5 seconds, plus a bounded startup 0x02
// claim burst (50ms cadence) that stops on CDJ link or the send cap.
func (d *VirtualDevice) keepAliveLoop(ctx context.Context) error {
	dst := &net.UDPAddr{IP: d.Broadcast, Port: announcePort}
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	// Send one 0x06 immediately.
	if err := d.sendKeepAlive(dst); err != nil {
		return err
	}

	// Rekordbox mode: a brief startup 0x02 claim burst that cycles through
	// {17, 18, 41, 42, 43, 44}, stopping when the CDJ links (0x46) OR after a
	// bounded number of sends — whichever comes first. Capping it is the fix
	// for the "long runtime then late link" bug: the burst used to be gated
	// ONLY on claimDone, so with no CDJ present it cycled device-slot claims
	// forever, and a CDJ that linked later never saw a stable identity (no
	// device name, broken settings handshake). rekordbox claims in a
	// short startup burst then sends only 0x06; the cap matches that intent.
	var fastTicker *time.Ticker
	rbSlots := []uint8{17, 18, 41, 42, 43, 44}
	rbSlotIdx := 0
	rbCounter := uint8(1)
	rbSendCount := 0
	const maxStartupRBClaims = 200 // ~10s at 50ms — generous startup window, then stop
	if d.DeviceType == proto.DeviceRekordbox {
		fastTicker = time.NewTicker(50 * time.Millisecond)
		defer fastTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutting down virtual device")
			return nil
		case <-ticker.C:
			// Type 0x06 at 1.5s intervals (both modes).
			pkt := proto.MarshalKeepAlive(d.Name, d.DeviceNumber, d.DeviceType, d.MAC, d.IP, d.Peers.Count()+1)
			if err := d.send(pkt, dst); err != nil {
				log.Printf("keep-alive send error: %v", err)
			}
		case <-func() <-chan time.Time {
			if fastTicker == nil {
				return nil
			}
			// Stop the 0x02 claim burst once the CDJ links (0x46) or after the
			// startup cap — never run it for the whole session (see above).
			if rbSendCount >= maxStartupRBClaims {
				return nil
			}
			select {
			case <-d.claimDone:
				return nil
			default:
				return fastTicker.C
			}
		}():
			// Rekordbox mode: cycle 0x02 through slots in pairs.
			// Stops when CDJ sends 0x46 (link activation).
			slot := rbSlots[rbSlotIdx]
			pkt := proto.MarshalSecondClaim(d.Name, slot, rbCounter, d.MAC, d.IP)
			d.send(pkt, dst)
			rbSendCount++
			if rbSendCount%2 == 0 {
				rbSlotIdx++
				if rbSlotIdx >= len(rbSlots) {
					rbSlotIdx = 0
					rbCounter++
				}
			}
		}
	}
}

func (d *VirtualDevice) hostname() string {
	h, _ := os.Hostname()
	return h
}

func (d *VirtualDevice) sendKeepAlive(dst *net.UDPAddr) error {
	if d.DeviceType == proto.DeviceRekordbox {
		// Rekordbox sends BOTH type 0x02 (primary) and 0x06 (secondary).
		// Type 0x02 has an incrementing counter at byte 0x2e.
		d.keepAliveCount++
		pkt02 := proto.MarshalRekordboxKeepAlive(d.Name, d.DeviceNumber, d.MAC, d.IP)
		pkt02[0x2e] = byte(int(d.DeviceNumber) + d.keepAliveCount) // incrementing counter
		pkt02[0x2f] = 0x06                                         // keepalive mode (not claim)
		d.send(pkt02, dst)

		// Send type 0x06 every 3rd tick (~4.5s, matching rekordbox ratio)
		if d.keepAliveCount%3 == 0 {
			pkt06 := proto.MarshalKeepAlive(d.Name, d.DeviceNumber, d.DeviceType, d.MAC, d.IP, d.Peers.Count()+1)
			d.send(pkt06, dst)
		}
		return nil
	}
	pkt := proto.MarshalKeepAlive(d.Name, d.DeviceNumber, d.DeviceType, d.MAC, d.IP, d.Peers.Count()+1)
	return d.send(pkt, dst)
}

// statusBroadcastLoop sends periodic status packets on port 50002.
// CDJ mode sends type 0x0a (292 bytes), rekordbox mode sends type 0x16 (48 bytes).
func (d *VirtualDevice) statusBroadcastLoop(ctx context.Context) {
	dst := &net.UDPAddr{IP: d.Broadcast, Port: statusPort}
	statusTicker := time.NewTicker(statusInterval)
	defer statusTicker.Stop()

	if d.DeviceType == proto.DeviceRekordbox {
		// Wait for claim to complete (CDJ sends 0x46) before starting 0x29.
		// rekordbox only sends 0x11/0x06 in response to CDJ requests,
		// not proactively. The 0x46 handler manages the full link activation.
		// rekordbox starts 0x29 at the same time as the first 0x46.
		log.Printf("waiting for claim to complete before starting 0x29 broadcast...")
		select {
		case <-d.claimDone:
		case <-ctx.Done():
			return
		}

		log.Printf("status broadcast started (type=0x29 to %s)", dst)

		// In rekordbox mode, the 0x46 handler manages the full link activation.
		// No proactive 0x11/0x06/0x47 — only respond to CDJ requests.
		// The 0x29 broadcast is the only proactive send on port 50002.
		for {
			select {
			case <-ctx.Done():
				return
			case <-statusTicker.C:
				// rekordbox sends 0x29 in pairs (two copies) to
				// broadcast at ~10Hz effective rate. No unicast to peers.
				//
				// NOTE: we do NOT broadcast a periodic 0x4a settings-notify
				// here. A full rekordbox capture (a capture)
				// shows rekordbox sends ZERO 0x4a packets on port 50002 in
				// either direction over an entire session — so 0x4a is not the
				// "settings available" advertisement the old code comment
				// claimed, and is not what enables the deck's MY SETTINGS LOAD
				// button. (0x4a is still sent on an actual settings edit, from
				// NotifySettingsChanged.) The button-enable signal lives in the
				// initial link handshake, which that capture didn't include.
				ext := proto.MarshalStatusRekordboxExt(d.Name, d.DeviceNumber)
				d.sendStatus(ext, dst)
				d.sendStatus(ext, dst)
			}
		}
	} else {
		// CDJ mode.
		var devSetting []byte
		if d.Settings != nil {
			devSetting = d.Settings.GetDevSetting()
		}
		cdjPkt := proto.MarshalStatusCDJ(d.Name, d.DeviceNumber, d.MediaSlot, d.TrackCount, devSetting)
		mediaPkt := proto.MarshalMediaResponse(d.Name, d.DeviceNumber, d.MediaSlot, d.TrackCount, d.MAC, d.IP)
		d.sendStatus(cdjPkt, dst)
		d.sendStatus(mediaPkt, dst)
		log.Printf("status broadcast started (type=0x0a to %s)", dst)

		for {
			select {
			case <-ctx.Done():
				return
			case <-statusTicker.C:
				var ds []byte
				if d.Settings != nil {
					ds = d.Settings.GetDevSetting()
				}
				pkt := proto.MarshalStatusCDJ(d.Name, d.DeviceNumber, d.MediaSlot, d.TrackCount, ds)
				d.sendStatus(pkt, dst)
			}
		}
	}
}

// listenStatus reads packets from port 50002 and handles media queries.
// listenBeats reads from port 50001 — primarily DJ-Link beat packets
// (which we don't act on yet), but also the type-0x03 mixer channel
// broadcasts that DJM family devices emit at ~3Hz. We merge the
// channel state from those packets into the existing mixerStatuses
// map so the API surfaces them alongside any 0x29/0x30 presence info.
func (d *VirtualDevice) listenBeats(ctx context.Context) {
	buf := make([]byte, 512)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		d.beatConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := d.beatConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("beat read error: %v", err)
			continue
		}
		if addr.IP.Equal(d.IP) {
			continue
		}
		if n < proto.MinPacketSize {
			continue
		}
		if _, err := proto.ValidatePacket(buf[:n]); err != nil {
			continue
		}
		pktType := buf[0x0a]
		if pktType != proto.TypeMixerChannels {
			continue
		}
		// Must be from a known mixer peer.
		if d.Peers == nil {
			continue
		}
		isMixer := false
		for _, p := range d.Peers.Peers() {
			if p.IP.Equal(addr.IP) && p.DeviceType == proto.DeviceMixer {
				isMixer = true
				break
			}
		}
		if !isMixer {
			continue
		}
		mx, ok := proto.ParseMixerStatus(buf[:n])
		if !ok {
			continue
		}
		d.mixerStatusesMu.Lock()
		if d.mixerStatuses == nil {
			d.mixerStatuses = make(map[uint8]*proto.MixerStatus)
		}
		prev := d.mixerStatuses[mx.DeviceNumber]
		// Merge channel state with whatever 0x29/0x30 presence info
		// we already have so the API can surface both at once.
		if prev != nil {
			prev.ChannelOnAir = mx.ChannelOnAir
			prev.ChannelStateKnown = true
		} else {
			d.mixerStatuses[mx.DeviceNumber] = mx
		}
		d.mixerStatusesMu.Unlock()
	}
}

func (d *VirtualDevice) listenStatus(ctx context.Context) {
	buf := make([]byte, 512)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		d.statusConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := d.statusConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("status read error: %v", err)
			continue
		}

		// Skip our own packets.
		if addr.IP.Equal(d.IP) {
			continue
		}

		// Parse status packets for the monitor display.
		pktType := uint8(0)
		if n >= proto.MinPacketSize {
			if _, err := proto.ValidatePacket(buf[:n]); err == nil {
				pktType = buf[0x0a]
			}
		}

		// Feed CDJ status to monitor.
		if pktType == proto.TypeStatusCDJ {
			if status, ok := proto.ParseCDJStatus(buf[:n]); ok && d.Monitor != nil {
				d.Monitor.Update(status)
			}
		}

		// Parse mixer status broadcasts. Older DJMs (900NXS2 family)
		// emit type 0x29 with channel + master state inline; newer
		// DJMs (DJM confirmed) emit a stripped 0x30 packet with
		// only name + device number (channel state appears to live
		// on a separate TCP control protocol that we haven't RE'd
		// yet). Both flow through ParseMixerStatus; the latter just
		// produces a MixerStatus with zero channel/master fields.
		if (pktType == proto.TypeMixerStatusLegacy || pktType == proto.TypeMixerStatusNew) && d.Peers != nil {
			isMixer := false
			for _, p := range d.Peers.Peers() {
				if p.IP.Equal(addr.IP) && p.DeviceType == proto.DeviceMixer {
					isMixer = true
					break
				}
			}
			if isMixer {
				if mx, ok := proto.ParseMixerStatus(buf[:n]); ok {
					d.mixerStatusesMu.Lock()
					if d.mixerStatuses == nil {
						d.mixerStatuses = make(map[uint8]*proto.MixerStatus)
					}
					_, alreadyLogged := d.mixerStatuses[mx.DeviceNumber]
					d.mixerStatuses[mx.DeviceNumber] = mx
					d.mixerStatusesMu.Unlock()
					if !alreadyLogged {
						log.Printf("mixer: first 0x29 from %s (%s) device %d — len=%d masterBPM=%.2f masterDev=%d onAir=%04b beat=%d\n%s",
							mx.Name, addr.IP, mx.DeviceNumber, n, mx.MasterBPM, mx.MasterDevice, mx.ChannelOnAir, mx.BeatInBar, hex.Dump(buf[:n]))
					}
				}
			}
		}

		// Per-packet logging here used to be unconditional. With 10 Hz CDJ
		// status (0x0a) plus our own 0x29 echo, each connected CDJ produced
		// ~20 log lines/sec and a steady stream of allocations in the hot
		// path. Now we only log unknown packet types (anything not status,
		// media query, settings, or 0x46 link keepalive) — useful for new
		// reverse-engineering, silent during normal operation.
		switch pktType {
		case proto.TypeStatusCDJ, 0x29, 0x30, proto.TypeStatusQuery, proto.TypeMediaQuery,
			0x35, 0x37, 0x48, 0x46, 0x1a:
			// expected, no log. 0x1a is the CDJ's ack of our 0x19 load
			// command (sent as a pair right after every load, on success
			// and failure alike — see the load watchdog in LoadTrackOnCDJ
			// for the actual "did the load take" check).
		default:
			log.Printf("status recv unknown type=0x%02x from %s (%d bytes)\n%s",
				pktType, addr, n, hex.Dump(buf[:n]))
		}

		// Respond to media queries and status queries on port 50002.
		// Important: respond to the CDJ's port 50002, NOT the ephemeral source port.
		replyAddr := &net.UDPAddr{IP: addr.IP, Port: statusPort}

		// 0x1c = "media unavailable" rejection of our 0x19 load command.
		//
		// We do NOT try to "recover" by re-sending MarshalMediaResponse:
		// pcap analysis (a capture vs the rekordbox reference
		// a capture) showed rekordbox NEVER resends
		// the media announce — and that our resend is actively harmful. The
		// deck interprets a fresh media announce as a re-announce and drops
		// its NFS mount, which turns a single transient 0x1c into a
		// persistent cascade ("no track will load anymore"). See the matching
		// note in LoadTrackOnCDJ. Just log it; the deck recovers on its own.
		if pktType == 0x1c {
			d.statusMu.Lock()
			pending := d.pendingLoad[addr.IP.String()]
			d.statusMu.Unlock()
			if pending != nil {
				log.Printf("status: 0x1c rejection of load track=%d from %s (not resending media — deck self-recovers)", pending.TrackID, addr.IP)
			}
		}

		// Check if this is a media query (type 0x05, 48 bytes).
		mq, ok := proto.ParseMediaQuery(buf[:n])
		if ok {
			log.Printf("media query from device %d, target %d, slot %d at %s\n%s",
				mq.DeviceNumber, mq.TargetDevice, mq.SlotRequested, addr, hex.Dump(buf[:n]))
			resp := proto.MarshalMediaResponse(d.Name, d.DeviceNumber, d.MediaSlot, d.TrackCount, d.MAC, d.IP)
			d.sendStatus(resp, replyAddr)
			d.sendStatus(resp, replyAddr) // rekordbox sends twice
		}

		// Respond to type 0x10 (rekordbox hello from CDJ) with 0x11 ONLY.
		// rekordbox does NOT send 0x06 here — that comes later via 0x05.
		if pktType == proto.TypeStatusQuery {
			announce := proto.MarshalRekordboxAnnounce(d.Name, d.DeviceNumber, d.hostname())
			d.sendStatus(announce, replyAddr)
		}

		// Type 0x35 (My Settings READ request) — CDJ asks to load settings.
		if pktType == 0x35 {
			log.Printf("status: settings read request (0x35) from %s — sending 0x36 response", addr)
			var mySetting, devSetting []byte
			if d.Settings != nil {
				mySetting = d.Settings.GetMySetting()
				devSetting = d.Settings.GetDevSetting()
			}
			resp := proto.MarshalMySettingsResponse(d.Name, d.DeviceNumber, d.MediaSlot, mySetting, devSetting)
			d.sendStatus(resp, replyAddr)
		}

		// Type 0x37 (My Settings WRITE) — CDJ saves MYSETTING to us.
		// Body at offset 0x30 contains 32 bytes of MYSETTING data.
		if pktType == 0x37 && n >= 0x50 {
			log.Printf("status: my settings write (0x37) from %s — saving + sending 0x38 ack", addr)
			if d.Settings != nil {
				d.Settings.SaveMySetting(buf[0x30:0x50])
			}
			ack := make([]byte, 40)
			proto.PutHeader(ack, 0x38)
			copy(ack[0x0b:0x1f], []byte(d.Name))
			ack[0x1f] = 0x01
			ack[0x21] = d.DeviceNumber
			ack[0x22] = 0x00
			ack[0x23] = 0x04
			ack[0x24] = d.DeviceNumber
			ack[0x25] = d.MediaSlot
			d.sendStatus(ack, replyAddr)
		}

		// Type 0x48 (Device Settings WRITE) — CDJ saves DEVSETTING to us.
		// Body at offset 0x30 contains 6 bytes of DEVSETTING data.
		if pktType == 0x48 && n >= 0x36 {
			log.Printf("status: device settings write (0x48) from %s — saving + sending 0x49 ack", addr)
			if d.Settings != nil {
				d.Settings.SaveDevSetting(buf[0x30:0x36])
			}
			ack := make([]byte, 40)
			proto.PutHeader(ack, 0x49)
			copy(ack[0x0b:0x1f], []byte(d.Name))
			ack[0x1f] = 0x01
			ack[0x21] = d.DeviceNumber
			ack[0x22] = 0x00
			ack[0x23] = 0x04
			ack[0x24] = d.DeviceNumber
			ack[0x25] = d.MediaSlot
			d.sendStatus(ack, replyAddr)
		}

		// Type 0x46 (Link keepalive) — CDJ sends this every ~5 seconds.
		// rekordbox sequence (from pcap):
		//   1st 0x46: respond with 0x16 (simple status) — triggers CDJ to send 0x05
		//   2nd 0x46: respond with 0x47 (link activation) — triggers CDJ to send 0x35
		// The CDJ only sends 0x35 (settings request) after receiving 0x47.
		if pktType == 0x46 {
			peerKey := addr.IP.String()
			d.statusMu.Lock()
			if d.linkAcked == nil {
				d.linkAcked = make(map[string]int)
			}
			count := d.linkAcked[peerKey]
			d.linkAcked[peerKey] = count + 1
			d.statusMu.Unlock()

			// Signal the keep-alive loop to stop 0x02 cycling.
			if d.claimDone != nil {
				select {
				case <-d.claimDone:
					// already closed
				default:
					close(d.claimDone)
				}
			}

			if count == 0 {
				// First 0x46: send 0x16, then another 3s later (matching rekordbox).
				log.Printf("status: first 0x46 from %s — sending 0x16", addr)
				statusPkt := proto.MarshalStatusRekordbox(d.Name, d.DeviceNumber)
				d.sendStatus(statusPkt, replyAddr)
				// Delayed second 0x16 triggers CDJ to send 0x05
				peerAddr := &net.UDPAddr{IP: addr.IP, Port: statusPort}
				go func() {
					time.Sleep(3 * time.Second)
					d.sendStatus(statusPkt, peerAddr)
					log.Printf("status: sent second 0x16 to %s (delayed 3s)", peerAddr)
				}()
			} else {
				// All subsequent 0x46: respond with 0x47 (link activation with DEVSETTING).
				// rekordbox responds to every 0x46 with 0x47.
				var ds []byte
				if d.Settings != nil {
					ds = d.Settings.GetDevSetting()
				}
				linkPkt := proto.MarshalLinkActivation(d.Name, d.DeviceNumber, d.MediaSlot, ds)
				d.sendStatus(linkPkt, replyAddr)
				// Diagnostic: log the keepalive cadence. The deck maintains its
				// NFS-mount/link context via this ~5s 0x46→0x47 handshake; if it
				// stops (e.g. after a track ENDs) a subsequent load can't fetch.
				// Logging every 0x46 lets us see exactly when the cadence lapses.
				log.Printf("status: 0x46 #%d from %s — sent 0x47 (link keepalive)", count+1, addr)
			}

			// Settings update: if this peer has pending settings from NotifySettingsChanged,
			// send 0x47 with updated DEVSETTING (CDJ's 0x46 is acknowledging our 0x4a).
			d.statusMu.Lock()
			pending := d.settingsPending != nil && d.settingsPending[peerKey]
			if pending {
				delete(d.settingsPending, peerKey)
			}
			d.statusMu.Unlock()
			if pending {
				var ds []byte
				if d.Settings != nil {
					ds = d.Settings.GetDevSetting()
				}
				linkPkt := proto.MarshalLinkActivation(d.Name, d.DeviceNumber, d.MediaSlot, ds)
				d.sendStatus(linkPkt, replyAddr)
				log.Printf("settings: sent 0x47 with updated DEVSETTING to %s (in response to 0x46)", addr)
			}
		}
	}
}

// NotifySettingsChanged sends a 0x4a settings notification to all connected CDJs.
// rekordbox sequence: 0x4a → CDJ responds 0x46 → RB sends 0x47.
// We set a flag so the next 0x46 from each peer triggers a 0x47 with updated settings.
func (d *VirtualDevice) NotifySettingsChanged() {
	if d.statusConn == nil || d.Peers == nil {
		return
	}
	notify := proto.MarshalSettingsNotify(d.Name, d.DeviceNumber, d.MediaSlot)
	d.statusMu.Lock()
	if d.settingsPending == nil {
		d.settingsPending = make(map[string]bool)
	}
	for _, p := range d.Peers.Peers() {
		d.settingsPending[p.IP.String()] = true
	}
	d.statusMu.Unlock()
	for _, p := range d.Peers.Peers() {
		addr := &net.UDPAddr{IP: p.IP, Port: statusPort}
		d.sendStatus(notify, addr)
		log.Printf("settings: sent 0x4a notification to %s", addr)
	}
}

// listenAnnouncements reads packets from the announce port and feeds them
// to the peer tracker.
func (d *VirtualDevice) listenAnnouncements(ctx context.Context) {
	buf := make([]byte, 512)
	// Track which peers we've already logged so the steady ~3Hz keepalive
	// stream doesn't fill the log file (and the hot path doesn't allocate
	// strings/hex.Dump output on every received packet).
	loggedPeer := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		d.announceConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := d.announceConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("announce read error: %v", err)
			continue
		}

		if d.Peers.HandlePacket(buf[:n]) {
			ka, _ := proto.ParseKeepAlive(buf[:n])
			if ka != nil {
				peerKey := fmt.Sprintf("%s/%d", ka.IP, ka.DeviceNumber)
				if !loggedPeer[peerKey] {
					loggedPeer[peerKey] = true
					log.Printf("peer: %s (%s) device %d at %s",
						ka.Name, ka.DeviceType, ka.DeviceNumber, ka.IP)
					if ka.DeviceType == proto.DeviceCDJ {
						log.Printf("CDJ keep-alive (%d bytes):\n%s", n, hex.Dump(buf[:n]))
					}
				}
			}
		}
	}
}

// send writes to the announce port (50000). Suppressed when unlinked
// so the CDJ times us out and removes us from its source list within
// ~5-6s (keep-alive timeout). This is the "fast visual disconnect"
// path — we tried mimicking rekordbox's "keep broadcasting,
// empty EXPORT" but the CDJ display didn't actually drop, so silence
// is the only signal that reliably clears the CDJ.
func (d *VirtualDevice) send(pkt []byte, dst *net.UDPAddr) error {
	if !d.linked.Load() {
		return nil
	}
	_, err := d.announceConn.WriteToUDP(pkt, dst)
	return err
}

// sendStatus writes to the status port (50002). Same gate as send().
func (d *VirtualDevice) sendStatus(pkt []byte, dst *net.UDPAddr) (int, error) {
	if !d.linked.Load() {
		return 0, nil
	}
	return d.statusConn.WriteToUDP(pkt, dst)
}

func (d *VirtualDevice) sendRepeat(ctx context.Context, pkt []byte, dst *net.UDPAddr, count int) error {
	for i := 0; i < count; i++ {
		if err := d.send(pkt, dst); err != nil {
			return err
		}
		if err := d.sleepCtx(ctx, claimInterval); err != nil {
			return err
		}
	}
	return nil
}

func (d *VirtualDevice) sleepCtx(ctx context.Context, dur time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(dur):
		return nil
	}
}

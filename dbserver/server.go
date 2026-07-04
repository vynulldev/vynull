// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

const (
	discoveryPort = 12523
)

// RootMenuEntry is one slot in the CDJ's top-level LINK menu. Filled
// by main.go from the api.MenuStore so users can configure
// visibility + order from the web UI without touching the dbserver.
type RootMenuEntry struct {
	ID       uint32
	Label    string
	ItemType uint32
}

// MenuSource lets handleRootMenu pull the user-configured root menu.
// When the Server has no MenuSource wired, handleRootMenu falls back
// to a hardcoded default set so existing installs keep working.
//
// TrackDetail returns the key of the field the CDJ should render in
// the second column of every track list (e.g. "bpm", "artist", "key").
// Empty string means "use the historical default" (BPM) so the
// fallback path doesn't have to mind it.
type MenuSource interface {
	RootMenu() []RootMenuEntry
	TrackDetail() string
}

// PlaylistEntry is the minimal shape the dbserver needs to render the
// CDJ's PLAYLIST menu. Implemented by api.PlaylistStore (and any future
// playlist source) so the dbserver doesn't depend on the api package.
type PlaylistEntry struct {
	ID       uint32
	Name     string
	IsFolder bool
}

// PlaylistSource exposes user-defined playlists to the dbserver. The
// shape is intentionally tree-walk friendly: Children gives one level
// at a time (matching how the CDJ navigates 0x1105), Tracks returns the
// ordered track IDs for a leaf playlist.
type PlaylistSource interface {
	Children(parentID uint32) []PlaylistEntry
	Tracks(playlistID uint32) []uint32
	// HistoryFolderID returns the playlist-store ID of the
	// auto-managed "History" folder (0 if it hasn't been created
	// yet — typically before any track has crossed the play-count
	// threshold). The HISTORY root-menu category lists this folder's
	// children (one playlist per day).
	HistoryFolderID() uint32
}

// Server implements the Pro DJ Link database server protocol.
type Server struct {
	Library      *library.Library
	PDB          *pdb.Database
	DeviceNumber uint8
	ExportRoot   string
	Analysis     *analysis.Store
	Folders      *pdb.FolderLookup
	Playlists    PlaylistSource // optional; if nil, PLAYLIST menu falls back to filesystem folders
	Menu         MenuSource     // optional; if nil, root menu falls back to a built-in default list
	Cues         *CueStore
	Settings     *device.CDJSettings
	ReplayDir    string // if set, serve raw response packets from this directory

	// OnPeerTeardown, if set, is invoked when a CDJ sends us a 0x0100
	// Teardown message — main.go wires this to PeerTracker.RemoveByIP so
	// we stop including the peer in keep-alive counts, settings
	// notifications, etc. Without this we'd wait 5s for the keep-alive
	// timeout to drop them.
	OnPeerTeardown func(net.IP)

	// LinkedFn, if set, gates new incoming TCP connections. When it
	// returns false, both the discovery port (12523) and the dynamic
	// query port immediately close any new connection — so a CDJ
	// can't re-browse our library after the user clicks UNLINK even
	// if it tries to reconnect.
	LinkedFn func() bool

	discoveryLn net.Listener
	dynamicLn   net.Listener
	dynamicPort uint16 // OS-assigned ephemeral port

	// activeConns tracks live dynamic-port client connections so we can
	// send a 0x0100 Teardown message to each one on shutdown.
	activeConns   map[net.Conn]struct{}
	activeConnsMu sync.Mutex
}

// Start begins listening on both the discovery port (12523) and a
// random dynamic query port.
func (s *Server) Start(ctx context.Context) error {
	var err error

	s.discoveryLn, err = net.Listen("tcp4", fmt.Sprintf(":%d", discoveryPort))
	if err != nil {
		return fmt.Errorf("bind discovery port %d: %w", discoveryPort, err)
	}

	// Let the OS pick a random ephemeral port.
	s.dynamicLn, err = net.Listen("tcp4", ":0")
	if err != nil {
		s.discoveryLn.Close()
		return fmt.Errorf("bind dynamic port: %w", err)
	}
	s.dynamicPort = uint16(s.dynamicLn.Addr().(*net.TCPAddr).Port)

	log.Printf("dbserver listening on ports %d (discovery) and %d (queries)", discoveryPort, s.dynamicPort)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		s.acceptDiscovery(ctx)
	}()

	go func() {
		defer wg.Done()
		s.acceptDynamic(ctx)
	}()

	<-ctx.Done()
	// Politely tell connected CDJs we're going away (this exact packet is
	// sent to each active dbserver session on shutdown).
	s.sendTeardownToActiveConns()
	s.discoveryLn.Close()
	s.dynamicLn.Close()
	wg.Wait()
	return nil
}

// sendTeardownToActiveConns sends a 0x0100 (Teardown) dbserver message
// with TxID = DBSetupTxID to each active dynamic-port client connection
// before we close the listener. CDJs interpret this as a clean session
// end (vs raw TCP RST which can leave the CDJ in a confused state on
// some firmware versions).
func (s *Server) sendTeardownToActiveConns() {
	s.activeConnsMu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConns))
	for c := range s.activeConns {
		conns = append(conns, c)
	}
	s.activeConnsMu.Unlock()

	if len(conns) == 0 {
		return
	}

	teardown := proto.MarshalDBMessage(&proto.DBMessage{
		TxID: proto.DBSetupTxID,
		Type: 0x0100,
	})
	log.Printf("dbserver: sending teardown (0x0100) to %d active session(s)", len(conns))
	for _, c := range conns {
		c.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := c.Write(teardown); err != nil {
			log.Printf("dbserver: teardown write to %s: %v", c.RemoteAddr(), err)
		}
	}
}

func (s *Server) trackConn(conn net.Conn) {
	s.activeConnsMu.Lock()
	defer s.activeConnsMu.Unlock()
	if s.activeConns == nil {
		s.activeConns = make(map[net.Conn]struct{})
	}
	s.activeConns[conn] = struct{}{}
}

func (s *Server) untrackConn(conn net.Conn) {
	s.activeConnsMu.Lock()
	defer s.activeConnsMu.Unlock()
	delete(s.activeConns, conn)
}

// ActiveSessions returns the count of live dynamic-port client connections.
// Used by the periodic resource logger to spot session accumulation.
func (s *Server) ActiveSessions() int {
	s.activeConnsMu.Lock()
	defer s.activeConnsMu.Unlock()
	return len(s.activeConns)
}

// Unlink sends a 0x0100 teardown to every active dbserver session and then
// closes the underlying TCP connections. CDJs interpret this as "rekordbox
// went away" and drop their cached link state. The keep-alive broadcaster
// keeps running, so CDJs can re-link on their own a few seconds later
// (typical Pioneer "unlink" UX). Returns the number of sessions closed.
func (s *Server) Unlink() int {
	s.sendTeardownToActiveConns()

	s.activeConnsMu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConns))
	for c := range s.activeConns {
		conns = append(conns, c)
	}
	s.activeConnsMu.Unlock()

	// Give the teardown bytes a moment to flush before we yank the socket.
	if len(conns) > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range conns {
		// Close triggers the handler goroutine's read to return io.EOF,
		// which untrackConn drops from the map naturally.
		c.Close()
	}
	if len(conns) > 0 {
		log.Printf("dbserver: unlink closed %d session(s)", len(conns))
	}
	return len(conns)
}

// acceptDiscovery handles connections on port 12523.
func (s *Server) acceptDiscovery(ctx context.Context) {
	for {
		conn, err := s.discoveryLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("dbserver discovery accept: %v", err)
				return
			}
		}
		if s.LinkedFn != nil && !s.LinkedFn() {
			log.Printf("dbserver discovery: rejecting %s (unlinked)", conn.RemoteAddr())
			conn.Close()
			continue
		}
		go s.handleDiscovery(conn)
	}
}

func (s *Server) handleDiscovery(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Read the CDJ's discovery query. Format: 4-byte length prefix + body.
	// E.g.: 00 00 00 0f "RemoteDBServer" 00 (4 + 15 = 19 bytes)
	// TCP may deliver in chunks, so use ReadFull for the length, then body.
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		log.Printf("dbserver discovery read length: %v", err)
		return
	}
	bodyLen := int(binary.BigEndian.Uint32(lenBuf[:]))
	if bodyLen > 256 || bodyLen < 0 {
		log.Printf("dbserver discovery: invalid length %d", bodyLen)
		return
	}
	buf := make([]byte, bodyLen)
	n := 0
	if bodyLen > 0 {
		var err error
		n, err = io.ReadFull(conn, buf)
		if err != nil {
			log.Printf("dbserver discovery read body: %v", err)
			return
		}
	}

	log.Printf("dbserver discovery from %s (%d bytes):\n%s",
		conn.RemoteAddr(), n, hex.Dump(buf[:n]))

	// Respond with dynamic port number for the dbserver.
	resp := make([]byte, 2)
	binary.BigEndian.PutUint16(resp, s.dynamicPort)
	log.Printf("dbserver discovery: sent port %d to %s", s.dynamicPort, conn.RemoteAddr())
	if _, err := conn.Write(resp); err != nil {
		log.Printf("dbserver discovery write: %v", err)
	}
}

// acceptDynamic handles connections on the dynamic port (1051).
func (s *Server) acceptDynamic(ctx context.Context) {
	for {
		conn, err := s.dynamicLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("dbserver dynamic accept: %v", err)
				return
			}
		}
		if s.LinkedFn != nil && !s.LinkedFn() {
			log.Printf("dbserver dynamic: rejecting %s (unlinked)", conn.RemoteAddr())
			conn.Close()
			continue
		}
		go s.handleSession(ctx, conn)
	}
}

// handleSession processes a full dbserver session on the dynamic port.
func (s *Server) handleSession(ctx context.Context, conn net.Conn) {
	s.trackConn(conn)
	defer s.untrackConn(conn)
	defer conn.Close()
	log.Printf("dbserver session from %s", conn.RemoteAddr())

	// Enable TCP keepalive to prevent idle timeout.
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(10 * time.Second)
	}

	// Handshake: CDJ sends 5 bytes, we echo them back.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	clientHandshake := make([]byte, 5)
	if _, err := io.ReadFull(conn, clientHandshake); err != nil {
		log.Printf("dbserver handshake read: %v", err)
		return
	}
	if _, err := conn.Write(clientHandshake); err != nil {
		log.Printf("dbserver handshake write: %v", err)
		return
	}
	log.Printf("dbserver handshake: echo %s", hex.EncodeToString(clientHandshake))
	log.Printf("dbserver handshake complete with %s", conn.RemoteAddr())

	conn.SetReadDeadline(time.Time{}) // clear deadline

	handler := &Handler{
		lib:          s.Library,
		pdb:          s.PDB,
		deviceNumber: s.DeviceNumber,
		exportRoot:   s.ExportRoot,
		analysis:     s.Analysis,
		folders:      s.Folders,
		playlists:    s.Playlists,
		menu:         s.Menu,
		cues:         s.Cues,
		settings:     s.Settings,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := readMessage(conn)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Printf("dbserver: client %s disconnected", conn.RemoteAddr())
			} else {
				log.Printf("dbserver read: %v", err)
			}
			return
		}

		// 0x0100 = Teardown. Peer is politely closing — notify so the
		// device layer can drop them from the active peer list immediately
		// (otherwise we wait up to 5s for keep-alive timeout).
		if msg.Type == 0x0100 && s.OnPeerTeardown != nil {
			if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
				log.Printf("dbserver: 0x0100 teardown from %s — removing peer", tcpAddr.IP)
				s.OnPeerTeardown(tcpAddr.IP)
			}
		}

		responses := handler.Handle(msg)
		// Coalesce all response messages into a single TCP write,
		// which sends Header+Items+Footer in one segment.
		var combined []byte
		for _, resp := range responses {
			data := proto.MarshalDBMessage(resp)

			// Replay mode: if we have a recorded response
			// for this response type, use those exact bytes instead.
			if s.ReplayDir != "" {
				if replacement := s.findReplay(resp.Type, len(data), msg); replacement != nil {
					// Fix up the txid to match the current request.
					binary.BigEndian.PutUint32(replacement[6:10], msg.TxID)
					log.Printf("dbserver REPLAY type=0x%04x txid=%08x (%d bytes from recording)",
						resp.Type, msg.TxID, len(replacement))
					data = replacement
				}
			}
			// Per-response send logging removed; menu queries fan out into
			// many sub-messages and each line allocated a formatted string.
			// Replay path keeps its log so unintended fallback is visible.
			//
			// At trace level, dump the exact bytes we put on the wire for each
			// response message so a hung deck can be traced to the last response
			// it accepted before its dbserver client wedged.
			if dlog.Enabled(dlog.Trace) {
				dlog.Tracef("dbserver SEND type=0x%04x txid=%08x (%d bytes):\n%s",
					resp.Type, resp.TxID, len(data), hex.Dump(data))
			}

			combined = append(combined, data...)
		}
		if len(combined) > 0 {
			if _, err := conn.Write(combined); err != nil {
				log.Printf("dbserver write: %v", err)
				return
			}
		}
	}
}

// readMessage reads a single dbserver message from the connection.
func readMessage(conn net.Conn) (*proto.DBMessage, error) {
	// Messages can be framed in two ways:
	// 1. [0x11] [magic 4] [0x11] [header+body] — standard framing
	// 2. [4-byte BE length] [0x11] [magic 4] [0x11] [header+body] — length-prefixed
	//
	// We detect which by reading the first byte.
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		return nil, err
	}

	// Handle length-prefixed framing: [4-byte BE length] [payload]
	// The payload contains: [0x11] [magic 4] [0x11] [header+args]
	if first[0] == 0x00 {
		var lenRest [3]byte
		if _, err := io.ReadFull(conn, lenRest[:]); err != nil {
			return nil, fmt.Errorf("read length prefix: %w", err)
		}
		msgLen := int(binary.BigEndian.Uint32(append([]byte{first[0]}, lenRest[:]...)))
		if msgLen <= 0 || msgLen > 65536 {
			return nil, fmt.Errorf("invalid message length: %d", msgLen)
		}
		payload := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, fmt.Errorf("read length-prefixed payload (%d bytes): %w", msgLen, err)
		}
		// Parse the payload — find the header after skipping frame bytes.
		// Payload format: [0x11?] [magic 4] [0x11] [header...]
		// or just: [header...] without magic
		// Try to find and skip past the magic.
		start := 0
		for start < len(payload) && payload[start] == 0x11 {
			start++
		}
		// Check if magic follows.
		if start+4 <= len(payload) && payload[start] == proto.DBMagic[0] &&
			payload[start+1] == proto.DBMagic[1] &&
			payload[start+2] == proto.DBMagic[2] &&
			payload[start+3] == proto.DBMagic[3] {
			start += 4
			// Skip separator 0x11.
			if start < len(payload) && payload[start] == 0x11 {
				start++
			}
		}
		if start >= len(payload) {
			return nil, fmt.Errorf("length-prefixed payload too short after stripping frame")
		}
		return proto.ParseDBMessage(payload[start:])
	}

	var magic [4]byte
	if first[0] == proto.DBMagic[0] {
		magic[0] = first[0]
		if _, err := io.ReadFull(conn, magic[1:]); err != nil {
			return nil, err
		}
	} else if first[0] == 0x11 {
		if _, err := io.ReadFull(conn, magic[:]); err != nil {
			return nil, err
		}
	} else {
		// Unknown framing — dump raw bytes for debugging.
		peek := make([]byte, 64)
		peek[0] = first[0]
		n, _ := conn.Read(peek[1:])
		log.Printf("dbserver unknown framing, raw bytes:\n%s", hex.Dump(peek[:n+1]))
		return nil, fmt.Errorf("unexpected byte 0x%02x", first[0])
	}

	if magic != proto.DBMagic {
		return nil, fmt.Errorf("invalid dbserver magic: %x", magic)
	}

	// Read fields in wire order:
	// After magic, read: txid(NumberField4), type(NumberField2),
	// argcount(NumberField1), tags(BinaryField)

	// Transaction ID: [0x11] [4 bytes]
	txidField := make([]byte, 5)
	if _, err := io.ReadFull(conn, txidField); err != nil {
		return nil, fmt.Errorf("read txid field: %w", err)
	}
	txid := binary.BigEndian.Uint32(txidField[1:5])

	// Message type: [0x10] [2 bytes]
	typeField := make([]byte, 3)
	if _, err := io.ReadFull(conn, typeField); err != nil {
		return nil, fmt.Errorf("read type field: %w", err)
	}
	msgType := binary.BigEndian.Uint16(typeField[1:3])

	// Argument count: [0x0f] [1 byte]
	countField := make([]byte, 2)
	if _, err := io.ReadFull(conn, countField); err != nil {
		return nil, fmt.Errorf("read argcount field: %w", err)
	}
	argCount := int(countField[1])

	// Tags: [0x14] [4-byte length] [12 bytes]
	tagsHeader := make([]byte, 5)
	if _, err := io.ReadFull(conn, tagsHeader); err != nil {
		return nil, fmt.Errorf("read tags header: %w", err)
	}
	tagsLen := int(binary.BigEndian.Uint32(tagsHeader[1:5]))
	tags := make([]byte, tagsLen)
	if tagsLen > 0 {
		if _, err := io.ReadFull(conn, tags); err != nil {
			return nil, fmt.Errorf("read tags data: %w", err)
		}
	}

	// Per-message recv logging removed from the hot path — menu browsing
	// produces dozens of these per second and hex.EncodeToString(tags)
	// allocates every time. Re-enable here if you're debugging a new
	// request type.

	// Read arguments as typed fields.
	// Each argument is: [type_tag 1] [data...]
	// NumberField: 0x0f XX | 0x10 XX XX | 0x11 XX XX XX XX
	// BinaryField: 0x14 [4-byte len] [data]
	// StringField: 0x26 [4-byte char count] [utf16 data]
	msg := &proto.DBMessage{
		TxID: txid,
		Type: msgType,
	}

	// Some read queries (waveform preview, etc.) advertise a trailing binary
	// arg (tag 0x03) that is never sent — we must NOT try to read it or we'd
	// block. But some commands DO transmit it, and skipping it then leaves
	// the blob in the TCP stream and desyncs framing on the next read
	// ("unexpected byte 0x14", session dropped until the deck reconnects):
	//   0x2705 = cue save (binary cue blob)
	//   0x2805 = tagged analysis-section read — sends a ~150-byte binary spec
	//            blob (e.g. "PVB2"/"EXT"). This was the cause of the deck
	//            dropping the dbserver link mid-session.
	actualArgCount := argCount
	sendsTrailingBinary := msgType == 0x2705 || msgType == 0x2805
	if !sendsTrailingBinary {
		for i := argCount - 1; i >= 0; i-- {
			if tags[i] == 0x03 {
				actualArgCount = i
			} else {
				break
			}
		}
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < actualArgCount; i++ {
		var tagByte [1]byte
		if _, err := io.ReadFull(conn, tagByte[:]); err != nil {
			return nil, fmt.Errorf("arg %d/%d tag: %w", i, actualArgCount, err)
		}
		arg := proto.DBArg{}
		switch tagByte[0] {
		case 0x0f, 0x04: // 1-byte number
			var v [1]byte
			if _, err := io.ReadFull(conn, v[:]); err != nil {
				return nil, fmt.Errorf("arg %d (int8): %w", i, err)
			}
			arg.Tag = proto.ArgInt8
			arg.Int8 = v[0]
		case 0x10, 0x05: // 2-byte number
			var v [2]byte
			if _, err := io.ReadFull(conn, v[:]); err != nil {
				return nil, fmt.Errorf("arg %d (int16): %w", i, err)
			}
			arg.Tag = proto.ArgInt16
			arg.Int16 = binary.BigEndian.Uint16(v[:])
		case 0x11, 0x06: // 4-byte number
			var v [4]byte
			if _, err := io.ReadFull(conn, v[:]); err != nil {
				return nil, fmt.Errorf("arg %d (int32): %w", i, err)
			}
			arg.Tag = proto.ArgInt32
			arg.Int32 = binary.BigEndian.Uint32(v[:])
		case 0x14, 0x03: // binary (0x14 = standard, 0x03 = CDJ alternate)
			var lenBuf [4]byte
			if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
				return nil, fmt.Errorf("arg %d (binary len): %w", i, err)
			}
			length := int(binary.BigEndian.Uint32(lenBuf[:]))
			data := make([]byte, length)
			if length > 0 {
				if _, err := io.ReadFull(conn, data); err != nil {
					return nil, fmt.Errorf("arg %d (binary data): %w", i, err)
				}
			}
			arg.Tag = proto.ArgBinary
			arg.Bytes = data
		case 0x26, 0x02: // string
			var lenBuf [4]byte
			if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
				return nil, fmt.Errorf("arg %d (string len): %w", i, err)
			}
			charCount := int(binary.BigEndian.Uint32(lenBuf[:]))
			data := make([]byte, charCount*2)
			if charCount > 0 {
				if _, err := io.ReadFull(conn, data); err != nil {
					return nil, fmt.Errorf("arg %d (string data): %w", i, err)
				}
			}
			arg.Tag = proto.ArgString
			arg.Str = string(data) // raw UTF-16 bytes
		default:
			return nil, fmt.Errorf("arg %d: unknown field tag 0x%02x", i, tagByte[0])
		}
		msg.Args = append(msg.Args, arg)
	}

	conn.SetReadDeadline(time.Time{}) // clear deadline
	return msg, nil
}

// findReplay looks for a recorded response file in the replay directory.
// Files are named like "PWV4_651_7273.bin", "PWAV_645_948.bin", etc.
// Matches by response type and content tag (e.g., PWV4 inside 0x4f02).
func (s *Server) findReplay(respType uint16, ourSize int, req *proto.DBMessage) []byte {
	if s.ReplayDir == "" {
		return nil
	}

	// Determine which tag to look for based on response type and request context.
	var prefix string
	switch respType {
	case 0x4f02: // ext analysis response — match by ANLZ tag
		// Check the request's tag fourcc
		if req != nil && req.Type == 0x2c04 && len(req.Args) >= 3 {
			v := req.Args[2].Int()
			b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
			tag := string([]byte{b[3], b[2], b[1], b[0]})
			switch tag {
			case "PWV4":
				prefix = "PWV4"
			case "PWV5":
				prefix = "PWV5"
			case "PQT2":
				prefix = "PQT2"
			case "PVB2":
				prefix = "NOT_FOUND" // real RB returns not-found for PVB2
			default:
				prefix = "NOT_FOUND"
			}
		}
	case 0x4402:
		prefix = "PWAV"
	case 0x4602:
		prefix = "PQTZ"
	case 0x4502:
		prefix = "PSSI_0x4502"
	case 0x4e02:
		prefix = "NXS2_CUE"
	default:
		return nil
	}

	if prefix == "" {
		return nil
	}

	// Find first matching file.
	entries, err := os.ReadDir(s.ReplayDir)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && filepath.Ext(name) == ".bin" {
			data, err := os.ReadFile(filepath.Join(s.ReplayDir, name))
			if err != nil {
				continue
			}
			return data
		}
	}

	return nil
}

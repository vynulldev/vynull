// SPDX-License-Identifier: GPL-3.0-or-later

package dbclient

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultDBPort is the dbserver port most CDJs use; DBServerPort falls back to
// it (via the caller) when the lookup service isn't reachable.
const DefaultDBPort = 1051

// DBServerPort asks a player which TCP port its dbserver listens on, by the
// standard query on port 12523: send a length-prefixed "RemoteDBServer" string
// and read back a 2-byte port. (Port varies by model/firmware, so it must be
// queried rather than assumed.)
func DBServerPort(ip net.IP) (int, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:12523", ip), 3*time.Second)
	if err != nil {
		return 0, fmt.Errorf("dial db-port lookup: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	greeting := append([]byte{0x00, 0x00, 0x00, 0x0f}, []byte("RemoteDBServer\x00")...)
	if _, err := conn.Write(greeting); err != nil {
		return 0, fmt.Errorf("db-port write: %w", err)
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return 0, fmt.Errorf("db-port read: %w", err)
	}
	port := int(binary.BigEndian.Uint16(portBuf[:]))
	if port == 0 {
		return 0, fmt.Errorf("db-port lookup returned 0")
	}
	return port, nil
}

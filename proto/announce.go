// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"fmt"
	"net"
)

// Packet sizes for the announcement sequence.
const (
	InitialAnnounceSize = 0x25 // 37 bytes
	FirstClaimSize      = 0x2c // 44 bytes
	SecondClaimSize     = 0x32 // 50 bytes
	FinalClaimSize      = 0x2a // 42 bytes
	KeepAliveSize       = 0x36 // 54 bytes
)

// KeepAlive represents a parsed keep-alive packet from the network.
type KeepAlive struct {
	Name         string
	DeviceNumber uint8
	DeviceType   DeviceType
	MAC          net.HardwareAddr
	IP           net.IP
}

// ParseKeepAlive parses a keep-alive packet (type 0x06).
func ParseKeepAlive(data []byte) (*KeepAlive, error) {
	if len(data) < KeepAliveSize {
		return nil, ErrPacketTooShort
	}
	typ, err := ValidatePacket(data)
	if err != nil {
		return nil, err
	}
	if typ != TypeKeepAlive {
		return nil, fmt.Errorf("expected keep-alive type 0x06, got 0x%02x", typ)
	}

	// Offset 0x21 contains the device type: 0x01=Mixer, 0x02=CDJ, 0x03=Rekordbox.
	// These now match our DeviceType enum directly.
	ka := &KeepAlive{
		Name:         DeviceName(data),
		DeviceNumber: data[0x24],
		DeviceType:   DeviceType(data[0x21]),
		MAC:          net.HardwareAddr(append([]byte(nil), data[0x26:0x2c]...)),
		IP:           net.IP(append([]byte(nil), data[0x2c:0x30]...)),
	}
	return ka, nil
}

// MarshalInitialAnnounce builds the initial announcement packet (type 0x0a).
// Sent 3 times at 300ms intervals at the start of the claim sequence.
func MarshalInitialAnnounce(name string, deviceType DeviceType, mac net.HardwareAddr) []byte {
	buf := make([]byte, InitialAnnounceSize)
	PutHeader(buf, TypeInitialAnnounce)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01 // protocol version
	buf[0x21] = 0x02 // subtype
	PutU16BE(buf, 0x22, InitialAnnounceSize)
	buf[0x24] = byte(deviceType)
	return buf
}

// MarshalFirstClaim builds the first claim packet (type 0x00).
// Contains the MAC address to begin claiming a device number.
func MarshalFirstClaim(name string, iteration uint8, mac net.HardwareAddr) []byte {
	buf := make([]byte, FirstClaimSize)
	PutHeader(buf, TypeFirstClaim)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01
	buf[0x21] = 0x03 // subtype: 0x02=CDJ, 0x03=rekordbox (from pcap)
	PutU16BE(buf, 0x22, FirstClaimSize)
	buf[0x24] = iteration
	buf[0x25] = 0x04 // device class: 0x01=CDJ, 0x04=rekordbox (from pcap)
	copy(buf[0x26:0x2c], mac)
	return buf
}

// MarshalSecondClaim builds the second claim packet (type 0x02).
// Includes IP, MAC, device number, and iteration count.
func MarshalSecondClaim(name string, deviceNumber uint8, iteration uint8, mac net.HardwareAddr, ip net.IP) []byte {
	buf := make([]byte, SecondClaimSize)
	PutHeader(buf, TypeSecondClaim)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01
	buf[0x21] = 0x03 // 0x02=CDJ, 0x03=rekordbox (from pcap)
	PutU16BE(buf, 0x22, SecondClaimSize)
	copy(buf[0x24:0x28], ip.To4())
	copy(buf[0x28:0x2e], mac)
	buf[0x2e] = deviceNumber
	buf[0x2f] = iteration
	buf[0x30] = 0x04 // device class: 0x02=CDJ, 0x04=rekordbox (from pcap)
	buf[0x31] = 0x01 // auto-assign flag
	return buf
}

// MarshalFinalClaim builds the final claim packet (type 0x04).
// Confirms the device number claim.
func MarshalFinalClaim(name string, deviceNumber uint8, iteration uint8) []byte {
	buf := make([]byte, FinalClaimSize)
	PutHeader(buf, TypeFinalClaim)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01
	buf[0x21] = 0x02
	PutU16BE(buf, 0x22, FinalClaimSize)
	buf[0x24] = deviceNumber
	buf[0x25] = iteration
	return buf
}

// MarshalRekordboxKeepAlive builds a rekordbox-style keepalive (type 0x02, 50 bytes).
// rekordbox uses type 0x02 as its continuous keepalive, NOT type 0x06.
func MarshalRekordboxKeepAlive(name string, deviceNumber uint8, mac net.HardwareAddr, ip net.IP) []byte {
	buf := make([]byte, 50)
	PutHeader(buf, 0x02)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01
	buf[0x21] = byte(DeviceRekordbox) // 0x03
	buf[0x22] = 0x00
	buf[0x23] = 0x32                  // packet size = 50
	copy(buf[0x24:0x28], ip.To4())
	copy(buf[0x28:0x2e], mac)
	buf[0x2e] = deviceNumber
	buf[0x2f] = 0x06                  // matches rekordbox
	buf[0x30] = 0x04
	buf[0x31] = 0x01
	return buf
}

// MarshalKeepAlive builds a keep-alive packet (type 0x06).
// Sent every ~1.5 seconds to maintain network presence.
func MarshalKeepAlive(name string, deviceNumber uint8, deviceType DeviceType, mac net.HardwareAddr, ip net.IP, peerCount uint8) []byte {
	buf := make([]byte, KeepAliveSize)
	PutHeader(buf, TypeKeepAlive)
	buf[0x0b] = 0x00
	PutDeviceName(buf, name)
	buf[0x20] = 0x01               // constant
	buf[0x21] = byte(deviceType)   // 0x01=CDJ, 0x02=Mixer, 0x03=Rekordbox
	buf[0x22] = 0x00               // padding
	buf[0x23] = 0x36               // subtype (0x36 = status keep-alive)
	buf[0x24] = deviceNumber
	buf[0x25] = 0x01 // always 0x01 in real keepalive (both CDJ and rekordbox)
	copy(buf[0x26:0x2c], mac)
	copy(buf[0x2c:0x30], ip.To4())
	// Bytes 0x30-0x35 vary by device type.
	switch deviceType {
	case DeviceRekordbox:
		buf[0x30] = peerCount // rekordbox uses peer count here too
		buf[0x31] = 0x01
		buf[0x32] = 0x00
		buf[0x33] = 0x00
		buf[0x34] = 0x04
		buf[0x35] = 0x08
	case DeviceCDJ:
		buf[0x30] = peerCount
		buf[0x31] = 0x00
		buf[0x32] = 0x00 // My Settings flag — keep 0 (no settings file)
		buf[0x33] = 0x00
		buf[0x34] = 0x01
		buf[0x35] = 0x20
	case DeviceMixer:
		buf[0x30] = peerCount
		buf[0x31] = 0x00
		buf[0x32] = 0x00
		buf[0x33] = 0x00
		buf[0x34] = 0x03
		buf[0x35] = 0x00
	}
	return buf
}

// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Magic is the 10-byte header that begins every Pro DJ Link packet.
var Magic = [10]byte{0x51, 0x73, 0x70, 0x74, 0x31, 0x57, 0x6d, 0x4a, 0x4f, 0x4c}

// DeviceType represents the type of device on the Pro DJ Link network.
type DeviceType uint8

// DeviceType values match the wire encoding at keep-alive offset 0x21:
// 0x01=Mixer, 0x02=CDJ, 0x03=Rekordbox.
const (
	DeviceMixer     DeviceType = 1
	DeviceCDJ       DeviceType = 2
	DeviceRekordbox DeviceType = 3
)

func (d DeviceType) String() string {
	switch d {
	case DeviceCDJ:
		return "CDJ"
	case DeviceMixer:
		return "Mixer"
	case DeviceRekordbox:
		return "Rekordbox"
	default:
		return fmt.Sprintf("Unknown(%d)", d)
	}
}

// Packet type bytes at offset 0x0a.
const (
	TypeInitialAnnounce uint8 = 0x0a
	TypeFirstClaim      uint8 = 0x00
	TypeSecondClaim     uint8 = 0x02
	TypeFinalClaim      uint8 = 0x04
	TypeKeepAlive       uint8 = 0x06
	TypeConflict        uint8 = 0x08
)

// MinPacketSize is the minimum valid packet size (magic + type byte).
const MinPacketSize = 11

var ErrInvalidMagic = errors.New("invalid Pro DJ Link magic header")
var ErrPacketTooShort = errors.New("packet too short")

// ValidatePacket checks the magic header and returns the packet type byte.
func ValidatePacket(data []byte) (uint8, error) {
	if len(data) < MinPacketSize {
		return 0, ErrPacketTooShort
	}
	for i := 0; i < 10; i++ {
		if data[i] != Magic[i] {
			return 0, ErrInvalidMagic
		}
	}
	return data[0x0a], nil
}

// DeviceName extracts the null-padded device name string from a packet.
// The name field is 20 bytes starting at offset 0x0c.
func DeviceName(data []byte) string {
	if len(data) < 0x20 {
		return ""
	}
	name := data[0x0c:0x20]
	for i, b := range name {
		if b == 0 {
			return string(name[:i])
		}
	}
	return string(name)
}

// PutHeader writes the common packet header into buf.
// It writes the magic bytes and the packet type at offset 0x0a.
func PutHeader(buf []byte, packetType uint8) {
	copy(buf[0:10], Magic[:])
	buf[0x0a] = packetType
}

// PutDeviceName writes a null-padded device name at offset 0x0c (20 bytes).
func PutDeviceName(buf []byte, name string) {
	field := buf[0x0c:0x20]
	for i := range field {
		field[i] = 0
	}
	copy(field, name)
}

// PutU16BE writes a big-endian uint16 at the given offset.
func PutU16BE(buf []byte, offset int, v uint16) {
	binary.BigEndian.PutUint16(buf[offset:], v)
}

// U16BE reads a big-endian uint16 at the given offset.
func U16BE(buf []byte, offset int) uint16 {
	return binary.BigEndian.Uint16(buf[offset:])
}

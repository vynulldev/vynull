// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"net"
	"testing"
)

func TestKeepAliveRoundTrip(t *testing.T) {
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	ip := net.IP{192, 168, 1, 100}

	pkt := MarshalKeepAlive("rekordbox", 17, DeviceRekordbox, mac, ip, 2)

	if len(pkt) != KeepAliveSize {
		t.Fatalf("expected packet size %d, got %d", KeepAliveSize, len(pkt))
	}

	// Validate magic header.
	typ, err := ValidatePacket(pkt)
	if err != nil {
		t.Fatalf("ValidatePacket: %v", err)
	}
	if typ != TypeKeepAlive {
		t.Fatalf("expected type 0x%02x, got 0x%02x", TypeKeepAlive, typ)
	}

	// Parse it back.
	ka, err := ParseKeepAlive(pkt)
	if err != nil {
		t.Fatalf("ParseKeepAlive: %v", err)
	}

	if ka.Name != "rekordbox" {
		t.Errorf("name = %q, want %q", ka.Name, "rekordbox")
	}
	if ka.DeviceNumber != 17 {
		t.Errorf("device number = %d, want 17", ka.DeviceNumber)
	}
	if ka.DeviceType != DeviceRekordbox {
		t.Errorf("device type = %d, want %d", ka.DeviceType, DeviceRekordbox)
	}
	if ka.MAC.String() != mac.String() {
		t.Errorf("mac = %s, want %s", ka.MAC, mac)
	}
	if !ka.IP.Equal(ip) {
		t.Errorf("ip = %s, want %s", ka.IP, ip)
	}
}

func TestInitialAnnounceSize(t *testing.T) {
	mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	pkt := MarshalInitialAnnounce("rekordbox", DeviceRekordbox, mac)
	if len(pkt) != InitialAnnounceSize {
		t.Fatalf("expected %d bytes, got %d", InitialAnnounceSize, len(pkt))
	}

	// Verify magic.
	_, err := ValidatePacket(pkt)
	if err != nil {
		t.Fatalf("ValidatePacket: %v", err)
	}

	// Verify device type byte at offset 0x24 in initial announce.
	if pkt[0x24] != byte(DeviceRekordbox) {
		t.Errorf("device type byte = 0x%02x, want 0x%02x", pkt[0x24], byte(DeviceRekordbox))
	}
}

func TestClaimPacketSizes(t *testing.T) {
	mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	ip := net.IP{10, 0, 0, 1}

	tests := []struct {
		name string
		pkt  []byte
		want int
	}{
		{"FirstClaim", MarshalFirstClaim("rekordbox", 1, mac), FirstClaimSize},
		{"SecondClaim", MarshalSecondClaim("rekordbox", 17, 1, mac, ip), SecondClaimSize},
		{"FinalClaim", MarshalFinalClaim("rekordbox", 17, 1), FinalClaimSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.pkt) != tt.want {
				t.Fatalf("size = %d, want %d", len(tt.pkt), tt.want)
			}
			_, err := ValidatePacket(tt.pkt)
			if err != nil {
				t.Fatalf("ValidatePacket: %v", err)
			}
		})
	}
}

func TestDeviceName(t *testing.T) {
	mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	ip := net.IP{10, 0, 0, 1}

	pkt := MarshalKeepAlive("CDJ-2000nexus", 1, DeviceCDJ, mac, ip, 0)
	name := DeviceName(pkt)
	if name != "CDJ-2000nexus" {
		t.Errorf("name = %q, want %q", name, "CDJ-2000nexus")
	}
}

func TestValidatePacketErrors(t *testing.T) {
	_, err := ValidatePacket([]byte{0x01, 0x02})
	if err != ErrPacketTooShort {
		t.Errorf("expected ErrPacketTooShort, got %v", err)
	}

	bad := make([]byte, 20)
	bad[0] = 0xFF
	_, err = ValidatePacket(bad)
	if err != ErrInvalidMagic {
		t.Errorf("expected ErrInvalidMagic, got %v", err)
	}
}

func TestParseKeepAliveWrongType(t *testing.T) {
	mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	pkt := MarshalInitialAnnounce("test", DeviceCDJ, mac)
	// Pad to KeepAliveSize so it passes length check.
	padded := make([]byte, KeepAliveSize)
	copy(padded, pkt)

	_, err := ParseKeepAlive(padded)
	if err == nil {
		t.Error("expected error for wrong packet type")
	}
}

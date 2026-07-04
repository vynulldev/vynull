// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"encoding/binary"
	"net"
)

// Status packet constants.
const (
	StatusPort = 50002

	// Status packet type bytes at offset 0x0a.
	TypeMediaQuery      uint8 = 0x05
	TypeMediaResponse   uint8 = 0x06
	TypeStatusCDJ       uint8 = 0x0a
	TypeStatusQuery     uint8 = 0x10 // sent by CDJs, purpose unclear
	TypeStatusRekordbox uint8 = 0x16 // rekordbox simple status (48 bytes)

	// Slot types for media queries/responses.
	SlotEmpty     uint8 = 0x00
	SlotCD        uint8 = 0x01
	SlotSD        uint8 = 0x02
	SlotUSB       uint8 = 0x03
	SlotRekordbox uint8 = 0x04
)

// MediaQuery represents a parsed media query from a CDJ.
type MediaQuery struct {
	DeviceNumber  uint8
	SlotRequested uint8
	TargetDevice  uint8
}

// ParseMediaQuery attempts to parse a media query packet (type 0x05, 48 bytes).
// Layout: magic(10) + type(1) + name(20) + const(1) + fixed(1) + D(1) + len(1) +
//
//	reserved(4) + Dr(1) + reserved(7) + Sr(1) = 48 bytes
func ParseMediaQuery(data []byte) (*MediaQuery, bool) {
	if len(data) < 0x30 { // 48 bytes
		return nil, false
	}
	typ, err := ValidatePacket(data)
	if err != nil || typ != TypeMediaQuery {
		return nil, false
	}
	return &MediaQuery{
		DeviceNumber:  data[0x21], // D: querying device's player number
		TargetDevice:  data[0x27], // Dr: whose slot to query
		SlotRequested: data[0x2f], // Sr: which slot (USB=3, SD=2, etc.)
	}, true
}

// MarshalMediaResponse builds a media response packet (type 0x06) advertising
// available media. This should be sent both in response to queries and
// proactively broadcast to announce media availability.
//
// Packet structure based on DJ Link Ecosystem Analysis documentation.
func MarshalMediaResponse(name string, deviceNumber uint8, slot uint8, trackCount uint16, mac net.HardwareAddr, ip net.IP) []byte {
	// 192 bytes matching rekordbox media response exactly.
	size := 192
	buf := make([]byte, size)

	// Standard Pro DJ Link header (magic + type).
	PutHeader(buf, TypeMediaResponse)

	// Device name at 0x0b (20 bytes) — NOT 0x0c like keepalive!
	nameBytes := []byte(name)
	for i := 0; i < 20 && i < len(nameBytes); i++ {
		buf[0x0b+i] = nameBytes[i]
	}

	buf[0x1f] = 0x01
	buf[0x20] = 0x01            // subtype
	buf[0x21] = deviceNumber    // D: responding device
	PutU16BE(buf, 0x22, 0x009c) // payload size (156)

	// Device number as u32 at 0x24.
	binary.BigEndian.PutUint32(buf[0x24:], uint32(deviceNumber))

	// Slot as u32 at 0x28.
	binary.BigEndian.PutUint32(buf[0x28:], uint32(slot))

	// Media name in UTF-16BE at 0x2c (64 bytes / 32 chars max).
	mediaName := name
	for i, r := range mediaName {
		off := 0x2c + i*2
		if off+1 >= 0x6c {
			break
		}
		binary.BigEndian.PutUint16(buf[off:], uint16(r))
	}

	// 0x6c-0xa5: rekordbox leaves these as zeros (no date, no u5).

	// Track count at 0xa6 (2 bytes big-endian).
	PutU16BE(buf, 0xa6, trackCount)

	// 0xa8-0xa9: zeros
	buf[0xaa] = 0x01 // track type: rekordbox analyzed
	buf[0xab] = 0x01 // My Settings available

	// 0xac-0xaf: playlist count.
	binary.BigEndian.PutUint32(buf[0xac:], 35)

	// 0xb0-0xbf: rekordbox leaves as zeros (no storage info).

	return buf
}

// MarshalLinkActivation builds the Link activation packet (type 0x47).
// Sent once to each CDJ on port 50002 to announce library availability.
// devSetting is the 6-byte DEVSETTING; if nil, defaults are used.
func MarshalLinkActivation(name string, deviceNumber uint8, slot uint8, devSetting []byte) []byte {
	buf := make([]byte, 72)
	PutHeader(buf, 0x47)
	putName50002(buf, name)

	buf[0x1f] = 0x01
	buf[0x20] = 0x01 // subtype
	buf[0x21] = deviceNumber
	buf[0x22] = 0x00
	buf[0x23] = 0x24 // remaining length (36)
	buf[0x24] = deviceNumber
	buf[0x25] = slot // 0x04 for rekordbox

	// Magic constant 0x12345678.
	buf[0x26] = 0x00
	buf[0x27] = 0x00
	buf[0x28] = 0x12
	buf[0x29] = 0x34
	buf[0x2a] = 0x56
	buf[0x2b] = 0x78

	// DEVSETTING data embedded in 0x47 at offsets 0x30-0x35:
	//   [0]: unknown (always 0x01)
	//   [1]: overview waveform (0x01=half, 0x02=full)
	//   [2]: waveform color (0x01=blue, 0x03=RGB, 0x04=3band)
	//   [3]: unknown (always 0x01)
	//   [4]: key display format (0x01=classic, 0x02=alphanumeric)
	//   [5]: waveform position (0x01=center, 0x02=left)
	if len(devSetting) >= 6 {
		copy(buf[0x30:0x36], devSetting)
	} else {
		buf[0x30] = 0x01
		buf[0x31] = 0x02 // full overview waveform
		buf[0x32] = 0x03 // RGB waveform color
		buf[0x33] = 0x01
		buf[0x34] = 0x02 // alphanumeric key display
		buf[0x35] = 0x01 // center position
	}
	buf[0x2f] = 0x01

	return buf
}

// putName50002 puts the device name at offset 0x0b for port 50002 packets.
// Port 50002 packets have name at 0x0b, port 50000 at 0x0c.
func putName50002(buf []byte, name string) {
	nameBytes := []byte(name)
	for i := 0; i < 20 && i < len(nameBytes); i++ {
		buf[0x0b+i] = nameBytes[i]
	}
}

// MarshalStatusRekordbox builds a rekordbox status packet (type 0x16, 48 bytes).
func MarshalStatusRekordbox(name string, deviceNumber uint8) []byte {
	buf := make([]byte, 48)
	PutHeader(buf, TypeStatusRekordbox)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	return buf
}

// MarshalSettingsNotify builds a settings notification packet (type 0x4a, 40 bytes).
// rekordbox sends this periodically on port 50002 to tell CDJs that
// settings are available for the given slot. The CDJ responds with 0x46 (link ack).
func MarshalSettingsNotify(name string, deviceNumber uint8, slot uint8) []byte {
	buf := make([]byte, 40)
	PutHeader(buf, 0x4a)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x22] = 0x00
	buf[0x23] = 0x04 // remaining length
	buf[0x24] = deviceNumber
	buf[0x25] = slot
	buf[0x26] = 0x00
	buf[0x27] = 0x00
	return buf
}

// MarshalStatusRekordboxExt builds a rekordbox extended status (type 0x29, 56 bytes).
func MarshalStatusRekordboxExt(name string, deviceNumber uint8) []byte {
	buf := make([]byte, 56)
	PutHeader(buf, 0x29)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x22] = 0x00
	buf[0x23] = 0x38
	buf[0x24] = deviceNumber
	buf[0x27] = 0xc0
	buf[0x29] = 0x10
	// 0x2c-0x2f / 0x37: zeros (we don't advertise an active beat-synced
	// player — our virtual rekordbox isn't a deck, and claiming master/beat
	// here conflicts with the actual beat-master on the network).
	buf[0x31] = 0x10
	buf[0x35] = 0x09
	buf[0x36] = 0xff
	return buf
}

// MarshalRekordboxAnnounce builds the type 0x11 initial announcement (296 bytes).
// Sent to each CDJ at startup before any other port 50002 traffic.
func MarshalRekordboxAnnounce(name string, deviceNumber uint8, hostname string) []byte {
	buf := make([]byte, 296)
	PutHeader(buf, 0x11)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x22] = 0x01
	buf[0x23] = 0x04
	buf[0x24] = deviceNumber
	buf[0x25] = 0x01

	// Hostname in UTF-16BE at offset 0x28 (256 bytes / 128 chars max).
	// rekordbox puts the computer name here (e.g., "STUDIO-PC").
	for i, r := range hostname {
		off := 0x28 + i*2
		if off+1 >= 0x128 { // max 256 bytes
			break
		}
		binary.BigEndian.PutUint16(buf[off:], uint16(r))
	}

	return buf
}

// MarshalTrackRefreshTrigger builds the type 0x1d "track data invalidated"
// broadcast (48 bytes) that rekordbox sends after editing cue colours,
// track colour, or rating. Connected CDJs receive it on UDP 50002
// (broadcast) and react by re-issuing dbserver queries for the affected
// track — that's how the new value reaches the deck without a reload.
//
// Reference: packet captures of rekordbox performing a cue-colour
// edit and a rating edit. Both have identical structure, only the track
// ID differs.
//
//	0x00-0x09: magic "Qspt1WmJOL"
//	0x0a:      0x1d (type)
//	0x0b-0x1e: name (20 bytes, "rekordbox" padded)
//	0x1f:      0x01
//	0x20:      0x01
//	0x21:      device number (17 for rekordbox)
//	0x22:      0x00
//	0x23:      0x0c — payload length (12)
//	0x24:      device number
//	0x25-0x27: zeros
//	0x28:      device number
//	0x29:      0x04 (rekordbox slot)
//	0x2a:      0x01 (deck/target)
//	0x2b:      0x00
//	0x2c-0x2f: BE uint32 track ID — the CDJ only acts on the trigger
//	           if this matches the track it currently has loaded.
func MarshalTrackRefreshTrigger(name string, deviceNumber uint8, trackID uint32) []byte {
	buf := make([]byte, 48)
	PutHeader(buf, 0x1d)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x23] = 0x0c
	buf[0x24] = deviceNumber
	buf[0x28] = deviceNumber
	buf[0x29] = 0x04
	buf[0x2a] = 0x01
	binary.BigEndian.PutUint32(buf[0x2c:], trackID)
	return buf
}

// MarshalRatingRefreshTrigger builds the type 0x1b "rating changed"
// broadcast (48 bytes) that rekordbox sends after editing a
// track's rating. CDJs receive it on UDP 50002 and re-fetch the new
// rating via dbserver — the value isn't encoded in the trigger.
//
// Reference: packet captures of rekordbox setting 5★, setting 1★,
// and clearing a rating. All the packets are byte-identical except the
// magic+type header — only the track ID varies in practice.
//
//	0x00-0x09: magic "Qspt1WmJOL"
//	0x0a:      0x1b (type)
//	0x0b-0x1e: name (20 bytes, "rekordbox" padded)
//	0x1f:      0x01
//	0x20:      0x01
//	0x21:      device number
//	0x23:      0x0c — payload length (12)
//	0x24:      device number
//	0x25:      0x01 — constant in captures (purpose unknown)
//	0x26-0x27: zeros
//	0x28-0x2b: BE uint32 track ID
//	0x2c-0x2f: 00 00 00 23 — constant in captures (likely a "rating
//	           data type" marker, but unverified)
func MarshalRatingRefreshTrigger(name string, deviceNumber uint8, trackID uint32) []byte {
	buf := make([]byte, 48)
	PutHeader(buf, 0x1b)
	putName50002(buf, name)
	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x23] = 0x0c
	buf[0x24] = deviceNumber
	buf[0x25] = 0x01
	binary.BigEndian.PutUint32(buf[0x28:], trackID)
	buf[0x2f] = 0x23
	return buf
}

// MarshalStatusCDJ builds a full CDJ-format status packet (292 bytes) sent
// on port 50002. This tells other CDJs about our media and playback state.
// The mediaSlot indicates which slot has media (SlotUSB, SlotSD, SlotRekordbox).
func MarshalStatusCDJ(name string, deviceNumber uint8, mediaSlot uint8, trackCount uint16, devSetting []byte) []byte {
	// Byte-perfect template from a real CDJ (firmware 1.85).
	// Only name, device number, and media flags are modified.
	buf := make([]byte, 292)

	// Copy the exact bytes captured from a real CDJ.
	copy(buf, []byte{
		0x51, 0x73, 0x70, 0x74, 0x31, 0x57, 0x6d, 0x4a, 0x4f, 0x4c, 0x0a, // magic + type
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // 0x0b: name placeholder
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, //
		0x01, 0x05, 0x01, 0x01, 0x00, // 0x1f-0x23
	})

	// Name at 0x0b.
	nameBytes := []byte(name)
	for i := 0; i < 20 && i < len(nameBytes); i++ {
		buf[0x0b+i] = nameBytes[i]
	}

	buf[0x24] = deviceNumber

	// 0x68: real CDJ has 0x01
	buf[0x68] = 0x01
	// 0x6a-0x6b: media state flags
	buf[0x6a] = 0x06
	buf[0x6b] = 0x04

	// Media flags.
	switch mediaSlot {
	case SlotUSB:
		buf[0x6f] = 0x00 // USB loaded
		buf[0x73] = 0x04 // SD absent
	case SlotSD:
		buf[0x6f] = 0x04
		buf[0x73] = 0x00
	default:
		buf[0x6f] = 0x04
		buf[0x73] = 0x04
	}

	buf[0x75] = 0x01 // media present

	// Firmware "1.85"
	copy(buf[0x7c:0x80], []byte("1.85"))

	// 0x87: real CDJ has 0x01
	buf[0x87] = 0x01

	// Flags and pitch defaults from real CDJ capture.
	buf[0x89] = 0x84
	buf[0x8a] = 0x0b
	buf[0x8b] = 0xfe
	buf[0x8d] = 0x10
	buf[0x90] = 0x7f
	buf[0x91] = 0xff
	buf[0x92] = 0xff
	buf[0x93] = 0xff
	buf[0x94] = 0x7f
	buf[0x95] = 0xff
	buf[0x96] = 0xff
	buf[0x97] = 0xff
	buf[0x99] = 0x10
	buf[0x9f] = 0xff
	buf[0xa0] = 0xff
	buf[0xa1] = 0xff
	buf[0xa2] = 0xff
	buf[0xa3] = 0xff
	buf[0xa4] = 0x01
	buf[0xa5] = 0xff
	buf[0xb6] = 0x01
	buf[0xc1] = 0x10
	buf[0xc5] = 0x00

	// Nexus generation: 0x1f matches real CDJ.
	buf[0xcc] = 0x1f

	// DEVSETTING area (0xcb-0xdd): when WE are the media source,
	// the CDJ reads our DEVSETTING to determine waveform color mode
	// for tracks loaded from us. Real CDJs send zeros here because
	// they read settings from their own USB — but we ARE the USB.
	if len(devSetting) >= 6 {
		buf[0xcb] = 0x0a
		buf[0xcd] = 0x03

		// DEVSETTING magic (0x12345678).
		buf[0xd0] = 0x12
		buf[0xd1] = 0x34
		buf[0xd2] = 0x56
		buf[0xd3] = 0x78
		buf[0xd4] = 0x00
		buf[0xd5] = 0x00
		buf[0xd6] = 0x00
		buf[0xd7] = 0x01

		// Copy saved DEVSETTING bytes (6 bytes: 0xd8-0xdd).
		copy(buf[0xd8:0xde], devSetting[:6])
	}

	return buf
}

// MarshalMySettingsResponse builds the My Settings response packet (type 0x36, 120 bytes).
// Sent in response to a CDJ's type 0x35 "Load Settings" request.
// Contains combined MYSETTING + MYSETTING2 data that the CDJ applies to configure
// its display settings (waveform color, jog settings, etc.).
//
// mySetting is the 40-byte .DAT-style MYSETTING body (8-byte magic + 32 fields);
// mySetting2 is the 40-byte MYSETTING2 wire body (fields only, no magic — that's
// what CDJSettings.GetMySetting2() returns). Either may be nil/short, in which
// case that section falls back to the built-in defaults.
func MarshalMySettingsResponse(name string, deviceNumber uint8, slot uint8, mySetting []byte, mySetting2 []byte, devSetting []byte) []byte {
	buf := make([]byte, 120)
	PutHeader(buf, 0x36)
	putName50002(buf, name)

	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x22] = 0x00
	buf[0x23] = 0x54 // remaining length (84)
	buf[0x24] = deviceNumber
	buf[0x25] = slot

	// DEVSETTING/MYSETTING magic.
	buf[0x28] = 0x12
	buf[0x29] = 0x34
	buf[0x2a] = 0x56
	buf[0x2b] = 0x78

	// Type = 3: combined MYSETTING + MYSETTING2.
	binary.BigEndian.PutUint32(buf[0x2c:], 3)

	// MYSETTING body (32 bytes of fields at wire offset 0x30-0x4f).
	// The wire format has NO 8-byte magic prefix — that lives in the .DAT
	// file format and at packet offset 0x28 above. So when our encoder
	// returns the 40-byte .DAT-style body, we skip the first 8 (the magic)
	// and copy the 32 field bytes that follow.
	if len(mySetting) >= 40 {
		copy(buf[0x30:0x50], mySetting[8:40])
	} else if len(mySetting) >= 32 {
		copy(buf[0x30:0x50], mySetting[:32])
	} else {
		// Defaults from pyrekordbox / rekordbox.
		copy(buf[0x30:], []byte{
			0x81, 0x83, 0x81, 0x88, 0x81, 0x01, 0x82, 0x81,
			0x81, 0x01, 0x01, 0x01, 0x82, 0x80, 0x80, 0x81,
			0x80, 0x81, 0x80, 0x00, 0x00, 0x81, 0x00, 0x00,
			0x81, 0x81, 0x81, 0x80, 0x81, 0x80, 0x00, 0x00,
		})
	}

	// MYSETTING2 body (40 bytes at 0x50-0x77). GetMySetting2() already returns
	// the wire body (fields only, no magic prefix), so it's copied straight in.
	if len(mySetting2) >= 40 {
		copy(buf[0x50:0x78], mySetting2[:40])
	} else {
		// Defaults — byte-identical to MySetting2Fields{}.Encode().
		copy(buf[0x50:], []byte{
			0x81,                         // vinyl_speed_adjust: touch
			0x80,                         // jog_display_mode: auto
			0x83,                         // pad_button_brightness: three
			0x83,                         // jog_lcd_brightness: three
			0x81,                         // waveform_divisions: phrase
			0x00, 0x00, 0x00, 0x00, 0x00, // padding
			0x80, // waveform: waveform
			0x81, // u2
			0x85, // beat_jump_beat_value: sixteen
			// rest is zeros (padding to 0x77)
		})
	}

	return buf
}

// MarshalLoadTrackCommand builds a "load track" command (type 0x19, 88 bytes).
// Sent to a CDJ on port 50002 to remotely load a track.
func MarshalLoadTrackCommand(name string, deviceNumber uint8, slot uint8, targetDevice uint8, trackID uint32) []byte {
	buf := make([]byte, 88)
	PutHeader(buf, 0x19)
	putName50002(buf, name)

	buf[0x1f] = 0x01
	buf[0x20] = 0x01
	buf[0x21] = deviceNumber
	buf[0x22] = 0x00
	buf[0x23] = 0x34 // remaining length (52)
	buf[0x24] = deviceNumber
	buf[0x25] = 0x00
	buf[0x28] = deviceNumber
	buf[0x29] = slot
	buf[0x2a] = 0x01 // always 1 (deck number, not target device — target is via dest IP)
	buf[0x2b] = 0x00

	// Track ID (big-endian uint32)
	binary.BigEndian.PutUint32(buf[0x2c:], trackID)

	// Unknown field (appears to be 0x32 = 50 in rekordbox)
	binary.BigEndian.PutUint32(buf[0x30:], 0x32)

	// Device index at 0x40 (0-indexed: CDJ1=0, CDJ2=1)
	if targetDevice > 0 {
		buf[0x40] = targetDevice - 1
	}

	// Second copy at offset 0x44
	binary.BigEndian.PutUint32(buf[0x44:], 0x32)

	// Byte 0x4b = 0x32 identifies this as a rekordbox-style load (vs the
	// player-to-player variant which leaves it 0). Per the deepsymmetry
	// loading-tracks doc: "the packets that rekordbox sends ... byte 4b
	// has the value 32 rather than 00." Without this byte, CDJs sometimes
	// treat the command as preview-only and fetch metadata but never
	// actually load the audio — symptom: dbserver dialogue completes
	// through PWV4 but stops before PWV5/PVB2/PQT2/0x3100 mount info and
	// the NFS audio download.
	buf[0x4b] = 0x32

	return buf
}

// MixerStatus is the parsed form of a DJM-class status broadcast
// (type 0x29 from a DeviceMixer peer). Field offsets are based on
// community RE for the DJM / DJM family; the DJM may
// have additions past the documented end of the packet that we
// silently ignore. Treat ChannelOnAir as a bitfield where bit N
// (0-indexed) = channel N+1 is currently on the master bus.
type MixerStatus struct {
	Name              string
	DeviceNumber      uint8
	MasterDevice      uint8   // device number of the current master (0 = unset)
	MasterBPM         float64 // master tempo in BPM
	BeatInBar         uint8   // 1..4
	ChannelOnAir      uint8   // bitfield: bit 0 = ch1, bit 1 = ch2, …
	ChannelStateKnown bool    // true once a 0x03 (or rich 0x29) packet has been parsed
}

// TypeMixerStatusLegacy is the DJM / older mixer status type
// (port 50002, ~56 bytes, includes channel + master state inline).
// TypeMixerStatusNew is what newer DJMs (DJM confirmed) broadcast
// on port 50002 — 36 bytes, presence only.
// TypeMixerChannels is the DJM's channel-state broadcast on port
// 50001 — 53 bytes, four per-channel on-air bytes at 0x24-0x27
// (0x01 = on-air, 0x00 = off-air). Confirmed against pcap of real
// rekordbox + DJM with manual fader toggles.
const (
	TypeMixerStatusLegacy uint8 = 0x29
	TypeMixerStatusNew    uint8 = 0x30
	TypeMixerChannels     uint8 = 0x03
)

// ParseMixerStatus parses a mixer status broadcast. Accepts both the
// legacy 0x29 (DJM family, ~56 bytes with channel/master
// state) and the newer 0x30 (DJM confirmed; 36 bytes, presence
// only — no channel state on the wire). The caller is responsible
// for gating this on "peer is a DeviceMixer".
//
// 0x29 offsets (from dysentery / beat-link):
//
//	0x0c-0x1f: device name (null-padded ASCII)
//	0x21:      this mixer's device number
//	0x2c-0x2d: master BPM × 100 (uint16 big-endian) — empty when no master
//	0x36:      channel on-air bitfield (low 4 bits = ch1..ch4)
//	0x60:      current master's device number (newer DJMs)
//	0xa6:      beat-in-bar (1..4) (some models)
//
// 0x30 offsets (observed from DJM broadcasts):
//
//	0x0b-0x1e: device name (no separator byte — name starts immediately
//	           after the type)
//	0x21:      this mixer's device number
//	(no channel state — DJM appears to use a separate TCP control
//	protocol for fader / on-air info that we haven't RE'd yet)
func ParseMixerStatus(data []byte) (*MixerStatus, bool) {
	if len(data) < 0x22 {
		return nil, false
	}
	if _, err := ValidatePacket(data); err != nil {
		return nil, false
	}
	switch data[0x0a] {
	case TypeMixerStatusLegacy:
		// Need the full 56-byte packet for the channel/master fields.
		if len(data) < 56 {
			return nil, false
		}
		s := &MixerStatus{
			Name:         DeviceName(data),
			DeviceNumber: data[0x21],
		}
		if mb := binary.BigEndian.Uint16(data[0x2c:0x2e]); mb != 0xffff {
			s.MasterBPM = float64(mb) / 100.0
		}
		s.ChannelOnAir = data[0x36] & 0x0f
		s.ChannelStateKnown = true
		if len(data) > 0x60 {
			s.MasterDevice = data[0x60]
		}
		if len(data) > 0xa6 {
			if v := data[0xa6]; v >= 1 && v <= 4 {
				s.BeatInBar = v
			}
		}
		return s, true
	case TypeMixerStatusNew:
		// DJM etc. — name shifted left by one byte (no 0x0b
		// separator), only presence info available.
		return &MixerStatus{
			Name:         mixerNameAt0b(data),
			DeviceNumber: data[0x21],
		}, true
	case TypeMixerChannels:
		// DJM channel-state broadcast on port 50001. Same name
		// layout as 0x30; channels at 0x24-0x27 (1 byte each, 0x01
		// = on-air). Packet is ~53 bytes.
		if len(data) < 0x28 {
			return nil, false
		}
		var onAir uint8
		for ch := 0; ch < 4; ch++ {
			if data[0x24+ch] == 0x01 {
				onAir |= 1 << uint(ch)
			}
		}
		return &MixerStatus{
			Name:              mixerNameAt0b(data),
			DeviceNumber:      data[0x21],
			ChannelOnAir:      onAir,
			ChannelStateKnown: true,
		}, true
	}
	return nil, false
}

// mixerNameAt0b reads the name field used by 0x30 / 0x03 packets,
// which place the name immediately after the type byte (no 0x0b
// separator). Returns the trimmed-on-NUL string.
func mixerNameAt0b(data []byte) string {
	end := 0x1f
	if end > len(data) {
		end = len(data)
	}
	name := data[0x0b:end]
	for i, b := range name {
		if b == 0 {
			return string(name[:i])
		}
	}
	return string(name)
}

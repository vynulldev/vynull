// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import "encoding/binary"

// CDJStatus represents a parsed CDJ status packet from port 50002.
type CDJStatus struct {
	Name         string
	DeviceNumber uint8
	Active       bool   // playing, searching, or loading
	TrackDevice  uint8  // Dr: device track is loaded from
	TrackSlot    uint8  // Sr: slot (0=none, 1=CD, 2=SD, 3=USB, 4=rekordbox)
	TrackType    uint8  // Tr: track type (0=none, 1=rekordbox, 2=unanalyzed)
	TrackID      uint32 // rekordbox ID of loaded track
	TrackNum     uint16 // position in playlist/menu
	BPM          uint16 // current BPM * 100
	Pitch        int32  // pitch adjustment (100000 = +0%, range ~50000-200000)
	PlayState    uint8  // P1: 0=empty, 3=playing, 5=paused, 6=cued, etc.
	Firmware     string // 4-char firmware version
	IsMaster     bool   // tempo master flag
	IsSync       bool   // sync enabled
	IsPlaying    bool   // currently playing
	IsOnAir      bool   // on-air (mixer channel up)
	BeatInBar    uint8  // current beat within bar (1-4)
	BeatInTrack  uint32 // M_b: beats elapsed since track start (0 if unknown)
	PacketNum    uint32 // sequence counter
}

// PlayStateString returns a human-readable play state.
func (s *CDJStatus) PlayStateString() string {
	switch s.PlayState {
	case 0x00:
		return "NO TRACK"
	case 0x02:
		return "LOADING"
	case 0x03:
		return "PLAYING"
	case 0x04:
		return "LOOPING"
	case 0x05:
		return "PAUSED"
	case 0x06:
		return "CUED"
	case 0x07:
		return "CUE PLAY"
	case 0x08:
		return "CUE SCRATCH"
	case 0x09:
		return "SEEKING"
	case 0x11:
		return "ENDED"
	default:
		return "UNKNOWN"
	}
}

// ParseCDJStatus parses a CDJ status packet (type 0x0a, 292 bytes).
func ParseCDJStatus(data []byte) (*CDJStatus, bool) {
	if len(data) < 0x7c {
		return nil, false
	}
	typ, err := ValidatePacket(data)
	if err != nil || typ != TypeStatusCDJ {
		return nil, false
	}

	s := &CDJStatus{}

	// Name at 0x0b (20 bytes).
	nameEnd := 0x0b
	for i := 0x0b; i < 0x1f && i < len(data); i++ {
		if data[i] == 0 {
			break
		}
		nameEnd = i + 1
	}
	s.Name = string(data[0x0b:nameEnd])

	s.DeviceNumber = data[0x24]
	s.Active = data[0x27] != 0
	s.TrackDevice = data[0x28]
	s.TrackSlot = data[0x29]
	s.TrackType = data[0x2a]

	if len(data) > 0x2f {
		s.TrackID = binary.BigEndian.Uint32(data[0x2c:0x30])
	}
	if len(data) > 0x33 {
		s.TrackNum = binary.BigEndian.Uint16(data[0x32:0x34])
	}

	// BPM at bytes 0x92-0x93 (for nexus/NXS2 packets with length >= 0xc8).
	if len(data) >= 0x94 {
		s.BPM = binary.BigEndian.Uint16(data[0x92:0x94])
	}

	// Pitch at bytes 0x8c-0x8f.
	if len(data) >= 0x90 {
		s.Pitch = int32(binary.BigEndian.Uint32(data[0x8c:0x90]))
	}

	s.PlayState = data[0x7b]

	// Firmware at 0x7c-0x7f.
	if len(data) >= 0x80 {
		s.Firmware = string(data[0x7c:0x80])
	}

	// Flags at 0x89.
	if len(data) > 0x89 {
		flags := data[0x89]
		s.IsPlaying = flags&0x40 != 0
		s.IsMaster = flags&0x20 != 0
		s.IsSync = flags&0x10 != 0
		s.IsOnAir = flags&0x08 != 0
	}

	// Beat in bar at 0xa6.
	if len(data) > 0xa6 {
		s.BeatInBar = data[0xa6]
	}

	// M_b (beat-in-track) at 0xa0-0xa3, big-endian. 0xFFFFFFFF means unknown
	// (e.g. before a track has been beat-gridded or position-resolved).
	if len(data) >= 0xa4 {
		bt := binary.BigEndian.Uint32(data[0xa0:0xa4])
		if bt != 0xFFFFFFFF {
			s.BeatInTrack = bt
		}
	}

	// Packet counter at 0xc8-0xcb.
	if len(data) >= 0xcc {
		s.PacketNum = binary.BigEndian.Uint32(data[0xc8:0xcc])
	}

	return s, true
}

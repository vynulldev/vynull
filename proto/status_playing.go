// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"encoding/binary"
	"math"
)

// Play-state values for CDJStatus.PlayState / the 0x7b byte of a status packet.
const (
	PlayStateNoTrack uint8 = 0x00
	PlayStateLoading uint8 = 0x02
	PlayStatePlaying uint8 = 0x03
	PlayStateLooping uint8 = 0x04
	PlayStatePaused  uint8 = 0x05
	PlayStateCued    uint8 = 0x06
	PlayStateEnded   uint8 = 0x11
)

// PitchZero is the Pro DJ Link wire value for 0% pitch (normal speed); the
// field scales linearly, so +100% doubles it and -100% is zero.
const PitchZero = 0x100000

// CDJPlayState carries the dynamic fields that turn the idle CDJ status into a
// playing one. Everything not listed here comes from the known-good idle
// template built by MarshalStatusCDJ.
//
// These are exactly the fields ParseCDJStatus reads back, so a status built
// from this struct round-trips through our own parser (and so drives our own
// monitor/overlay/MPRIS/history). The NXS2 mirror copies of pitch/BPM that we
// do not parse (0x90-0x97, 0xc0-0xc7) are left at their template values; making
// them byte-exact against a real CDJ is a Phase-0 (capture + diff) task.
type CDJPlayState struct {
	PlayState   uint8   // 0x7b
	TrackDevice uint8   // 0x28 (Dr)
	TrackSlot   uint8   // 0x29 (Sr)
	TrackType   uint8   // 0x2a (Tr)
	TrackID     uint32  // 0x2c (rekordbox track ID)
	TrackNum    uint16  // 0x32 (position in the loaded menu/playlist)
	BPM         uint16  // 0x92 (BPM * 100; 0xFFFF = unknown)
	PitchPct    float64 // encoded to 0x8c (PitchZero = 0%)
	BeatInTrack uint32  // 0xa0 (beats since track start; 0xFFFFFFFF = unknown)
	BeatInBar   uint8   // 0xa6 (1-4)
	Master      bool    // 0x89 bit 0x20
	Sync        bool    // 0x89 bit 0x10
	OnAir       bool    // 0x89 bit 0x08
	PacketNum   uint32  // 0xc8 (sequence counter)
}

// PitchToWire converts a pitch percentage to the Pro DJ Link wire value
// (PitchZero == 0%). It clamps at the floor so a -100% never underflows.
func PitchToWire(pct float64) uint32 {
	v := math.Round(float64(PitchZero) * (1 + pct/100))
	if v < 0 {
		return 0
	}
	return uint32(v)
}

// WireToPitch is the inverse of PitchToWire.
func WireToPitch(v uint32) float64 {
	return (float64(v)/float64(PitchZero) - 1) * 100
}

// playStateActive reports whether a play state should set the Active (0x27)
// byte, which a real CDJ raises while playing, looping, searching, or loading.
func playStateActive(s uint8) bool {
	switch s {
	case PlayStatePlaying, PlayStateLooping, PlayStateLoading:
		return true
	default:
		return false
	}
}

// MarshalStatusCDJPlaying builds a dynamic playing CDJ status (type 0x0a) by
// overlaying p onto the known-good idle template. The idle fields (name, device
// number, media, firmware, DEVSETTING) carry through unchanged.
func MarshalStatusCDJPlaying(name string, deviceNumber uint8, mediaSlot uint8, trackCount uint16, devSetting []byte, p CDJPlayState) []byte {
	buf := MarshalStatusCDJ(name, deviceNumber, mediaSlot, trackCount, devSetting)

	// Loaded-track source and ID.
	buf[0x28] = p.TrackDevice
	buf[0x29] = p.TrackSlot
	buf[0x2a] = p.TrackType
	binary.BigEndian.PutUint32(buf[0x2c:0x30], p.TrackID)
	binary.BigEndian.PutUint16(buf[0x32:0x34], p.TrackNum)

	// Active while playing/looping/loading.
	if playStateActive(p.PlayState) {
		buf[0x27] = 0x01
	} else {
		buf[0x27] = 0x00
	}

	buf[0x7b] = p.PlayState

	// Status flags at 0x89: keep the template's other bits, set ours.
	const (
		flagPlaying = 0x40
		flagMaster  = 0x20
		flagSync    = 0x10
		flagOnAir   = 0x08
	)
	flags := buf[0x89] &^ byte(flagPlaying|flagMaster|flagSync|flagOnAir)
	if p.PlayState == PlayStatePlaying || p.PlayState == PlayStateLooping {
		flags |= flagPlaying
	}
	if p.Master {
		flags |= flagMaster
	}
	if p.Sync {
		flags |= flagSync
	}
	if p.OnAir {
		flags |= flagOnAir
	}
	buf[0x89] = flags

	// Pitch (0x8c) and BPM (0x92).
	binary.BigEndian.PutUint32(buf[0x8c:0x90], PitchToWire(p.PitchPct))
	binary.BigEndian.PutUint16(buf[0x92:0x94], p.BPM)

	// Beat position.
	binary.BigEndian.PutUint32(buf[0xa0:0xa4], p.BeatInTrack)
	buf[0xa6] = p.BeatInBar

	// Sequence counter.
	binary.BigEndian.PutUint32(buf[0xc8:0xcc], p.PacketNum)

	return buf
}

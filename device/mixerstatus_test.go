// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"encoding/binary"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// Wire-packet builders for the three mixer broadcast types, laid out per the
// offsets documented on proto.ParseMixerStatus. Each goes through the real
// parser, so the tests cover parse + merge, not a hand-built MixerStatus.

func synthPktHeader(typ uint8, size int) []byte {
	p := make([]byte, size)
	copy(p, proto.Magic[:])
	p[0x0a] = typ
	return p
}

// synthChannels builds a 0x03 channel-state broadcast (newer DJMs, port
// 50001): name at 0x0b, device number at 0x21, per-channel on-air bytes at
// 0x24-0x27.
func synthChannels(name string, devNum uint8, onAir [4]bool) []byte {
	p := synthPktHeader(proto.TypeMixerChannels, 53)
	copy(p[0x0b:], name)
	p[0x21] = devNum
	for ch, on := range onAir {
		if on {
			p[0x24+ch] = 0x01
		}
	}
	return p
}

// synthStatusNew builds a stripped 0x30 status broadcast (newer DJMs, port
// 50002): name at 0x0b, device number at 0x21, no channel or master state.
func synthStatusNew(name string, devNum uint8) []byte {
	p := synthPktHeader(proto.TypeMixerStatusNew, 36)
	copy(p[0x0b:], name)
	p[0x21] = devNum
	return p
}

// synthStatusLegacy builds a rich 0x29 status broadcast (older DJMs): name
// at 0x0c, device number at 0x21, master BPM ×100 at 0x2c, on-air bitfield
// at 0x36.
func synthStatusLegacy(name string, devNum uint8, masterBPM float64, onAirBits uint8) []byte {
	p := synthPktHeader(proto.TypeMixerStatusLegacy, 56)
	copy(p[0x0c:], name)
	p[0x21] = devNum
	binary.BigEndian.PutUint16(p[0x2c:], uint16(masterBPM*100))
	p[0x36] = onAirBits
	return p
}

func parse(t *testing.T, pkt []byte) *proto.MixerStatus {
	t.Helper()
	mx, ok := proto.ParseMixerStatus(pkt)
	if !ok {
		t.Fatalf("synthesized packet type 0x%02x did not parse", pkt[0x0a])
	}
	return mx
}

// TestMixerStatus0x30DoesNotClobberChannelState replays the newer-DJM
// packet interleaving that made mixer views flip between the rich
// channel-state line and the bare "detected" fallback: 0x03 channel packets
// on the on-air port teach the channel state, and stripped 0x30 status
// packets used to overwrite the shared entry wholesale, wiping
// ChannelStateKnown until the next 0x03. The snapshot must stay rich across
// any number of interleaved 0x30s.
func TestMixerStatus0x30DoesNotClobberChannelState(t *testing.T) {
	d := &VirtualDevice{}

	// Channel state arrives first (0x03, channels 1+2 on-air).
	d.recordMixerChannels(parse(t, synthChannels("DJM-A9", 33, [4]bool{true, true, false, false})))
	snap := d.MixerSnapshot()
	mx, ok := snap[33]
	if !ok || !mx.ChannelStateKnown || mx.ChannelOnAir != 0b0011 {
		t.Fatalf("after 0x03: %+v (want known, on-air 0011)", mx)
	}

	// The real-world flip trigger: stripped 0x30s interleaved with 0x03s.
	for i := 0; i < 20; i++ {
		d.recordMixerStatus(parse(t, synthStatusNew("DJM-A9", 33)))
		mx = d.MixerSnapshot()[33]
		if !mx.ChannelStateKnown {
			t.Fatalf("0x30 #%d wiped ChannelStateKnown — views would flip to the 'detected' fallback", i+1)
		}
		if mx.ChannelOnAir != 0b0011 {
			t.Fatalf("0x30 #%d lost channel state: on-air %04b, want 0011", i+1, mx.ChannelOnAir)
		}
		d.recordMixerChannels(parse(t, synthChannels("DJM-A9", 33, [4]bool{true, true, false, false})))
	}

	// A channel change mid-stream still lands.
	d.recordMixerChannels(parse(t, synthChannels("DJM-A9", 33, [4]bool{false, false, true, true})))
	d.recordMixerStatus(parse(t, synthStatusNew("DJM-A9", 33)))
	if mx = d.MixerSnapshot()[33]; mx.ChannelOnAir != 0b1100 {
		t.Fatalf("channel change lost through 0x30: on-air %04b, want 1100", mx.ChannelOnAir)
	}
}

// TestMixerStatusLegacy0x29ReplacesWholesale: a rich 0x29 carries channel
// and master state inline, so it must replace the entry (not merge stale
// fields over fresh ones).
func TestMixerStatusLegacy0x29ReplacesWholesale(t *testing.T) {
	d := &VirtualDevice{}
	d.recordMixerChannels(parse(t, synthChannels("DJM-900", 33, [4]bool{true, false, false, false})))
	d.recordMixerStatus(parse(t, synthStatusLegacy("DJM-900", 33, 128.0, 0b0110)))
	mx := d.MixerSnapshot()[33]
	if !mx.ChannelStateKnown || mx.ChannelOnAir != 0b0110 || mx.MasterBPM != 128.0 {
		t.Fatalf("0x29 not applied wholesale: %+v", mx)
	}
}

// TestMixerStatusFirstPacketFlag: recordMixerStatus reports the first status
// packet from a mixer (the listener logs it), and only the first.
func TestMixerStatusFirstPacketFlag(t *testing.T) {
	d := &VirtualDevice{}
	if !d.recordMixerStatus(parse(t, synthStatusNew("DJM-A9", 33))) {
		t.Error("first status packet not flagged")
	}
	if d.recordMixerStatus(parse(t, synthStatusNew("DJM-A9", 33))) {
		t.Error("second status packet flagged as first")
	}
	// A 0x30 arriving before any 0x03 must leave state honestly unknown.
	if mx := d.MixerSnapshot()[33]; mx.ChannelStateKnown {
		t.Errorf("0x30 alone must not claim channel state: %+v", mx)
	}
}

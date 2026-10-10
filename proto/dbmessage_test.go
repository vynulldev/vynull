// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import "testing"

// NOTE on coverage: MarshalDBMessage (field-framed responses) and
// ParseDBMessage (raw-framed deck requests) implement two different wire
// dialects, so a marshal→parse round-trip does not exist on the real wire.
// Two permanently-skipped "round trip" tests sat here from the initial
// commit until the v0.5.0 audit removed them. Honest wire-level coverage
// needs golden fixtures captured from a real deck session (request bytes
// in, expected DBMessage out; response DBMessage in, expected bytes out) —
// capture with the tooling in vynull-tools when the rig is next up.

func TestUTF16Encoding(t *testing.T) {
	tests := []string{
		"hello",
		"",
		"日本語",
		"café",
	}
	for _, s := range tests {
		encoded := encodeUTF16BE(s)
		decoded := decodeUTF16BE(encoded)
		if decoded != s {
			t.Errorf("roundtrip %q -> %q", s, decoded)
		}
	}
}

// TestParseDBMessageMalformed ensures the decoder never panics on malformed or
// truncated input — a dbserver peer must not be able to crash the session by
// sending a short or junk frame (link-spec "malformed-framing" suite).
func TestParseDBMessageMalformed(t *testing.T) {
	// Every length from empty through just past the 19-byte header, including
	// the 11-18 byte range that used to panic on the data[7:19] tag slice.
	for n := 0; n <= 32; n++ {
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = byte(i)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ParseDBMessage panicked on %d-byte input: %v", n, r)
				}
			}()
			msg, err := ParseDBMessage(buf)
			if n < 19 && err == nil {
				t.Errorf("%d-byte input: expected error, got msg=%+v", n, msg)
			}
		}()
	}

	// A valid 19-byte header whose arg tags claim oversized binary/string
	// payloads must error, not panic or over-read.
	hdr := func(argCount byte, tags ...byte) []byte {
		b := make([]byte, 19)
		b[6] = argCount
		copy(b[7:19], tags)
		return b
	}
	cases := [][]byte{
		hdr(1, ArgBinary), // binary length field truncated
		append(hdr(1, ArgBinary), 0xff, 0xff, 0xff, 0xff), // binary length 4G, no data
		append(hdr(1, ArgString), 0xff, 0xff, 0xff, 0xff), // string charCount 4G
		append(hdr(1, ArgInt32), 0x01),                    // int32 truncated
		hdr(12, ArgInt8, ArgInt8, ArgInt8),                // argCount 12, tags present, no arg bytes
	}
	for i, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d panicked: %v", i, r)
				}
			}()
			ParseDBMessage(c) // must return (error or ok), never panic
		}()
	}
}

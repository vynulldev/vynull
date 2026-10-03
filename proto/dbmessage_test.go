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

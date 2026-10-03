// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/vynulldev/vynull/proto"
)

// makeNXS2CueBlob builds a minimal 124-byte NXS2 cue blob with the given cue
// number, palette index (0x4e), and RGB (0x4f-0x51), matching what
// ParseCueBlob reads.
func makeNXS2CueBlob(number uint16, idx, r, g, b byte) []byte {
	blob := make([]byte, 124)
	blob[0x04] = byte(number)
	blob[0x06] = 1    // type = cue
	blob[0x34] = 0x42 // NXS2 picker-coloured marker
	blob[0x4e] = idx
	blob[0x4f], blob[0x50], blob[0x51] = r, g, b
	return blob
}

func TestParseCueBlobCDJDefaultGreen(t *testing.T) {
	// A default hot cue set on the CDJ arrives as palette idx 0 + RGB 00ff30
	// (green). Taking idx 0 alone would paint it Pioneer orange; the RGB must
	// win so the web UI shows green.
	cue, err := ParseCueBlob(makeNXS2CueBlob(5, 0x00, 0x00, 0xff, 0x30), 42)
	if err != nil {
		t.Fatal(err)
	}
	if cue.ColorID != 0x16 {
		t.Errorf("CDJ green cue: color_id = %#x, want 0x16 (green), not orange", cue.ColorID)
	}
}

func TestParseCueBlobHonoursPaletteIndex(t *testing.T) {
	// A non-zero palette index is authoritative; RGB is not consulted.
	cue, _ := ParseCueBlob(makeNXS2CueBlob(1, 0x2a, 0x00, 0x00, 0x00), 42)
	if cue.ColorID != 0x2a {
		t.Errorf("color_id = %#x, want 0x2a (index honoured)", cue.ColorID)
	}
}

func TestParseCueBlobNoColour(t *testing.T) {
	// idx 0 with all-zero RGB is a genuinely uncoloured cue → stays 0.
	cue, _ := ParseCueBlob(makeNXS2CueBlob(1, 0x00, 0x00, 0x00, 0x00), 42)
	if cue.ColorID != 0 {
		t.Errorf("color_id = %#x, want 0 (no colour)", cue.ColorID)
	}
}

func TestLoadAllRederivesCueColour(t *testing.T) {
	dir := t.TempDir()
	// A cue stored before the fix: the JSON has color_id 0, but the raw CDJ
	// blob carries green in its RGB bytes. Loading should re-derive green.
	if err := os.WriteFile(filepath.Join(dir, "cues_7.json"),
		[]byte(`[{"number":5,"type":1,"time_ms":0,"loop_ms":-1,"status":1,"color_id":0}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cue_7_5.bin"),
		makeNXS2CueBlob(5, 0x00, 0x00, 0xff, 0x30), 0o644); err != nil {
		t.Fatal(err)
	}
	cues := NewCueStore(dir).GetCues(7)
	if len(cues) != 1 || cues[0].ColorID != 0x16 {
		t.Fatalf("re-derived colour = %+v, want one cue with color_id 0x16", cues)
	}
}

// TestDeckMemoryCueSavesDontOverwrite pins the 0x2705 memory-cue remap:
// decks mark memory cues with number 0 on the wire, and the store keys
// by number — without remapping to the internal 9+ range, every
// deck-saved memory cue overwrote the previous one (field report).
func TestDeckMemoryCueSavesDontOverwrite(t *testing.T) {
	h := &Handler{cues: NewCueStore(t.TempDir())}
	mk := func(timeMs uint32) []byte {
		b := makeNXS2CueBlob(0, 0, 0x00, 0xff, 0x30) // number 0 = memory cue
		binary.LittleEndian.PutUint32(b[0x0c:], timeMs)
		return b
	}
	save := func(blob []byte) {
		h.Handle(&proto.DBMessage{Type: 0x2705, Args: []proto.DBArg{
			proto.ArgI32(0), proto.ArgI32(42), proto.ArgI32(0), proto.ArgI32(0), proto.ArgBlob(blob),
		}})
	}
	save(mk(1000))
	save(mk(5000))

	cues := h.cues.GetCues(42)
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2 (second memory cue overwrote the first)", len(cues))
	}
	nums := map[uint16]bool{cues[0].Number: true, cues[1].Number: true}
	if !nums[9] || !nums[10] {
		t.Fatalf("cue numbers = %v, want internal memory numbers 9 and 10", nums)
	}
}

// TestMarshalCueBlobMemoryWireNumber pins the outbound half of the
// convention: internal memory numbers (9+) go on the wire as 0 — the
// deck's marker for a memory cue — while hot pads 1-8 keep their number.
func TestMarshalCueBlobMemoryWireNumber(t *testing.T) {
	mem := MarshalCueBlob(&CuePoint{Number: 9, Type: 1, TimeMs: 1234, LoopMs: -1, Status: 1})
	if n := binary.LittleEndian.Uint16(mem[0x04:]); n != 0 {
		t.Fatalf("memory cue wire number = %d, want 0", n)
	}
	hot := MarshalCueBlob(&CuePoint{Number: 3, Type: 1, TimeMs: 1234, LoopMs: -1, Status: 1})
	if n := binary.LittleEndian.Uint16(hot[0x04:]); n != 3 {
		t.Fatalf("hot cue wire number = %d, want 3", n)
	}
}

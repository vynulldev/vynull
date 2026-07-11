// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"os"
	"path/filepath"
	"testing"
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

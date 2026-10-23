// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"bytes"
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// An imported track's 0x2004 preview is built from its .DAT previews: each
// PWAV entry as height then whiteness, the PWV2 entries, then the footer.
func TestPreviewFromANLZ(t *testing.T) {
	pwav := make([]byte, 400)
	for i := range pwav {
		pwav[i] = byte(i%8)<<5 | byte(i%32) // whiteness, height
	}
	pwv2 := bytes.Repeat([]byte{7}, 100)
	got := PreviewFromANLZ(pwav, pwv2)
	if len(got) != 904 {
		t.Fatalf("%d bytes, want 904", len(got))
	}
	for i := range pwav {
		if h, w := got[i*2], got[i*2+1]; h != byte(i%32) || w != byte(i%8) {
			t.Fatalf("entry %d: height %d whiteness %d, want %d %d", i, h, w, i%32, i%8)
		}
	}
	if !bytes.Equal(got[800:900], pwv2) || !bytes.Equal(got[900:], previewFooter[:]) {
		t.Errorf("tail % x, want PWV2 then the footer", got[800:])
	}
	if PreviewFromANLZ(pwav[:399], pwv2) != nil {
		t.Error("a short PWAV built a preview")
	}
}

// A result's own preview wins; an imported one (only the .DAT previews) gets
// one built; a result with neither has none.
func TestDeckPreview(t *testing.T) {
	own := make([]byte, 904)
	own[0] = 9
	if got := DeckPreview(&analysis.Result{WavePreview: own, WavePreviewANLZ: make([]byte, 400)}); !bytes.Equal(got, own) {
		t.Error("the result's own preview wasn't served")
	}
	if got := DeckPreview(&analysis.Result{WavePreviewANLZ: make([]byte, 400), WaveTinyANLZ: make([]byte, 100)}); len(got) != 904 {
		t.Errorf("imported result: %d bytes, want 904", len(got))
	}
	if got := DeckPreview(&analysis.Result{}); got != nil {
		t.Errorf("no previews: got %d bytes", len(got))
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/proto"
)

// A deck asking for an imported track's preview (0x2004) gets one built from
// the track's .DAT previews instead of "no data".
func TestImportedTrackWavePreview(t *testing.T) {
	store := analysis.NewStore()
	store.Set(7, &analysis.Result{Imported: true, WavePreviewANLZ: make([]byte, 400), WaveTinyANLZ: make([]byte, 100)})
	h := &Handler{analysis: store}
	resp := h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetWavePreview,
		Args: []proto.DBArg{proto.ArgI32(0x01080401), proto.ArgI32(4), proto.ArgI32(7), proto.ArgI32(0)}})
	if len(resp) != 1 || len(resp[0].Args) != 4 || resp[0].Args[2].Int() != 904 {
		t.Fatalf("reply %+v, want a 904-byte preview", resp)
	}
}

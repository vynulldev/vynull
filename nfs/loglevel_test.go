// SPDX-License-Identifier: GPL-3.0-or-later

package nfs

import (
	"bytes"
	"encoding/binary"
	"log"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/internal/dlog"
)

// rpcReadCall builds a minimal Sun-RPC NFS READ call (prog 100003 v2 proc 6)
// with null auth and a zeroed fh/offset/count body.
func rpcReadCall() []byte {
	var b bytes.Buffer
	for _, v := range []uint32{0x12345678, 0 /* CALL */, 2, 100003, 2, 6 /* READ */} {
		binary.Write(&b, binary.BigEndian, v)
	}
	b.Write(make([]byte, 16)) // null credential + verifier
	b.Write(make([]byte, 40)) // fh(32) + offset(4) + count(4)
	return b.Bytes()
}

// TestReadLoggingLevelGated pins the --log-level contract for the NFS
// per-packet lines: at the default info level a READ produces NO log output
// (the old behaviour logged three lines per packet, hex dumps included,
// which spammed the log during playback); at trace the per-packet lines
// appear.
func TestReadLoggingLevelGated(t *testing.T) {
	srv := NewServer(t.TempDir())
	pkt := rpcReadCall()

	capture := func(level dlog.Level) string {
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(prev)
		defer dlog.SetLevel(dlog.Info)
		dlog.SetLevel(level)
		srv.dispatchRPC(pkt, srv.handleNFS)
		return buf.String()
	}

	if out := capture(dlog.Info); strings.Contains(out, "nfs: READ") {
		t.Errorf("info level logged per-packet READ lines:\n%s", out)
	}
	out := capture(dlog.Trace)
	if !strings.Contains(out, "nfs: READ") {
		t.Errorf("trace level missing per-packet READ lines:\n%s", out)
	}
	if !strings.Contains(out, "nfs: RPC call") {
		t.Errorf("trace level missing RPC call line:\n%s", out)
	}
}

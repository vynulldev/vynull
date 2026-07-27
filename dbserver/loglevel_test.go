// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// TestBrowseLoggingLevelGated pins the --log-level contract for dbserver:
// at the default info level a routine browse request (root menu) produces
// NO log output — the old behaviour logged the per-message type line plus
// per-request menu detail for every deck button press — while debug shows
// the browse detail and trace adds the per-message line.
func TestBrowseLoggingLevelGated(t *testing.T) {
	h := &Handler{lib: library.New()}
	msg := &proto.DBMessage{Type: 0x1000} // root menu request

	capture := func(level dlog.Level) string {
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(prev)
		defer dlog.SetLevel(dlog.Info)
		dlog.SetLevel(level)
		h.Handle(msg)
		return buf.String()
	}

	if out := capture(dlog.Info); strings.Contains(out, "dbserver") {
		t.Errorf("info level logged browse lines:\n%s", out)
	}
	if out := capture(dlog.Debug); !strings.Contains(out, "root menu") {
		t.Errorf("debug level missing browse detail:\n%s", out)
	}
	if out := capture(dlog.Trace); !strings.Contains(out, "dbserver msg type=") {
		t.Errorf("trace level missing per-message line:\n%s", out)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"log"
	"strings"
	"testing"
)

// TestCaptureLogsIncludesStartupLines pins the DIAG log-tail contract: lines
// logged after CaptureLogs but before the API server starts (library scan,
// pdb load, NFS setup) must be served by the ring the server adopts. The
// ring used to attach only at Server.Start, so exactly those lines — the
// ones needed to debug a setup remotely — never reached the web UI.
func TestCaptureLogsIncludesStartupLines(t *testing.T) {
	prev := log.Writer()
	defer func() {
		log.SetOutput(prev)
		earlyRing = nil
	}()

	CaptureLogs()
	log.Printf("pdb: loaded 80 tracks from test-startup-line")

	s := &Server{}
	s.installLogTail()
	found := false
	for _, e := range s.logs.since(0) {
		if strings.Contains(e.Line, "test-startup-line") {
			found = true
		}
	}
	if !found {
		t.Fatal("startup line logged before installLogTail is missing from the diag ring")
	}
}

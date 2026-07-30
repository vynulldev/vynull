// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runCLI invokes the built binary's subcommand against the test server.
func runCLI(t *testing.T, s *server, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(cmd.Environ(), "VYNULL_ADDR="+s.baseURL)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCLISubcommands drives the shell surface end to end: add, search
// (human + json), playlists, status, and load's error paths. The load
// success path needs a CDJ to accept the command, so it is exercised up to
// the API boundary (unknown device → clean error).
func TestCLISubcommands(t *testing.T) {
	s := startServer(t, "")
	media := t.TempDir()
	kick := kickWAV(t, media, "cli-kick.wav", 124)

	// add
	out, err := runCLI(t, s, "add", kick)
	if err != nil || !strings.Contains(out, "added 1") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	s.waitFor("track analyzed", 2*time.Minute, func() bool {
		ts := s.tracks()
		return len(ts) == 1 && ts[0].BPM > 0
	})

	// search (human): header + the track
	out, err = runCLI(t, s, "search", "cli-kick")
	if err != nil || !strings.Contains(out, "cli-kick") || !strings.Contains(out, "124") {
		t.Fatalf("search: %v\n%s", err, out)
	}
	// search --json: valid machine-readable output
	out, err = runCLI(t, s, "search", "--json", "cli-kick")
	if err != nil {
		t.Fatalf("search --json: %v\n%s", err, out)
	}
	var hits []struct {
		ID  uint32  `json:"id"`
		BPM float64 `json:"bpm"`
	}
	if err := json.Unmarshal([]byte(out), &hits); err != nil || len(hits) != 1 || hits[0].BPM != 124 {
		t.Fatalf("search --json parse: %v\n%s", err, out)
	}
	// search miss: empty result, exit 0
	if out, err = runCLI(t, s, "search", "zzz-no-such"); err != nil {
		t.Fatalf("empty search must exit 0: %v\n%s", err, out)
	}

	// playlists (empty library → header only, exit 0)
	if out, err = runCLI(t, s, "playlists"); err != nil {
		t.Fatalf("playlists: %v\n%s", err, out)
	}

	// status
	out, err = runCLI(t, s, "status")
	if err != nil || !strings.Contains(out, "1 tracks") {
		t.Fatalf("status: %v\n%s", err, out)
	}

	// load error paths: bad deck, ambiguous/unknown query, no deck on link
	if out, _ = runCLI(t, s, "load", "1", "9"); !strings.Contains(out, "deck must be 1-4") {
		t.Fatalf("load bad deck: %s", out)
	}
	if out, _ = runCLI(t, s, "load", "zzz-no-such", "2"); !strings.Contains(out, "no track matches") {
		t.Fatalf("load unknown query: %s", out)
	}
	out, err = runCLI(t, s, "load", "1", "2")
	if err == nil {
		t.Fatalf("load with no CDJ on the link must fail cleanly, got:\n%s", out)
	}

	// unknown flag → clean error, non-zero exit
	if out, err = runCLI(t, s, "search", "--bogus"); err == nil || !strings.Contains(out, "unknown flag") {
		t.Fatalf("unknown flag: %v\n%s", err, out)
	}

	// help surfaces: `vynull help` lists every command; `<cmd> -h` prints
	// that command's usage and exits 0.
	out, err = runCLI(t, s, "help")
	if err != nil {
		t.Fatalf("help: %v\n%s", err, out)
	}
	for _, name := range []string{"search", "add", "load", "players", "playlists", "status"} {
		if !strings.Contains(out, name) {
			t.Errorf("help output missing %q:\n%s", name, out)
		}
	}
	if out, err = runCLI(t, s, "load", "-h"); err != nil || !strings.Contains(out, "usage: vynull load") {
		t.Fatalf("load -h: %v\n%s", err, out)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vynulldev/vynull/dbserver"
)

func cueRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	s.handleCues(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	return response
}

func requirePersisted(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var receipt struct{ OK, Persisted bool }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &receipt) != nil || !receipt.OK || !receipt.Persisted {
		t.Fatalf("write not confirmed: %d %s", response.Code, response.Body.String())
	}
}

func TestCueAPIConfirmsCreateReplaceLoopDeleteOnDisk(t *testing.T) {
	dir := t.TempDir()
	store := dbserver.NewCueStore(dir)
	defer store.Close()
	s := &Server{Cues: &CueStoreAdapter{Store: store}}
	requirePersisted(t, cueRequest(s, "POST", "/api/tracks/7/cues", `{"number":1,"time_ms":125,"color_id":42}`))
	requirePersisted(t, cueRequest(s, "POST", "/api/tracks/7/cues", `{"number":1,"type":"cue","time_ms":625,"color_id":42}`))
	requirePersisted(t, cueRequest(s, "POST", "/api/tracks/7/cues", `{"number":2,"type":"loop","time_ms":1125,"loop_ms":3125}`))
	check := func(want int) []dbserver.CuePoint {
		reloaded := dbserver.NewCueStore(dir)
		defer reloaded.Close()
		cues := reloaded.GetCues(7)
		if len(cues) != want {
			t.Fatalf("disk cues: %+v", cues)
		}
		return cues
	}
	cues := check(2)
	if cues[0].TimeMs != 625 || cues[0].LoopMs != -1 || cues[0].ColorID != 42 || cues[1].LoopMs != 3125 {
		t.Fatalf("round trip: %+v", cues)
	}
	requirePersisted(t, cueRequest(s, "DELETE", "/api/tracks/7/cues/1", ""))
	if check(1)[0].Number != 2 {
		t.Fatal("wrong cue deleted")
	}
	for _, body := range []string{`{"number":0}`, `{"number":2,"type":"invalid"}`, `{"number":2,"type":"loop","time_ms":100,"loop_ms":100}`} {
		if r := cueRequest(s, "POST", "/api/tracks/7/cues", body); r.Code != 400 {
			t.Fatalf("invalid edit: %d", r.Code)
		}
	}
	if check(1)[0].LoopMs != 3125 {
		t.Fatal("invalid input changed the saved loop")
	}
}

func TestCueAPIFailedDiskWritesAreNotAcknowledgedAndExplicitRetryWorks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "cues")
	backup := filepath.Join(base, "backup")
	store := dbserver.NewCueStore(dir)
	defer store.Close()
	s := &Server{Cues: &CueStoreAdapter{Store: store}}
	body := `{"number":1,"type":"cue","time_ms":125,"loop_ms":-1}`
	for _, method := range []string{"POST", "DELETE"} {
		// Break the parent path without depending on root/permission semantics.
		if err := os.Rename(dir, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
			t.Fatal(err)
		}
		path := "/api/tracks/7/cues"
		if method == "DELETE" {
			path += "/1"
		}
		response := cueRequest(s, method, path, body)
		if response.Code != 500 || !strings.Contains(response.Body.String(), "disk persistence failed") {
			t.Fatalf("false success: %d %s", response.Code, response.Body.String())
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, dir); err != nil {
			t.Fatal(err)
		}
		requirePersisted(t, cueRequest(s, method, path, body))
		reloaded := dbserver.NewCueStore(dir)
		count := len(reloaded.GetCues(7))
		reloaded.Close()
		if (method == "POST" && count != 1) || (method == "DELETE" && count != 0) {
			t.Fatalf("retry %s: %d cues", method, count)
		}
	}
}

// Legacy/custom adapters may not implement flushing; keep the ack compatible
// without asserting that an in-memory readback proves persistence.
type volatileCueStore struct{ CueStoreInterface }

func TestCueAPIReportsUnconfirmedPersistenceForAlternateStores(t *testing.T) {
	store := dbserver.NewCueStore(t.TempDir())
	defer store.Close()
	s := &Server{Cues: volatileCueStore{&CueStoreAdapter{Store: store}}}
	response := cueRequest(s, "POST", "/api/tracks/7/cues", `{"number":1,"time_ms":0}`)
	var receipt struct{ OK, Persisted bool }
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Persisted {
		t.Fatalf("receipt: %+v", receipt)
	}
}

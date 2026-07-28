// SPDX-License-Identifier: GPL-3.0-or-later

package mpris

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// withSessionBus runs fn with DBUS_SESSION_BUS_ADDRESS pointing at a private
// dbus-daemon, so the test never touches (or requires) the user's bus.
func withSessionBus(t *testing.T, fn func()) {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read bus address: %v", err)
	}
	old := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	os.Setenv("DBUS_SESSION_BUS_ADDRESS", addr[:len(addr)-1])
	defer os.Setenv("DBUS_SESSION_BUS_ADDRESS", old)
	fn()
}

// TestPublisher drives the full surface against a private bus: name claim,
// identity, read-only capabilities, and the stopped -> playing -> stopped
// lifecycle with metadata following the snapshot.
func TestPublisher(t *testing.T) {
	withSessionBus(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var current NowPlaying
		_, err := Start(ctx, func() NowPlaying { return current }, 50*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}

		client, err := dbus.ConnectSessionBus()
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		obj := client.Object(busName, objectPath)

		get := func(iface, name string) interface{} {
			v, err := obj.GetProperty(iface + "." + name)
			if err != nil {
				t.Fatalf("get %s.%s: %v", iface, name, err)
			}
			return v.Value()
		}
		waitStatus := func(want string) {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if get(playerFace, "PlaybackStatus") == want {
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatalf("PlaybackStatus never became %q", want)
		}

		// Identity + read-only capabilities.
		if id := get(rootIface, "Identity"); id != "Vynull" {
			t.Errorf("Identity = %v", id)
		}
		for _, cap := range []string{"CanPlay", "CanPause", "CanSeek", "CanControl", "CanGoNext"} {
			if v := get(playerFace, cap); v != false {
				t.Errorf("%s = %v, want false (read-only player)", cap, v)
			}
		}
		waitStatus("Stopped")

		// Deck starts playing.
		current = NowPlaying{
			Playing: true, DeviceNumber: 2, TrackID: 42,
			Title: "Encounter", Artist: "Jerome Isma-Ae",
			DurationMs: 374000, PositionMs: 61500,
			ArtURL: "http://127.0.0.1:9443/api/artwork/42",
		}
		waitStatus("Playing")
		md := get(playerFace, "Metadata").(map[string]dbus.Variant)
		if v := md["xesam:title"].Value(); v != "Encounter" {
			t.Errorf("title = %v", v)
		}
		if v := md["xesam:artist"].Value().([]string); len(v) != 1 || v[0] != "Jerome Isma-Ae" {
			t.Errorf("artist = %v", v)
		}
		if v := md["mpris:length"].Value(); v != int64(374000000) {
			t.Errorf("length = %v µs", v)
		}
		if v := md["mpris:artUrl"].Value(); v != "http://127.0.0.1:9443/api/artwork/42" {
			t.Errorf("artUrl = %v", v)
		}
		if pos := get(playerFace, "Position").(int64); pos != 61500000 {
			t.Errorf("Position = %d µs, want 61500000", pos)
		}

		// Control methods exist and no-op (a read-only player must still
		// answer them without error).
		if call := obj.Call(playerFace+".Play", 0); call.Err != nil {
			t.Errorf("Play call: %v", call.Err)
		}
		if call := obj.Call(playerFace+".Seek", 0, int64(1000)); call.Err != nil {
			t.Errorf("Seek call: %v", call.Err)
		}

		// Deck stops.
		current = NowPlaying{}
		waitStatus("Stopped")
		md = get(playerFace, "Metadata").(map[string]dbus.Variant)
		if v := md["xesam:title"].Value(); v != "" {
			t.Errorf("stopped metadata still carries a title: %v", v)
		}
		if v := md["mpris:trackid"].Value(); v != dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack") {
			t.Errorf("stopped trackid = %v", v)
		}
	})
}

// TestStartWithoutBus: no session bus must yield a clean error, not a hang
// or panic — headless boxes hit this on every boot.
func TestStartWithoutBus(t *testing.T) {
	old := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/vynull-test-bus")
	defer os.Setenv("DBUS_SESSION_BUS_ADDRESS", old)
	if _, err := Start(context.Background(), func() NowPlaying { return NowPlaying{} }, time.Second); err == nil {
		t.Fatal("expected an error with no session bus")
	}
}

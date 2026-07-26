// SPDX-License-Identifier: GPL-3.0-or-later

// Package e2e drives the real vynull binary over its HTTP API against
// synthesized media, covering the main user-facing flows end to end: the
// analysis pipeline (BPM integer snap, waveforms, artwork), playlist
// membership, the stale-cache upgrade path, and a light bulk-add
// stability pass.
//
// Gated behind VYNULL_E2E=1 so `go test ./...` stays fast:
//
//	VYNULL_E2E=1 go test ./e2e/ -v
//
// The suite builds the binary once, launches one server per test with an
// isolated temp data-dir on a free port, and synthesizes its own audio
// (deterministic kick-train WAVs with known ground-truth BPM; an MP3 with
// embedded cover art via ffmpeg). It skips itself when the DJ-Link UDP
// ports are held by a running instance.
package e2e

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	if os.Getenv("VYNULL_E2E") == "" {
		fmt.Println("e2e: set VYNULL_E2E=1 to run")
		os.Exit(0)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Println("e2e: ffmpeg not on PATH")
		os.Exit(1)
	}
	// The device side binds fixed UDP ports; a running vynull instance
	// makes every server exit at startup. Probe and bail out early with a
	// clear message instead of failing each test cryptically.
	if pc, err := net.ListenPacket("udp4", ":50000"); err != nil {
		fmt.Println("e2e: UDP 50000 busy — is a vynull instance running? skipping")
		os.Exit(0)
	} else {
		pc.Close()
	}
	dir, err := os.MkdirTemp("", "vynull-e2e-*")
	if err != nil {
		fmt.Println("e2e:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	binPath = filepath.Join(dir, "vynull")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = ".." // module root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Printf("e2e: build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// server is one running vynull instance over a temp data-dir.
type server struct {
	t       *testing.T
	cmd     *exec.Cmd
	baseURL string
	dataDir string
	logPath string
}

// startServer launches the built binary on a free port with the given
// data-dir (created if empty) and waits for the API to come up.
func startServer(t *testing.T, dataDir string) *server {
	t.Helper()
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	logPath := filepath.Join(t.TempDir(), "server.log")
	cmd := exec.Command(binPath,
		"--interface", "lo",
		"--data-dir", dataDir,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--tui=false",
		"--log-file", logPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{
		t:       t,
		cmd:     cmd,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: dataDir,
		logPath: logPath,
	}
	t.Cleanup(s.stop)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.baseURL + "/api/tracks")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	logTail, _ := os.ReadFile(logPath)
	t.Fatalf("server did not come up on %s; log:\n%s", s.baseURL, tail(logTail, 2000))
	return nil
}

func (s *server) stop() {
	if s.cmd.Process == nil {
		return
	}
	s.cmd.Process.Signal(syscall.SIGINT)
	done := make(chan struct{})
	go func() { s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		s.cmd.Process.Kill()
		<-done
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

// ---------- HTTP helpers ----------

func (s *server) post(path string, body any) *http.Response {
	s.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.baseURL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		s.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func (s *server) postOK(path string, body any, out any) {
	s.t.Helper()
	resp := s.post(path, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		msg.ReadFrom(resp.Body)
		s.t.Fatalf("POST %s: %d %s", path, resp.StatusCode, msg.String())
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			s.t.Fatalf("POST %s: decode: %v", path, err)
		}
	}
}

func (s *server) getJSON(path string, out any) int {
	s.t.Helper()
	resp, err := http.Get(s.baseURL + path)
	if err != nil {
		s.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			s.t.Fatalf("GET %s: decode: %v", path, err)
		}
	}
	return resp.StatusCode
}

// track mirrors the API track payload fields the tests assert on.
type track struct {
	ID          uint32  `json:"id"`
	Title       string  `json:"title"`
	BPM         float64 `json:"bpm"`
	DetectedBPM float64 `json:"detected_bpm"`
	Key         string  `json:"key"`
	Duration    float64 `json:"duration"`
	ArtID       uint32  `json:"art_id"`
	ArtChecked  bool    `json:"art_checked"`
}

func (s *server) tracks() []track {
	s.t.Helper()
	var ts []track
	if code := s.getJSON("/api/tracks", &ts); code != http.StatusOK {
		s.t.Fatalf("GET /api/tracks: %d", code)
	}
	return ts
}

// addTracks POSTs paths to /api/tracks/add and returns the library rows.
func (s *server) addTracks(paths ...string) []track {
	s.t.Helper()
	s.postOK("/api/tracks/add", map[string]any{"paths": paths}, nil)
	return s.tracks()
}

// waitFor polls cond until it returns true or the deadline passes.
func (s *server) waitFor(what string, timeout time.Duration, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	logTail, _ := os.ReadFile(s.logPath)
	s.t.Fatalf("timed out waiting for %s; server log tail:\n%s", what, tail(logTail, 3000))
}

// ---------- media synthesis ----------

const synthRate = 44100

// writeWAV writes 16-bit mono PCM at synthRate.
func writeWAV(t *testing.T, path string, samples []float32) {
	t.Helper()
	var buf bytes.Buffer
	n := len(samples)
	dataLen := n * 2
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataLen))
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&buf, binary.LittleEndian, uint32(synthRate))
	binary.Write(&buf, binary.LittleEndian, uint32(synthRate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataLen))
	for _, v := range samples {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		binary.Write(&buf, binary.LittleEndian, int16(v*32767))
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// kickTrain renders a four-on-the-floor kick pattern at exactly the given
// BPM (decaying 60Hz bursts), the same signal the analysis unit tests use —
// so the detected BPM has an exact expected value.
func kickTrain(bpm, durSec float64) []float32 {
	n := int(synthRate * durSec)
	out := make([]float32, n)
	period := 60.0 / bpm * synthRate
	burst := int(0.08 * synthRate)
	twoPiF := 2.0 * math.Pi * 60.0 / synthRate
	for beat := 0; ; beat++ {
		start := int(float64(beat) * period)
		if start >= n {
			break
		}
		for i := 0; i < burst && start+i < n; i++ {
			env := math.Exp(-float64(i) / (0.02 * synthRate))
			out[start+i] += float32(0.8 * env * math.Sin(twoPiF*float64(i)))
		}
	}
	return out
}

// kickWAV writes a kick train and returns its path.
func kickWAV(t *testing.T, dir string, name string, bpm float64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	writeWAV(t, p, kickTrain(bpm, 30))
	return p
}

// writeCoverJPEG writes a small gradient JPEG (a stand-in cover image).
func writeCoverJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 200, 255})
		}
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, jb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// flacKick transcodes a kick train to FLAC (the lossless analysis path —
// no encoder-delay compensation).
func flacKick(t *testing.T, dir, name string, bpm float64) string {
	t.Helper()
	wav := kickWAV(t, dir, name+".tmp.wav", bpm)
	flac := filepath.Join(dir, name)
	out, err := exec.Command("ffmpeg", "-y", "-i", wav, "-c:a", "flac", flac).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg flac: %v\n%s", err, out)
	}
	os.Remove(wav)
	return flac
}

// mp3WithArt transcodes a kick train to MP3 with an embedded JPEG cover.
func mp3WithArt(t *testing.T, dir string, bpm float64) string {
	t.Helper()
	wav := kickWAV(t, dir, "art-src.wav", bpm)
	cover := filepath.Join(dir, "cover.jpg")
	writeCoverJPEG(t, cover)
	mp3 := filepath.Join(dir, "with-art.mp3")
	out, err := exec.Command("ffmpeg", "-y", "-i", wav, "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-q:a", "5",
		"-c:v", "copy", "-id3v2_version", "3",
		"-metadata:s:v", "comment=Cover (front)", mp3).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg mp3+art: %v\n%s", err, out)
	}
	os.Remove(wav)   // only the MP3 should enter the library
	os.Remove(cover) // and no stray folder art beside it
	return mp3
}

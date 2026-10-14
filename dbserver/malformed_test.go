// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// The malformed-framing cases from the link-spec conformance suite
// (rekordbox 7.2.19 oracle), byte for byte. rekordbox answers bad magic, too
// many arguments, and an unknown field tag with a teardown; for the others it
// keeps waiting for the rest of the request.
var malformedFrames = []struct {
	name     string
	hex      string
	wantErr  bool // false: the frame parses (the suite's oversized-blob tags its blob 0x03, the trailing argument CDJs advertise but never send, so it is not read)
	badFrame bool // rekordbox answers with a teardown
}{
	{"bad-magic", "110000000011000000011010040f001400000000", true, true},
	{"truncated-header", "11872349ae1100000001", true, false},
	{"too-many-arguments", "11872349ae11000000011010040f211400000021000000000000000000000000000000000000000000000000000000000000000000", true, true},
	{"unknown-field-tag", "11872349ae11000000011010040f01140000000106ff", true, true},
	{"tag-list-too-short", "11872349ae11000000011010040f011400000000", true, false},
	{"oversized-blob", "11872349ae11000000011010040f0114000000010314ffffffff", false, false},
	// A blob that is read (tag 0x14) declaring 4 GiB: discarded, never
	// allocated, and the session ends when the sender stops.
	{"oversized-blob-read", "11872349ae11000000011010040f0114000000011414ffffffff", true, false},
	{"oversized-string-read", "11872349ae11000000011010040f0114000000012626ffffffff", true, false},
}

// readFrame sends raw bytes to readMessage and closes the sending side, so a
// request still waiting for more data ends with an error instead of blocking.
func readFrame(t *testing.T, raw []byte) error {
	t.Helper()
	server, client := net.Pipe()
	defer server.Close()
	go func() {
		client.Write(raw)
		client.Close()
	}()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("readMessage panicked: %v", r)
				done <- nil
			}
		}()
		_, err := readMessage(server)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("readMessage did not return")
		return nil
	}
}

func TestReadMessageMalformedFrames(t *testing.T) {
	for _, c := range malformedFrames {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := hex.DecodeString(c.hex)
			err := readFrame(t, raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("error = %v, want error %v", err, c.wantErr)
			}
			if got := errors.Is(err, errBadFrame); got != c.badFrame {
				t.Errorf("bad-frame = %v (%v), want %v", got, err, c.badFrame)
			}
		})
	}
}

// A bad frame gets rekordbox's teardown (0x0100, TxID 0xfffffffe) before the
// session closes, and the daemon keeps serving.
func TestSessionTeardownOnBadFrame(t *testing.T) {
	// Real TCP, not net.Pipe: a pipe has no buffer, so the client's write of
	// a frame the server stops reading would block the teardown.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if server, err := ln.Accept(); err == nil {
			(&Server{}).handleSession(context.Background(), server)
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	client.SetDeadline(time.Now().Add(3 * time.Second))
	handshake := []byte{0x11, 0x00, 0x00, 0x00, 0x01}
	client.Write(handshake)
	echo := make([]byte, 5)
	if _, err := io.ReadFull(client, echo); err != nil || !bytes.Equal(echo, handshake) {
		t.Fatalf("handshake echo %x, %v", echo, err)
	}
	raw, _ := hex.DecodeString(malformedFrames[0].hex) // bad magic
	client.Write(raw)
	want := proto.MarshalDBMessage(&proto.DBMessage{TxID: proto.DBSetupTxID, Type: 0x0100})
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reply %x (%v), want teardown %x", got, err, want)
	}
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Error("session still open after the teardown")
	}
}

// No input may panic the frame reader.
func FuzzReadMessage(f *testing.F) {
	for _, c := range malformedFrames {
		raw, _ := hex.DecodeString(c.hex)
		f.Add(raw)
	}
	f.Add(proto.MarshalDBMessage(&proto.DBMessage{TxID: 1, Type: 0x1000, Args: []proto.DBArg{proto.ArgI32(1)}}))
	f.Fuzz(func(t *testing.T, raw []byte) {
		readFrame(t, raw)
	})
}

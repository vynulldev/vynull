// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bufio"
	"bytes"
	"net"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/vynulldev/vynull/proto"
)

// TestRenderedListsNotKept opens lists the way a CDJ-2000NXS2 does, each
// render taking the next transaction ID, and checks that the session lets a
// list go once later queries have replaced it: keeping every query's list by
// its ID kept them all for the whole session.
func TestRenderedListsNotKept(t *testing.T) {
	h := &Handler{lib: fixtureLibrary()}
	tx := uint32(100)
	open := func() []*menuItem {
		h.Handle(&proto.DBMessage{TxID: tx, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(0)}})
		items := h.pendingItems
		h.Handle(&proto.DBMessage{TxID: tx + 1, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
			proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0), proto.ArgI32(uint32(len(items))), proto.ArgI32(0)}})
		tx += 2
		return items
	}
	first := weak.Make(open()[0])
	for range 3 {
		open()
	}
	runtime.GC()
	if first.Value() != nil {
		t.Error("the first TRACK list is still kept after three more")
	}
	runtime.KeepAlive(h)
}

// TestRenderReusingQueryID checks that a render with its query's
// transaction ID still gets the query's list, once.
func TestRenderReusingQueryID(t *testing.T) {
	h := &Handler{lib: fixtureLibrary()}
	h.Handle(&proto.DBMessage{TxID: 7, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(0)}})
	want := h.pendingItems
	if got := h.getPendingTxID(7); len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("render with the query's ID got %d rows, want the query's %d", len(got), len(want))
	}
	if got := h.getPendingTxID(7); got != nil {
		t.Errorf("a second render with the same ID got %d rows again", len(got))
	}
}

// TestReadMessageBuffered checks that requests read through bufConn come
// out as they do from the connection itself, several arriving at once.
func TestReadMessageBuffered(t *testing.T) {
	var frames []byte
	reqs := []*proto.DBMessage{
		{TxID: 1, Type: proto.DBMsgGetTracks, Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(2)}},
		{TxID: 2, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0), proto.ArgI32(20), proto.ArgI32(0)}},
		{TxID: 3, Type: 0x2705, Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(42), proto.ArgI32(0), proto.ArgI32(76), proto.ArgBlob(make([]byte, 76))}},
	}
	for _, m := range reqs {
		frames = append(frames, proto.MarshalDBMessage(m)...)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go client.Write(frames) // all three in one write
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	rc := bufConn{Conn: server, r: bufio.NewReader(server)}
	for _, want := range reqs {
		got, err := readMessage(rc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(proto.MarshalDBMessage(got), proto.MarshalDBMessage(want)) {
			t.Errorf("read %+v, want %+v", got, want)
		}
	}
}

// BenchmarkReadMessage measures reading a deck's requests from a loopback
// TCP connection, from the connection itself and through bufConn.
func BenchmarkReadMessage(b *testing.B) {
	frame := proto.MarshalDBMessage(&proto.DBMessage{TxID: 1, Type: proto.DBMsgRenderMenu, Args: []proto.DBArg{
		proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(8), proto.ArgI32(0), proto.ArgI32(20), proto.ArgI32(0)}})
	for _, buffered := range []bool{false, true} {
		b.Run(map[bool]string{false: "conn", true: "buffered"}[buffered], func(b *testing.B) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			defer ln.Close()
			go func() {
				c, err := net.Dial("tcp", ln.Addr().String())
				if err != nil {
					return
				}
				defer c.Close()
				for {
					if _, err := c.Write(frame); err != nil {
						return
					}
				}
			}()
			server, err := ln.Accept()
			if err != nil {
				b.Fatal(err)
			}
			defer server.Close()
			var conn net.Conn = server
			if buffered {
				conn = bufConn{Conn: server, r: bufio.NewReader(server)}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := readMessage(conn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

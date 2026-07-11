// SPDX-License-Identifier: GPL-3.0-or-later

// Package dbclient is a minimal Pro DJ Link dbserver *client*. It queries the
// dbserver another player (a CDJ with a USB/SD, or a linked rekordbox) runs, to
// fetch metadata for tracks that aren't served by us — the inverse of the
// dbserver we implement in package dbserver, and modelled on it (our server's
// request parsers are the spec for what a client sends, and its response
// marshaling is the format a client parses; see docs/design/external-metadata.md).
//
// Status: the wire codec is exercised by unit tests against that same
// marshaling. The live handshake / framing / menu-descriptor packing can only
// be confirmed against real hardware, so Dial logs verbose hex to make the first
// on-deck run diagnosable.
package dbclient

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/vynulldev/vynull/proto"
)

// handshake is the 5-byte preamble a client sends first; the dbserver echoes it.
var handshake = []byte{0x11, 0x00, 0x00, 0x00, 0x01}

// Client is a connected dbserver client session. Not safe for concurrent use;
// the dbserver is stateful (one request/response at a time per connection).
type Client struct {
	conn      net.Conn
	playerNum uint8
	txID      uint32
	timeout   time.Duration
}

// Dial connects to ip:port, performs the handshake and setup, and returns a
// ready client. playerNum is our own device number (sent in setup and packed
// into menu requests).
func Dial(ip net.IP, port int, playerNum uint8) (*Client, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial dbserver %s:%d: %w", ip, port, err)
	}
	c := &Client{conn: conn, playerNum: playerNum, txID: 1, timeout: 4 * time.Second}

	// Handshake: send 5 bytes, read the echo.
	c.conn.SetDeadline(time.Now().Add(c.timeout))
	if _, err := c.conn.Write(handshake); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake write: %w", err)
	}
	echo := make([]byte, len(handshake))
	if _, err := io.ReadFull(c.conn, echo); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake read: %w", err)
	}

	// Setup: tell the server our player number; it replies [0, serverPlayer].
	if _, err := c.request(proto.DBSetupTxID, proto.DBMsgSetup, proto.ArgI32(uint32(playerNum))); err != nil {
		conn.Close()
		return nil, fmt.Errorf("setup: %w", err)
	}
	return c, nil
}

// Close ends the session.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) nextTx() uint32 { c.txID++; return c.txID }

// send writes one message.
func (c *Client) send(msg *proto.DBMessage) error {
	c.conn.SetDeadline(time.Now().Add(c.timeout))
	_, err := c.conn.Write(proto.MarshalDBMessage(msg))
	return err
}

// request sends a message and reads a single response.
func (c *Client) request(txID uint32, typ uint16, args ...proto.DBArg) (*proto.DBMessage, error) {
	if err := c.send(&proto.DBMessage{TxID: txID, Type: typ, Args: args}); err != nil {
		return nil, err
	}
	return c.recv()
}

// recv reads one field-tagged dbserver message, mirroring the framing package
// dbserver's readMessage produces (magic, then txid/type/argcount/tags fields,
// then typed args). String args are decoded from UTF-16BE to text.
func (c *Client) recv() (*proto.DBMessage, error) {
	c.conn.SetDeadline(time.Now().Add(c.timeout))
	return decodeMessage(c.conn)
}

// decodeMessage reads one field-tagged dbserver message from r.
func decodeMessage(r io.Reader) (*proto.DBMessage, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return nil, err
	}
	var magic [4]byte
	switch first[0] {
	case 0x11:
		if _, err := io.ReadFull(r, magic[:]); err != nil {
			return nil, err
		}
	case proto.DBMagic[0]:
		magic[0] = first[0]
		if _, err := io.ReadFull(r, magic[1:]); err != nil {
			return nil, err
		}
	case 0x00:
		return nil, fmt.Errorf("length-prefixed framing not handled yet (first byte 0x00) — capture the hex from your deck")
	default:
		return nil, fmt.Errorf("unexpected framing byte 0x%02x", first[0])
	}
	if magic != proto.DBMagic {
		return nil, fmt.Errorf("bad dbserver magic %x", magic)
	}

	txid, err := readNumField(r, 4)
	if err != nil {
		return nil, fmt.Errorf("txid: %w", err)
	}
	typ, err := readNumField(r, 2)
	if err != nil {
		return nil, fmt.Errorf("type: %w", err)
	}
	argc, err := readNumField(r, 1)
	if err != nil {
		return nil, fmt.Errorf("argcount: %w", err)
	}
	if _, err := readBinField(r); err != nil { // 12-byte tags array; not needed
		return nil, fmt.Errorf("tags: %w", err)
	}

	msg := &proto.DBMessage{TxID: txid, Type: uint16(typ)}
	for i := 0; i < int(argc); i++ {
		arg, err := readArg(r)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		msg.Args = append(msg.Args, arg)
	}
	return msg, nil
}

// readNumField reads a 1/2/4-byte number field (tag byte + value).
func readNumField(r io.Reader, size int) (uint32, error) {
	buf := make([]byte, 1+size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	switch size {
	case 1:
		return uint32(buf[1]), nil
	case 2:
		return uint32(binary.BigEndian.Uint16(buf[1:])), nil
	default:
		return binary.BigEndian.Uint32(buf[1:]), nil
	}
}

// readBinField reads a binary field (0x14 tag + 4-byte length + data).
func readBinField(r io.Reader) ([]byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(h[1:]))
	data := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// readArg reads one typed argument (its own inline tag + value).
func readArg(r io.Reader) (proto.DBArg, error) {
	var tag [1]byte
	if _, err := io.ReadFull(r, tag[:]); err != nil {
		return proto.DBArg{}, err
	}
	switch tag[0] {
	case 0x0f, 0x04:
		v, err := readN(r, 1)
		return proto.DBArg{Tag: proto.ArgInt8, Int8: v[0]}, err
	case 0x10, 0x05:
		v, err := readN(r, 2)
		return proto.DBArg{Tag: proto.ArgInt16, Int16: binary.BigEndian.Uint16(v)}, err
	case 0x11, 0x06:
		v, err := readN(r, 4)
		return proto.DBArg{Tag: proto.ArgInt32, Int32: binary.BigEndian.Uint32(v)}, err
	case 0x14, 0x03:
		data, err := readLenPrefixed(r, 1)
		return proto.DBArg{Tag: proto.ArgBinary, Bytes: data}, err
	case 0x26, 0x02:
		data, err := readLenPrefixed(r, 2) // length is a UTF-16 char count
		return proto.DBArg{Tag: proto.ArgString, Str: proto.DecodeUTF16BE(data)}, err
	default:
		return proto.DBArg{}, fmt.Errorf("unknown arg tag 0x%02x", tag[0])
	}
}

func readN(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

// readLenPrefixed reads a 4-byte length then length*unit bytes.
func readLenPrefixed(r io.Reader, unit int) ([]byte, error) {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(l[:])) * unit
	b := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// SPDX-License-Identifier: GPL-3.0-or-later

package proto

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// DBServer magic bytes that prefix every message on the dynamic port.
var DBMagic = [4]byte{0x87, 0x23, 0x49, 0xae}

// DBServer message types.
const (
	DBMsgSetup         uint16 = 0x0000
	DBMsgRootMenu      uint16 = 0x1000
	DBMsgGetArtists    uint16 = 0x1002
	DBMsgGetAlbums     uint16 = 0x1003
	DBMsgGetTracks     uint16 = 0x1004
	DBMsgGetBPM        uint16 = 0x1006
	DBMsgGetByArtist   uint16 = 0x1102
	DBMsgGetByAlbum    uint16 = 0x1103
	DBMsgGetByBPM      uint16 = 0x1106
	DBMsgGetMetadata   uint16 = 0x2002
	DBMsgGetArtwork    uint16 = 0x2003
	DBMsgGetWavePreview uint16 = 0x2004
	DBMsgGetCuePoints  uint16 = 0x2104
	DBMsgGetBeatGrid   uint16 = 0x2204
	DBMsgGetWaveDetail uint16 = 0x2904
	DBMsgGetWaveColor  uint16 = 0x2c04
	DBMsgRenderMenu    uint16 = 0x3000
	DBMsgSuccess       uint16 = 0x4000
	DBMsgMenuHeader    uint16 = 0x4001
	DBMsgMenuItem      uint16 = 0x4101
	DBMsgMenuFooter    uint16 = 0x4201
	DBMsgBeatGridResp  uint16 = 0x4602
)

// Special transaction ID used for setup messages.
const DBSetupTxID uint32 = 0xfffffffe

// Argument type tags.
const (
	ArgInt8   byte = 0x0f
	ArgInt16  byte = 0x10
	ArgInt32  byte = 0x11
	ArgBinary byte = 0x14
	ArgString byte = 0x26
)

// DBMessage represents a parsed dbserver message.
type DBMessage struct {
	TxID             uint32
	Type             uint16
	Args             []DBArg
	DeclaredArgCount int      // if > 0, override arg count in header (for phantom args)
	ExtraTags        []byte   // extra tag bytes to append after actual arg tags
	OverrideTags     []byte   // if set, use this exact tag array (ignores auto-generation)
}

// DBArg is a single argument in a dbserver message.
type DBArg struct {
	Tag   byte
	Int8  uint8
	Int16 uint16
	Int32 uint32
	Bytes []byte  // for binary args
	Str   string  // for string args
}

// Int returns the integer value regardless of which int size was used.
func (a DBArg) Int() uint32 {
	switch a.Tag {
	case ArgInt8:
		return uint32(a.Int8)
	case ArgInt16:
		return uint32(a.Int16)
	case ArgInt32:
		return a.Int32
	default:
		return 0
	}
}

// ParseDBMessage parses a dbserver message from bytes.
// The caller should have already stripped the leading magic bytes.
func ParseDBMessage(data []byte) (*DBMessage, error) {
	if len(data) < 11 {
		return nil, fmt.Errorf("dbmessage too short: %d bytes", len(data))
	}

	msg := &DBMessage{
		TxID: binary.BigEndian.Uint32(data[0:4]),
		Type: binary.BigEndian.Uint16(data[4:6]),
	}

	argCount := int(data[6])
	tags := data[7:19] // 12 bytes of type tags

	pos := 19
	for i := 0; i < argCount && i < 12; i++ {
		if pos >= len(data) {
			break
		}
		arg := DBArg{Tag: tags[i]}
		switch tags[i] {
		case ArgInt8:
			if pos+1 > len(data) {
				return nil, fmt.Errorf("arg %d: int8 truncated", i)
			}
			arg.Int8 = data[pos]
			pos++
		case ArgInt16:
			if pos+2 > len(data) {
				return nil, fmt.Errorf("arg %d: int16 truncated", i)
			}
			arg.Int16 = binary.BigEndian.Uint16(data[pos : pos+2])
			pos += 2
		case ArgInt32:
			if pos+4 > len(data) {
				return nil, fmt.Errorf("arg %d: int32 truncated", i)
			}
			arg.Int32 = binary.BigEndian.Uint32(data[pos : pos+4])
			pos += 4
		case ArgBinary:
			if pos+4 > len(data) {
				return nil, fmt.Errorf("arg %d: binary length truncated", i)
			}
			length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			pos += 4
			if pos+length > len(data) {
				return nil, fmt.Errorf("arg %d: binary data truncated", i)
			}
			arg.Bytes = append([]byte(nil), data[pos:pos+length]...)
			pos += length
		case ArgString:
			if pos+4 > len(data) {
				return nil, fmt.Errorf("arg %d: string length truncated", i)
			}
			charCount := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			pos += 4
			byteLen := charCount * 2 // UTF-16
			if pos+byteLen > len(data) {
				return nil, fmt.Errorf("arg %d: string data truncated", i)
			}
			arg.Str = decodeUTF16BE(data[pos : pos+byteLen])
			pos += byteLen
		default:
			// Unknown type, skip
		}
		msg.Args = append(msg.Args, arg)
	}
	return msg, nil
}

// MarshalDBMessage serializes a dbserver message.
// Uses the same field-based format the CDJ sends:
//   NumberField(4, MESSAGE_START)
//   NumberField(4, txid)
//   NumberField(2, type)
//   NumberField(1, argcount)
//   BinaryField(12, tags)
//   [arg fields...]
func MarshalDBMessage(msg *DBMessage) []byte {
	var buf []byte

	buf = appendNumber4(buf, 0x872349ae)
	buf = appendNumber4(buf, msg.TxID)
	buf = appendNumber2(buf, msg.Type)

	argCount := len(msg.Args)
	if msg.DeclaredArgCount > 0 {
		argCount = msg.DeclaredArgCount
	}
	buf = appendNumber1(buf, byte(argCount))

	// Tags array: 0x06=int32, 0x05=int16, 0x04=int8, 0x02=string, 0x03=binary
	var tags []byte
	if msg.OverrideTags != nil {
		// Use explicit tag array as-is.
		tags = msg.OverrideTags
	} else {
		tagLen := argCount
		tags = make([]byte, tagLen)
		for i, arg := range msg.Args {
			if i < len(msg.Args) && i < tagLen {
				switch arg.Tag {
				case ArgInt32:
					tags[i] = 0x06
				case ArgInt16:
					tags[i] = 0x05
				case ArgInt8:
					tags[i] = 0x04
				case ArgString, ArgStringWrapped:
					tags[i] = 0x02
				case ArgBinary:
					tags[i] = 0x03
				default:
					tags[i] = arg.Tag
				}
			}
		}
		// Fill remaining tag slots from ExtraTags (for phantom args).
		for i, t := range msg.ExtraTags {
			pos := len(msg.Args) + i
			if pos < tagLen {
				tags[pos] = t
			}
		}
	}
	buf = appendBinary(buf, tags)

	for _, arg := range msg.Args {
		switch arg.Tag {
		case ArgInt8:
			buf = appendNumber1(buf, arg.Int8)
		case ArgInt16:
			buf = appendNumber2(buf, arg.Int16)
		case ArgInt32:
			buf = appendNumber4(buf, arg.Int32)
		case ArgBinary:
			buf = appendBinary(buf, arg.Bytes)
		case ArgString:
			buf = appendString(buf, arg.Str)
		case ArgStringWrapped:
			buf = appendStringWrapped(buf, arg.Str)
		}
	}

	return buf
}

// MarshalDBMessageRaw serializes WITHOUT inline type tags (raw format).
// Layout: [magic 4] [txid 4] [type 2] [argcount 1] [tags 12] [raw args]
func MarshalDBMessageRaw(msg *DBMessage) []byte {
	size := 4 + 4 + 2 + 1 + 12
	for _, arg := range msg.Args {
		switch arg.Tag {
		case ArgInt8:
			size++
		case ArgInt16:
			size += 2
		case ArgInt32:
			size += 4
		case ArgBinary:
			size += 4 + len(arg.Bytes)
		case ArgString:
			size += 4 + (len(utf16.Encode([]rune(arg.Str)))+1)*2
		case ArgStringWrapped:
			extra := 0
			if len(arg.Str) > 0 {
				extra = 2 // fffa + fffb
			}
			size += 4 + (len(utf16.Encode([]rune(arg.Str)))+1+extra)*2
		}
	}

	buf := make([]byte, size)
	copy(buf[0:4], DBMagic[:])
	binary.BigEndian.PutUint32(buf[4:8], msg.TxID)
	binary.BigEndian.PutUint16(buf[8:10], msg.Type)
	buf[10] = byte(len(msg.Args))

	for i, arg := range msg.Args {
		if i < 12 {
			switch arg.Tag {
			case ArgInt32:
				buf[11+i] = 0x06
			case ArgInt16:
				buf[11+i] = 0x05
			case ArgInt8:
				buf[11+i] = 0x04
			case ArgString:
				buf[11+i] = 0x02
			case ArgBinary:
				buf[11+i] = 0x03
			}
		}
	}

	pos := 23
	for _, arg := range msg.Args {
		switch arg.Tag {
		case ArgInt8:
			buf[pos] = arg.Int8
			pos++
		case ArgInt16:
			binary.BigEndian.PutUint16(buf[pos:], arg.Int16)
			pos += 2
		case ArgInt32:
			binary.BigEndian.PutUint32(buf[pos:], arg.Int32)
			pos += 4
		case ArgBinary:
			binary.BigEndian.PutUint32(buf[pos:], uint32(len(arg.Bytes)))
			pos += 4
			copy(buf[pos:], arg.Bytes)
			pos += len(arg.Bytes)
		case ArgString:
			encoded := encodeUTF16BE(arg.Str)
			binary.BigEndian.PutUint32(buf[pos:], uint32(len(encoded)/2))
			pos += 4
			copy(buf[pos:], encoded)
			pos += len(encoded)
		}
	}
	return buf[:pos]
}

func appendNumber1(buf []byte, v byte) []byte {
	return append(buf, 0x0f, v)
}

func appendNumber2(buf []byte, v uint16) []byte {
	b := make([]byte, 3)
	b[0] = 0x10
	binary.BigEndian.PutUint16(b[1:], v)
	return append(buf, b...)
}

func appendNumber4(buf []byte, v uint32) []byte {
	b := make([]byte, 5)
	b[0] = 0x11
	binary.BigEndian.PutUint32(b[1:], v)
	return append(buf, b...)
}

func appendBinary(buf []byte, data []byte) []byte {
	b := make([]byte, 5+len(data))
	b[0] = 0x14
	binary.BigEndian.PutUint32(b[1:5], uint32(len(data)))
	copy(b[5:], data)
	return append(buf, b...)
}

func appendString(buf []byte, s string) []byte {
	encoded := encodeUTF16BE(s)
	b := make([]byte, 5+len(encoded))
	b[0] = 0x26
	binary.BigEndian.PutUint32(b[1:5], uint32(len(encoded)/2))
	copy(b[5:], encoded)
	return append(buf, b...)
}

func appendStringWrapped(buf []byte, s string) []byte {
	encoded := encodeUTF16BEWrapped(s)
	b := make([]byte, 5+len(encoded))
	b[0] = 0x26
	binary.BigEndian.PutUint32(b[1:5], uint32(len(encoded)/2))
	copy(b[5:], encoded)
	return append(buf, b...)
}

// Helper to build common argument types.

func ArgI32(v uint32) DBArg {
	return DBArg{Tag: ArgInt32, Int32: v}
}

func ArgI16(v uint16) DBArg {
	return DBArg{Tag: ArgInt16, Int16: v}
}

func ArgI8(v uint8) DBArg {
	return DBArg{Tag: ArgInt8, Int8: v}
}

func ArgStr(s string) DBArg {
	return DBArg{Tag: ArgString, Str: s}
}

// ArgStrWrapped creates a string arg with 0xfffa/0xfffb markers (for category labels).
const ArgStringWrapped byte = 0x27 // internal tag, encoded as 0x26 on wire

func ArgStrW(s string) DBArg {
	return DBArg{Tag: ArgStringWrapped, Str: s}
}

func ArgBlob(data []byte) DBArg {
	return DBArg{Tag: ArgBinary, Bytes: data}
}

// DecodeUTF16BE is the exported form of decodeUTF16BE so callers
// outside this package (e.g. dbserver's search handler, which receives
// inline-tagged string args as raw UTF-16 bytes) can decode them
// without re-implementing the loop.
func DecodeUTF16BE(data []byte) string { return decodeUTF16BE(data) }

func decodeUTF16BE(data []byte) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	u16s := make([]uint16, len(data)/2)
	for i := range u16s {
		u16s[i] = binary.BigEndian.Uint16(data[i*2:])
	}
	// Trim trailing NUL.
	for len(u16s) > 0 && u16s[len(u16s)-1] == 0 {
		u16s = u16s[:len(u16s)-1]
	}
	return string(utf16.Decode(u16s))
}

func encodeUTF16BE(s string) []byte {
	runes := []rune(s)
	var u16s []uint16
	u16s = append(u16s, utf16.Encode(runes)...)
	// NUL terminator.
	u16s = append(u16s, 0)
	buf := make([]byte, len(u16s)*2)
	for i, v := range u16s {
		binary.BigEndian.PutUint16(buf[i*2:], v)
	}
	return buf
}

func encodeUTF16BEWrapped(s string) []byte {
	runes := []rune(s)
	var u16s []uint16
	if len(runes) > 0 {
		u16s = append(u16s, 0xfffa)
		u16s = append(u16s, utf16.Encode(runes)...)
		u16s = append(u16s, 0xfffb)
	}
	u16s = append(u16s, 0)
	buf := make([]byte, len(u16s)*2)
	for i, v := range u16s {
		binary.BigEndian.PutUint16(buf[i*2:], v)
	}
	return buf
}

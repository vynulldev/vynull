// SPDX-License-Identifier: GPL-3.0-or-later

// Package tagtest writes small tagged audio files for tests: an ID3v2.3 tag
// and nothing else, which is enough for the tag reader.
package tagtest

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"
)

// WriteMP3 writes an .mp3 at path holding only an ID3v2.3 tag with the given
// frames, keyed by frame ID: text frames such as TIT2 (title), TPE1 (artist),
// TALB, TCON, TYER, TRCK, TPOS, TPUB (label), TPE4 (remixer), TOPE and TSST,
// and COMM for the comment.
func WriteMP3(t testing.TB, path string, frames map[string]string) {
	t.Helper()
	ids := make([]string, 0, len(frames))
	for id := range frames {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var body []byte
	for _, id := range ids {
		data := []byte{0} // ISO-8859-1 text
		if id == "COMM" {
			data = append(data, "eng"...) // language, then an empty description
			data = append(data, 0)
		}
		data = append(data, frames[id]...)
		frame := make([]byte, 10, 10+len(data))
		copy(frame, id)
		binary.BigEndian.PutUint32(frame[4:], uint32(len(data)))
		body = append(body, append(frame, data...)...)
	}
	n := len(body) // the tag size is syncsafe: 7 bits per byte
	header := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	if err := os.WriteFile(path, append(header, body...), 0o644); err != nil {
		t.Fatal(err)
	}
}

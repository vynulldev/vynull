// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"fmt"
	"log"
	"reflect"
	"testing"
)

func TestLogRingBasic(t *testing.T) {
	r := NewLogRing(3)
	if got := r.Lines(); len(got) != 0 {
		t.Fatalf("empty ring returned %v", got)
	}
	r.Write([]byte("a\nb\n"))
	if got := r.Lines(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %v", got)
	}
	// Overflow: oldest lines drop, order stays oldest-first.
	r.Write([]byte("c\nd\n"))
	if got := r.Lines(); !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Fatalf("after wrap got %v", got)
	}
}

func TestLogRingPartialWrites(t *testing.T) {
	r := NewLogRing(8)
	// A line split across writes must land as one line once terminated.
	r.Write([]byte("hel"))
	r.Write([]byte("lo wor"))
	if got := r.Lines(); len(got) != 0 {
		t.Fatalf("unterminated line surfaced early: %v", got)
	}
	r.Write([]byte("ld\nnext\n"))
	if got := r.Lines(); !reflect.DeepEqual(got, []string{"hello world", "next"}) {
		t.Fatalf("got %v", got)
	}
}

func TestLogRingAsStdlibLogSink(t *testing.T) {
	r := NewLogRing(16)
	l := log.New(r, "", log.LstdFlags)
	for i := 0; i < 3; i++ {
		l.Printf("line %d", i)
	}
	got := r.Lines()
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %v", got)
	}
	for i, line := range got {
		want := fmt.Sprintf("line %d", i)
		if len(line) < len(want) || line[len(line)-len(want):] != want {
			t.Errorf("line %d = %q, want suffix %q", i, line, want)
		}
	}
}

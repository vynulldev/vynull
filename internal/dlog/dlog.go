// SPDX-License-Identifier: GPL-3.0-or-later

// Package dlog adds simple level-gated logging on top of the standard
// log package. The standard library's log has no built-in level filter,
// so historically we had to choose between always-on log.Printf calls
// (spammy) or removing them entirely (lost diagnostic value).
//
// Usage:
//
//	dlog.SetLevel(dlog.Debug)        // typically once, from main
//	dlog.Infof("server listening on %s", addr)
//	if dlog.Enabled(dlog.Trace) {    // guard expensive formatting
//	    dlog.Tracef("packet:\n%s", hex.Dump(buf))
//	}
//
// Output goes through the stdlib log package, so existing log.SetOutput
// (e.g. to redirect everything to a file) continues to work unchanged.
package dlog

import (
	"log"
	"strings"
	"sync/atomic"
)

// Level orders log severities from least to most verbose. Calls below
// the current level are suppressed.
type Level int32

const (
	Error Level = iota
	Warn
	Info
	Debug
	Trace
)

// current is atomic so SetLevel can be called from any goroutine and
// the guard checks in hot paths don't need locking.
var current atomic.Int32

func init() { current.Store(int32(Info)) }

// SetLevel changes the active threshold. Goroutine-safe.
func SetLevel(l Level) { current.Store(int32(l)) }

// GetLevel returns the current threshold.
func GetLevel() Level { return Level(current.Load()) }

// Enabled reports whether messages at level l will be emitted. Use it
// to skip expensive argument formatting (hex.Dump, JSON encoding, etc.)
// when the corresponding level is disabled.
func Enabled(l Level) bool { return Level(current.Load()) >= l }

// Parse turns a CLI string into a Level. Returns the matched level and
// true, or Info and false on an unknown value.
func Parse(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return Error, true
	case "warn", "warning":
		return Warn, true
	case "info":
		return Info, true
	case "debug":
		return Debug, true
	case "trace":
		return Trace, true
	}
	return Info, false
}

// String returns the canonical name of a level.
func (l Level) String() string {
	switch l {
	case Error:
		return "error"
	case Warn:
		return "warn"
	case Info:
		return "info"
	case Debug:
		return "debug"
	case Trace:
		return "trace"
	}
	return "info"
}

// Errorf logs at Error level. Always shown unless level is below Error
// (which isn't currently possible — Error is the minimum).
func Errorf(format string, args ...any) {
	if Level(current.Load()) >= Error {
		log.Printf("ERROR "+format, args...)
	}
}

// Warnf logs at Warn level.
func Warnf(format string, args ...any) {
	if Level(current.Load()) >= Warn {
		log.Printf("WARN "+format, args...)
	}
}

// Infof logs at Info level. No prefix — Info is the default level and
// represents "normal operational output", matching how existing
// log.Printf calls already read.
func Infof(format string, args ...any) {
	if Level(current.Load()) >= Info {
		log.Printf(format, args...)
	}
}

// Debugf logs at Debug level. Off by default.
func Debugf(format string, args ...any) {
	if Level(current.Load()) >= Debug {
		log.Printf("DEBUG "+format, args...)
	}
}

// Tracef logs at Trace level. Used for per-packet dumps and other
// extremely-verbose output. Off by default.
func Tracef(format string, args ...any) {
	if Level(current.Load()) >= Trace {
		log.Printf("TRACE "+format, args...)
	}
}

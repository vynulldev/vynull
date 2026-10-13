// SPDX-License-Identifier: GPL-3.0-or-later

// Package buildinfo reports which build of Vynull is running. Release builds
// set Version, Commit, and Date with -ldflags -X; any left empty fall back to
// what the Go toolchain stamps into every binary built from a git checkout
// (the module version derived from the nearest tag, the commit, its time, and
// whether the working tree had uncommitted changes).
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Set at link time, e.g.
//
//	go build -ldflags "-X github.com/vynulldev/vynull/internal/buildinfo.Version=v0.6.1"
var (
	Version string
	Commit  string
	Date    string
)

// Info describes the running build.
type Info struct {
	Version   string `json:"version"`              // e.g. v0.6.1, or a pseudo-version between tags; "dev" when unknown
	Commit    string `json:"commit,omitempty"`     // full git commit hash
	Date      string `json:"date,omitempty"`       // commit time, RFC 3339
	Dirty     bool   `json:"dirty,omitempty"`      // built from a working tree with uncommitted changes
	GoVersion string `json:"go_version,omitempty"` // toolchain that built the binary
}

var (
	once sync.Once
	info Info
)

// Get returns the running build's info.
func Get() Info {
	once.Do(func() { info = read(debug.ReadBuildInfo) })
	return info
}

func read(readBuild func() (*debug.BuildInfo, bool)) Info {
	i := Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version()}
	if bi, ok := readBuild(); ok {
		if i.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			i.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			case "vcs.modified":
				i.Dirty = s.Value == "true"
			}
		}
	}
	// The toolchain marks a dirty tree in the pseudo-version too; report it
	// once, through Dirty.
	i.Version = strings.TrimSuffix(i.Version, "+dirty")
	if i.Version == "" {
		i.Version = "dev"
	}
	return i
}

// ShortCommit returns the first 7 characters of the commit hash, or "".
func (i Info) ShortCommit() string {
	if len(i.Commit) > 7 {
		return i.Commit[:7]
	}
	return i.Commit
}

// String is a one-line description, e.g. "v0.6.1 (b690653)" or
// "v0.6.2-0.20261012001650-b690653b5c1e (b690653, modified)".
func (i Info) String() string {
	s := i.Version
	var extra []string
	if c := i.ShortCommit(); c != "" {
		extra = append(extra, c)
	}
	if i.Dirty {
		extra = append(extra, "modified")
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

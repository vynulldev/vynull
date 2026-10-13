// SPDX-License-Identifier: GPL-3.0-or-later

package buildinfo

import (
	"runtime/debug"
	"testing"
)

func stamped(version string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: version}, Settings: settings}, true
	}
}

func TestReadFromToolchainStamp(t *testing.T) {
	i := read(stamped("v0.6.2-0.20261012001650-b690653b5c1e+dirty",
		debug.BuildSetting{Key: "vcs.revision", Value: "b690653b5c1e0000000000000000000000000000"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-10-13T00:16:50Z"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	))
	if i.Version != "v0.6.2-0.20261012001650-b690653b5c1e" || !i.Dirty || i.Date != "2026-10-13T00:16:50Z" {
		t.Fatalf("got %+v", i)
	}
	if got := i.String(); got != "v0.6.2-0.20261012001650-b690653b5c1e (b690653, modified)" {
		t.Errorf("String() = %q", got)
	}
}

func TestLinkerValuesWin(t *testing.T) {
	Version, Commit = "v0.6.1", "abcdef0123456789"
	defer func() { Version, Commit = "", "" }()
	i := read(stamped("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "ffffffff"}))
	if i.Version != "v0.6.1" || i.Commit != "abcdef0123456789" {
		t.Fatalf("got %+v", i)
	}
	if got := i.String(); got != "v0.6.1 (abcdef0)" {
		t.Errorf("String() = %q", got)
	}
}

func TestUnknownBuild(t *testing.T) {
	i := read(func() (*debug.BuildInfo, bool) { return nil, false })
	if i.Version != "dev" || i.String() != "dev" {
		t.Fatalf("got %+v / %q", i, i.String())
	}
	if i := read(stamped("(devel)")); i.Version != "dev" {
		t.Errorf("(devel) should read as dev, got %q", i.Version)
	}
}

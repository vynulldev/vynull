// SPDX-License-Identifier: GPL-3.0-or-later
package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// helperDepsInVenv verifies the python bootstrap resolves a venv python that
// can import sqlcipher3, creating one under t.TempDir() when none exists yet.
func TestHelperDepsInVenv(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	venv := filepath.Join(t.TempDir(), "venv")
	if err := ensureHelperVenv(venv); err != nil {
		t.Fatalf("ensureHelperVenv: %v", err)
	}
	out, err := exec.Command(filepath.Join(venv, "bin", "python3"),
		"-c", "import sqlcipher3; print(sqlcipher3.__file__)").CombinedOutput()
	if err != nil {
		t.Fatalf("venv python cannot import sqlcipher3: %v (%s)", err, out)
	}
}

// helperReusesExistingVenv: a second call on an already-provisioned venv must
// be a cheap no-op (no reinstall) and still yield a working interpreter.
func TestHelperReusesExistingVenv(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	venv := filepath.Join(t.TempDir(), "venv")
	if err := ensureHelperVenv(venv); err != nil {
		t.Fatalf("first ensureHelperVenv: %v", err)
	}
	marker := filepath.Join(venv, ".deps-ok")
	st1, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("marker missing after provision: %v", err)
	}
	if err := ensureHelperVenv(venv); err != nil {
		t.Fatalf("second ensureHelperVenv: %v", err)
	}
	st2, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("marker missing after reuse: %v", err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("second call re-provisioned the venv (marker mtime changed)")
	}
}

// helperVenvOverrideEnv: VYNULL_PYTHON short-circuits venv creation entirely.
func TestHelperVenvOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakepython")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VYNULL_PYTHON", fake)
	got, err := helperPython(filepath.Join(dir, "venv"))
	if err != nil {
		t.Fatalf("helperPython: %v", err)
	}
	if got != fake {
		t.Errorf("helperPython = %q, want override %q", got, fake)
	}
	// and no venv should have been created next to it
	if _, err := os.Stat(filepath.Join(dir, "venv")); err == nil {
		t.Errorf("venv created despite VYNULL_PYTHON override")
	}
}

// helperSkipsInstallWhenDepsPresent: a system python that already imports
// sqlcipher3 is used as-is (no venv built).
func TestHelperSkipsInstallWhenDepsPresent(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if err := exec.Command("python3", "-c", "import sqlcipher3").Run(); err != nil {
		t.Skip("system python3 lacks sqlcipher3; bootstrap path covered by TestHelperDepsInVenv")
	}
	py, err := helperPython(t.TempDir())
	if err != nil {
		t.Fatalf("helperPython: %v", err)
	}
	if !strings.HasSuffix(py, "python3") {
		t.Errorf("helperPython = %q, want a python3 path", py)
	}
}

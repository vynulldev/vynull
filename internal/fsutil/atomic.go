// SPDX-License-Identifier: GPL-3.0-or-later

// Package fsutil provides small filesystem helpers.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path atomically: it writes to a temp file in the
// same directory, fsyncs it, and renames it over the destination. Unlike
// os.WriteFile, a crash (power loss, OOM, etc.) mid-write can never leave a
// truncated or half-written destination — the rename either fully replaces
// the old file or doesn't happen at all. Use for any file whose corruption
// would lose data (the library, tag/playlist/cue stores, etc.).
//
// The temp file is created in the destination's directory so the rename
// stays on one filesystem (rename is only atomic within a filesystem). A
// crash before the rename may leave a stray ".<name>.tmp-*" file — harmless,
// and it never corrupts the real destination.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we return before a successful rename. After a
	// successful rename tmpName no longer exists, so this is a no-op.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

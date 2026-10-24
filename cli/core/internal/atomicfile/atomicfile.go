// Package atomicfile writes files atomically: data is written to a temporary
// file in the destination directory, fsynced, chmodded and renamed over the
// target path, so readers never observe a partially written file and a crash
// can never corrupt the destination.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write atomically writes data to path with the given permissions.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(fmt.Errorf("write %s: %w", name, err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync %s: %w", name, err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("close %s: %w", name, err))
	}
	if err := os.Chmod(name, perm); err != nil {
		return fail(fmt.Errorf("chmod %s: %w", name, err))
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

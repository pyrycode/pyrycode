package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicReplace overwrites targetPath with newData using the standard POSIX
// write-temp + fsync + rename dance, so an interruption mid-write leaves
// targetPath pointing at either the old contents or the new contents — never
// a truncated file.
//
// The new file is created with the supplied mode (applied via an explicit
// Chmod since os.CreateTemp opens at 0o600). targetPath does not need to
// exist beforehand; if it does, it is replaced in place.
//
// SAME-FILESYSTEM CAVEAT: rename(2) is only atomic when source and
// destination are on the same filesystem. AtomicReplace creates its temp
// file in filepath.Dir(targetPath) precisely so the eventual rename never
// crosses a mount point. Callers MUST therefore pass a path whose parent
// directory already exists and is writable; AtomicReplace does not MkdirAll
// the parent.
//
// On any error path before the successful rename, the temp file is removed
// so no stragglers accumulate in the install directory.
func AtomicReplace(targetPath string, newData []byte, mode os.FileMode) error {
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)

	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomic replace: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()

	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(newData); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic replace: write temp: %w", err)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic replace: chmod temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic replace: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("atomic replace: close temp: %w", err)
	}
	if err := os.Rename(tmp, targetPath); err != nil {
		return fmt.Errorf("atomic replace: rename %s -> %s: %w", tmp, targetPath, err)
	}
	renamed = true
	return nil
}

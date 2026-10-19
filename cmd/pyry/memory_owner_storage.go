package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func memoryOwnerStorage(ctx context.Context, home string, e effectiveMemory) (string, *os.File, error) {
	canonical, err := filepath.EvalSymlinks(e.VaultPath)
	if err != nil || canonical != e.VaultPath || !filepath.IsAbs(canonical) {
		return "", nil, errMemoryDestination
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil || realHome != home {
		return "", nil, errMemoryDestination
	}
	sum := sha256.Sum256([]byte(canonical))
	store := filepath.Join(home, ".pyry", "memory", "stores", hex.EncodeToString(sum[:]))
	roots := append([]string{canonical}, e.SearchRoots...)
	roots = append(roots, e.AdditionalRoots...)
	for _, root := range roots {
		if memoryOverlaps(filepath.Join(home, ".pyry", "memory", "stores"), root) {
			return "", nil, errMemoryDestination
		}
	}
	dir, err := memoryOpen(unix.AT_FDCWD, home, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return "", nil, errMemoryDestination
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil || !ownedByEUID(info) || info.Mode().Perm()&0022 != 0 {
		return "", nil, errMemoryDestination
	}
	for _, name := range []string{".pyry", "memory", "stores", hex.EncodeToString(sum[:])} {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if err := unix.Mkdirat(int(dir.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return "", nil, errMemoryDestination
		}
		next, err := memoryOpen(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return "", nil, errMemoryDestination
		}
		if !memoryProtected(next, true) {
			_ = next.Close()
			return "", nil, errMemoryDestination
		}
		_ = dir.Close()
		dir = next
	}
	lock, err := memoryOpen(int(dir.Fd()), "owner.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return "", nil, errMemoryDestination
	}
	var stat unix.Stat_t
	if !memoryProtected(lock, false) || unix.Fstat(int(lock.Fd()), &stat) != nil || stat.Nlink != 1 {
		_ = lock.Close()
		return "", nil, errMemoryDestination
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return "", nil, errMemoryOccupied
		}
		return "", nil, errMemoryDestination
	}
	// Inspect private mutable state only, never user source folders. Python repeats
	// validation at its use boundary after inheriting the exclusive descriptor.
	err = filepath.WalkDir(store, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return errMemoryDestination
		}
		info, err := d.Info()
		if err != nil || !memoryProtectedInfo(info, d.IsDir()) {
			return errMemoryDestination
		}
		if !d.IsDir() {
			var s unix.Stat_t
			if unix.Lstat(path, &s) != nil || s.Nlink != 1 {
				return errMemoryDestination
			}
		}
		return nil
	})
	if err != nil {
		_ = lock.Close()
		return "", nil, err
	}
	return store, lock, nil
}

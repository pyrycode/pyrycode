package main

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errMemoryTranscript = errors.New("memory transcript unavailable")

// The held directory descriptor confines every leaf operation, including rename.
// Traversal refuses symlinks; newly created directories are private.
func memoryTranscriptDir(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errMemoryTranscript
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errMemoryTranscript
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) {
			e = unix.Mkdirat(fd, part, 0700)
			if e == nil || errors.Is(e, unix.EEXIST) {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		_ = unix.Close(fd)
		if e != nil {
			return -1, errMemoryTranscript
		}
		fd = next
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = unix.Close(fd)
		return -1, errMemoryTranscript
	}
	return fd, nil
}
func publishMemoryTranscript(ctx context.Context, dir, name, body string, beforeRename func()) error {
	if len(name) != 67 || !strings.HasSuffix(name, ".md") || strings.Trim(name[:64], "0123456789abcdef") != "" {
		return errMemoryTranscript
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := memoryTranscriptDir(dir)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	safe := func() bool {
		var stat unix.Stat_t
		err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		return errors.Is(err, unix.ENOENT) || err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0077 == 0 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
	}
	if !safe() {
		return errMemoryTranscript
	}
	temp := ".transcript-" + rand.Text()
	tmp, err := unix.Openat(fd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errMemoryTranscript
	}
	defer unix.Unlinkat(fd, temp, 0)
	f := os.NewFile(uintptr(tmp), temp)
	_, err = f.WriteString(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errMemoryTranscript
	}
	if beforeRename != nil {
		beforeRename()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !safe() {
		return errMemoryTranscript
	}
	if unix.Renameat(fd, temp, fd, name) != nil {
		return errMemoryTranscript
	}
	return nil
}

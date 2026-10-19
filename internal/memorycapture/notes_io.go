package memorycapture

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

func noteDirectory(ctx context.Context, absolute string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrNoteAccess
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			unix.Close(fd)
			return nil, err
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, ErrNoteAccess
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "capture directory"), nil
}
func noteParent(ctx context.Context, r noteRoot, p string) (*os.File, error) {
	// Traversal from / rejects substitutions in canonical root ancestors too.
	f, err := noteDirectory(ctx, r.name)
	if err != nil {
		return nil, err
	}
	a, e := f.Stat()
	b, e2 := r.file.Stat()
	if e != nil || e2 != nil || !os.SameFile(a, b) {
		f.Close()
		return nil, ErrNoteAccess
	}
	for _, part := range strings.Split(path.Dir(p), "/") {
		if part == "." {
			continue
		}
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		fd, e := unix.Openat(int(f.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		f.Close()
		if e != nil {
			return nil, ErrNoteAccess
		}
		f = os.NewFile(uintptr(fd), "capture parent")
	}
	return f, nil
}
func noteFile(dir *os.File, name string, flags int, write bool) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "capture note")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFREG || write && st.Nlink != 1 {
		f.Close()
		return nil, ErrNoteAccess
	}
	return f, nil
}
func noteFence(ctx context.Context, r noteRoot, p string, dir, leaf *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := noteParent(ctx, r, p)
	if err != nil {
		return err
	}
	defer current.Close()
	a, e := current.Stat()
	b, e2 := dir.Stat()
	if e != nil || e2 != nil || !os.SameFile(a, b) {
		return ErrNoteAccess
	}
	if leaf != nil {
		f, e := noteFile(dir, path.Base(p), unix.O_RDONLY, false)
		if e != nil {
			return ErrNoteAccess
		}
		defer f.Close()
		a, e = f.Stat()
		b, e2 = leaf.Stat()
		if e != nil || e2 != nil || !os.SameFile(a, b) {
			return ErrNoteAccess
		}
	}
	return nil
}
func (n *Notes) readBytes(ctx context.Context, f *os.File) ([]byte, error) {
	var data []byte
	buf := make([]byte, 32<<10)
	for {
		n.checkpoint("read")
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := f.Read(buf)
		data = append(data, buf[:count]...)
		if len(data) > maxNoteBytes {
			return nil, ErrNoteLimit
		}
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			return nil, ErrNoteAccess
		}
	}
}

// Write creates or replaces a vault-relative Markdown note. Parents must exist.
// An empty changed path means identical bytes; errors never report a changed path.
func (n *Notes) Write(ctx context.Context, p string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > maxNoteBytes {
		return "", ErrNoteLimit
	}
	if err := notePath(p); err != nil {
		return "", err
	}
	if noteProtected(p) {
		return "", ErrNoteAccess
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	if len(n.roots) == 0 {
		return "", ErrNoteAccess
	}
	r := n.roots[0]
	if noteProtected(r.name + "/" + p) {
		return "", ErrNoteAccess
	}
	dir, err := noteParent(ctx, r, p)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	old, err := noteFile(dir, path.Base(p), unix.O_RDONLY, true)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return "", ErrNoteAccess
	}
	mode := os.FileMode(0644)
	if old != nil {
		defer old.Close()
		info, e := old.Stat()
		if e != nil {
			return "", ErrNoteAccess
		}
		mode = info.Mode().Perm()
		before, e := n.readBytes(ctx, old)
		if e != nil {
			return "", e
		}
		if bytes.Equal(before, data) {
			n.checkpoint("access")
			if e := noteFence(ctx, r, p, dir, old); e != nil {
				return "", e
			}
			var st unix.Stat_t
			if unix.Fstat(int(old.Fd()), &st) != nil || st.Nlink != 1 {
				return "", ErrNoteAccess
			}
			return "", nil
		}
	}
	temp := ".capture-" + rand.Text()
	f, err := noteFile(dir, temp, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, true)
	if err != nil {
		return "", ErrNoteAccess
	}
	defer func() { _ = f.Close() }()            // Keep the retained inode alive through publication.
	defer unix.Unlinkat(int(dir.Fd()), temp, 0) // Best effort; publication may consume it.
	for offset := 0; offset < len(data); {
		n.checkpoint("write")
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := min(offset+32<<10, len(data))
		count, e := f.Write(data[offset:end])
		if e != nil || count == 0 {
			return "", ErrNoteAccess
		}
		offset += count
	}
	if f.Chmod(mode) != nil || f.Sync() != nil {
		return "", ErrNoteAccess
	}
	// Surface the writer's close error while a duplicate prevents inode reuse.
	duplicate, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return "", ErrNoteAccess
	}
	guard := os.NewFile(uintptr(duplicate), "capture retained note")
	closeErr := f.Close()
	f = guard
	if closeErr != nil {
		return "", ErrNoteAccess
	}
	n.checkpoint("access")
	if err := noteFence(ctx, r, p, dir, old); err != nil {
		return "", err
	}
	// Check the temporary inode and target again with write-specific link policy.
	for _, check := range []struct {
		name string
		held *os.File
	}{{temp, f}, {path.Base(p), old}} {
		now, e := noteFile(dir, check.name, unix.O_RDONLY, true)
		if check.held == nil && errors.Is(e, unix.ENOENT) {
			continue
		}
		if e != nil {
			return "", ErrNoteAccess
		}
		a, e := now.Stat()
		b, e2 := f.Stat()
		if check.held != nil {
			b, e2 = check.held.Stat()
		}
		now.Close()
		if check.held == nil || e != nil || e2 != nil || !os.SameFile(a, b) {
			return "", ErrNoteAccess
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if old == nil {
		// Link publication cannot overwrite a concurrently created destination.
		err = unix.Linkat(int(dir.Fd()), temp, int(dir.Fd()), path.Base(p), 0)
		if err == nil {
			err = unix.Unlinkat(int(dir.Fd()), temp, 0)
		}
	} else {
		err = unix.Renameat(int(dir.Fd()), temp, int(dir.Fd()), path.Base(p))
	}
	if err != nil || dir.Sync() != nil {
		return "", ErrNoteAccess
	}
	if err := noteFence(ctx, r, p, dir, f); err != nil {
		return "", err
	}
	return p, nil
}

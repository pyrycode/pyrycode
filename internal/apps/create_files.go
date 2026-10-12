package apps

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// childDir pins a directory without following even an in-root symlink.
func childDir(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func writeSource(ctx context.Context, root, id string, files []sourceFile) error {
	parent, err := privateDir(filepath.Dir(root))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = unix.Mkdirat(int(parent.Fd()), filepath.Base(root), 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	owned, err := childDir(parent, filepath.Base(root))
	if err != nil {
		return err
	}
	defer owned.Close()
	if err = unix.Mkdirat(int(owned.Fd()), id, 0700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrIdentityUnavailable
		}
		return err
	}
	app, err := childDir(owned, id)
	if err != nil {
		return err
	}
	defer app.Close()
	if err = unix.Mkdirat(int(app.Fd()), "source", 0700); err != nil {
		return err
	}
	source, err := childDir(app, "source")
	if err != nil {
		return err
	}
	defer source.Close()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeSourceFile(source, file); err != nil {
			return err
		}
	}
	return nil
}

func writeSourceFile(source *os.File, file sourceFile) error {
	dir, err := childDir(source, ".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	parts := strings.Split(file.name, "/")
	for _, part := range parts[:len(parts)-1] {
		if err = unix.Mkdirat(int(dir.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := childDir(dir, part)
		if err != nil {
			return err
		}
		_ = dir.Close()
		dir = next
	}
	fd, err := unix.Openat(int(dir.Fd()), parts[len(parts)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), file.name)
	_, err = f.Write(file.data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readSourceManifest(root, id string) ([]byte, error) {
	dir, err := privateDir(filepath.Join(root, id, "source"))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := regularAt(dir, "app.json", false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 16385))
}

package memoryruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func randomName() string { return rand.Text() }
func owned(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Geteuid())
}
func openPrivateHome(home string) (*os.Root, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return nil, ErrUnsafe
	}
	for p := home; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		s, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || (s.Uid != uint32(os.Geteuid()) && s.Uid != 0) {
			return nil, ErrUnsafe
		}
		if p == home && !owned(info) {
			return nil, ErrUnsafe
		}
		if info.Mode().Perm()&0022 != 0 && !(p != home && s.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return nil, ErrUnsafe
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return os.OpenRoot(home)
}
func privateDir(root *os.Root, name string) error {
	if !fs.ValidPath(name) {
		return ErrUnsafe
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || !owned(info) || info.Mode().Perm()&0022 != 0 {
			return ErrUnsafe
		}
	}
	return nil
}
func regularFile(root *os.Root, name string, flags int) (*os.File, error) {
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrUnsafe
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !owned(info) || s.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}
func acquire(ctx context.Context, root *os.Root, name string) (*os.File, error) {
	f, err := regularFile(root, name, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func publish(root *os.Root, destination string, data []byte) error {
	if f, err := regularFile(root, destination, os.O_RDONLY); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := destination + "-" + randomName()
	f, err := regularFile(root, temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer root.Remove(temp) // Best effort: rename may already have consumed the temporary file.
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temp, destination); err != nil {
		return err
	}
	dir, err := root.Open(path.Dir(destination))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func treeSeal(ctx context.Context, home *os.Root, generation string, harden bool) (string, error) {
	info, err := home.Lstat(generation)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm()&0022 != 0 {
		return "", ErrUnsafe
	}
	root, err := home.OpenRoot(generation)
	if err != nil {
		return "", err
	}
	defer root.Close()
	h := sha256.New()
	var total int64
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return ErrUnsafe
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !owned(info) {
			return ErrUnsafe
		}
		mode := info.Mode()
		perm := mode.Perm()
		fmt.Fprintf(h, "%s\x00%d\x00", name, mode.Type())
		switch {
		case mode.IsDir():
			if mode.Perm()&0022 != 0 {
				return ErrUnsafe
			}
			if harden {
				if err := root.Chmod(name, 0700); err != nil {
					return err
				}
				perm = 0700
			}
		case mode&os.ModeSymlink != 0:
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if _, err := root.Stat(name); err != nil {
				return ErrUnsafe
			}
			fmt.Fprint(h, target)
		case mode.IsRegular():
			s := info.Sys().(*syscall.Stat_t)
			if s.Nlink != 1 {
				return ErrUnsafe
			}
			total += info.Size()
			if total > 2<<30 {
				return ErrUnsafe
			}
			f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			if harden {
				perm = os.FileMode(0600)
				if mode.Perm()&0111 != 0 {
					perm = 0700
				}
				if err := f.Chmod(perm); err != nil {
					f.Close()
					return err
				}
			}
			_, err = io.Copy(h, f)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return ErrUnsafe
		}
		fmt.Fprint(h, "\x00", perm, "\x00")
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

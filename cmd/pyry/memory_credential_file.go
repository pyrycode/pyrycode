package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type memoryCredentialSelection struct {
	Backend    string `json:"backend"`
	Reference  string `json:"reference"`
	Generation string `json:"generation"`
}
type memoryCredentialStore struct {
	home   string
	rename func(int, string, int, string) error
}

func memoryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errMemorySave
	}
	return hex.EncodeToString(b[:]), nil
}
func validMemoryID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == s
}
func memoryProtected(f *os.File, dir bool) bool {
	info, err := f.Stat()
	return err == nil && memoryProtectedInfo(info, dir)
}
func memoryProtectedInfo(info fs.FileInfo, dir bool) bool {
	mode := os.FileMode(0600)
	if dir {
		mode = os.ModeDir | 0700
	}
	return ownedByEUID(info) && info.Mode() == mode
}

func memoryOpen(dir int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "memory credential"), nil
}

// directory pins each traversed component; no metadata can choose an external path.
func (s memoryCredentialStore) directory(ctx context.Context, create bool) (*os.File, error) {
	f, err := memoryOpen(unix.AT_FDCWD, s.home, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, errMemoryStorage
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() || !ownedByEUID(info) || info.Mode().Perm()&0022 != 0 {
		_ = f.Close()
		return nil, errMemoryStorage
	}
	for _, name := range []string{".pyry", "memory", "credentials"} {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if create {
			err = unix.Mkdirat(int(f.Fd()), name, 0700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				_ = f.Close()
				return nil, errMemoryStorage
			}
		}
		next, openErr := memoryOpen(int(f.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY)
		_ = f.Close()
		if openErr != nil {
			if !create && errors.Is(openErr, unix.ENOENT) {
				return nil, nil
			}
			return nil, errMemoryStorage
		}
		if !memoryProtected(next, true) {
			_ = next.Close()
			return nil, errMemoryStorage
		}
		f = next
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != unix.EWOULDBLOCK {
			_ = f.Close()
			return nil, errMemoryStorage
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func memoryRead(ctx context.Context, dir *os.File, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := memoryOpen(int(dir.Fd()), name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close() // best-effort descriptor cleanup
	if !memoryProtected(f, false) {
		return nil, errMemoryStorage
	}
	b, err := io.ReadAll(io.LimitReader(f, maxAccountTokenBytes+1))
	if err != nil || len(b) > maxAccountTokenBytes {
		return nil, errMemorySelection
	}
	return b, ctx.Err()
}
func memorySelected(ctx context.Context, dir *os.File) (memoryCredentialSelection, string, error) {
	var sel memoryCredentialSelection
	b, err := memoryRead(ctx, dir, "openai.json")
	if errors.Is(err, unix.ENOENT) {
		return sel, "", nil
	}
	if err != nil {
		return sel, "", errMemorySelection
	}
	if json.Unmarshal(b, &sel) != nil {
		return sel, "", errMemorySelection
	}
	canonical, _ := json.Marshal(sel)
	if !bytes.Equal(b, canonical) || sel.Backend != "file" || !validMemoryID(sel.Generation) || len(sel.Reference) != 46 || sel.Reference[:14] != "memory:openai:" || !validMemoryID(sel.Reference[14:]) {
		return sel, "", errMemorySelection
	}
	b, err = memoryRead(ctx, dir, sel.Generation+".token")
	if err != nil {
		return sel, "", errMemorySelection
	}
	token, failure := parseTokenBytes(b)
	if failure != "" {
		return sel, "", errMemorySelection
	}
	return sel, token, nil
}
func (s memoryCredentialStore) selected(ctx context.Context) (memoryCredentialSelection, string, error) {
	ctx, cancel := context.WithTimeout(ctx, memoryCredentialDeadline)
	defer cancel()
	dir, err := s.directory(ctx, false)
	if err != nil {
		return memoryCredentialSelection{}, "", err
	}
	if dir == nil {
		return memoryCredentialSelection{}, "", ctx.Err()
	}
	defer dir.Close() // releases the directory lock
	sel, token, err := memorySelected(ctx, dir)
	if ctx.Err() != nil {
		return sel, "", ctx.Err()
	}
	return sel, token, err
}
func (s memoryCredentialStore) resolve(ctx context.Context, ref string) (string, error) {
	sel, token, err := s.selected(ctx)
	if err != nil {
		return "", err
	}
	if sel.Reference == "" || sel.Reference != ref {
		return "", errMemorySelection
	}
	return token, nil
}
func (s memoryCredentialStore) status(ctx context.Context) (bool, error) {
	sel, _, err := s.selected(ctx)
	return sel.Reference != "", err
}
func memoryWrite(ctx context.Context, dir *os.File, name string, b []byte) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := memoryOpen(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return errMemorySave
	}
	defer func() {
		_ = f.Close() // best-effort cleanup of this exclusive creation
		if result != nil {
			_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		}
	}()
	if !memoryProtected(f, false) {
		return errMemoryStorage
	}
	if _, err = f.Write(b); err != nil || f.Sync() != nil || f.Close() != nil {
		return errMemorySave
	}
	return ctx.Err()
}
func (s memoryCredentialStore) set(ctx context.Context, in *os.File) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, memoryCredentialDeadline)
	defer cancel()
	token, err := readMemoryInput(ctx, in)
	if err != nil {
		return "", err
	}
	dir, err := s.directory(ctx, true)
	if err != nil {
		return "", err
	}
	defer dir.Close() // releases the directory lock
	sel, _, err := memorySelected(ctx, dir)
	if err != nil {
		return "", err
	}
	old := sel.Generation
	if sel.Reference == "" {
		id, err := memoryID()
		if err != nil {
			return "", err
		}
		sel.Reference = "memory:openai:" + id
	}
	sel.Generation, err = memoryID()
	if err != nil {
		return "", err
	}
	sel.Backend = "file"
	secret := sel.Generation + ".token"
	temp := sel.Generation + ".selection"
	committed := false
	if err = memoryWrite(ctx, dir, secret, []byte(token)); err != nil {
		return "", err
	}
	defer func() {
		if !committed {
			_ = unix.Unlinkat(int(dir.Fd()), secret, 0)
		}
	}()
	b, _ := json.Marshal(sel)
	if err = memoryWrite(ctx, dir, temp, b); err != nil {
		return "", err
	}
	defer unix.Unlinkat(int(dir.Fd()), temp, 0) // best-effort cleanup of our staged selection
	if err = ctx.Err(); err != nil {
		return "", err
	}
	rename := s.rename
	if rename == nil {
		rename = unix.Renameat
	}
	if err = rename(int(dir.Fd()), temp, int(dir.Fd()), "openai.json"); err != nil {
		return "", errMemorySave
	}
	committed = true
	// Cleanup is best effort after the atomic selection commit; it cannot undo success.
	if old != "" {
		_ = unix.Unlinkat(int(dir.Fd()), old+".token", 0)
	}
	return sel.Reference, nil
}

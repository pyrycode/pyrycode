package memorycapture

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maxRecord = 64 << 20

func safeDirectory(st unix.Stat_t, private, home bool) bool {
	mode := uint32(st.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return false
	}
	if private {
		return st.Uid == uint32(os.Geteuid()) && mode&07777 == 0700
	}
	if home {
		return st.Uid == uint32(os.Geteuid()) && mode&0022 == 0
	}
	if st.Uid != uint32(os.Geteuid()) && st.Uid != 0 {
		return false
	}
	return mode&0022 == 0 || st.Uid == 0 && mode&unix.S_ISVTX != 0
}

func safeRecord(st unix.Stat_t) bool {
	mode := uint32(st.Mode)
	return mode&unix.S_IFMT == unix.S_IFREG && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && mode&07777 == 0600
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir < 0 || s.home == "" {
		return nil
	}
	err := unix.Close(s.dir)
	s.dir = -1
	return err
}

// Traversal holds each parent until its child has been opened and checked.
// State components require exact private modes; ancestor directories may be
// account/root owned, with root-owned sticky directories allowed for /tmp.
func openDirectory(home string, create bool) (int, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		return -1, ErrUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Join(home, ".pyry/memory/capture"), "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("directory root open: %w", err)
	}
	for i, part := range parts {
		operation := "directory openat"
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create && i >= len(parts)-3 {
			operation = "directory mkdirat"
			e = unix.Mkdirat(fd, part, 0700)
			if e == nil {
				operation = "directory parent sync"
				e = unix.Fsync(fd)
			}
			if e == nil || errors.Is(e, unix.EEXIST) {
				operation = "directory created openat"
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		if e == nil {
			var st unix.Stat_t
			operation = "directory fstat"
			e = unix.Fstat(next, &st)
			private := i >= len(parts)-3
			if e == nil && !safeDirectory(st, private, i == len(parts)-4) {
				operation = "directory validation"
				e = ErrUnsafe
			}
			// Also sync pre-existing entries: another creator may not yet have
			// synced its parent, or may have failed just after mkdir.
			if e == nil && create && private {
				operation = "directory parent sync"
				e = unix.Fsync(fd)
			}
		}
		_ = unix.Close(fd) // Parent no longer needed after opening the child.
		if e != nil {
			if next >= 0 {
				_ = unix.Close(next)
			}
			return -1, fmt.Errorf("%s: %w", operation, errors.Join(ErrUnsafe, e))
		}
		fd = next
	}
	return fd, nil
}

func privateFile(dir int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("file openat: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	if err != nil {
		err = fmt.Errorf("file fstat: %w", err)
	}
	if err == nil && !safeRecord(st) {
		err = ErrUnsafe
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func sameFile(dir int, name string, want os.FileInfo) error {
	f, err := privateFile(dir, name, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) && want == nil {
		return nil
	}
	if err != nil {
		return errors.Join(ErrUnsafe, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("file identity stat: %w", err)
	}
	if want == nil || !os.SameFile(info, want) {
		return ErrUnsafe
	}
	return nil
}

func (s *Store) checkDirectory() error {
	fd, err := openDirectory(s.home, false)
	if err != nil {
		return fmt.Errorf("directory check: %w", err)
	}
	defer unix.Close(fd)
	var held, current unix.Stat_t
	if err = unix.Fstat(s.dir, &held); err != nil {
		return fmt.Errorf("directory held fstat: %w", errors.Join(ErrUnsafe, err))
	}
	if err = unix.Fstat(fd, &current); err != nil {
		return fmt.Errorf("directory current fstat: %w", errors.Join(ErrUnsafe, err))
	}
	if held.Dev != current.Dev || held.Ino != current.Ino {
		return ErrUnsafe
	}
	return nil
}

func (s *Store) lock(ctx context.Context) (*os.File, error) {
	err := s.fault("lock-open")
	var f *os.File
	if err == nil {
		f, err = privateFile(s.dir, ".lock", unix.O_CREAT|unix.O_RDWR)
	}
	if err != nil {
		return nil, fmt.Errorf("lock open: %w", err)
	}
	for {
		if err = ctx.Err(); err != nil {
			err = fmt.Errorf("lock wait: %w", err)
			break
		}
		err = s.fault("lock-flock")
		if err == nil {
			err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		}
		if err == nil {
			info, e := f.Stat()
			if e == nil {
				e = sameFile(s.dir, ".lock", info)
			}
			if e == nil {
				return f, nil
			}
			err = fmt.Errorf("lock validation: %w", e)
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			err = fmt.Errorf("lock flock: %w", err)
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	_ = f.Close()
	return nil, err
}

func readRecord(dir int, name string, id Identity) (_ *record, _ []byte, _ *os.File, retErr error) {
	f, err := privateFile(dir, name, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("record open: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = f.Close()
		} // Best effort on a rejected record.
	}()
	raw, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("record read: %w", err)
	}
	if len(raw) > maxRecord || !utf8.Valid(raw) {
		return nil, nil, nil, ErrCorrupt
	}
	var r record
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF || r.Version != 1 || r.Identity != id || !validPayload(r.Batch.Payload) || r.Batch.ID != batchID(id, r.Batch.Payload) || r.Checksum != checksum(r) {
		return nil, nil, nil, ErrCorrupt
	}
	for i, p := range r.Batch.ChangedPaths {
		if !validPath(p) || i > 0 && p <= r.Batch.ChangedPaths[i-1] {
			return nil, nil, nil, ErrCorrupt
		}
	}
	// A previous rename may have succeeded while its parent sync failed. Recovery
	// must make the complete surviving record durable before authorizing execution.
	if err = f.Sync(); err != nil {
		return nil, nil, nil, fmt.Errorf("record recovery sync: %w", err)
	}
	return &r, raw, f, nil
}

func (s *Store) transaction(ctx context.Context, id Identity, change func(*record) (*record, error)) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("capture transaction: %w", retErr)
		}
	}()
	if !validIdentity(id) {
		return ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dir < 0 {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkDirectory(); err != nil {
		return err
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if e := lock.Close(); retErr == nil && e != nil {
			retErr = fmt.Errorf("lock close: %w", e)
		}
	}()
	lockInfo, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("lock stat: %w", err)
	}
	name := recordName(id)
	r, raw, held, err := readRecord(s.dir, name, id)
	if err != nil {
		return err
	}
	var info os.FileInfo
	if held != nil {
		// Keep the validated inode alive until the transaction finishes. A
		// removed and recreated path must not reuse its inode number.
		defer func() {
			if e := held.Close(); retErr == nil && e != nil {
				retErr = fmt.Errorf("record close: %w", e)
			}
		}()
		info, err = held.Stat()
		if err != nil {
			return fmt.Errorf("record stat: %w", err)
		}
	}
	next, err := change(r)
	if err != nil {
		return err
	}
	fence := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := s.checkDirectory(); e != nil {
			return e
		}
		if e := sameFile(s.dir, ".lock", lockInfo); e != nil {
			return fmt.Errorf("lock identity: %w", e)
		}
		return nil
	}
	safe := func() error {
		if e := fence(); e != nil {
			return e
		}
		if e := sameFile(s.dir, name, info); e != nil {
			return fmt.Errorf("record identity: %w", e)
		}
		if info != nil {
			_, now, current, e := readRecord(s.dir, name, id)
			if e != nil {
				return e
			}
			if current == nil {
				return ErrUnsafe
			}
			if e = current.Close(); e != nil {
				return fmt.Errorf("record recheck close: %w", e)
			}
			if !bytes.Equal(raw, now) {
				return ErrUnsafe
			}
		}
		return nil
	}
	if next != nil {
		next.Checksum = checksum(*next)
		data, e := json.Marshal(next)
		if e != nil {
			return e
		}
		if len(data) > maxRecord {
			return ErrInvalid
		}
		if e = s.publish(data, name, id, safe, fence); e != nil {
			return e
		}
	} else {
		if err = safe(); err != nil {
			return err
		}
		if err = unix.Fsync(s.dir); err != nil {
			return fmt.Errorf("directory recovery sync: %w", err)
		}
	}
	return fence()
}

func (s *Store) fault(stage string) error {
	if s.hook != nil {
		return s.hook(stage)
	}
	return nil
}

func (s *Store) publish(data []byte, name string, id Identity, safe, fence func() error) (retErr error) {
	temp := ".tmp-" + rand.Text()
	err := s.fault("temp-create")
	var f *os.File
	if err == nil {
		f, err = privateFile(s.dir, temp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	}
	if err != nil {
		return fmt.Errorf("temporary create: %w", err)
	}
	defer unix.Unlinkat(s.dir, temp, 0) // Best effort: rename may have consumed it.
	// The original descriptor must close before rename to surface close errors,
	// but a duplicate keeps its inode alive through source revalidation.
	duplicate, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("temporary duplicate: %w", err)
	}
	guard := os.NewFile(uintptr(duplicate), temp)
	defer func() {
		if e := guard.Close(); retErr == nil && e != nil {
			retErr = fmt.Errorf("temporary guard close: %w", e)
		}
	}()
	operation := "temporary stat"
	info, err := f.Stat()
	if err == nil {
		operation = "temporary write"
		err = s.fault("write")
	}
	if err == nil {
		var n int
		n, err = f.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		operation = "temporary sync"
		err = s.fault("file-sync")
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		operation = "temporary close"
		err = closeErr
	}
	if err == nil {
		err = s.fault("close")
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if err = s.fault("rename"); err != nil {
		return fmt.Errorf("record rename: %w", err)
	}
	if err = safe(); err != nil {
		return err
	}
	if err = sameFile(s.dir, temp, info); err != nil {
		return fmt.Errorf("temporary identity: %w", err)
	}
	if err = unix.Renameat(s.dir, temp, s.dir, name); err != nil {
		return fmt.Errorf("record rename: %w", err)
	}
	if err = s.fault("dir-sync"); err != nil {
		return fmt.Errorf("directory sync: %w", err)
	}
	if err = unix.Fsync(s.dir); err != nil {
		return fmt.Errorf("directory sync: %w", err)
	}
	if err = fence(); err != nil {
		return err
	}
	if err = sameFile(s.dir, name, info); err != nil {
		return fmt.Errorf("published record identity: %w", err)
	}
	_, current, held, err := readRecord(s.dir, name, id)
	if err != nil {
		return err
	}
	if held == nil {
		return ErrUnsafe
	}
	if err = held.Close(); err != nil {
		return fmt.Errorf("published record close: %w", err)
	}
	if !bytes.Equal(data, current) {
		return ErrUnsafe
	}
	return nil
}

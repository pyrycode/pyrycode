package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"
)

type backupEntry struct {
	Path   []byte
	Kind   string
	Size   int64
	SHA256 string
}
type backupEvidence struct {
	Version   int
	Reference BackupReference
	Entries   []backupEntry
}
type backupOps struct {
	hook        func(string) error
	nodeStarted func(*os.Process)
}

func (o backupOps) step(ctx context.Context, phase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.hook != nil {
		return errors.Join(o.hook(phase), ctx.Err())
	}
	return nil
}
func backupIdentity(root, server, app string, ref BackupReference) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validID(server) || !validID(app) || !validID(ref.PublishID) || ref.ServerID != server || ref.AppID != app || ref.Root != filepath.Join(root, app, "backups", ref.PublishID) {
		return ErrInvalidStorage
	}
	return nil
}
func backupDir(parent *os.File, name string, create bool) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("apps: create backup directory %q: %w", name, err)
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("apps: open backup directory %q: %w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// SnapshotData snapshots all data without running migrations or changing registry
// state. The caller must serialize operations and quiesce every app writer.
// Existing publish IDs are conflicts, including incomplete prior attempts.
func SnapshotData(ctx context.Context, root, server, app, publish, node string) (BackupReference, error) {
	return (backupOps{}).snapshot(ctx, root, server, app, publish, node)
}
func (o backupOps) snapshot(ctx context.Context, root, server, app, publish, node string) (BackupReference, error) {
	ref := BackupReference{server, app, publish, filepath.Join(root, app, "backups", publish)}
	if err := errors.Join(ctx.Err(), backupIdentity(root, server, app, ref)); err != nil {
		return BackupReference{}, err
	}
	selected, err := privateDir(filepath.Join(root, app))
	if err != nil {
		return BackupReference{}, err
	}
	defer selected.Close()
	parent, err := backupDir(selected, "backups", true)
	if err != nil {
		return BackupReference{}, err
	}
	defer parent.Close()
	if err = parent.Chmod(0700); err != nil {
		return BackupReference{}, err
	}
	if err = unix.Mkdirat(int(parent.Fd()), publish, 0700); errors.Is(err, unix.EEXIST) {
		return BackupReference{}, ErrConflict
	} else if err != nil {
		return BackupReference{}, fmt.Errorf("apps: create publish directory %q: %w", publish, err)
	}
	dest, err := backupDir(parent, publish, false)
	if err != nil {
		return BackupReference{}, err
	}
	defer dest.Close()
	source, err := backupDir(selected, "data", false)
	if err == nil {
		defer source.Close()
		target, e := backupDir(dest, "data", true)
		if e != nil {
			return BackupReference{}, e
		}
		defer target.Close()
		expected, e := o.tree(ctx, source, target, "data")
		if e != nil {
			return BackupReference{}, fmt.Errorf("apps: copy data: %w", e)
		}
		actual, e := o.tree(ctx, target, nil, "data")
		if e != nil || !reflect.DeepEqual(expected, actual) {
			return BackupReference{}, errors.Join(ErrInvalidStorage, e)
		}
		initialized, e := backupSQLiteEntries(expected, false)
		if e != nil {
			return BackupReference{}, e
		}
		if initialized {
			if e = o.sqliteSnapshot(ctx, node, dest, target); e != nil {
				return BackupReference{}, e
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackupReference{}, err
	}
	if err = o.step(ctx, "verify"); err != nil {
		return BackupReference{}, err
	}
	entries, err := o.tree(ctx, dest, nil, ".")
	if err != nil {
		return BackupReference{}, err
	}
	evidence := backupEvidence{1, ref, entries}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) >= 16<<20 {
		return BackupReference{}, errors.Join(ErrInvalidStorage, err)
	}
	pending, err := regularAt(dest, "verification.pending", true)
	if err != nil {
		return BackupReference{}, err
	}
	_, err = pending.Write(encoded)
	if err == nil {
		err = o.sync(ctx, pending)
	}
	err = errors.Join(err, pending.Close())
	if err != nil {
		return BackupReference{}, err
	}
	for _, dir := range []*os.File{dest, parent, selected} {
		if err = o.sync(ctx, dir); err != nil {
			return BackupReference{}, err
		}
	}
	if err = unix.Renameat(int(dest.Fd()), "verification.pending", int(dest.Fd()), "verification.json"); err != nil {
		return BackupReference{}, fmt.Errorf("apps: publish backup evidence: %w", err)
	}
	if err = o.step(ctx, "sealed"); err == nil {
		err = o.sync(ctx, dest)
	}
	if err != nil {
		return BackupReference{}, errors.Join(err, invalidateBackupEvidence(dest))
	}
	return ref, nil
}

// VerifyBackup verifies persisted identity, the exact tree and SQL integrity.
// It needs no source tree, migrates nothing and never repairs sealed contents.
func VerifyBackup(ctx context.Context, root, server, app string, ref BackupReference, node string) error {
	return (backupOps{}).verify(ctx, root, server, app, ref, node)
}
func (o backupOps) verify(ctx context.Context, root, server, app string, ref BackupReference, node string) error {
	if err := errors.Join(ctx.Err(), backupIdentity(root, server, app, ref)); err != nil {
		return err
	}
	dir, err := privateDir(ref.Root)
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := regularAt(dir, "verification.json", false)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		file.Close()
		return errors.Join(ErrInvalidStorage, err)
	}
	encoded, err := io.ReadAll(io.LimitReader(backupReader{ctx, file}, 16<<20))
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	var evidence backupEvidence
	if err = json.Unmarshal(encoded, &evidence); err != nil || evidence.Version != 1 || evidence.Reference != ref || evidence.Entries == nil {
		return ErrInvalidStorage
	}
	canonical, err := json.Marshal(evidence)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return ErrInvalidStorage
	}
	entries, err := o.tree(ctx, dir, nil, ".")
	if err != nil || !reflect.DeepEqual(entries, evidence.Entries) {
		return errors.Join(ErrInvalidStorage, err)
	}
	initialized, err := backupSQLiteEntries(entries, true)
	if err != nil {
		return err
	}
	if initialized {
		return o.verifyBackupSQLite(ctx, node, dir)
	}
	return ctx.Err()
}
func (o backupOps) sync(ctx context.Context, file *os.File) error {
	if err := o.step(ctx, "sync"); err != nil {
		return backupError("sync snapshot entry "+file.Name(), err)
	}
	return backupError("sync snapshot entry "+file.Name(), errors.Join(file.Sync(), ctx.Err()))
}

func backupError(operation string, err error) error {
	if err != nil {
		return fmt.Errorf("apps: %s: %w", operation, err)
	}
	return nil
}

func invalidateBackupEvidence(dir *os.File) error {
	removeErr := unix.Unlinkat(int(dir.Fd()), "verification.json", 0)
	syncErr := dir.Sync()
	return errors.Join(backupError("remove backup evidence", removeErr), backupError("sync backup invalidation", syncErr))
}

type backupReader struct {
	context.Context
	io.Reader
}

func (r backupReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
func (o backupOps) tree(ctx context.Context, dir, dest *os.File, prefix string) ([]backupEntry, error) {
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	if dest != nil {
		if err = dir.Chmod(0700); err != nil {
			return nil, err
		}
	} else if info.Mode().Perm() != 0700 {
		return nil, ErrInvalidStorage
	}
	result := []backupEntry{{Path: []byte(prefix), Kind: "directory"}}
	if _, err = dir.Seek(0, 0); err != nil {
		return nil, err
	}
	items, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	for _, item := range items {
		if err = o.step(ctx, "copy"); err != nil {
			return nil, err
		}
		name := item.Name()
		if prefix == "." && name == "verification.json" {
			continue
		}
		path := filepath.Join(prefix, name)
		if item.IsDir() {
			child, e := backupDir(dir, name, false)
			if e != nil {
				return nil, fmt.Errorf("apps: traverse snapshot directory %q: %w", path, e)
			}
			var output *os.File
			if dest != nil {
				output, e = backupDir(dest, name, true)
				if e != nil {
					child.Close()
					return nil, fmt.Errorf("apps: create snapshot directory %q: %w", path, e)
				}
			}
			entries, e := o.tree(ctx, child, output, path)
			_ = child.Close()
			if output != nil {
				if e == nil {
					e = o.sync(ctx, output)
				}
				e = errors.Join(e, output.Close())
			}
			if e != nil {
				return nil, e
			}
			result = append(result, entries...)
			continue
		}
		file, e := regularAt(dir, name, false)
		if e != nil {
			return nil, e
		}
		entry, e := o.copyFile(ctx, file, dest, name, path)
		e = errors.Join(e, file.Close())
		if e != nil {
			return nil, e
		}
		result = append(result, entry)
	}
	if dest != nil {
		if err = o.sync(ctx, dest); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (o backupOps) copyFile(ctx context.Context, file, dest *os.File, name, path string) (backupEntry, error) {
	info, err := file.Stat()
	if err != nil {
		return backupEntry{}, err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return backupEntry{}, ErrInvalidStorage
	}
	h := sha256.New()
	var writer io.Writer = h
	var output *os.File
	if dest != nil {
		if err = file.Chmod(0600); err != nil {
			return backupEntry{}, err
		}
		output, err = regularAt(dest, name, true)
		if err != nil {
			return backupEntry{}, err
		}
		writer = io.MultiWriter(h, output)
	} else if info.Mode().Perm() != 0600 {
		return backupEntry{}, ErrInvalidStorage
	}
	n, err := io.CopyBuffer(writer, backupReader{ctx, file}, make([]byte, 32768))
	if output != nil {
		if err == nil {
			err = o.sync(ctx, output)
		}
		err = errors.Join(err, output.Close())
	}
	if err != nil || n != info.Size() {
		return backupEntry{}, errors.Join(ErrInvalidStorage, err)
	}
	return backupEntry{[]byte(path), "file", n, fmt.Sprintf("%x", h.Sum(nil))}, nil
}

package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"
)

// RestoreData freshly verifies the persisted backup and restores its entire data
// tree without migrations or registry changes. Callers must serialize operations
// and quiesce all app writers. An error can leave partial current data; retry from
// the same backup. The backup is never changed or deleted by restoration.
func RestoreData(ctx context.Context, root, server, app string, ref BackupReference, node string) error {
	return backupError("restore app data", (backupOps{}).restore(ctx, root, server, app, ref, node))
}

func (o backupOps) restore(ctx context.Context, root, server, app string, ref BackupReference, node string) error {
	if err := VerifyBackup(ctx, root, server, app, ref, node); err != nil {
		return err
	}
	selected, err := privateDir(filepath.Join(root, app))
	if err != nil {
		return err
	}
	defer selected.Close()
	backup, err := privateDir(ref.Root)
	if err != nil {
		return err
	}
	defer backup.Close()
	source, expected, err := restoreDataTree(ctx, backup)
	if err != nil {
		return err
	}
	if source != nil {
		defer source.Close()
	}
	current, err := backupDir(selected, "data", false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if current != nil {
		defer current.Close()
		if err = o.removeRestoreTree(ctx, current, false); err != nil {
			return backupError("inspect current data", err)
		}
		if err = o.removeRestoreTree(ctx, current, true); err != nil {
			return backupError("remove current data", err)
		}
		if err = o.step(ctx, "replace"); err != nil {
			return err
		}
		if err = unix.Unlinkat(int(selected.Fd()), "data", unix.AT_REMOVEDIR); err != nil {
			return backupError("remove current data directory", err)
		}
	}
	if source != nil {
		target, err := backupDir(selected, "data", true)
		if err != nil {
			return err
		}
		o.readOnlySource = true
		_, err = o.tree(ctx, source, target, "data")
		err = errors.Join(err, target.Close())
		if err != nil {
			return backupError("copy restored data", err)
		}
	}
	if err = o.step(ctx, "restored-verify"); err != nil {
		return err
	}
	restored, actual, err := restoreDataTree(ctx, selected)
	if restored != nil {
		err = errors.Join(err, restored.Close())
	}
	if err != nil || !reflect.DeepEqual(expected, actual) {
		return errors.Join(ErrInvalidStorage, err)
	}
	initialized, err := backupSQLiteEntries(actual, true)
	if err != nil {
		return err
	}
	if initialized {
		if err = o.verifyBackupSQLite(ctx, node, selected); err != nil {
			return err
		}
	}
	if err = o.step(ctx, "restored"); err != nil {
		return err
	}
	return o.sync(ctx, selected)
}

func restoreDataTree(ctx context.Context, parent *os.File) (*os.File, []backupEntry, error) {
	dir, err := backupDir(parent, "data", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		return nil, nil, err
	}
	entries, err := (backupOps{}).tree(ctx, dir, nil, "data")
	if err != nil {
		return nil, nil, errors.Join(err, dir.Close())
	}
	return dir, entries, err
}

// removeRestoreTree first supports a read-only preflight of the entire tree.
// Deletion repeats each no-follow/type/link check and syncs emptied directories.
func (o backupOps) removeRestoreTree(ctx context.Context, dir *os.File, remove bool) error {
	if _, err := dir.Seek(0, 0); err != nil {
		return err
	}
	items, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	for _, item := range items {
		if err = ctx.Err(); err != nil {
			return err
		}
		flags := 0
		if item.IsDir() {
			child, err := backupDir(dir, item.Name(), false)
			if err != nil {
				return err
			}
			err = errors.Join(o.removeRestoreTree(ctx, child, remove), child.Close())
			if err != nil {
				return err
			}
			flags = unix.AT_REMOVEDIR
		} else {
			file, err := regularAt(dir, item.Name(), false)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err == nil && info.Sys().(*syscall.Stat_t).Nlink != 1 {
				err = ErrInvalidStorage
			}
			err = errors.Join(err, file.Close())
			if err != nil {
				return err
			}
		}
		if remove {
			if err = o.step(ctx, "replace"); err != nil {
				return err
			}
			if err = unix.Unlinkat(int(dir.Fd()), item.Name(), flags); err != nil {
				return backupError("unlink current data entry", err)
			}
		}
	}
	if remove {
		return o.sync(ctx, dir)
	}
	return ctx.Err()
}

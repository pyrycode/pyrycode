package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackupUninitializedSidecars(t *testing.T) {
	for _, state := range []string{"absent", "zero"} {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			t.Run(state+suffix, func(t *testing.T) {
				root, data, ref := backupFixture(t)
				backupOK(t, os.Mkdir(data, 0700))
				if state == "zero" {
					backupWrite(t, filepath.Join(data, "app.sqlite"), "")
				}
				backupCreate(t, root, ref, "")
				t.Run("snapshot", func(t *testing.T) {
					backupWrite(t, filepath.Join(data, "app.sqlite"+suffix), "transient")
					next := testID(43)
					_, err := SnapshotData(context.Background(), root, testHost, testApp, next, "")
					backupCheck(t, errors.Is(err, ErrInvalidStorage), "sealed orphan sidecar", err)
					_, err = os.Stat(filepath.Join(root, testApp, "backups", next, "verification.json"))
					backupCheck(t, errors.Is(err, os.ErrNotExist), "published incomplete evidence", err)
					bytes, err := os.ReadFile(filepath.Join(data, "app.sqlite"+suffix))
					backupCheck(t, err == nil && string(bytes) == "transient", "source changed", err)
				})

				// Even matching persisted evidence cannot authorize sealed sidecars.
				t.Run("verify", func(t *testing.T) {
					backupWrite(t, filepath.Join(ref.Root, "data/app.sqlite"+suffix), "transient")
					dir, err := privateDir(ref.Root)
					backupOK(t, err)
					entries, err := (backupOps{}).tree(context.Background(), dir, nil, ".")
					backupOK(t, errors.Join(err, dir.Close()))
					encoded, err := json.Marshal(backupEvidence{1, ref, entries})
					backupOK(t, err)
					backupWrite(t, filepath.Join(ref.Root, "verification.json"), string(encoded))
					backupOK(t, os.RemoveAll(data))
					err = VerifyBackup(context.Background(), root, testHost, testApp, ref, "")
					backupCheck(t, errors.Is(err, ErrInvalidStorage), "accepted sealed sidecar", err)
				})
			})
		}
	}
}

func TestBackupCancellation(t *testing.T) {
	for _, phase := range []string{"before", "copy", "sealed"} {
		t.Run(phase, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "file"), "recoverable")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			ops := backupOps{hook: func(p string) error {
				if p == phase {
					cancel()
				}
				return nil
			}}
			_, err := ops.snapshot(ctx, root, testHost, testApp, ref.PublishID, "")
			backupCheck(t, errors.Is(err, context.Canceled), err)
			backupCheck(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, "") != nil, "accepted cancelled snapshot")
			_, err = os.Stat(filepath.Join(ref.Root, "verification.json"))
			backupCheck(t, errors.Is(err, os.ErrNotExist), "cancelled seal kept evidence", err)
			bytes, err := os.ReadFile(filepath.Join(data, "file"))
			backupCheck(t, err == nil && string(bytes) == "recoverable", "source changed", err)
		})
	}
	for _, phase := range []string{"before", "copy"} {
		t.Run("verify-"+phase, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "file"), "sealed")
			backupCreate(t, root, ref, "")
			backupOK(t, os.RemoveAll(data))
			before, err := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
			backupOK(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
				err = VerifyBackup(ctx, root, testHost, testApp, ref, "")
			} else {
				ops := backupOps{hook: func(p string) error {
					if p == "copy" {
						cancel()
					}
					return nil
				}}
				err = ops.verify(ctx, root, testHost, testApp, ref, "")
			}
			backupCheck(t, errors.Is(err, context.Canceled), "verification ignored cancellation", err)
			after, err := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
			backupCheck(t, err == nil && string(before) == string(after), "verification changed evidence", err)
			bytes, err := os.ReadFile(filepath.Join(ref.Root, "data/file"))
			backupCheck(t, err == nil && string(bytes) == "sealed", "verification changed data", err)
			backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, ""))
		})
	}
}

func TestBackupSQLiteCancellation(t *testing.T) {
	node := backupNode(t)
	for _, mode := range []string{"snapshot", "verify"} {
		t.Run(mode, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupOK(t, os.Mkdir(data, 0700))
			db := filepath.Join(data, "app.sqlite")
			err := exec.Command(node, "-e", `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1]);db.exec('CREATE TABLE saved(value TEXT)');db.close()`, db).Run()
			backupOK(t, err)
			if mode == "verify" {
				backupCreate(t, root, ref, node)
				db = filepath.Join(ref.Root, "data/app.sqlite")
			}
			before, err := os.ReadFile(db)
			backupOK(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var child *os.Process
			var signalErr error
			ops := backupOps{nodeStarted: func(p *os.Process) {
				child = p
				signalErr = p.Signal(unix.Signal(0))
				cancel()
			}}
			if mode == "snapshot" {
				_, err = ops.snapshot(ctx, root, testHost, testApp, ref.PublishID, node)
				backupCheck(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, node) != nil, "accepted cancelled SQLite snapshot")
			} else {
				err = ops.verify(ctx, root, testHost, testApp, ref, node)
				backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, node))
			}
			backupCheck(t, errors.Is(err, context.Canceled), err)
			backupOK(t, signalErr)
			backupCheck(t, child != nil && errors.Is(child.Signal(unix.Signal(0)), os.ErrProcessDone), "SQLite child not joined", child)
			after, err := os.ReadFile(db)
			backupCheck(t, err == nil && string(before) == string(after), "database changed", err)
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				_, err := os.Lstat(db + suffix)
				backupCheck(t, errors.Is(err, os.ErrNotExist), "created sidecar", suffix, err)
			}
		})
	}
}

func TestBackupSQLiteStorageErrors(t *testing.T) {
	node := backupNode(t)
	for _, operation := range []string{"replace", "sidecar", "work"} {
		t.Run(operation, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupOK(t, os.Mkdir(data, 0700))
			db := filepath.Join(data, "app.sqlite")
			backupOK(t, exec.Command(node, "-e", `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1]);db.exec('CREATE TABLE saved(value TEXT)');db.close()`, db).Run())
			before, err := os.ReadFile(db)
			backupOK(t, err)
			changed := false
			ops := backupOps{hook: func(p string) error {
				if p == "sync" && !changed {
					if _, err := os.Stat(filepath.Join(ref.Root, "sqlite-work/backup.sqlite")); err == nil {
						changed = true
						switch operation {
						case "replace":
							backupOK(t, os.Remove(filepath.Join(ref.Root, "data/app.sqlite")))
							backupOK(t, os.Mkdir(filepath.Join(ref.Root, "data/app.sqlite"), 0700))
						case "sidecar":
							backupOK(t, os.Mkdir(filepath.Join(ref.Root, "data/app.sqlite-wal"), 0700))
						case "work":
							backupWrite(t, filepath.Join(ref.Root, "sqlite-work/leftover"), "injected")
						}
					}
				}
				return nil
			}}
			_, err = ops.snapshot(context.Background(), root, testHost, testApp, ref.PublishID, node)
			want := map[string]string{"replace": "replace snapshot database", "sidecar": "remove snapshot sidecar", "work": "remove snapshot SQLite work directory"}[operation]
			backupCheck(t, err != nil && strings.Contains(err.Error(), want), err)
			backupCheck(t, errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOTEMPTY), err)
			backupCheck(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, node) != nil, "accepted failed snapshot")
			after, err := os.ReadFile(db)
			backupCheck(t, err == nil && string(before) == string(after), "source changed", err)
		})
	}
}

func TestBackupInvalidationErrors(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	backupOK(t, err)
	backupOK(t, dir.Close())
	err = invalidateBackupEvidence(dir)
	backupCheck(t, errors.Is(err, unix.EBADF) && errors.Is(err, os.ErrClosed), err)
	backupCheck(t, strings.Contains(err.Error(), "remove backup evidence") && strings.Contains(err.Error(), "sync backup invalidation"), err)
}

func TestBackupStorageErrors(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		parent, err := os.Open(t.TempDir())
		backupOK(t, err)
		backupOK(t, parent.Close())
		for _, create := range []bool{false, true} {
			_, err := backupDir(parent, "child", create)
			op := "open"
			if create {
				op = "create"
			}
			backupCheck(t, errors.Is(err, unix.EBADF) && strings.Contains(err.Error(), op+" backup directory"), err)
		}
	})
	for _, phase := range []string{"rename", "invalidate"} {
		t.Run(phase, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "file"), "recoverable")
			sentinel := errors.New("seal failed")
			changed := false
			ops := backupOps{hook: func(p string) error {
				pending := filepath.Join(ref.Root, "verification.pending")
				if phase == "rename" && p == "sync" && !changed {
					if _, err := os.Stat(pending); err == nil {
						backupOK(t, os.Remove(pending))
						changed = true
					}
				}
				if phase == "invalidate" && p == "sealed" {
					evidence := filepath.Join(ref.Root, "verification.json")
					backupOK(t, os.Remove(evidence))
					backupOK(t, os.Mkdir(evidence, 0700))
					return sentinel
				}
				return nil
			}}
			_, err := ops.snapshot(context.Background(), root, testHost, testApp, ref.PublishID, "")
			if phase == "rename" {
				backupCheck(t, errors.Is(err, unix.ENOENT) && strings.Contains(err.Error(), "publish backup evidence"), err)
			} else {
				backupCheck(t, errors.Is(err, sentinel) && (errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EPERM)) && strings.Contains(err.Error(), "remove backup evidence"), err)
			}
			backupCheck(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, "") != nil, "accepted failed seal")
			bytes, err := os.ReadFile(filepath.Join(data, "file"))
			backupCheck(t, err == nil && string(bytes) == "recoverable", "source changed", err)
		})
	}
}

package apps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func backupFixture(t *testing.T) (string, string, BackupReference) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	backupOK(t, err)
	app := filepath.Join(root, testApp)
	backupOK(t, os.Mkdir(app, 0700))
	ref := BackupReference{testHost, testApp, testID(42), filepath.Join(app, "backups", testID(42))}
	return root, filepath.Join(app, "data"), ref
}
func backupWrite(t *testing.T, path, value string) {
	t.Helper()
	backupOK(t, os.MkdirAll(filepath.Dir(path), 0700))
	err := os.WriteFile(path, []byte(value), 0600)
	if skipEILSEQ(t, err) {
		return
	}
	backupOK(t, err)
}

// skipEILSEQ skips the current test when err is EILSEQ: some filesystems
// (APFS) reject a filename containing an invalid UTF-8 byte sequence that
// others (ext4) accept. It checks the error rather than the OS, so the case
// still runs wherever the filesystem allows it. It reports whether it skipped.
func skipEILSEQ(t *testing.T, err error) bool {
	t.Helper()
	if !errors.Is(err, unix.EILSEQ) {
		return false
	}
	t.Skip("filesystem rejects invalid UTF-8 filename (EILSEQ)")
	return true
}
func backupCreate(t *testing.T, root string, ref BackupReference, node string) {
	t.Helper()
	got, err := SnapshotData(context.Background(), root, testHost, testApp, ref.PublishID, node)
	backupCheck(t, err == nil && got == ref, got, err)
}
func TestBackupAliasedTempRoot(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "alias")
	backupOK(t, os.Symlink(dir, alias))
	t.Setenv("TMPDIR", alias)
	t.Run("states", TestBackupStates)
	t.Run("containment", TestBackupContainment)
}
func TestBackupStates(t *testing.T) {
	for _, state := range []string{"absent", "empty", "zero", "nested"} {
		t.Run(state, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			if state != "absent" {
				backupOK(t, os.Mkdir(data, 0700))
			}
			if state == "zero" {
				backupWrite(t, filepath.Join(data, "app.sqlite"), "")
			}
			if state == "nested" {
				backupWrite(t, filepath.Join(data, "nested/file\xff"), "saved")
				backupOK(t, os.Mkdir(filepath.Join(data, "empty"), 0700))
			}
			backupCreate(t, root, ref, "")
			before, err := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
			backupOK(t, err)
			if _, err := SnapshotData(context.Background(), root, testHost, testApp, ref.PublishID, ""); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
			backupCheck(t, string(before) == string(after), "backup overwritten")
			backupOK(t, os.RemoveAll(data))
			backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, ""))
			files, err := os.ReadDir(filepath.Join(ref.Root, "data"))
			if state == "absent" {
				backupCheck(t, errors.Is(err, os.ErrNotExist), err)
				return
			}
			backupOK(t, err)
			backupCheck(t, len(files) == map[string]int{"empty": 0, "zero": 1, "nested": 2}[state], files)
			if state == "zero" {
				info, err := os.Stat(filepath.Join(ref.Root, "data/app.sqlite"))
				backupCheck(t, err == nil && info.Size() == 0, info, err)
			}
			info, err := os.Stat(filepath.Join(ref.Root, "verification.json"))
			backupCheck(t, err == nil && info.Mode().Perm() == 0600, info, err)

		})
	}
}
func TestBackupTampering(t *testing.T) {
	for _, mutation := range []string{"missing", "changed", "added", "directory", "evidence", "identity", "incomplete"} {
		t.Run(mutation, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "nested/file"), "saved")
			backupCreate(t, root, ref, "")
			switch mutation {
			case "missing":
				backupOK(t, os.Remove(filepath.Join(ref.Root, "data/nested/file")))
			case "changed":
				backupWrite(t, filepath.Join(ref.Root, "data/nested/file"), "other")
			case "added":
				backupWrite(t, filepath.Join(ref.Root, "data/new"), "new")
			case "directory":
				backupOK(t, os.Mkdir(filepath.Join(ref.Root, "data/empty"), 0700))
			case "evidence":
				backupWrite(t, filepath.Join(ref.Root, "verification.json"), "{}")
			case "identity":
				b, err := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
				backupOK(t, err)
				backupWrite(t, filepath.Join(ref.Root, "verification.json"), strings.ReplaceAll(string(b), testHost, testID(8)))
			case "incomplete":
				backupOK(t, os.Remove(filepath.Join(ref.Root, "verification.json")))
			}
			if err := VerifyBackup(context.Background(), root, testHost, testApp, ref, ""); err == nil {
				t.Fatal("accepted tampering")
			}
		})
	}
}
func TestBackupContainment(t *testing.T) {
	for _, kind := range []string{"foreign", "escape", "publish", "root", "data-link", "entry-link", "parent-link", "fifo", "hardlink", "backup-link"} {
		t.Run(kind, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			outside := t.TempDir()
			target := filepath.Join(outside, "file")
			backupWrite(t, target, "outside")
			switch kind {
			case "foreign":
				ref.ServerID = testID(8)
			case "escape":
				ref.Root = outside
			case "publish":
				ref.PublishID = "../escape"
			case "root":
				root = root + "/../" + filepath.Base(root)
			case "data-link":
				backupOK(t, os.Symlink(outside, data))
			case "entry-link":
				backupOK(t, os.Mkdir(data, 0700))
				backupOK(t, os.Symlink(target, filepath.Join(data, "link")))
			case "parent-link":
				backupOK(t, os.Remove(filepath.Join(root, testApp)))
				backupOK(t, os.Symlink(outside, filepath.Join(root, testApp)))
			case "fifo":
				backupOK(t, os.Mkdir(data, 0700))
				backupOK(t, unix.Mkfifo(filepath.Join(data, "pipe"), 0600))
			case "hardlink":
				backupOK(t, os.Mkdir(data, 0700))
				backupOK(t, os.Link(target, filepath.Join(data, "link")))
			case "backup-link":
				backupOK(t, os.Symlink(outside, filepath.Join(root, testApp, "backups")))
			}
			if kind == "foreign" || kind == "escape" {
				if err := VerifyBackup(context.Background(), root, testHost, testApp, ref, ""); err == nil {
					t.Fatal("accepted reference")
				}
			} else if _, err := SnapshotData(context.Background(), root, testHost, testApp, ref.PublishID, ""); err == nil {
				t.Fatal("accepted unsafe source")
			}
			bytes, err := os.ReadFile(target)
			backupCheck(t, err == nil && string(bytes) == "outside", "outside changed", err)
		})
	}
}
func TestBackupFailures(t *testing.T) {
	for _, phase := range []string{"copy", "sqlite", "verify", "sync", "cancel", "final-sync"} {
		t.Run(phase, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "nested/file"), "recoverable")
			if phase == "sqlite" {
				backupWrite(t, filepath.Join(data, "app.sqlite"), "not sqlite")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("injected")
			sealed := false
			ops := backupOps{hook: func(p string) error {
				if phase == "cancel" && p == "copy" {
					cancel()
				}
				if p == "sealed" {
					sealed = true
				}
				if p == phase || (phase == "final-sync" && p == "sync" && sealed) {
					return sentinel
				}
				return nil
			}}
			_, err := ops.snapshot(ctx, root, testHost, testApp, ref.PublishID, "")
			want := sentinel
			if phase == "cancel" {
				want = context.Canceled
			}
			backupCheck(t, errors.Is(err, want), err)
			if err := VerifyBackup(context.Background(), root, testHost, testApp, ref, ""); err == nil {
				t.Fatal("accepted incomplete snapshot")
			}
			bytes, err := os.ReadFile(filepath.Join(data, "nested/file"))
			backupCheck(t, err == nil && string(bytes) == "recoverable", "source changed", err)
			_, err = SnapshotData(context.Background(), root, testHost, testApp, ref.PublishID, "")
			backupCheck(t, errors.Is(err, ErrConflict), err)
		})
	}
	root, data, ref := backupFixture(t)
	backupWrite(t, filepath.Join(data, "app.sqlite"), "corrupt")
	if _, err := SnapshotData(context.Background(), root, testHost, testApp, ref.PublishID, os.Getenv("HOSTED_APP_NODE")); err == nil {
		t.Fatal("corrupt database accepted")
	}
}
func backupNode(t *testing.T) string {
	t.Helper()
	node := os.Getenv("HOSTED_APP_NODE")
	if node == "" {
		node = "node"
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil || !supportedNode(strings.TrimSpace(string(version))) {
		backupCheck(t, os.Getenv("HOSTED_APP_NODE") == "", "selected Node unsupported", string(version), err)
		t.Skip("set HOSTED_APP_NODE: SQL skip provides no evidence")
	}
	return node
}
func TestBackupSQLiteWAL(t *testing.T) {
	node := backupNode(t)
	root, data, ref := backupFixture(t)
	backupWrite(t, filepath.Join(data, "nested/file"), "app-owned")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `const {DatabaseSync}=require('node:sqlite');const fs=require('node:fs');const db=new DatabaseSync(process.argv[1]);db.exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE preferences(value TEXT); CREATE TABLE pyry_schema_migrations(version INTEGER PRIMARY KEY,sha256 TEXT NOT NULL); INSERT INTO pyry_schema_migrations VALUES(1,'checksum'); PRAGMA wal_checkpoint(TRUNCATE); INSERT INTO preferences VALUES('saved')");fs.writeFileSync(process.argv[2],'ready');process.stdin.resume();process.stdin.on('end',()=>{db.close();process.exit()});`
	cmd := exec.CommandContext(ctx, node, "-e", script, filepath.Join(data, "app.sqlite"), filepath.Join(root, "ready"))
	in, err := cmd.StdinPipe()
	backupOK(t, err)
	backupOK(t, cmd.Start())
	defer func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			backupOK(t, cmd.Wait())
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		backupCheck(t, ctx.Err() == nil, ctx.Err())
		time.Sleep(10 * time.Millisecond)
	}
	wal, err := os.Stat(filepath.Join(data, "app.sqlite-wal"))
	backupCheck(t, err == nil && wal.Size() > 0, "no committed WAL", wal, err)
	main := filepath.Join(root, "main-only.sqlite")
	bytes, err := os.ReadFile(filepath.Join(data, "app.sqlite"))
	backupOK(t, err)
	backupWrite(t, main, string(bytes))
	check := `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1],{readOnly:true});console.log(JSON.stringify({preferences:db.prepare('SELECT value FROM main.preferences').all(),ledger:db.prepare('SELECT version,sha256 FROM main.pyry_schema_migrations').all(),mode:db.prepare('PRAGMA journal_mode').get()}));db.close()`
	proof, err := exec.CommandContext(ctx, node, "-e", check, main).Output()
	backupCheck(t, !(err != nil || !strings.Contains(string(proof), `"preferences":[]`)), "main alone already contains preference", string(proof), err)
	backupCreate(t, root, ref, node)
	backupOK(t, in.Close())
	backupOK(t, cmd.Wait())
	backupOK(t, os.RemoveAll(data))
	sealed := filepath.Join(ref.Root, "data/app.sqlite")
	before, err := os.ReadFile(sealed)
	backupOK(t, err)
	for i := 0; i < 2; i++ {
		backupOK(t, VerifyBackup(ctx, root, testHost, testApp, ref, node))
	}
	after, err := os.ReadFile(sealed)
	backupCheck(t, err == nil && string(before) == string(after), "verification modified database", err)
	proof, err = exec.CommandContext(ctx, node, "-e", check, sealed).Output()
	backupOK(t, err)
	backupCheck(t, !(!strings.Contains(string(proof), `"value":"saved"`) || !strings.Contains(string(proof), `"sha256":"checksum"`) || !strings.Contains(string(proof), `"journal_mode":"delete"`)), string(proof))
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(sealed + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("sidecar", suffix, err)
		}
	}
	bytes, err = os.ReadFile(filepath.Join(ref.Root, "data/nested/file"))
	backupCheck(t, err == nil && string(bytes) == "app-owned", string(bytes), err)
	backupWrite(t, sealed, "corrupt")
	if err := VerifyBackup(ctx, root, testHost, testApp, ref, node); err == nil {
		t.Fatal("accepted corrupt SQL")
	}
}

func backupOK(t *testing.T, err error) {
	t.Helper()
	backupCheck(t, !(err != nil), err)
}

func backupCheck(t *testing.T, ok bool, details ...any) {
	t.Helper()
	if !ok {
		t.Fatal(details...)
	}
}

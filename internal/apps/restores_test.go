package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func restoreReference(t *testing.T, ref BackupReference) BackupReference {
	t.Helper()
	b, err := json.Marshal(ref)
	backupOK(t, err)
	var loaded BackupReference
	backupOK(t, json.Unmarshal(b, &loaded))
	return loaded
}

func restoreInventory(t *testing.T, path string) []backupEntry {
	t.Helper()
	dir, err := privateDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	backupOK(t, err)
	defer dir.Close()
	entries, err := (backupOps{}).tree(context.Background(), dir, nil, "data")
	backupOK(t, err)
	return entries
}

func TestRestoreStates(t *testing.T) {
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
			want := restoreInventory(t, filepath.Join(ref.Root, "data"))
			before := restoreInventory(t, ref.Root)
			for i := 0; i < 2; i++ {
				backupWrite(t, filepath.Join(data, "candidate/extra"), "candidate")
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					backupWrite(t, filepath.Join(data, "app.sqlite"+suffix), "obsolete")
				}
				backupOK(t, RestoreData(context.Background(), root, testHost, testApp, restoreReference(t, ref), ""))
				backupCheck(t, reflect.DeepEqual(want, restoreInventory(t, data)), state, "wrong restored state")
				backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, ""))
				backupCheck(t, reflect.DeepEqual(before, restoreInventory(t, ref.Root)), "backup changed")
			}
		})
	}
}

func TestRestoreTampering(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "added", "directory", "corrupt-evidence", "incomplete", "foreign-evidence"} {
		t.Run(kind, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "nested/file"), "saved")
			backupCreate(t, root, ref, "")
			backupOK(t, RestoreData(context.Background(), root, testHost, testApp, ref, ""))
			backupWrite(t, filepath.Join(data, "candidate"), "keep")
			before := restoreInventory(t, data)
			switch kind {
			case "missing":
				backupOK(t, os.Remove(filepath.Join(ref.Root, "data/nested/file")))
			case "changed":
				backupWrite(t, filepath.Join(ref.Root, "data/nested/file"), "other")
			case "added":
				backupWrite(t, filepath.Join(ref.Root, "data/added"), "new")
			case "directory":
				backupOK(t, os.Mkdir(filepath.Join(ref.Root, "data/added"), 0700))
			case "corrupt-evidence":
				backupWrite(t, filepath.Join(ref.Root, "verification.json"), "{}")
			case "incomplete":
				backupOK(t, os.Remove(filepath.Join(ref.Root, "verification.json")))
			case "foreign-evidence":
				b, err := os.ReadFile(filepath.Join(ref.Root, "verification.json"))
				backupOK(t, err)
				backupWrite(t, filepath.Join(ref.Root, "verification.json"), strings.ReplaceAll(string(b), testHost, testID(8)))
			}
			backupCheck(t, RestoreData(context.Background(), root, testHost, testApp, ref, "") != nil, "accepted stale verification")
			backupCheck(t, reflect.DeepEqual(before, restoreInventory(t, data)), "changed current data before rejecting backup")
		})
	}
}

func TestRestoreContainment(t *testing.T) {
	for _, kind := range []string{"foreign", "foreign-app", "escape", "publish", "root", "parent-link", "backups-link", "backup-entry-link", "backup-fifo", "backup-hardlink", "data-link", "entry-link", "fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "saved"), "saved")
			backupCreate(t, root, ref, "")
			outside, err := filepath.EvalSymlinks(t.TempDir())
			backupOK(t, err)
			target := filepath.Join(outside, "file")
			backupWrite(t, target, "outside")
			sibling := filepath.Join(root, testID(8), "data/saved")
			backupWrite(t, sibling, "other app")
			selected := ref
			switch kind {
			case "foreign":
				selected.ServerID = testID(8)
			case "foreign-app":
				selected.AppID = testID(8)
			case "escape":
				selected.Root = outside
			case "publish":
				selected.PublishID = "../escape"
			case "root":
				root += "/../" + filepath.Base(root)
			case "parent-link":
				backupOK(t, os.Rename(filepath.Dir(data), filepath.Join(outside, "app")))
				backupOK(t, os.Symlink(filepath.Join(outside, "app"), filepath.Dir(data)))
			case "backups-link":
				parent := filepath.Dir(ref.Root)
				backupOK(t, os.Rename(parent, filepath.Join(outside, "backups")))
				backupOK(t, os.Symlink(filepath.Join(outside, "backups"), parent))
			case "data-link":
				backupOK(t, os.Rename(data, filepath.Join(outside, "data")))
				backupOK(t, os.Symlink(filepath.Join(outside, "data"), data))
			default:
				entry := filepath.Join(data, "unsafe")
				if strings.HasPrefix(kind, "backup-") {
					entry = filepath.Join(ref.Root, "data/unsafe")
				}
				switch {
				case strings.HasSuffix(kind, "entry-link"):
					backupOK(t, os.Symlink(target, entry))
				case strings.HasSuffix(kind, "fifo"):
					backupOK(t, unix.Mkfifo(entry, 0600))
				case strings.HasSuffix(kind, "hardlink"):
					backupOK(t, os.Link(target, entry))
				}
			}
			backupCheck(t, RestoreData(context.Background(), root, testHost, testApp, selected, "") != nil, "accepted unsafe tree/reference")
			b, err := os.ReadFile(target)
			backupCheck(t, err == nil && string(b) == "outside", "outside changed", err)
			b, err = os.ReadFile(sibling)
			backupCheck(t, err == nil && string(b) == "other app", "another app changed", err)
			b, err = os.ReadFile(filepath.Join(data, "saved"))
			backupCheck(t, err == nil && string(b) == "saved", "current tree changed", err)
		})
	}
}

func TestRestoreFailures(t *testing.T) {
	for _, phase := range []string{"copy", "replace", "partial-delete", "restored-verify", "corrupt-restored", "sync", "final-sync", "cancel-before", "cancel-copy", "cancel-replace", "cancel-final"} {
		t.Run(phase, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupWrite(t, filepath.Join(data, "nested/file"), "saved")
			backupCreate(t, root, ref, "")
			before := restoreInventory(t, ref.Root)
			backupWrite(t, filepath.Join(data, "candidate/extra"), "remove")
			backupWrite(t, filepath.Join(data, "nested/file"), "mutated")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("injected restore failure")
			replacements, final := 0, false
			ops := backupOps{hook: func(p string) error {
				if p == "replace" {
					replacements++
				}
				if p == "restored" {
					final = true
				}
				if p == phase || (phase == "partial-delete" && replacements == 2) || (phase == "final-sync" && p == "sync" && final) {
					return sentinel
				}
				if (phase == "cancel-copy" && p == "copy") || (phase == "cancel-replace" && replacements == 2) || (phase == "cancel-final" && final) {
					cancel()
				}
				if phase == "corrupt-restored" && p == "restored-verify" {
					backupWrite(t, filepath.Join(data, "nested/file"), "corrupt")
				}
				return nil
			}}
			if phase == "cancel-before" {
				cancel()
			}
			err := ops.restore(ctx, root, testHost, testApp, ref, "")
			want := sentinel
			if strings.HasPrefix(phase, "cancel-") {
				want = context.Canceled
			} else if phase == "corrupt-restored" {
				want = ErrInvalidStorage
			}
			backupCheck(t, errors.Is(err, want), phase, err)
			if phase == "partial-delete" || phase == "cancel-replace" {
				_, err := os.Stat(filepath.Join(data, "candidate/extra"))
				backupCheck(t, errors.Is(err, os.ErrNotExist), "interruption did not follow a deletion", err)
			}
			backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, ""))
			backupCheck(t, reflect.DeepEqual(before, restoreInventory(t, ref.Root)), "failed attempt changed backup")
			backupOK(t, RestoreData(context.Background(), root, testHost, testApp, restoreReference(t, ref), ""))
			backupCheck(t, reflect.DeepEqual(restoreInventory(t, filepath.Join(ref.Root, "data")), restoreInventory(t, data)), "retry did not converge")
			backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, ""))
		})
	}
}

func TestRestoreSQLiteWAL(t *testing.T) {
	node := backupNode(t)
	root, data, ref := backupFixture(t)
	backupWrite(t, filepath.Join(data, "nested/file"), "app-owned")
	backupOK(t, os.Mkdir(filepath.Join(data, "empty"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `const {DatabaseSync}=require('node:sqlite');const fs=require('node:fs');const db=new DatabaseSync(process.argv[1]);db.exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE preferences(value TEXT); CREATE TABLE pyry_schema_migrations(version INTEGER PRIMARY KEY,sha256 TEXT NOT NULL); INSERT INTO pyry_schema_migrations VALUES(1,'checksum'),(2,'second'); PRAGMA wal_checkpoint(TRUNCATE); INSERT INTO preferences VALUES('saved')");fs.writeFileSync(process.argv[2],'ready');process.stdin.resume();process.stdin.on('end',()=>{db.close();process.exit()});`
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
	backupCheck(t, err == nil && wal.Size() > 0, "no WAL", err)
	b, err := os.ReadFile(filepath.Join(data, "app.sqlite"))
	backupOK(t, err)
	main := filepath.Join(root, "main-only.sqlite")
	backupWrite(t, main, string(b))
	check := `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1],{readOnly:true});console.log(JSON.stringify({preferences:db.prepare('SELECT value FROM main.preferences').all(),ledger:db.prepare('SELECT version,sha256 FROM main.pyry_schema_migrations ORDER BY version').all(),schema:db.prepare("SELECT name,sql FROM main.sqlite_schema WHERE type='table' ORDER BY name").all()}));db.close()`
	proof, err := exec.CommandContext(ctx, node, "-e", check, main).Output()
	backupCheck(t, err == nil && strings.Contains(string(proof), `"preferences":[]`), "saved preference already in main", string(proof), err)
	backupCreate(t, root, ref, node)
	backupOK(t, in.Close())
	backupOK(t, cmd.Wait())
	want := restoreInventory(t, filepath.Join(ref.Root, "data"))
	before := restoreInventory(t, ref.Root)
	proof, err = exec.CommandContext(ctx, node, "-e", check, filepath.Join(ref.Root, "data/app.sqlite")).Output()
	backupOK(t, err)
	backupCheck(t, strings.Contains(string(proof), `"value":"saved"`) && strings.Contains(string(proof), `"sha256":"second"`), string(proof))
	for i := 0; i < 2; i++ {
		mutate := `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1]);db.exec("ALTER TABLE preferences ADD COLUMN candidate TEXT; UPDATE preferences SET value='changed'; UPDATE pyry_schema_migrations SET sha256='changed'; CREATE TABLE candidate_only(id INTEGER)");db.close()`
		output, err := exec.CommandContext(ctx, node, "-e", mutate, filepath.Join(data, "app.sqlite")).CombinedOutput()
		backupCheck(t, err == nil, string(output), err)
		backupWrite(t, filepath.Join(data, "candidate/extra"), "candidate")
		backupWrite(t, filepath.Join(data, "nested/file"), "changed")
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			backupWrite(t, filepath.Join(data, "app.sqlite"+suffix), "obsolete")
		}
		backupOK(t, RestoreData(ctx, root, testHost, testApp, restoreReference(t, ref), node))
		backupCheck(t, reflect.DeepEqual(want, restoreInventory(t, data)), "tree/content mismatch")
		got, err := exec.CommandContext(ctx, node, "-e", check, filepath.Join(data, "app.sqlite")).Output()
		backupCheck(t, err == nil && string(got) == string(proof), "schema/preferences/ledger changed", string(got), err)
		backupOK(t, VerifyBackup(ctx, root, testHost, testApp, ref, node))
		backupCheck(t, reflect.DeepEqual(before, restoreInventory(t, ref.Root)), "backup changed")
	}
}

func TestRestoreSQLiteGenericAndCancellation(t *testing.T) {
	node := backupNode(t)
	for _, mode := range []string{"generic", "node-cancel", "node-failure"} {
		t.Run(mode, func(t *testing.T) {
			root, data, ref := backupFixture(t)
			backupOK(t, os.Mkdir(data, 0700))
			seed := `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1]);db.exec("CREATE TABLE arbitrary(value TEXT); INSERT INTO arbitrary VALUES('original')");db.close()`
			output, err := exec.Command(node, "-e", seed, filepath.Join(data, "app.sqlite")).CombinedOutput()
			backupCheck(t, err == nil, string(output), err)
			backupCreate(t, root, ref, node)
			before := restoreInventory(t, ref.Root)
			backupWrite(t, filepath.Join(data, "app.sqlite"), "corrupt candidate")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var child *os.Process
			ops := backupOps{nodeStarted: func(p *os.Process) {
				child = p
				if mode == "node-cancel" {
					cancel()
				} else if mode == "node-failure" {
					backupOK(t, p.Kill())
				}
			}}
			err = ops.restore(ctx, root, testHost, testApp, ref, node)
			if mode == "generic" {
				backupOK(t, err)
			} else {
				backupCheck(t, err != nil, "reported success after Node failure")
				if mode == "node-cancel" {
					backupCheck(t, errors.Is(err, context.Canceled), err)
				}
			}
			backupCheck(t, child != nil && errors.Is(child.Signal(syscall.Signal(0)), os.ErrProcessDone), "Node child not joined")
			backupOK(t, VerifyBackup(context.Background(), root, testHost, testApp, ref, node))
			backupCheck(t, reflect.DeepEqual(before, restoreInventory(t, ref.Root)), "backup changed")
			backupOK(t, RestoreData(context.Background(), root, testHost, testApp, restoreReference(t, ref), node))
			check := `const {DatabaseSync}=require('node:sqlite');const db=new DatabaseSync(process.argv[1],{readOnly:true});if(db.prepare('SELECT value FROM main.arbitrary').get().value!=='original'||db.prepare("SELECT count(*) AS n FROM main.sqlite_schema WHERE name='pyry_schema_migrations'").get().n!==0)throw Error('wrong schema');db.close()`
			output, err = exec.Command(node, "-e", check, filepath.Join(data, "app.sqlite")).CombinedOutput()
			backupCheck(t, err == nil, string(output), err)
		})
	}
}

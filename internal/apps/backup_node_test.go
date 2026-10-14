package apps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupNodeDirectoryIdentity(t *testing.T) {
	node := backupNode(t)
	for _, mode := range []string{"snapshot", "verify"} {
		for _, replacement := range []string{"directory", "symlink"} {
			t.Run(mode+"/"+replacement, func(t *testing.T) {
				parent, outside := t.TempDir(), t.TempDir()
				selected := filepath.Join(parent, "selected")
				backupOK(t, os.Mkdir(selected, 0700))
				backupOK(t, exec.Command(node, "-e", `const {DatabaseSync}=require('node:sqlite');const d=new DatabaseSync(process.argv[1]);d.exec("CREATE TABLE saved(value TEXT);INSERT INTO saved VALUES('selected')");d.close()`, filepath.Join(selected, "app.sqlite")).Run())
				foreign := filepath.Join(outside, "data")
				backupOK(t, os.Mkdir(foreign, 0700))
				backupWrite(t, filepath.Join(foreign, "app.sqlite"), "outside sentinel")
				dir, err := privateDir(selected)
				backupOK(t, err)
				defer dir.Close()
				before, err := os.Getwd()
				backupOK(t, err)
				moved := filepath.Join(outside, "moved")
				changed := false
				ops := backupOps{hook: func(phase string) error {
					if phase == "sqlite-start" {
						changed = true
						backupOK(t, os.Rename(selected, moved))
						if replacement == "symlink" {
							backupOK(t, os.Symlink(foreign, selected))
						} else {
							backupOK(t, os.Mkdir(selected, 0700))
							backupWrite(t, filepath.Join(selected, "app.sqlite"), "replacement sentinel")
						}
					}
					return nil
				}}
				backupOK(t, ops.runBackupNode(context.Background(), node, dir, mode))
				backupCheck(t, changed, "launch substitution did not execute")
				after, err := os.Getwd()
				backupCheck(t, err == nil && before == after, "parent cwd changed", err)
				backupCheck(t, string(testBytes(t, filepath.Join(foreign, "app.sqlite"))) == "outside sentinel", "foreign database changed")
				if replacement == "directory" {
					backupCheck(t, string(testBytes(t, filepath.Join(selected, "app.sqlite"))) == "replacement sentinel", "replacement changed")
				}
				if mode == "snapshot" {
					b, err := exec.Command(node, "-e", `const {DatabaseSync}=require('node:sqlite');const d=new DatabaseSync(process.argv[1],{readOnly:true});console.log(d.prepare('SELECT value FROM saved').get().value);d.close()`, filepath.Join(moved, "backup.sqlite")).Output()
					backupCheck(t, err == nil && strings.TrimSpace(string(b)) == "selected", "wrong selected database", string(b), err)
				}
			})
		}
	}
}

func TestBackupNodeFailures(t *testing.T) {
	for _, arm := range []string{"unsupported", "version", "exec", "sql"} {
		t.Run(arm, func(t *testing.T) {
			dir, err := privateDir(t.TempDir())
			backupOK(t, err)
			defer dir.Close()
			node := writeNodeDouble(t, "v24.15.0", arm == "exec")
			if arm == "unsupported" {
				node = writeNodeDouble(t, "v25.8.0", false)
			}
			if arm == "version" {
				node = filepath.Join(t.TempDir(), "missing")
			}
			if arm == "sql" {
				node = backupNode(t)
			}
			err = (backupOps{}).runBackupNode(context.Background(), node, dir, "verify")
			backupCheck(t, err != nil, "accepted subprocess failure")
			if arm == "version" {
				backupCheck(t, errors.Is(err, os.ErrNotExist), err)
			}
			if arm == "exec" {
				backupCheck(t, strings.Contains(err.Error(), "exec snapshot node"), "missing bootstrap diagnostic", err)
			}
		})
	}
}

func TestBackupNodeBootstrapRejectsInvalidDirectory(t *testing.T) {
	executable, err := os.Executable()
	backupOK(t, err)
	file, err := os.CreateTemp(t.TempDir(), "regular")
	backupOK(t, err)
	defer file.Close()
	for _, arm := range []string{"missing", "regular"} {
		t.Run(arm, func(t *testing.T) {
			cmd := exec.Command(executable, "--pyry-internal-backup-node", "/unused/node", "verify")
			cmd.Env = []string{}
			if arm == "regular" {
				cmd.ExtraFiles = []*os.File{file}
			}
			output, err := cmd.CombinedOutput()
			backupCheck(t, err != nil && strings.Contains(string(output), "snapshot directory") || err != nil && strings.Contains(string(output), "not a directory"), "invalid descriptor accepted", string(output), err)
		})
	}
}

func TestCommittedTemplateUpdateSnapshot(t *testing.T) {
	node := backupNode(t)
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	data := filepath.Join(r.root, testApp, "data")
	backupOK(t, os.MkdirAll(data, 0700))
	backupOK(t, exec.Command(node, "-e", `const {DatabaseSync}=require('node:sqlite');const d=new DatabaseSync(process.argv[1]);d.exec("CREATE TABLE saved(value TEXT);INSERT INTO saved VALUES('retained')");d.close()`, filepath.Join(data, "app.sqlite")).Run())
	ref, err := SnapshotData(context.Background(), r.root, testHost, testApp, "88888888-8888-4888-8888-888888888888", node)
	backupOK(t, err)
	backupOK(t, VerifyBackup(context.Background(), r.root, testHost, testApp, ref, node))
}

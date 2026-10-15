package apps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

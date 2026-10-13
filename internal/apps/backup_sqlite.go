package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const backupSQL = `const {DatabaseSync,backup}=require('node:sqlite');
const options={defensive:true,allowExtension:false,enableForeignKeyConstraints:true,timeout:1000};
function integrity(db){const rows=db.prepare('PRAGMA main.integrity_check').all();if(rows.length!==1||Object.values(rows[0])[0]!=='ok'||db.prepare('PRAGMA main.foreign_key_check').all().length)throw Error('invalid SQL integrity');}
(async()=>{if(process.argv[1]==='snapshot'){
 const source=new DatabaseSync('../data/app.sqlite',{...options,readOnly:true});
 try{integrity(source);await backup(source,'backup.sqlite');}finally{source.close();}
 const sealed=new DatabaseSync('backup.sqlite',options);
 try{if(Object.values(sealed.prepare('PRAGMA journal_mode=DELETE').get())[0]!=='delete')throw Error('not standalone');integrity(sealed);}finally{sealed.close();}
}else{
 const fs=require('node:fs');for(const suffix of ['-wal','-shm','-journal']){try{fs.lstatSync('app.sqlite'+suffix);throw Error('sidecar');}catch(e){if(e.code!=='ENOENT')throw e;}}
 const fd=fs.openSync('app.sqlite',fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW);const header=Buffer.alloc(20);try{if(fs.readSync(fd,header,0,20,0)!==20||header.subarray(0,16).toString()!=='SQLite format 3\x00'||header[18]!==1||header[19]!==1)throw Error('not standalone');}finally{fs.closeSync(fd);}
 const sealed=new DatabaseSync('app.sqlite',{...options,readOnly:true});try{integrity(sealed);}finally{sealed.close();}}
})().catch(()=>{process.exitCode=1});`

func backupSQLiteEntries(entries []backupEntry, sealed bool) (bool, error) {
	var initialized, sidecars bool
	for _, entry := range entries {
		switch string(entry.Path) {
		case "data/app.sqlite":
			if entry.Kind != "file" {
				return false, ErrInvalidStorage
			}
			initialized = entry.Size > 0
		case "data/app.sqlite-wal", "data/app.sqlite-shm", "data/app.sqlite-journal":
			sidecars = true
		}
	}
	if sidecars && (sealed || !initialized) {
		return false, fmt.Errorf("apps: SQLite transient sidecars without an initialized source database or inside a sealed backup: %w", ErrInvalidStorage)
	}
	return initialized, nil
}

func (o backupOps) runBackupNode(ctx context.Context, node string, dir *os.File, mode string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, node, "--version")
	version.Env = []string{}
	version.WaitDelay = time.Second
	bytes, err := version.Output()
	if err != nil {
		return fmt.Errorf("apps: snapshot node version: %w", errors.Join(err, ctx.Err()))
	}
	if !supportedNode(strings.TrimSpace(string(bytes))) {
		return errors.New("apps: unsupported snapshot node")
	}
	cmd := exec.CommandContext(ctx, node, "-e", backupSQL, mode)
	cmd.Env = []string{}
	cmd.Dir = fmt.Sprintf("/dev/fd/%d", dir.Fd())
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("apps: start snapshot SQLite: %w", errors.Join(err, ctx.Err()))
	}
	if o.nodeStarted != nil {
		o.nodeStarted(cmd.Process)
	}
	if err = errors.Join(cmd.Wait(), ctx.Err()); err != nil {
		return fmt.Errorf("apps: snapshot SQLite: %w", err)
	}
	return nil
}
func (o backupOps) sqliteSnapshot(ctx context.Context, node string, root, data *os.File) error {
	if err := o.step(ctx, "sqlite"); err != nil {
		return err
	}
	work, err := backupDir(root, "sqlite-work", true)
	if err != nil {
		return err
	}
	defer work.Close()
	if err = o.runBackupNode(ctx, node, work, "snapshot"); err != nil {
		return err
	}
	db, err := regularAt(work, "backup.sqlite", false)
	if err != nil {
		return err
	}
	err = db.Chmod(0600)
	if err == nil {
		err = o.sync(ctx, db)
	}
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	if err = unix.Renameat(int(work.Fd()), "backup.sqlite", int(data.Fd()), "app.sqlite"); err != nil {
		return fmt.Errorf("apps: replace snapshot database: %w", err)
	}
	for _, name := range []string{"app.sqlite-wal", "app.sqlite-shm", "app.sqlite-journal"} {
		if err = unix.Unlinkat(int(data.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("apps: remove snapshot sidecar %q: %w", name, err)
		}
	}
	if err = unix.Unlinkat(int(root.Fd()), "sqlite-work", unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("apps: remove snapshot SQLite work directory: %w", err)
	}
	if err = o.sync(ctx, data); err != nil {
		return err
	}
	return o.verifyBackupSQLite(ctx, node, root)
}
func (o backupOps) verifyBackupSQLite(ctx context.Context, node string, root *os.File) error {
	data, err := backupDir(root, "data", false)
	if err != nil {
		return err
	}
	defer data.Close()
	return o.runBackupNode(ctx, node, data, "verify")
}

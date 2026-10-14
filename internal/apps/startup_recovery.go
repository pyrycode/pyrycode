package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func recoveryError(p *publication) *LastError {
	if p.startupRecovery {
		if p.journal.ActivationCommitted {
			return nil
		}
		return &LastError{"app.unavailable", "Publication interrupted; pre-publication data recovered"}
	}
	return &LastError{"app.unavailable", "Update failed; previous release recovered"}
}

// Admission precedes reconciliation and any preparation that could write data.
// The caller holds the registry lock during owner construction.
func (o *Owner) admitStartupRecoveryLocked(r Record) {
	ctx, cancel := context.WithCancel(o.reconcileCtx)
	p := &publication{ctx: ctx, cancel: cancel, record: r, previous: r, journal: r.PublishJournal,
		done: make(chan struct{}), health: make(chan struct{}), startupRecovery: true, retiredOld: true}
	if p.journal.ActivationCommitted {
		p.previous.ActiveRelease = p.journal.Candidate.ReleaseVersion
	}
	p.unsubscribe = o.registry.subscribeLocked(func(c Change) {
		if c.AppID != r.AppID {
			return
		}
		// Completion may correct an old active pointer to the journal's candidate.
		// Its synchronous notification must not cancel the newly confirmed service.
		completed := c.Record != nil && p.journal.ActivationCommitted && c.Record.PublishJournal == nil && c.Record.ActiveRelease == p.journal.Candidate.ReleaseVersion
		if c.Record == nil || c.Record.Desired != r.Desired || c.Record.Manifest != r.Manifest || c.Record.ActiveRelease != r.ActiveRelease && !completed {
			p.invalidated = true
			cancel()
		}
	})
	o.publishing[r.AppID] = p
	go o.recoverStartup(r.AppID, p)
}

func (o *Owner) startupRecoveryCurrentLocked(app string, p *publication) error {
	r := o.currentLocked(app)
	if o.closed || p.ctx.Err() != nil || p.invalidated || o.removing[app] || o.publishing[app] != p || o.registry.uncertain != nil ||
		r.AppID == "" || r.Revision != p.record.Revision || r.Desired != p.record.Desired || r.RetirementPending ||
		!reflect.DeepEqual(r.PublishJournal, p.journal) || r.PublishJournal == nil {
		return ErrStale
	}
	return nil
}

func (o *Owner) recoverStartup(app string, p *publication) {
	defer func() {
		p.unsubscribe()
		p.cancel()
		o.registry.mu.Lock()
		o.mu.Lock()
		delete(o.publishing, app)
		close(p.done)
		o.mu.Unlock()
		o.registry.mu.Unlock()
		o.signal()
	}()
	var err error
	code := "app.io_failed"
	if !p.journal.ActivationCommitted {
		err = o.backupOps.restore(p.ctx, o.registry.root, p.record.Manifest.ServerID(), app, p.journal.VerifiedBackup, o.node)
	} else {
		code = "app.unavailable"
		// Committed recovery skips restoration's confinement checks. Require the
		// existing data directory and database without creating or following links.
		var data *os.File
		data, err = privateDir(filepath.Join(o.registry.root, app, "data"))
		if err == nil {
			var db *os.File
			db, err = regularAt(data, "app.sqlite", false)
			if db != nil {
				err = errors.Join(err, db.Close())
			}
			err = errors.Join(err, data.Close())
		}
	}
	if err == nil && p.previous.ActiveRelease != "" {
		code = "app.unavailable"
		if p.record.Desired == "available" {
			err = o.restartRecovery(app, p)
			if err == nil {
				return
			}
		} else {
			err = o.validateStoppedRecovery(p)
			if err == nil {
				err = o.backupOps.step(p.ctx, "recovery-validated")
			}
			var retirement *candidateRetirementError
			if errors.As(err, &retirement) {
				p.retirementErr = retirement
			}
		}
	}
	// A failed private launch must be completely retired before releasing ownership.
	if err != nil && p.g != nil {
		o.mu.Lock()
		p.g.retired = true
		p.g.cancel()
		o.mu.Unlock()
		<-p.g.done
	}
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	retirement := p.retirementErr
	if p.g != nil {
		retirement = errors.Join(retirement, p.g.cleanupErr)
	}
	if retirement != nil {
		o.failures = errors.Join(o.failures, retirement)
		r := o.currentLocked(app)
		if r.PublishJournal != nil && r.PublishJournal.VerifiedBackup == p.journal.VerifiedBackup {
			// A newer stop or metadata commit must not erase writer uncertainty.
			u := &lifecycleUpdate{serverID: r.Manifest.ServerID(), expected: r.Revision, metadata: func(r *Record) error { r.RetirementPending = true; return nil }}
			_, e := o.registry.changeLocked("lifecycle", app, nil, u)
			o.failures = errors.Join(o.failures, e)
		}
		return
	}
	if o.startupRecoveryCurrentLocked(app, p) != nil {
		return
	}
	writeErr := o.changePublicationLocked(app, p, p.ctx, func(r *Record) error {
		r.State = "failed"
		if r.Desired == "stopped" {
			r.State = "stopped"
		}
		r.LastError = &LastError{code, "Interrupted publication recovery is pending"}
		if err == nil {
			r.PublishJournal, r.PendingRelease = nil, ""
			r.LastError = recoveryError(p)
			if p.journal.ActivationCommitted {
				r.ActiveRelease = p.journal.Candidate.ReleaseVersion
			}
		}
		return nil
	})
	if writeErr != nil {
		o.failures = errors.Join(o.failures, writeErr)
	}
}

// Import the verified release's readiness checks, without its service or migrator.
// Read-only SQLite prevents validation from changing application data.
const stoppedRecoveryJS = `import {DatabaseSync} from 'node:sqlite';
import {schema} from './dist/service/database.js';
import {ledger,migrationFiles} from './dist/service/migrations.js';
const db=new DatabaseSync(process.env.PYRY_APP_DB_PATH,{readOnly:true,enableForeignKeyConstraints:true,defensive:true,allowExtension:false,timeout:1000});
try { schema(db); ledger(db,migrationFiles(),true); } finally { db.close(); }`

func (o *Owner) validateStoppedRecovery(p *publication) error {
	ctx, cancel := context.WithTimeout(p.ctx, 60*time.Second)
	defer cancel()
	dir, name, err := selectBuild(ctx, o.registry.root, p.previous)
	if err != nil {
		return err
	}
	_ = dir.Close()
	build, err := VerifyBuild(ctx, o.registry.root, p.previous.Manifest.ServerID(), p.previous.AppID, name)
	if err != nil {
		return err
	}
	private := p.previous
	private.Manifest = build.Manifest
	_, err = candidateCommandWithCleanup(ctx, filepath.Join(o.registry.root, private.AppID, "builds", name),
		serviceEnvironment(private, filepath.Join(o.registry.root, private.AppID, "data"), 1), o.node,
		[]string{"--input-type=module", "-e", stoppedRecoveryJS}, false, func(pid int) error { return candidateCleanupWithKill(pid, o.kill) })
	return err
}

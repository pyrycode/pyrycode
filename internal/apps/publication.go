package apps

import (
	"context"
	"errors"
	"path/filepath"
)

// PublishResult projects committed public fields without storage paths or history.
// Healthy confirms activation health; RecoveryPending forbids ordinary success.
type PublishResult struct {
	ServerID, AppID, CandidateRelease             string
	Lifecycle                                     Lifecycle
	Revision                                      uint64
	Healthy, ActivationCommitted, RecoveryPending bool
}

// PublishFailure carries static phase and recovery feedback, never diagnostics.
type PublishFailure struct{ Phase, Recovery string }

func (f *PublishFailure) Error() string { return "apps: publication " + f.Phase + ": " + f.Recovery }

type publication struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	unsubscribe         func()
	done, health        chan struct{}
	record              Record
	release             Release
	journal             *PublishJournal
	g                   *generation
	activated           bool
	failurePhase        string
	retirementErr       error
	activationUncertain bool
	previous            Record
	old                 *generation
	retiredOld          bool
	recovering          bool
	recovered           bool
	recoveryFeedback    string
	invalidated         bool // Protected by the registry mutex, including callbacks.
}

func publicPublication(server, app string, p *publication, r Record) PublishResult {
	return PublishResult{ServerID: server, AppID: app, CandidateRelease: p.release.ReleaseVersion,
		Lifecycle: Lifecycle{Title: r.Title, Desired: r.Desired, State: r.State, ActiveRelease: r.ActiveRelease, PendingRelease: r.PendingRelease, LastError: r.LastError},
		Revision:  r.Revision, Healthy: p.activated, ActivationCommitted: p.activated, RecoveryPending: r.recoveryPending()}
}

// Publish performs checked publication or update. Tools are selected by the daemon;
// Node must be the owner's executable. Success includes durable finalization.
func (o *Owner) Publish(ctx context.Context, serverID, appID string, tools Toolchain) (result PublishResult, err error) {
	p, err := o.admitPublication(ctx, serverID, appID, tools)
	if err != nil {
		return result, &PublishFailure{"admission", "Use a local registration without competing publication or recovery work."}
	}
	phase, mutated := "preparation", false
	detachShutdown := context.AfterFunc(o.reconcileCtx, p.cancel)
	defer func() {
		p.unsubscribe()
		detachShutdown()
		o.registry.mu.Lock()
		o.mu.Lock()
		delete(o.publishing, appID)
		close(p.done)
		o.mu.Unlock()
		o.registry.mu.Unlock()
		o.signal()
	}()
	defer func() {
		if err != nil {
			o.finishPublicationFailure(appID, p, mutated, phase)
		}
		o.registry.mu.Lock()
		o.mu.Lock()
		r := o.currentLocked(appID)
		result = publicPublication(serverID, appID, p, r)
		result.RecoveryPending = result.RecoveryPending || o.registry.uncertain != nil
		result.Healthy = p.activated && o.usableLocked(p.g)
		o.mu.Unlock()
		o.registry.mu.Unlock()
		if err != nil {
			recovery := "Candidate failed; pre-publication data is retained."
			if result.RecoveryPending {
				recovery = "Recovery is pending; retry is barred."
			}
			if result.ActivationCommitted {
				recovery = "Activation committed; journal finalization is pending."
			}
			if p.activationUncertain {
				recovery = "Activation durability is uncertain; recovery is pending."
			}
			if p.recoveryFeedback != "" && !result.ActivationCommitted && !p.activationUncertain {
				recovery = p.recoveryFeedback
			}
			err = &PublishFailure{phase, recovery}
		}
	}()
	defer p.cancel()
	p.release, err = o.registry.PrepareCandidate(p.ctx, serverID, appID, tools)
	if err != nil {
		var failure *CandidateFailure
		if errors.As(err, &failure) {
			phase = failure.Phase
		}
		return result, err
	}
	phase = "verification"
	build, e := VerifyBuild(p.ctx, o.registry.root, serverID, appID, p.release.BuildID)
	if e != nil || build.Manifest.ReleaseVersion() != p.release.ReleaseVersion || p.release.BuildRoot != filepath.Join(o.registry.root, appID, "builds", build.BuildID) {
		return result, ErrInvalidStorage
	}
	// Preparation changes only history. Accept its revision while rejecting other
	// lifecycle or manifest changes made during the build.
	o.registry.mu.Lock()
	o.mu.Lock()
	r := o.currentLocked(appID)
	if e = o.publicationCurrentLocked(appID, p, false); e == nil && (r.Title != p.record.Title || r.State != p.record.State || r.PendingRelease != p.record.PendingRelease) {
		e = ErrStale
	}
	if e == nil {
		p.record = r
		if p.previous.ActiveRelease != "" && p.release.ReleaseVersion == r.ActiveRelease && o.usableLocked(p.old) {
			p.activated, p.g = true, p.old
			o.mu.Unlock()
			o.registry.mu.Unlock()
			return result, nil
		}
		if p.previous.ActiveRelease != "" && !newerCandidate(p.release.ReleaseVersion, r.ActiveRelease) {
			e = ErrConflict
		}
	}
	if e == nil {
		e = o.changePublicationLocked(appID, p, p.ctx, func(r *Record) error {
			r.PendingRelease = p.release.ReleaseVersion
			r.State = "starting"
			r.LastError = nil
			r.RetirementPending = p.previous.ActiveRelease != ""
			return nil
		})
	}
	o.mu.Unlock()
	o.registry.mu.Unlock()
	if e != nil {
		return result, e
	}
	phase = "retirement"
	if e = o.retirePrevious(p); e != nil {
		return result, e
	}
	phase = "snapshot"
	id, e := mintAppID()
	if e != nil {
		return result, e
	}
	backup, e := o.backupOps.snapshot(p.ctx, o.registry.root, serverID, appID, id, o.node)
	if e != nil {
		return result, e
	}
	if e = o.backupOps.verify(p.ctx, o.registry.root, serverID, appID, backup, o.node); e != nil {
		return result, e
	}
	phase = "journal"
	p.journal = &PublishJournal{Candidate: ReleaseReference{serverID, appID, p.release.ReleaseVersion}, VerifiedBackup: backup}
	if p.previous.ActiveRelease != "" {
		p.journal.Previous = &ReleaseReference{serverID, appID, p.previous.ActiveRelease}
	}
	if e = o.changePublication(appID, p, func(r *Record) error {
		r.PublishJournal, r.RetirementPending = p.journal, false
		return nil
	}); e != nil {
		return result, e
	}
	phase = "migration"
	if e = o.checkPublication(appID, p); e != nil {
		return result, e
	}
	// prepareRelease can create database files, so even its failure needs restore.
	mutated = true
	private := p.record
	private.Manifest = build.Manifest
	private.ActiveRelease = p.release.ReleaseVersion
	selected, data, e := prepareRelease(p.ctx, o.registry.root, private, o.node)
	if e != nil || selected != p.release.BuildRoot {
		return result, ErrInvalidStorage
	}
	listener, port, e := o.reserve(appID)
	if e != nil {
		return result, e
	}
	defer func() {
		listener.Close()
		o.mu.Lock()
		if p.g == nil {
			delete(o.ports, port)
		}
		o.mu.Unlock()
	}()
	migration, cancel := context.WithTimeout(p.ctx, o.migrateTimeout)
	_, e = candidateCommandWithCleanup(migration, selected, serviceEnvironment(private, data, port), o.node, []string{"dist/service/migrate.js"}, false, func(pid int) error {
		return candidateCleanupWithKill(pid, o.kill)
	})
	cancel()
	if e != nil {
		var retirement *candidateRetirementError
		if errors.As(e, &retirement) {
			p.retirementErr = retirement
			o.remember(retirement)
		}
		return result, e
	}
	phase = "health"
	if e = o.launchPublication(appID, p, private, selected, data, listener, port); e != nil {
		return result, e
	}
	select {
	case <-p.health:
	case <-p.g.done:
		o.mu.Lock()
		if p.failurePhase != "" {
			phase = p.failurePhase
		}
		o.mu.Unlock()
		return result, ErrUnavailable
	case <-p.ctx.Done():
		return result, p.ctx.Err()
	}
	phase = "finalization"
	if e = o.changePublication(appID, p, func(r *Record) error { r.PublishJournal = nil; return nil }); e != nil {
		return result, e
	}
	return result, nil
}

func (o *Owner) admitPublication(ctx context.Context, server, app string, tools Toolchain) (*publication, error) {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if !validID(server) || !validID(app) {
		return nil, ErrInvalidIdentity
	}
	if server != o.registry.snapshot.ServerID {
		return nil, ErrForeignHost
	}
	if o.closed || ctx.Err() != nil {
		return nil, ErrClosed
	}
	if o.registry.uncertain != nil {
		return nil, o.registry.uncertain
	}
	r := o.currentLocked(app)
	if r.AppID == "" {
		return nil, ErrNotFound
	}
	if tools.Node != o.node || !filepath.IsAbs(tools.NPM) {
		return nil, ErrInvalidStorage
	}
	if o.publishing[app] != nil || o.removing[app] || r.recoveryPending() {
		return nil, ErrConflict
	}
	if g := o.generations[app]; r.ActiveRelease == "" && g != nil && !generationDone(g) {
		return nil, ErrConflict
	}
	r, _, e := o.registry.commitObservationLocked(server, app, r.Revision, Observation{Desired: "available"})
	if e != nil {
		return nil, e
	}
	work, cancel := context.WithCancel(ctx)
	p := &publication{ctx: work, cancel: cancel, done: make(chan struct{}), health: make(chan struct{}), record: r}
	p.previous, p.old = r, o.generations[app]
	// Register cancellation with admission held: even stop then available under
	// the same identity cannot pass unobserved before preparation starts.
	manifest := r.Manifest
	p.unsubscribe = o.registry.subscribeLocked(func(c Change) {
		if c.AppID == app && (c.Record == nil || c.Record.Desired != "available" || c.Record.Manifest != manifest) {
			p.invalidated = true
			cancel()
		}
	})
	o.publishing[app] = p
	return p, nil
}

// publicationCurrentLocked checks operation ownership and live intent. Callers
// holding registry then owner locks can include the exact expected revision.
func (o *Owner) publicationCurrentLocked(app string, p *publication, revision bool) error {
	r := o.currentLocked(app)
	if o.closed || p.ctx.Err() != nil || o.publishing[app] != p || o.removing[app] || r.AppID == "" || r.Desired != "available" || r.Manifest != p.record.Manifest || r.ActiveRelease != p.record.ActiveRelease || revision && r.Revision != p.record.Revision {
		return ErrStale
	}
	return nil
}
func (o *Owner) checkPublication(app string, p *publication) error {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.publicationCurrentLocked(app, p, true)
}
func (o *Owner) changePublication(app string, p *publication, mutate func(*Record) error) error {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if e := o.publicationCurrentLocked(app, p, true); e != nil {
		return e
	}
	return o.changePublicationLocked(app, p, p.ctx, mutate)
}
func (o *Owner) changePublicationLocked(app string, p *publication, ctx context.Context, mutate func(*Record) error) error {
	u := &lifecycleUpdate{serverID: p.record.Manifest.ServerID(), expected: p.record.Revision, ctx: ctx, metadata: mutate}
	_, e := o.registry.changeLocked("lifecycle", app, nil, u)
	if e == nil {
		p.record = u.result
	}
	return e
}

func (o *Owner) finishPublicationFailure(app string, p *publication, mutated bool, phase string) {
	if p.previous.ActiveRelease != "" {
		o.finishUpdateFailure(app, p, mutated, phase)
		return
	}
	o.mu.Lock()
	activated := p.activated
	g := p.g
	if !activated && g != nil {
		g.retired = true
		g.cancel()
	}
	o.mu.Unlock()
	if activated {
		return
	} // The durable decision forbids restoring old data.
	recoveryErr := p.retirementErr
	if g != nil {
		<-g.done
		recoveryErr = errors.Join(recoveryErr, g.cleanupErr)
	}
	o.registry.mu.Lock()
	recoveryErr = errors.Join(recoveryErr, o.registry.uncertain)
	o.registry.mu.Unlock()
	if mutated && recoveryErr == nil {
		recoveryErr = RestoreData(context.Background(), o.registry.root, p.record.Manifest.ServerID(), app, p.journal.VerifiedBackup, o.node)
	}
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	r := o.currentLocked(app)
	if r.AppID == "" || o.publishing[app] != p || r.ActiveRelease != "" {
		return
	}
	// Recovery preserves newer intent and metadata, but can complete only this
	// journal. A failed restoration leaves its evidence for a future recovery.
	if r.PublishJournal != nil && (p.journal == nil || r.PublishJournal.VerifiedBackup.PublishID != p.journal.VerifiedBackup.PublishID) {
		return
	}
	u := &lifecycleUpdate{serverID: r.Manifest.ServerID(), expected: r.Revision, metadata: func(r *Record) error {
		if recoveryErr == nil {
			r.PublishJournal = nil
			r.PendingRelease = ""
		}
		if r.Desired == "stopped" {
			r.State = "stopped"
		} else {
			r.State = "failed"
		}
		code, message := "app.unavailable", "First publication failed during "+phase
		if recoveryErr != nil {
			code, message = "app.io_failed", "Publication data recovery is pending"
		}
		r.LastError = &LastError{code, message}
		return nil
	}}
	_, e := o.registry.changeLocked("lifecycle", app, nil, u)
	if e != nil && recoveryErr == nil && r.PublishJournal != nil {
		// Recovery is complete, but journal clearing did not persist. Keep that
		// evidence and report failure while admission remains barred.
		u.metadata = func(record *Record) error {
			if record.Desired == "stopped" {
				record.State = "stopped"
			} else {
				record.State = "failed"
			}
			record.LastError = &LastError{"app.io_failed", "Publication recovery finalization is pending"}
			return nil
		}
		_, e = o.registry.changeLocked("lifecycle", app, nil, u)
	}
	if e == nil {
		p.record = u.result
	}
	// Failed finalization retains the previously durable journal; Publish reports
	// recovery pending from that committed state rather than leaking the I/O error.
}

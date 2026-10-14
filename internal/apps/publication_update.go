package apps

import (
	"context"
	"errors"
	"path/filepath"
)

func (o *Owner) retirePrevious(p *publication) error {
	o.mu.Lock()
	p.retiredOld = true
	g := p.old
	if g != nil {
		g.retired = true
		g.cancel()
	}
	o.mu.Unlock()
	if g == nil {
		return nil
	}
	<-g.done
	err := g.cleanupErr
	if g.cmd != nil && g.cmd.Process != nil {
		err = errors.Join(err, candidateCleanupWithKill(g.cmd.Process.Pid, o.kill))
	}
	p.retirementErr = err
	return err
}

// Recovery ignores cancellation of the candidate, but never newer intent,
// ownership, removal or shutdown. All callers hold registry then owner locks.
func (o *Owner) recoveryCurrentLocked(app string, p *publication) error {
	r := o.currentLocked(app)
	if o.closed || p.invalidated || o.removing[app] || o.publishing[app] != p || r.AppID == "" || r.Desired != "available" || r.ActiveRelease != p.previous.ActiveRelease || r.Manifest != p.record.Manifest || r.Revision != p.record.Revision || o.registry.uncertain != nil {
		return ErrStale
	}
	return nil
}

func (o *Owner) confirmRecoveryLocked(app string, g *generation) (Record, error) {
	p := g.publication
	if e := o.recoveryCurrentLocked(app, p); e != nil {
		return Record{}, e
	}
	if e := o.changePublicationLocked(app, p, g.ctx, func(r *Record) error {
		if r.PublishJournal != nil && (p.journal == nil || r.PublishJournal.VerifiedBackup.PublishID != p.journal.VerifiedBackup.PublishID || r.PublishJournal.ActivationCommitted) {
			return ErrStale
		}
		r.State, r.PendingRelease, r.PublishJournal = "running", "", nil
		r.LastError = &LastError{"app.unavailable", "Update failed; previous release recovered"}
		return nil
	}); e != nil {
		return Record{}, e
	}
	p.recovered = true
	close(p.health)
	return p.record, nil
}

func (o *Owner) restartPrevious(app string, p *publication) error {
	private := p.record
	private.ActiveRelease = p.previous.ActiveRelease
	// Select the existing predecessor, then independently verify its sealed bytes.
	dir, name, e := selectBuild(context.Background(), o.registry.root, private)
	if e != nil {
		return e
	}
	_ = dir.Close()
	build, e := VerifyBuild(context.Background(), o.registry.root, private.Manifest.ServerID(), app, name)
	if e != nil {
		return e
	}
	private.Manifest = build.Manifest
	listener, port, e := o.reserve(app)
	if e != nil {
		return e
	}
	defer listener.Close()
	o.mu.Lock()
	p.recovering = true
	o.mu.Unlock()
	if e = o.launchPublication(app, p, private, filepath.Join(o.registry.root, app, "builds", name), filepath.Join(o.registry.root, app, "data"), listener, port); e != nil {
		return e
	}
	select {
	case <-p.health:
		o.registry.mu.Lock()
		o.mu.Lock()
		usable := o.usableLocked(p.g)
		o.mu.Unlock()
		o.registry.mu.Unlock()
		if !usable {
			return ErrUnavailable
		}
		return nil
	case <-p.g.done:
		return ErrUnavailable
	case <-o.reconcileCtx.Done():
		p.g.cancel()
		<-p.g.done
		return ErrClosed
	}
}

func (o *Owner) finishUpdateFailure(app string, p *publication, mutated bool, phase string) {
	o.mu.Lock()
	activated, g := p.activated, p.g
	if !activated && g != nil {
		g.retired = true
		g.cancel()
	}
	o.mu.Unlock()
	if activated {
		return
	} // The durable activation decision is authoritative.
	recoveryErr := p.retirementErr
	if g != nil {
		<-g.done
		recoveryErr = errors.Join(recoveryErr, g.cleanupErr)
	}
	o.registry.mu.Lock()
	recoveryErr = errors.Join(recoveryErr, o.registry.uncertain)
	o.registry.mu.Unlock()
	if mutated && recoveryErr == nil {
		recoveryErr = RestoreData(context.Background(), o.registry.root, p.previous.Manifest.ServerID(), app, p.journal.VerifiedBackup, o.node)
		if recoveryErr != nil {
			p.recoveryFeedback = "Restoration failed; previous release is unavailable."
		}
	}
	o.registry.mu.Lock()
	o.mu.Lock()
	r := o.currentLocked(app)
	if r.AppID == "" || o.publishing[app] != p || r.ActiveRelease != p.previous.ActiveRelease || r.PublishJournal != nil && (p.journal == nil || r.PublishJournal.VerifiedBackup.PublishID != p.journal.VerifiedBackup.PublishID || r.PublishJournal.ActivationCommitted) {
		o.mu.Unlock()
		o.registry.mu.Unlock()
		return
	}
	p.record = r
	retained := !p.retiredOld && o.usableLocked(p.old)
	restart := recoveryErr == nil && p.retiredOld && o.recoveryCurrentLocked(app, p) == nil
	o.mu.Unlock()
	o.registry.mu.Unlock()
	if restart {
		recoveryErr = o.restartPrevious(app, p)
		if recoveryErr == nil {
			p.recoveryFeedback = "Update failed; unchanged previous release health confirmed."
			if mutated {
				p.recoveryFeedback = "Previous data restored and previous release health confirmed."
			}
			return
		}
		if errors.Is(recoveryErr, ErrUnavailable) {
			p.recoveryFeedback = "Previous release health is unconfirmed; recovery is pending."
		}
		if p.g != nil {
			o.mu.Lock()
			p.g.retired = true
			p.g.cancel()
			o.mu.Unlock()
			<-p.g.done
		}
	}
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	r = o.currentLocked(app)
	if r.AppID == "" || r.ActiveRelease != p.previous.ActiveRelease {
		return
	}
	state := "failed"
	if r.Desired == "stopped" {
		state = "stopped"
	} else if retained && recoveryErr == nil {
		state = "running"
	}
	code, message := "app.unavailable", "Update failed during "+phase
	if recoveryErr != nil {
		code, message = "app.io_failed", "Update recovery is unconfirmed"
		if errors.Is(recoveryErr, ErrUnavailable) {
			code, message = "app.unavailable", "Previous release health could not be confirmed"
		}
		o.retryLocked(app).suppressed = true
	}
	clear := recoveryErr == nil && !restart
	e := o.changePublicationLocked(app, p, context.Background(), func(r *Record) error {
		r.State, r.LastError = state, &LastError{code, message}
		if clear {
			r.PublishJournal, r.PendingRelease = nil, ""
		}
		return nil
	})
	if e != nil {
		o.retryLocked(app).suppressed = true
		p.recoveryFeedback = "Recovery is unconfirmed; retry is barred."
	} else if retained && recoveryErr == nil {
		p.recoveryFeedback = "Update failed; previous release retained and healthy."
	} else if recoveryErr == nil {
		p.recoveryFeedback = "Previous data retained; service remains stopped."
	} else if p.recoveryFeedback == "" {
		p.recoveryFeedback = "Recovery is unconfirmed; retry is barred."
	}
}

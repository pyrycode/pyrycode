package apps

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func serviceEnvironment(r Record, data string, port int) []string {
	return []string{"PYRY_APP_ID=" + r.AppID, "PYRY_SERVER_ID=" + r.Manifest.ServerID(), "PYRY_APP_RELEASE=" + r.ActiveRelease, "PYRY_APP_DATA_DIR=" + data, "PYRY_APP_DB_PATH=" + data + "/app.sqlite", "PYRY_APP_BIND_HOST=127.0.0.1", "PYRY_APP_PORT=" + strconv.Itoa(port), "NODE_ENV=production"}
}

func (o *Owner) launchPublication(app string, p *publication, private Record, data string, listener net.Listener, port int) error {
	o.registry.mu.Lock()
	o.mu.Lock()
	e := o.publicationCurrentLocked(app, p, true)
	if e == nil {
		work, cancel := context.WithCancel(o.reconcileCtx)
		g := &generation{ctx: work, cancel: cancel, record: private, port: port, exited: make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{}), publication: p}
		g.cmd = exec.Command(o.node, "dist/service/main.js")
		g.cmd.Dir = p.release.BuildRoot
		g.cmd.Env = serviceEnvironment(private, data, port)
		g.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		e = listener.Close()
		if e == nil {
			e = g.cmd.Start()
		}
		if e == nil {
			p.g = g
			o.generations[app] = g
			go o.supervisePublication(app, p, g, time.Now())
		} else {
			cancel()
		}
	}
	if e != nil {
		delete(o.ports, port)
	}
	o.mu.Unlock()
	o.registry.mu.Unlock()
	return e
}

func (o *Owner) supervisePublication(app string, p *publication, g *generation, spawned time.Time) {
	defer o.completed(app, g)
	defer func() { o.mu.Lock(); delete(o.ports, g.port); o.mu.Unlock() }()
	detach := context.AfterFunc(p.ctx, func() {
		o.mu.Lock()
		if !p.activated {
			g.cancel()
		}
		o.mu.Unlock()
	})
	defer detach()
	go func() { _ = g.cmd.Wait(); close(g.exited) }()
	close(g.ready)
	reason := o.supervise(g.ctx, app, g, spawned)
	o.mu.Lock()
	g.healthy = false
	activated := p.activated
	cancelled := g.ctx.Err() != nil
	g.cancel()
	o.mu.Unlock()
	if activated && reason != "" && !cancelled {
		_, _, e := o.observeFailure(app, g, reason)
		o.rememberObservation(e)
	}
	g.cleanupErr = errors.Join(o.cleanup(g.cmd.Process.Pid), candidateCleanup(g.cmd.Process.Pid))
	<-g.exited
}

// activatePublicationLocked shares registry then owner locks with health's final
// liveness check. The journal decision and public release are one durable write.
func (o *Owner) activatePublicationLocked(app string, g *generation) (Record, error) {
	p := g.publication
	if e := o.publicationCurrentLocked(app, p, true); e != nil {
		return Record{}, e
	}
	e := o.changePublicationLocked(app, p, p.ctx, func(r *Record) error {
		if r.PublishJournal == nil || r.PublishJournal.VerifiedBackup.PublishID != p.journal.VerifiedBackup.PublishID {
			return ErrStale
		}
		r.Title = g.record.Manifest.Title()
		r.ActiveRelease = p.release.ReleaseVersion
		r.PendingRelease = ""
		r.LastError = nil
		r.State = "running"
		r.PublishJournal.ActivationCommitted = true
		return nil
	})
	if e != nil {
		p.failurePhase = "activation"
		return Record{}, e
	}
	p.activated = true
	close(p.health)
	return p.record, nil
}

func (o *Owner) removeRegistration(app string) (bool, error) {
	if !validID(app) {
		return false, ErrInvalidIdentity
	}
	o.registry.mu.Lock()
	o.mu.Lock()
	if o.removing[app] {
		o.mu.Unlock()
		o.registry.mu.Unlock()
		return false, ErrConflict
	}
	o.removing[app] = true
	o.mu.Unlock()
	o.registry.mu.Unlock()
	defer func() { o.mu.Lock(); delete(o.removing, app); o.mu.Unlock() }()
	if e := o.Stop(app); e != nil && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrRemoved) {
		return false, e
	}
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	r := o.currentLocked(app)
	if r.PublishJournal != nil {
		return false, ErrConflict
	}
	return o.registry.changeLocked("remove", app, nil, nil)
}

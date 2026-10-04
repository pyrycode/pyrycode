package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/update"
)

// Auto-update defaults (#2716). The startup delay lets a freshly started daemon
// settle before its first request; the interval follows a completed check; the
// quiet window is how long every session must have been untouched before the
// daemon counts as idle.
const (
	autoUpdateStartDelay  = 2 * time.Minute
	autoUpdateInterval    = 4 * time.Hour
	autoUpdateQuietWindow = 15 * time.Minute
	// autoUpdateRestartPoll is how often a selected release re-asks for idle
	// before installation and again before restarting.
	autoUpdateRestartPoll = time.Minute
	// autoUpdateMaxLoggedTag bounds the network-supplied tag in the log line.
	autoUpdateMaxLoggedTag = 64
)

// daemonIdle reports whether the daemon may be restarted without interrupting
// anyone: no conversation has a turn open and no session was active within
// quiet of now. A connected phone is deliberately not an input — a phone left
// connected overnight would otherwise hold every update off forever.
func daemonIdle(anyTurnOpen bool, lastActive []time.Time, now time.Time, quiet time.Duration) bool {
	if anyTurnOpen {
		return false
	}
	cutoff := now.Add(-quiet)
	for _, t := range lastActive {
		if t.After(cutoff) {
			return false
		}
	}
	return true
}

// autoUpdater checks the latest release on a schedule and, when the daemon is
// idle, installs an eligible one through installRelease — the same signature,
// checksum and AtomicReplace path `pyry update` uses — and restarts the managed
// unit. Explicit requests share the same worker even with scheduling disabled.
type autoUpdater struct {
	opts       updateOptions
	idle       func() bool
	logger     *slog.Logger
	startDelay time.Duration
	interval   time.Duration
	// restartPoll is how often installation and restart re-ask idle.
	restartPoll time.Duration
	// wait optionally controls duration waits; nil uses context-aware timers.
	wait func(context.Context, time.Duration) bool

	mu            sync.Mutex
	active        *updateAttempt
	installedDone chan struct{}
	workers       sync.WaitGroup
}

// updateAttempt publishes its immutable decision before work can install.
// Terminal fields are read only after done closes.
type updateAttempt struct {
	decided, done chan struct{}
	result        control.UpdateWhenIdleResult
	err           error
	installed     bool
}

func (p *updateAttempt) decide(result control.UpdateWhenIdleResult, err error) {
	p.result, p.err = result, err
	if result.Decision == control.UpdateWillInstall {
		close(p.decided)
	}
}

// begin is the sole attempt producer, shared by explicit and scheduled checks.
func (a *autoUpdater) begin(ctx context.Context) *updateAttempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.installedDone == nil {
		a.installedDone = make(chan struct{})
	}
	if a.active != nil {
		return a.active
	}
	p := &updateAttempt{decided: make(chan struct{}), done: make(chan struct{})}
	a.active = p
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		p.installed = a.perform(ctx, p)
		a.mu.Lock()
		if !p.installed {
			a.active = nil
		} else {
			close(a.installedDone)
		}
		// A rejected/failed check can be retried as soon as its caller returns.
		if p.result.Decision != control.UpdateWillInstall {
			close(p.decided)
		}
		close(p.done)
		a.mu.Unlock()
	}()
	return p
}

// request uses the daemon context, never the requesting connection's lifetime.
// Always read the published decision, even if an immediate restart cancelled ctx.
func (a *autoUpdater) request(ctx context.Context) (control.UpdateWhenIdleResult, error) {
	p := a.begin(ctx)
	<-p.decided
	return p.result, p.err
}

// join is called after control handlers and the scheduler have stopped producing.
func (a *autoUpdater) join() { a.workers.Wait() }

// Run checks once after startDelay and waits interval after each unsuccessful
// completed check. It returns nil when ctx is done, and also after a check that
// installed a release: a second install could lose the rollback copy in pyry.prev.
func (a *autoUpdater) Run(ctx context.Context) error {
	a.mu.Lock()
	if a.installedDone == nil {
		a.installedDone = make(chan struct{})
	}
	installedDone := a.installedDone
	a.mu.Unlock()
	waitCtx, cancel := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-installedDone:
			cancel()
		case <-waitCtx.Done():
		}
	}()
	defer func() { cancel(); <-watchDone }()
	delay := a.startDelay
	for a.waitFor(waitCtx, delay) {
		if a.check(ctx) {
			return nil
		}
		delay = a.interval
	}
	return nil
}

// check reports whether a release was installed. A busy eligible check logs one
// waiting_for_idle entry, then a terminal outcome unless the wait is cancelled.
func (a *autoUpdater) check(ctx context.Context) bool {
	p := a.begin(ctx)
	<-p.done
	return p.installed
}

func (a *autoUpdater) perform(ctx context.Context, p *updateAttempt) bool {
	if err := ctx.Err(); err != nil {
		p.decide(control.UpdateWhenIdleResult{}, err)
		return false
	}
	o := a.opts
	attrs := []any{"current", o.currentVersion}
	logOutcome := func(level slog.Level, outcome string, extra ...any) {
		args := append([]any{"outcome", outcome}, attrs...)
		a.logger.Log(ctx, level, "auto-update check", append(args, extra...)...)
	}

	target := o.executablePath()
	if strings.HasPrefix(target, "/opt/homebrew/") {
		logOutcome(slog.LevelInfo, "skipped", "reason", "homebrew install")
		p.decide(control.UpdateWhenIdleResult{Decision: control.UpdateNotEligible, Reason: "homebrew install"}, nil)
		return false
	}
	argv := update.DetectRestartCommand(o.probeRestart())
	if argv == nil {
		logOutcome(slog.LevelInfo, "skipped", "reason", "no managed unit")
		p.decide(control.UpdateWhenIdleResult{Decision: control.UpdateNotEligible, Reason: "no managed unit"}, nil)
		return false
	}

	body, err := o.fetcher.FetchLatestRelease(ctx, o.repo)
	if err != nil {
		logOutcome(slog.LevelWarn, "failed", "err", err)
		p.decide(control.UpdateWhenIdleResult{}, err)
		return false
	}
	rel, err := update.ParseRelease(body)
	if err != nil {
		logOutcome(slog.LevelWarn, "failed", "err", err)
		p.decide(control.UpdateWhenIdleResult{}, err)
		return false
	}
	attrs = append(attrs, "latest", truncateTag(rel.Tag))

	switch err := update.Eligible(o.currentVersion, rel); {
	case errors.Is(err, update.ErrUpToDate):
		logOutcome(slog.LevelInfo, "up_to_date")
		p.decide(control.UpdateWhenIdleResult{Decision: control.UpdateUpToDate}, nil)
		return false
	case err != nil:
		logOutcome(slog.LevelInfo, "skipped", "reason", err.Error())
		p.decide(control.UpdateWhenIdleResult{Decision: control.UpdateNotEligible, Reason: err.Error()}, nil)
		return false
	}

	p.decide(control.UpdateWhenIdleResult{Decision: control.UpdateWillInstall, ReleaseTag: rel.Tag}, nil)
	if !a.waitUntilIdle(ctx, func() { logOutcome(slog.LevelInfo, "waiting_for_idle") }) {
		return false
	}
	if err := installRelease(ctx, o, target, rel.Tag); err != nil {
		logOutcome(slog.LevelWarn, "failed", "err", err)
		return false
	}
	// Logged before the restart is issued: the restart ends this process.
	logOutcome(slog.LevelInfo, "installed")
	a.restart(ctx, argv)
	return true
}

// restart waits for the daemon to be idle again — a turn may have opened while
// the release downloaded — and then hands the restart to the service manager.
// The manager's SIGTERM is what cancels ctx, and it follows the manager's
// acceptance of the request, so the restart command being killed then cannot
// cancel the restart.
func (a *autoUpdater) restart(ctx context.Context, argv []string) {
	if !a.waitUntilIdle(ctx, nil) {
		return
	}
	if err := a.opts.runRestart(ctx, argv); err != nil {
		a.logger.Error("auto-update restart failed", "err", err)
	}
}

// waitUntilIdle calls onWait once when entering a busy wait. Cancellation is
// checked before each idle poll and again before permitting the caller to act.
func (a *autoUpdater) waitUntilIdle(ctx context.Context, onWait func()) bool {
	for ctx.Err() == nil {
		if a.idle() {
			return ctx.Err() == nil
		}
		if onWait != nil {
			onWait()
			onWait = nil
		}
		if !a.waitFor(ctx, a.restartPoll) {
			return false
		}
	}
	return false
}

// waitFor makes cancellation take precedence when a timer and ctx are both ready.
func (a *autoUpdater) waitFor(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if a.wait != nil {
		return a.wait(ctx, d) && ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// truncateTag bounds a network-supplied tag before it reaches a log line.
func truncateTag(tag string) string {
	if len(tag) > autoUpdateMaxLoggedTag {
		return tag[:autoUpdateMaxLoggedTag]
	}
	return tag
}

// newAutoUpdater builds the daemon's updater over the same production seams
// `pyry update` uses, with progress discarded: the daemon reports through
// structured check outcomes instead.
func newAutoUpdater(idle func() bool, logger *slog.Logger) (*autoUpdater, error) {
	o, err := productionUpdateOptions(io.Discard)
	if err != nil {
		return nil, err
	}
	return &autoUpdater{
		opts:        o,
		idle:        idle,
		logger:      logger,
		startDelay:  autoUpdateStartDelay,
		interval:    autoUpdateInterval,
		restartPoll: autoUpdateRestartPoll,
	}, nil
}

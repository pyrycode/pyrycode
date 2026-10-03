package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/update"
)

// Auto-update defaults (#2716). The startup delay lets a freshly started daemon
// settle before its first request; the interval is how often it asks again; the
// quiet window is how long every session must have been untouched before the
// daemon counts as idle.
const (
	autoUpdateStartDelay  = 2 * time.Minute
	autoUpdateInterval    = 4 * time.Hour
	autoUpdateQuietWindow = 15 * time.Minute
	// autoUpdateRestartPoll is how often a swapped-in binary re-asks for idle
	// before restarting, when a turn opened while the release downloaded.
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
// unit. It is built only when the daemon runs with -pyry-auto-update.
type autoUpdater struct {
	opts       updateOptions
	idle       func() bool
	logger     *slog.Logger
	startDelay time.Duration
	interval   time.Duration
	// restartPoll is how often a finished install re-asks idle before restarting.
	restartPoll time.Duration
}

// Run checks once after startDelay and then every interval. It returns nil when
// ctx is done, and also after a check that installed a release: the process is
// about to be restarted, and a second install would overwrite pyry.prev with
// the build just installed, losing the rollback copy.
func (a *autoUpdater) Run(ctx context.Context) error {
	timer := time.NewTimer(a.startDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if a.check(ctx) {
			return nil
		}
		timer.Reset(a.interval)
	}
}

// check runs one check and ends in exactly one "auto-update check" log line. It
// reports whether a release was installed.
func (a *autoUpdater) check(ctx context.Context) (installed bool) {
	o := a.opts
	attrs := []any{"current", o.currentVersion}
	logOutcome := func(level slog.Level, outcome string, extra ...any) {
		args := append([]any{"outcome", outcome}, attrs...)
		a.logger.Log(ctx, level, "auto-update check", append(args, extra...)...)
	}

	target := o.executablePath()
	if strings.HasPrefix(target, "/opt/homebrew/") {
		logOutcome(slog.LevelInfo, "skipped", "reason", "homebrew install")
		return false
	}
	argv := update.DetectRestartCommand(o.probeRestart())
	if argv == nil {
		logOutcome(slog.LevelInfo, "skipped", "reason", "no managed unit")
		return false
	}

	body, err := o.fetcher.FetchLatestRelease(ctx, o.repo)
	if err != nil {
		logOutcome(slog.LevelWarn, "failed", "err", err)
		return false
	}
	rel, err := update.ParseRelease(body)
	if err != nil {
		logOutcome(slog.LevelWarn, "failed", "err", err)
		return false
	}
	attrs = append(attrs, "latest", truncateTag(rel.Tag))

	switch err := update.Eligible(o.currentVersion, rel); {
	case errors.Is(err, update.ErrUpToDate):
		logOutcome(slog.LevelInfo, "up_to_date")
		return false
	case err != nil:
		logOutcome(slog.LevelInfo, "skipped", "reason", err.Error())
		return false
	}

	if !a.idle() {
		logOutcome(slog.LevelInfo, "waiting_for_idle")
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
	for !a.idle() {
		t := time.NewTimer(a.restartPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	if err := a.opts.runRestart(ctx, argv); err != nil {
		a.logger.Error("auto-update restart failed", "err", err)
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
// `pyry update` uses, with progress discarded: the daemon reports through its
// one log line per check instead.
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

package ptyrunner

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun/streamjson"
)

// runWatchdog delegates the per-tick spinner-parse + tracker-observe + wedge-
// check loop to sess.RunWatchdog (the method reaches the session's buffer
// internally) and maps a non-nil return into the pyrycode-specific side
// effects: log the wedge error, set ExitReasonError on the emitter, and cancel
// the run context. A nil return (ctx cancellation) is a no-op.
//
// Discipline: the only thing logged is the tuidriver-generated watchdog
// error string (which carries last-state + duration but no Event content).
func runWatchdog(
	ctx context.Context,
	sess *tuidriver.Session,
	tr *tuidriver.Tracker,
	emitter *streamjson.Emitter,
	cancel context.CancelFunc,
	tick time.Duration,
	logger *slog.Logger,
) {
	if err := sess.RunWatchdog(ctx, tr, tuidriver.WatchdogOpts{Tick: tick}); err != nil {
		logger.Warn("ptyrunner: watchdog fired", "err", err)
		emitter.SetExitReason(streamjson.ExitReasonError)
		cancel()
	}
}

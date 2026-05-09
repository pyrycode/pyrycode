package conversations

import (
	"context"
	"log/slog"
	"time"
)

// SweepInterval is the production tick interval for RunSweepLoop. One hour is
// safe relative to archiveIdleThreshold (30 days): missing one tick delays an
// archive by one hour, which is harmless.
const SweepInterval = time.Hour

// RunSweepLoop ticks every interval and applies Sweep + (conditional) Save to
// reg. On each tick it calls Sweep(reg, time.Now()). When the returned count is
// non-zero it calls reg.Save(path); a successful Save logs at INFO with the
// archived count, a failed Save logs at ERROR and the loop continues to the
// next tick. A zero-count tick does NOT call Save (avoids fsync churn on every
// hour with no changes).
//
// Designed to be run as a goroutine inside an errgroup (or equivalent)
// supervising it via context cancellation. Returns nil on ctx cancellation.
// Does NOT perform a final on-shutdown sweep — by design; the next pyry start
// will run the next tick within SweepInterval.
//
// Save errors are ALWAYS non-fatal: a transient failure (e.g. disk briefly
// unwritable) must not bring down the daemon's errgroup. Tick frequency is
// hourly, so a single missed Save loses at most one hour of archived-count
// persistence; the next successful tick archives the same set again.
//
// Caller responsibilities:
//   - reg has been Loaded by the caller before this loop starts.
//   - path is the same path the caller will Load from on the next pyry start.
//   - log is non-nil (the pool's logger or slog.Default()).
//   - interval > 0 (callers in production pass SweepInterval; tests pass small
//     deterministic values). interval <= 0 panics via time.NewTicker.
func RunSweepLoop(ctx context.Context, reg *Registry, path string, interval time.Duration, log *slog.Logger) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			sweepOnce(reg, path, log)
		}
	}
}

// sweepOnce performs a single tick: Sweep + (conditional) Save + log. Factored
// out of RunSweepLoop so unit tests can drive the tick body deterministically
// without spinning a ticker.
func sweepOnce(reg *Registry, path string, log *slog.Logger) {
	n := Sweep(reg, time.Now())
	if n == 0 {
		return
	}
	if err := reg.Save(path); err != nil {
		log.Error("conversations: sweep save failed", "err", err, "archived", n)
		return
	}
	log.Info("conversations: archived idle conversations", "count", n)
}

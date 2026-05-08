package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// isTransientStartupError reports whether err matches the dial-error shape
// produced when the daemon is mid-restart: either the unix socket file
// does not exist yet (syscall.ENOENT) or the file exists but the daemon
// has not yet begun accepting connections (syscall.ECONNREFUSED).
//
// Returns false for nil, for any other syscall, and for higher-level
// failures (timeouts, EOF, protocol errors). #199 wires this into a
// bounded retry loop in the client dial path.
func isTransientStartupError(err error) bool {
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

// dialRetryBudget bounds the wall-clock time dialWithRetry spends
// re-trying transient startup errors before surfacing the most recent
// failure to the caller. Sized for the launchctl kickstart / pyry
// update self-restart window observed in production.
const dialRetryBudget = 1500 * time.Millisecond

// dialRetryInterval is the sleep between retry attempts. ~30 attempts
// fit inside the budget.
const dialRetryInterval = 50 * time.Millisecond

// dialFunc is the underlying dial primitive — netDialUnix in
// production, swappable in tests via dialWithRetry's fn argument.
type dialFunc func(ctx context.Context, socketPath string) (net.Conn, error)

// netDialUnix is the production dialFunc.
func netDialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath)
}

// dial connects to the daemon's control socket, retrying briefly on
// transient startup errors (ENOENT / ECONNREFUSED) so that client
// commands issued during the launchctl kickstart / pyry update
// self-restart window do not surface the socket-not-yet-bound race to
// users. Non-transient errors return immediately.
func dial(ctx context.Context, socketPath string) (net.Conn, error) {
	return dialWithRetry(ctx, socketPath, netDialUnix, dialRetryBudget, dialRetryInterval)
}

// dialWithRetry calls fn repeatedly while it returns a transient
// startup error, up to budget elapsed wall-clock time, polling every
// interval. The first attempt is immediate. A non-transient error
// surfaces immediately; budget exhaustion preserves the most recent
// transient error wrapped identically to a single-shot dial. ctx
// cancel during the inter-attempt sleep returns the most recent
// transient error wrapped (callers already chose the deadline).
func dialWithRetry(
	ctx context.Context,
	socketPath string,
	fn dialFunc,
	budget time.Duration,
	interval time.Duration,
) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DialTimeout)
		defer cancel()
	}

	deadline := time.Now().Add(budget)
	for {
		conn, err := fn(ctx, socketPath)
		if err == nil {
			return conn, nil
		}
		if !isTransientStartupError(err) {
			return nil, fmt.Errorf("dial %s: %w", socketPath, err)
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("dial %s: %w", socketPath, err)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("dial %s: %w", socketPath, err)
		case <-timer.C:
		}
	}
}

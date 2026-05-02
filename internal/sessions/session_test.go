package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// TestSession_State_DelegatesToSupervisor confirms that Session.State returns
// the underlying *supervisor.Supervisor's snapshot. We assert the
// pre-Run initial state — Phase=PhaseStarting, ChildPID=0 — which is what
// supervisor.New installs.
func TestSession_State_DelegatesToSupervisor(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	sess := pool.Default()
	st := sess.State()
	if st.Phase != supervisor.PhaseStarting {
		t.Errorf("State.Phase = %q, want %q", st.Phase, supervisor.PhaseStarting)
	}
	if st.ChildPID != 0 {
		t.Errorf("State.ChildPID = %d, want 0 before Run", st.ChildPID)
	}
}

// TestSession_Attach_NoBridge verifies that calling Attach on a session built
// without a bridge surfaces ErrAttachUnavailable.
func TestSession_Attach_NoBridge(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	sess := pool.Default()
	done, err := sess.Attach(strings.NewReader(""), io.Discard)
	if done != nil {
		t.Errorf("Attach (no bridge) returned non-nil done %v, want nil", done)
	}
	if !errors.Is(err, ErrAttachUnavailable) {
		t.Errorf("Attach (no bridge) err = %v, want ErrAttachUnavailable", err)
	}
}

// TestSession_Attach_DelegatesToBridge verifies the happy-path Attach
// delegates to supervisor.Bridge.Attach and that a busy second concurrent
// Attach surfaces supervisor.ErrBridgeBusy verbatim.
func TestSession_Attach_DelegatesToBridge(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, true)
	sess := pool.Default()

	// First Attach: hand it an io.Pipe whose writer we control. The bridge's
	// input pump will block on Read, keeping the attachment alive until we
	// close the writer in t.Cleanup.
	pr, pw := io.Pipe()
	done, err := sess.Attach(pr, io.Discard)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if done == nil {
		t.Fatal("first Attach returned nil done channel")
	}
	t.Cleanup(func() {
		_ = pw.Close() // unblock the input pump so the goroutine exits
		<-done
		_ = pr.Close()
	})

	// Second Attach must observe the busy bridge and surface supervisor's
	// sentinel verbatim (no wrap), so callers can keep using errors.Is with
	// supervisor.ErrBridgeBusy.
	done2, err := sess.Attach(strings.NewReader(""), io.Discard)
	if done2 != nil {
		t.Errorf("second Attach returned non-nil done, want nil")
	}
	if !errors.Is(err, supervisor.ErrBridgeBusy) {
		t.Errorf("second Attach err = %v, want supervisor.ErrBridgeBusy", err)
	}
}

// TestSession_Run_StopsOnContextCancel exercises the lifecycle delegation:
// Session.Run blocks on supervisor.Run, which returns context.Canceled when
// the surrounding ctx is cancelled. /bin/sleep stands in for the claude
// binary — the supervisor spawns it in a PTY, ctx cancellation tears it
// down, supervisor.Run returns ctx.Err() directly.
func TestSession_Run_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	pool := helperPoolWithSleepArgs(t)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()

	// Give the supervisor a moment to spawn the child before cancelling, so
	// we exercise the running-child cancellation path rather than the
	// pre-spawn ctx.Err() check.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after cancel")
	}
}

// helperPoolIdle builds a Pool whose bootstrap session runs `/bin/sleep 3600`
// with the supplied idle timeout. Backoff is shortened so the supervisor's
// PhaseStarting transitions happen quickly.
func helperPoolIdle(t *testing.T, idle time.Duration) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			IdleTimeout:    idle,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// pollUntil retries fn until it returns true or timeout elapses. Caller
// uses this to wait for the lifecycle goroutine to settle into a state.
func pollUntil(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestSession_IdleEvictionFires: with no clients attached and a short idle
// timeout, the lifecycle goroutine evicts the supervisor.
func TestSession_IdleEvictionFires(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 100*time.Millisecond)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict within 2s; state=%v", sess.LifecycleState())
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestSession_IdleEvictionDeferredWhileAttached: with attached>0, the timer
// re-arms instead of evicting. Detaching lets eviction proceed.
func TestSession_IdleEvictionDeferredWhileAttached(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bridge := supervisor.NewBridge(logger)
	cfg := Config{
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
			Bridge:         bridge,
			IdleTimeout:    80 * time.Millisecond,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger: logger,
	}
	pool, err := New(cfg)
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	pr, pw := io.Pipe()
	done, err := sess.Attach(pr, io.Discard)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// State should remain active for at least 4× the idle timeout while
	// the bridge is held attached.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if sess.LifecycleState() != stateActive {
			t.Fatalf("state changed while attached: %v", sess.LifecycleState())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Detach: close the writer so the bridge's input pump returns and
	// `attached` decrements. After that, eviction should fire promptly.
	_ = pw.Close()
	<-done
	_ = pr.Close()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict after detach; state=%v", sess.LifecycleState())
	}
}

// TestSession_ActivateRespawns: an evicted session moves back to active when
// Activate is called, and the supervisor re-enters PhaseRunning.
func TestSession_ActivateRespawns(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 80*time.Millisecond)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatal("session did not evict")
	}

	activateCtx, activateCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer activateCancel()
	if err := sess.Activate(activateCtx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got := sess.LifecycleState(); got != stateActive {
		t.Errorf("LifecycleState = %v, want active", got)
	}
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.State().Phase == supervisor.PhaseRunning
	}) {
		t.Errorf("supervisor did not re-enter PhaseRunning after Activate; phase=%v", sess.State().Phase)
	}
}

// TestSession_ActivateNoOpWhenActive: Activate on an already-active session
// returns immediately.
func TestSession_ActivateNoOpWhenActive(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 0) // eviction disabled
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()
	// Give the lifecycle goroutine a tick to enter runActive.
	time.Sleep(20 * time.Millisecond)

	deadline := time.Now().Add(500 * time.Millisecond)
	activateCtx, activateCancel := context.WithDeadline(context.Background(), deadline)
	defer activateCancel()
	if err := sess.Activate(activateCtx); err != nil {
		t.Errorf("Activate(active session) = %v, want nil", err)
	}
}

// TestSession_ActivateCtxCancellation: an already-cancelled ctx fails fast
// when the session is evicted.
func TestSession_ActivateCtxCancellation(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 80*time.Millisecond)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatal("session did not evict")
	}

	cancelled, cc := context.WithCancel(context.Background())
	cc()
	if err := sess.Activate(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("Activate(cancelled) err = %v, want context.Canceled", err)
	}
}

// TestSession_ShutdownFromActive: outer ctx cancel returns ctx.Err from Run
// when the session is active.
func TestSession_ShutdownFromActive(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 0) // eviction disabled
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestSession_ShutdownFromEvicted: outer ctx cancel returns ctx.Err from Run
// while the session is sitting in stateEvicted.
func TestSession_ShutdownFromEvicted(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 80*time.Millisecond)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatal("session did not evict")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return from evicted state")
	}
}

package sessions

import (
	"context"
	"errors"
	"io"
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

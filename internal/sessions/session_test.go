package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSession_State_DelegatesToSupervisor confirms that Session.State returns
// the underlying *supervisor.Supervisor's snapshot. We assert the
// pre-Run initial state — Phase=PhaseStarting, ChildPID=0 — which is what
// the runner installs.
func TestSession_State_DelegatesToSupervisor(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	sess := pool.Default()
	st := sess.State()
	if st.Phase != PhaseStarting {
		t.Errorf("State.Phase = %q, want %q", st.Phase, PhaseStarting)
	}
	if st.ChildPID != 0 {
		t.Errorf("State.ChildPID = %d, want 0 before Run", st.ChildPID)
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
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	// Bridge mode: callers run sess.Run, which spawns the bootstrap
	// supervisor. Foreground mode in a Run-reaching fixture is the deadlock
	// surface #41 surfaced.
	//
	// #839: the bootstrap now spawns with a trailing "--session-id <uuid>", so a
	// bare `/bin/sleep 3600` stand-in would exit immediately on the unknown flag
	// and crash-loop (lessons.md "Pool.Create appends --session-id"). The sh
	// stand-in ignores its positional args and execs a long sleep, restoring the
	// live long-lived child these fixtures had before the flag was appended.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		RunnerFactory: testRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     []string{"-c", "exec sleep 3600", "--"},
			IdleTimeout:    idle,
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

// TestSession_IdleEvictionFires: with no turn-busy signal (nil TurnBusy, the
// PTY path's shape) and a short idle timeout, the lifecycle goroutine evicts
// the supervisor.
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

// TestSession_IdleEviction_DefersWhileTurnBusy: while Config.TurnBusy reports
// the session busy, each idle-timer fire re-arms instead of evicting; once it
// reports idle, the next fire evicts with ReasonEviction (#1486).
func TestSession_IdleEviction_DefersWhileTurnBusy(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	var busy atomic.Bool
	busy.Store(true)
	var calls atomic.Int32
	var askedMu sync.Mutex
	var asked []SessionID
	pool, err := New(Config{
		RunnerFactory: testRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     []string{"-c", "exec sleep 3600", "--"},
			IdleTimeout:    50 * time.Millisecond,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		TurnBusy: func(id SessionID) bool {
			calls.Add(1)
			askedMu.Lock()
			asked = append(asked, id)
			askedMu.Unlock()
			return busy.Load()
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	rec := &transitionRecorder{}
	pool.SetTransitionObserver(rec.observe)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	// Vacuity guard: the timer must have fired, and been deferred, more than
	// once before the busy half says anything.
	if !pollUntil(t, 2*time.Second, func() bool { return calls.Load() >= 3 }) {
		t.Fatalf("TurnBusy consulted %d times within 2s, want >= 3", calls.Load())
	}
	if got := sess.LifecycleState(); got != stateActive {
		t.Fatalf("session left active while a turn was open; state=%v", got)
	}
	if n := rec.len(); n != 0 {
		t.Fatalf("observer fired %d times while busy, want 0", n)
	}
	askedMu.Lock()
	for _, id := range asked {
		if id != sess.ID() {
			t.Errorf("TurnBusy asked about %q, want %q", id, sess.ID())
		}
	}
	askedMu.Unlock()

	busy.Store(false)
	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict within 2s of the turn closing; state=%v", sess.LifecycleState())
	}
	got := rec.snapshot()
	if len(got) != 1 || got[0].Reason != ReasonEviction {
		t.Fatalf("transitions = %+v, want exactly one %q", got, ReasonEviction)
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
		return sess.State().Phase == PhaseRunning
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

// activateCancelledCalls is the sample size for the fail-fast assertion below.
// One call cannot decide it: before the entry guard, an already-cancelled ctx
// raced a closed activeCh in Activate's select and Go picks uniformly among
// ready arms, so a single call was a coin flip and a green verdict meant
// nothing. Over N calls a broken guard survives with probability 2^-N.
const activateCancelledCalls = 400

// TestSession_ActivateCancelledCtx_ActiveFailsFast: Activate with an
// already-cancelled ctx returns ctx.Err() on every call against a session that
// is active with its transition complete — the state a closed activeCh
// encodes by construction (see closedChan).
//
// Bare Session literal: no pool, no lifecycle goroutine, no child, nothing
// scheduler-dependent in either direction. The runner must be fakeRunner,
// whose WaitForPTY returns nil unconditionally exactly as the production
// (*streamsup.Runner).WaitForPTY does; lifecycleRunner's WaitForPTY is a
// second select on ctx.Done() and would mask the leak this test measures.
func TestSession_ActivateCancelledCtx_ActiveFailsFast(t *testing.T) {
	t.Parallel()
	sess := &Session{
		id:         "activate-cancelled-active",
		sup:        fakeRunner{},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		lcState:    stateActive,
		activeCh:   closedChan(),
		evictedCh:  make(chan struct{}),
		activateCh: make(chan struct{}, 1),
		evictCh:    make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	leaked, firstAt := 0, -1
	for i := 0; i < activateCancelledCalls; i++ {
		if err := sess.Activate(ctx); !errors.Is(err, context.Canceled) {
			leaked++
			if firstAt < 0 {
				firstAt = i
			}
		}
	}
	if leaked > 0 {
		t.Errorf("Activate(cancelled) leaked %d of %d calls (first at call %d), want context.Canceled every time",
			leaked, activateCancelledCalls, firstAt)
	}
}

// TestSession_ActivateCancelledCtx_EvictedRequestsNothing: Activate with an
// already-cancelled ctx against an evicted session fails fast without asking
// for a re-activation, so no supervisor starts and no child is spawned.
//
// The empty activateCh is the observable, not a timed "still evicted" poll:
// the buffered send on activateCh is the sole trigger that lets runEvicted
// return and Run reach transitionTo(stateActive), so an un-armed trigger is a
// deterministic proxy for "nothing was spawned". Observing the absence of a
// spawn directly can only ever be "not yet".
//
// No Run goroutine, so nothing drains activateCh and the depth assertion is a
// fact rather than a race against a wake-up.
func TestSession_ActivateCancelledCtx_EvictedRequestsNothing(t *testing.T) {
	t.Parallel()
	sess := &Session{
		id:         "activate-cancelled-evicted",
		sup:        fakeRunner{},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		lcState:    stateEvicted,
		activeCh:   make(chan struct{}),
		evictedCh:  closedChan(),
		activateCh: make(chan struct{}, 1),
		evictCh:    make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sess.Activate(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Activate(cancelled) err = %v, want context.Canceled", err)
	}
	if n := len(sess.activateCh); n != 0 {
		t.Errorf("len(activateCh) = %d, want 0 — a re-activation was requested for a caller that had already given up", n)
	}
	if st := sess.LifecycleState(); st != stateEvicted {
		t.Errorf("LifecycleState = %v, want %v", st, stateEvicted)
	}
}

// TestSession_Activate_GuaranteesPTYBound is the load-bearing test for #396:
// once Activate returns nil from an evicted session, the supervisor's PTY
// must already be bound. Without the strengthened contract, a
// send_message handler running immediately after Activate could observe
// the silent-drop-on-nil branch in supervisor.WriteUserTurn.
//
// We assert indirectly via supervisor.WaitForPTY with a brief deadline.
// Activate's own final statement is WaitForPTY(ctx), so a nil result here
// confirms sessReadyCh is already closed — i.e. the PTY was bound at the
// instant Activate returned.
//
// #1181 — root cause of the prior flake was test-timing fragility, not a
// production race. The old fixture armed an 80ms idle timeout with no client
// attached and reached the evicted state by waiting for that timer to fire.
// On re-activation runActive re-armed a fresh 80ms idle timer; with no client
// to defer it, the timer could fire again in the gap between Activate
// returning and the 10ms re-check. That eviction calls setSession(nil), which
// freshens sessReadyCh to a new unclosed channel, so the re-check then waited
// on an open channel and hit its deadline — matching the observed
// "deadline exceeded" signature. The bind itself never failed.
//
// The fix is structural, not a widened deadline: helperPoolIdle(t, 0)
// disables idle eviction entirely (timerCh stays nil), so no timer can ever
// fire to freshen sessReadyCh after Activate. The evicted->active cycle is
// driven deterministically via Evict/Activate. Once re-activation runs
// setSession, sessReadyCh stays closed permanently, so the re-check reads an
// already-closed channel with no timing dependence.
func TestSession_Activate_GuaranteesPTYBound(t *testing.T) {
	t.Parallel()
	pool := helperPoolIdle(t, 0) // idle eviction disabled — see #1181 note above
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session never reached stateActive; state=%v", sess.LifecycleState())
	}

	// Drive to evicted deterministically. Evict force-evicts via evictCh and
	// is independent of the (disabled) idle timer; it blocks until the evict
	// persist completes, so no poll is needed.
	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if got := sess.LifecycleState(); got != stateEvicted {
		t.Fatalf("post-Evict lcState = %v, want stateEvicted", got)
	}

	activateCtx, activateCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer activateCancel()
	if err := sess.Activate(activateCtx); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	// Immediately after Activate, the supervisor's PTY readiness chan must
	// already be closed: a brief-deadline WaitForPTY returns nil. With idle
	// eviction disabled the channel is permanently closed once re-activation
	// runs setSession, so this read is deterministic — no timer can freshen it
	// back to an unclosed channel (#1181). A non-cancelled short-deadline ctx
	// (not an already-cancelled one) avoids select-randomization: select picks
	// randomly when both branches are ready. Keep the deadline short — it
	// preserves "bound at return, not eventually"; a wide window would wait for
	// a late bind and mask a returns-before-bound regression.
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer checkCancel()
	if err := sess.sup.WaitForPTY(checkCtx); err != nil {
		t.Errorf("WaitForPTY immediately after Activate = %v, want nil (PTY must be bound)", err)
	}
}

// TestSession_IdleEviction_EmitsLogRecord covers AC#3: an eviction
// event produces at least one log record at the moment SIGKILL fires
// that identifies the cause as idle-timeout eviction.
func TestSession_IdleEviction_EmitsLogRecord(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sleep"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}

	rec := &recordingHandler{}
	logger := slog.New(rec)
	// Bridge mode: this test calls sess.Run, which spawns the bootstrap
	// supervisor. Foreground mode in a Run-reaching fixture is the deadlock
	// surface #41 surfaced.
	cfg := Config{
		RunnerFactory: testRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sleep",
			ClaudeArgs:     []string{"3600"},
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

	if !pollUntil(t, 2*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatal("session did not evict")
	}

	found := rec.findEvent("session.idle_eviction")
	if found == nil {
		t.Fatalf("expected log record with event=session.idle_eviction; got events=%v", rec.events())
	}
	if got := found.attrString("session_id"); got != string(sess.ID()) {
		t.Errorf("session_id attr = %q, want %q", got, string(sess.ID()))
	}
	if !found.attrBool("bootstrap") {
		t.Errorf("bootstrap attr = false, want true")
	}
	if found.Level < slog.LevelWarn {
		t.Errorf("level = %v, want >= WARN", found.Level)
	}
}

// recordingHandler captures slog records for assertions.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

type recordView struct{ slog.Record }

func (r recordView) attrString(key string) string {
	var out string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out = a.Value.String()
			return false
		}
		return true
	})
	return out
}

func (r recordView) attrBool(key string) bool {
	var out bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out = a.Value.Bool()
			return false
		}
		return true
	})
	return out
}

func (h *recordingHandler) findEvent(event string) *recordView {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		var got string
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "event" {
				got = a.Value.String()
				return false
			}
			return true
		})
		if got == event {
			v := recordView{Record: rec}
			return &v
		}
	}
	return nil
}

func (h *recordingHandler) events() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.records))
	for _, rec := range h.records {
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "event" {
				out = append(out, a.Value.String())
				return false
			}
			return true
		})
	}
	return out
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

// TestSession_Run_RemovedCh_ExitsClean: closing removedCh while the lifecycle
// goroutine is parked in runEvicted makes Run return nil (a clean exit, not a
// ctx.Err), so Pool.Remove's shared-errgroup Run never propagates a teardown.
// Also asserts removal wins over a racing Activate: a removed session can never
// resurrect back into runActive. Bare Session literals — the evicted→removed
// path touches neither sup nor pool.
func TestSession_Run_RemovedCh_ExitsClean(t *testing.T) {
	t.Parallel()

	newEvictedSession := func() *Session {
		return &Session{
			id:         "removed-test",
			log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			removedCh:  make(chan struct{}),
			lcState:    stateEvicted,
			activeCh:   make(chan struct{}),
			evictedCh:  closedChan(),
			activateCh: make(chan struct{}, 1),
			evictCh:    make(chan struct{}, 1),
		}
	}

	t.Run("parked_then_removed", func(t *testing.T) {
		t.Parallel()
		sess := newEvictedSession()
		errCh := make(chan error, 1)
		go func() { errCh <- sess.Run(context.Background()) }()

		// Level-triggered: the close wakes runEvicted whether it is already
		// parked or about to enter the select.
		close(sess.removedCh)

		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run err = %v, want nil (clean removal exit)", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after removedCh close")
		}
	})

	t.Run("removal_wins_over_activate", func(t *testing.T) {
		t.Parallel()
		sess := newEvictedSession()
		// Pre-load a pending Activate AND close removedCh before Run is
		// scheduled: whichever select arm fires, Run's isRemoved() re-check
		// sees the closed removedCh and exits nil rather than re-activating
		// (which would deref the nil supervisor).
		sess.activateCh <- struct{}{}
		close(sess.removedCh)

		errCh := make(chan error, 1)
		go func() { errCh <- sess.Run(context.Background()) }()

		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run err = %v, want nil (removal wins over Activate)", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return; removed session may have re-activated")
		}
	})
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

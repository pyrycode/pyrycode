package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// baseRunner is the five trivial sessions.Runner methods, mirroring
// internal/sessions/runner_test.go's fakeRunner but in package main so the
// interrupt-dispatch stubs can embed it and add the one runner-type-specific
// method under test. The lifecycle is never driven here, so Run returns nil.
type baseRunner struct{}

func (baseRunner) State() supervisor.State { return supervisor.State{} }
func (baseRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return nil
}
func (baseRunner) WaitForPTY(ctx context.Context) error { return nil }
func (baseRunner) Run(ctx context.Context) error        { return nil }
func (baseRunner) Restart(args []string)                {}

// interruptRunnerStub is a sessions.Runner whose interrupt is Interrupt() —
// streamRunner-shaped (the stream-json arm). It records the call and returns a
// configurable error.
type interruptRunnerStub struct {
	baseRunner
	calls int
	err   error
}

func (r *interruptRunnerStub) Interrupt() error { r.calls++; return r.err }

// sendEscRunnerStub is a sessions.Runner whose interrupt is SendEsc() —
// *supervisor.Supervisor-shaped (the PTY arm).
type sendEscRunnerStub struct {
	baseRunner
	calls int
	err   error
}

func (r *sendEscRunnerStub) SendEsc() error { r.calls++; return r.err }

// inertRunnerStub exposes neither interrupt method — the unknown-runner case.
type inertRunnerStub struct{ baseRunner }

// TestInterruptRunner_Dispatch covers the SendEsc-vs-Interrupt type dispatch: the
// *streamsup.Runner (adapted as streamRunner) interrupts via Interrupt(), the PTY
// *supervisor.Supervisor via SendEsc(), and an unknown runner is inert. The two
// concrete runner types expose mutually exclusive methods, so the switch is
// unambiguous; the chosen method's error propagates unchanged.
func TestInterruptRunner_Dispatch(t *testing.T) {
	t.Parallel()

	t.Run("Interrupt() runner dispatches to Interrupt", func(t *testing.T) {
		r := &interruptRunnerStub{}
		if err := interruptRunner(r); err != nil {
			t.Fatalf("interruptRunner: unexpected err %v", err)
		}
		if r.calls != 1 {
			t.Errorf("Interrupt called %d times, want 1", r.calls)
		}
	})

	t.Run("SendEsc() runner dispatches to SendEsc", func(t *testing.T) {
		r := &sendEscRunnerStub{}
		if err := interruptRunner(r); err != nil {
			t.Fatalf("interruptRunner: unexpected err %v", err)
		}
		if r.calls != 1 {
			t.Errorf("SendEsc called %d times, want 1", r.calls)
		}
	})

	t.Run("runner with neither method is inert", func(t *testing.T) {
		if err := interruptRunner(&inertRunnerStub{}); err != nil {
			t.Errorf("interruptRunner on inert runner = %v, want nil (no actuation beats wrong actuation)", err)
		}
	})

	t.Run("error from the chosen method propagates", func(t *testing.T) {
		want := errors.New("no live child")
		if err := interruptRunner(&interruptRunnerStub{err: want}); !errors.Is(err, want) {
			t.Errorf("interruptRunner (Interrupt) err = %v, want %v", err, want)
		}
		if err := interruptRunner(&sendEscRunnerStub{err: want}); !errors.Is(err, want) {
			t.Errorf("interruptRunner (SendEsc) err = %v, want %v", err, want)
		}
	})
}

// TestResolveBoundRunner exercises the active-conversation → bound-runner
// resolution against a real *sessions.Pool + *conversations.Registry — the layer
// the injected-seam AC2 test cannot reach. The load-bearing case is
// empty-CurrentSessionID: Pool.Lookup("") returns the BOOTSTRAP session, so
// without the empty-binding guard an unbound conversation's interrupt would
// resolve to the shared bootstrap runner (the #678 cross-conversation isolation
// break this ticket must not reintroduce).
func TestResolveBoundRunner(t *testing.T) {
	t.Parallel()
	pool := newRouterTestPool(t)
	bootstrapID := pool.Default().ID()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bound", CurrentSessionID: string(bootstrapID), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	t.Run("unknown conversation is inert", func(t *testing.T) {
		r, ok := resolveBoundRunner(reg, pool, "conv-does-not-exist")
		if ok || r != nil {
			t.Errorf("resolveBoundRunner(unknown) = (%v, %v), want (nil, false)", r, ok)
		}
	})

	t.Run("empty CurrentSessionID is inert, never the bootstrap runner", func(t *testing.T) {
		// The hazard the guard defeats: a bare Pool.Lookup("") hands back the
		// bootstrap session, so a missing binding must be rejected before Lookup.
		boot, err := pool.Lookup("")
		if err != nil || boot != pool.Default() {
			t.Fatalf("precondition: Lookup(\"\") = (%v, %v), want bootstrap session", boot, err)
		}
		r, ok := resolveBoundRunner(reg, pool, "conv-unbound")
		if ok {
			t.Errorf("resolveBoundRunner(unbound) ok = true, want false")
		}
		if r != nil {
			t.Errorf("resolveBoundRunner(unbound) = %v, want nil — an unbound conversation must NEVER resolve to the bootstrap runner", r)
		}
	})

	t.Run("dangling binding is inert", func(t *testing.T) {
		r, ok := resolveBoundRunner(reg, pool, "conv-dangling")
		if ok || r != nil {
			t.Errorf("resolveBoundRunner(dangling) = (%v, %v), want (nil, false)", r, ok)
		}
	})

	t.Run("bound conversation resolves to that session's runner", func(t *testing.T) {
		r, ok := resolveBoundRunner(reg, pool, "conv-bound")
		if !ok {
			t.Fatalf("resolveBoundRunner(bound) ok = false, want true")
		}
		if r != pool.Default().Runner() {
			t.Errorf("resolveBoundRunner(bound) returned the wrong runner; want the bound session's Runner()")
		}
	})
}

// TestActiveInterrupter is the AC2 proof: an inbound interrupt reaches the runner
// bound to the active conversation and NOT the bootstrap supervisor. The bound
// fake records exactly one interrupt; a separate bootstrap fake wired nowhere
// records zero. It also covers AC3's inert states (no active conversation, and an
// unbound/dangling resolution), each of which records which arm it took (#1192).
//
// SendEsc runs synchronously on the test goroutine here, so auditLogger's plain
// buffer needs no synchronisation. Every log assertion names its event exactly and
// never the shared v2.interrupt. prefix — #1193 adds a record to the success path,
// which must not turn these negative assertions red.
func TestActiveInterrupter(t *testing.T) {
	t.Parallel()

	const (
		noActiveConvEvent  = `"event":"v2.interrupt.no_active_conv"`
		noBoundRunnerEvent = `"event":"v2.interrupt.no_bound_runner"`
	)

	t.Run("interrupt reaches the bound runner, not the bootstrap", func(t *testing.T) {
		bound := &interruptRunnerStub{}
		bootstrap := &interruptRunnerStub{} // wired nowhere: must never be touched
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv: func() string { return "A" },
			resolveRunner: func(convID string) (sessions.Runner, bool) {
				if convID == "A" {
					return bound, true
				}
				return nil, false
			},
			log: logger,
		}
		if err := ai.SendEsc(); err != nil {
			t.Fatalf("SendEsc: unexpected err %v", err)
		}
		if bound.calls != 1 {
			t.Errorf("bound runner interrupted %d times, want 1", bound.calls)
		}
		if bootstrap.calls != 0 {
			t.Errorf("bootstrap runner interrupted %d times, want 0 — interrupt must NOT reach the bootstrap", bootstrap.calls)
		}
		// Arm exclusivity: an emission placed above either guard would show up here.
		for _, unwanted := range []string{noActiveConvEvent, noBoundRunnerEvent} {
			if strings.Contains(logBuf.String(), unwanted) {
				t.Errorf("a resolved interrupt emitted %s; that record belongs to an inert arm\nlog:\n%s", unwanted, logBuf.String())
			}
		}
	})

	t.Run("no active conversation is inert (AC3)", func(t *testing.T) {
		resolved := false
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv:   func() string { return "" },
			resolveRunner: func(string) (sessions.Runner, bool) { resolved = true; return nil, false },
			log:           logger,
		}
		if err := ai.SendEsc(); err != nil {
			t.Errorf("SendEsc = %v, want nil", err)
		}
		if resolved {
			t.Errorf("resolveRunner called with no active conversation, want short-circuit")
		}
		if !strings.Contains(logBuf.String(), noActiveConvEvent) {
			t.Errorf("no %s record; the arm must say which one it took\nlog:\n%s", noActiveConvEvent, logBuf.String())
		}
		if strings.Contains(logBuf.String(), noBoundRunnerEvent) {
			t.Errorf("emitted %s from the no-active-conversation arm\nlog:\n%s", noBoundRunnerEvent, logBuf.String())
		}
	})

	t.Run("unbound/dangling resolution is inert (AC3)", func(t *testing.T) {
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv:   func() string { return "A" },
			resolveRunner: func(string) (sessions.Runner, bool) { return nil, false },
			log:           logger,
		}
		if err := ai.SendEsc(); err != nil {
			t.Errorf("SendEsc = %v, want nil", err)
		}
		if !strings.Contains(logBuf.String(), noBoundRunnerEvent) {
			t.Errorf("no %s record; the arm must say which one it took\nlog:\n%s", noBoundRunnerEvent, logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"conversation_id":"A"`) {
			t.Errorf("record does not identify the conversation\nlog:\n%s", logBuf.String())
		}
		if strings.Contains(logBuf.String(), noActiveConvEvent) {
			t.Errorf("emitted %s from the unresolvable-binding arm\nlog:\n%s", noActiveConvEvent, logBuf.String())
		}
	})

	t.Run("a live runner's interrupt error propagates", func(t *testing.T) {
		want := errors.New("no live child")
		ai := activeInterrupter{
			currentConv:   func() string { return "A" },
			resolveRunner: func(string) (sessions.Runner, bool) { return &interruptRunnerStub{err: want}, true },
		}
		if err := ai.SendEsc(); !errors.Is(err, want) {
			t.Errorf("SendEsc err = %v, want %v (best-effort: relay handler Warn-logs it)", err, want)
		}
	})
}

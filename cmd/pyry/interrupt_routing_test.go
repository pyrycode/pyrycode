package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
//
// Since #1193 each case also pins the returned arm — the value SendEsc logs
// verbatim. The arm is asserted alongside the call count rather than instead of it:
// the count proves the method ran, the arm proves the dispatcher named the one it
// ran.
func TestInterruptRunner_Dispatch(t *testing.T) {
	t.Parallel()

	t.Run("Interrupt() runner dispatches to Interrupt", func(t *testing.T) {
		r := &interruptRunnerStub{}
		arm, err := interruptRunner(r)
		if err != nil {
			t.Fatalf("interruptRunner: unexpected err %v", err)
		}
		if r.calls != 1 {
			t.Errorf("Interrupt called %d times, want 1", r.calls)
		}
		if arm != armInterrupt {
			t.Errorf("arm = %q, want %q", arm, armInterrupt)
		}
	})

	t.Run("SendEsc() runner dispatches to SendEsc", func(t *testing.T) {
		r := &sendEscRunnerStub{}
		arm, err := interruptRunner(r)
		if err != nil {
			t.Fatalf("interruptRunner: unexpected err %v", err)
		}
		if r.calls != 1 {
			t.Errorf("SendEsc called %d times, want 1", r.calls)
		}
		if arm != armSendEsc {
			t.Errorf("arm = %q, want %q", arm, armSendEsc)
		}
	})

	t.Run("runner with neither method is inert", func(t *testing.T) {
		arm, err := interruptRunner(&inertRunnerStub{})
		if err != nil {
			t.Errorf("interruptRunner on inert runner = %v, want nil (no actuation beats wrong actuation)", err)
		}
		if arm != armNone {
			t.Errorf("arm = %q, want %q", arm, armNone)
		}
	})

	t.Run("error from the chosen method propagates", func(t *testing.T) {
		// The arm assertions here are what prove it is derived from the runner's
		// type and not from the error: both cases fail identically.
		want := errors.New("no live child")
		arm, err := interruptRunner(&interruptRunnerStub{err: want})
		if !errors.Is(err, want) {
			t.Errorf("interruptRunner (Interrupt) err = %v, want %v", err, want)
		}
		if arm != armInterrupt {
			t.Errorf("failing Interrupt arm = %q, want %q", arm, armInterrupt)
		}
		arm, err = interruptRunner(&sendEscRunnerStub{err: want})
		if !errors.Is(err, want) {
			t.Errorf("interruptRunner (SendEsc) err = %v, want %v", err, want)
		}
		if arm != armSendEsc {
			t.Errorf("failing SendEsc arm = %q, want %q", arm, armSendEsc)
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
// unbound/dangling resolution), each of which records which arm it took (#1192),
// and since #1193 the two arms on the far side of interruptRunner: a successful
// actuation naming which method dispatched it, and a bound runner exposing neither.
//
// SendEsc runs synchronously on the test goroutine here, so auditLogger's plain
// buffer needs no synchronisation. Every log assertion names its event exactly and
// never the shared v2.interrupt. prefix — #1193 added a record to the success path,
// and a prefix-wide absence assertion would have gone red the moment it landed. The
// rule outlives its occasion: it holds for the next record added to this family.
func TestActiveInterrupter(t *testing.T) {
	t.Parallel()

	const (
		noActiveConvEvent  = `"event":"v2.interrupt.no_active_conv"`
		noBoundRunnerEvent = `"event":"v2.interrupt.no_bound_runner"`
		dispatchedEvent    = `"event":"v2.interrupt.dispatched"`
		noActuatorEvent    = `"event":"v2.interrupt.no_actuator"`
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
		// Each inert event is named exactly, so #1193's distinct dispatched event
		// leaves this loop green.
		for _, unwanted := range []string{noActiveConvEvent, noBoundRunnerEvent, noActuatorEvent} {
			if strings.Contains(logBuf.String(), unwanted) {
				t.Errorf("a resolved interrupt emitted %s; that record belongs to an inert arm\nlog:\n%s", unwanted, logBuf.String())
			}
		}
		if !strings.Contains(logBuf.String(), dispatchedEvent) {
			t.Errorf("no %s record; a successful actuation must say which arm dispatched it\nlog:\n%s", dispatchedEvent, logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"arm":"interrupt"`) {
			t.Errorf("dispatched record does not name the Interrupt() arm\nlog:\n%s", logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"conversation_id":"A"`) {
			t.Errorf("dispatched record does not identify the conversation\nlog:\n%s", logBuf.String())
		}
		// AC3 log hygiene, asserted as a CLOSED key set rather than a
		// forbidden-substring list: a substring list goes vacuous on a misspelled or
		// mis-cased needle, where a closed set also fails on a future field addition
		// — which is the behaviour wanted on a route whose records are written
		// beside identity-bearing scopes. Identifiers and enumerated outcomes only.
		wantKeys := map[string]bool{
			slog.TimeKey: true, slog.LevelKey: true, slog.MessageKey: true,
			"event": true, "arm": true, "conversation_id": true,
		}
		for k := range dispatchedRecord(t, logBuf) {
			if !wantKeys[k] {
				t.Errorf("dispatched record carries unexpected key %q; identifiers and enumerated outcomes only\nlog:\n%s", k, logBuf.String())
			}
			delete(wantKeys, k)
		}
		for k := range wantKeys {
			t.Errorf("dispatched record is missing key %q\nlog:\n%s", k, logBuf.String())
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

	t.Run("the SendEsc() arm records which method dispatched it", func(t *testing.T) {
		bound := &sendEscRunnerStub{}
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv:   func() string { return "A" },
			resolveRunner: func(string) (sessions.Runner, bool) { return bound, true },
			log:           logger,
		}
		if err := ai.SendEsc(); err != nil {
			t.Fatalf("SendEsc: unexpected err %v", err)
		}
		if bound.calls != 1 {
			t.Errorf("bound runner interrupted %d times, want 1", bound.calls)
		}
		if !strings.Contains(logBuf.String(), dispatchedEvent) {
			t.Errorf("no %s record\nlog:\n%s", dispatchedEvent, logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"arm":"send_esc"`) {
			t.Errorf("dispatched record does not name the SendEsc() arm\nlog:\n%s", logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"conversation_id":"A"`) {
			t.Errorf("dispatched record does not identify the conversation\nlog:\n%s", logBuf.String())
		}
		if strings.Contains(logBuf.String(), noActuatorEvent) {
			t.Errorf("an actuated interrupt emitted %s\nlog:\n%s", noActuatorEvent, logBuf.String())
		}
	})

	t.Run("a runner exposing neither method records that it went inert", func(t *testing.T) {
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv:   func() string { return "A" },
			resolveRunner: func(string) (sessions.Runner, bool) { return &inertRunnerStub{}, true },
			log:           logger,
		}
		if err := ai.SendEsc(); err != nil {
			t.Errorf("SendEsc = %v, want nil (no actuation beats wrong actuation)", err)
		}
		if !strings.Contains(logBuf.String(), noActuatorEvent) {
			t.Errorf("no %s record; the arm must say which one it took\nlog:\n%s", noActuatorEvent, logBuf.String())
		}
		if !strings.Contains(logBuf.String(), `"conversation_id":"A"`) {
			t.Errorf("record does not identify the conversation\nlog:\n%s", logBuf.String())
		}
		// The non-vacuity guard for the pair: this is what proves the two records
		// are mutually exclusive rather than dispatched firing on every resolved
		// interrupt regardless of arm.
		if strings.Contains(logBuf.String(), dispatchedEvent) {
			t.Errorf("an inert runner emitted %s; nothing was dispatched\nlog:\n%s", dispatchedEvent, logBuf.String())
		}
	})

	t.Run("a live runner's interrupt error propagates", func(t *testing.T) {
		want := errors.New("no live child")
		logger, logBuf := auditLogger()
		ai := activeInterrupter{
			currentConv:   func() string { return "A" },
			resolveRunner: func(string) (sessions.Runner, bool) { return &interruptRunnerStub{err: want}, true },
			log:           logger,
		}
		if err := ai.SendEsc(); !errors.Is(err, want) {
			t.Errorf("SendEsc err = %v, want %v (best-effort: relay handler Warn-logs it)", err, want)
		}
		// The dispatched record is emitted even when the arm FAILED: it names which
		// arm was dispatched to, not that the child quiesced. The relay handler's
		// v2.interrupt.keystroke_err carries the error but not the arm, so
		// suppressing this record on error would leave a failed actuation unnamed.
		if !strings.Contains(logBuf.String(), dispatchedEvent) || !strings.Contains(logBuf.String(), `"arm":"interrupt"`) {
			t.Errorf("a FAILING actuation must still record the arm it dispatched to\nlog:\n%s", logBuf.String())
		}
	})
}

// dispatchedRecord returns the single v2.interrupt.dispatched record in buf,
// decoded from auditLogger's JSON lines. Local to this file: auditRecords filters
// on the permission-audit message and returns zero records here.
func dispatchedRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if rec["event"] == "v2.interrupt.dispatched" {
			found = append(found, rec)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d v2.interrupt.dispatched records, want exactly 1\nlog:\n%s", len(found), buf.String())
	}
	return found[0]
}

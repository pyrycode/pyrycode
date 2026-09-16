package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #2477 activeSessionStarter's wrap-up arm ---
//
// The rows here are about DISPATCH: which path a frame takes, in what order the
// wrap-up and the rotation happen, and what a second frame gets. What the wrap-up
// itself does is session_reset_test.go's subject.
//
// Every double in this file is mutex-guarded, unlike new_session_starter_test.go's
// starterProbe, and that is not belt-and-braces: on the wrap-up arm the rotation
// runs on a goroutine StartNewSession spawned, so the assertion and the write are
// genuinely on different goroutines and -race says so.

// safeLog is the log sink these tests read from. A strings.Builder cannot serve:
// slog's handler serialises its OWN writes, never a reader's, so a test that reads
// the log while the reset goroutine is still writing to it races — and on this arm
// the interesting records are written after the last thing a test can synchronise
// on, so polling is the only way to see them.
type safeLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *safeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *safeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// awaitRecord polls until the log contains event, which is how a record written
// after the rotation — the one thing a test can wait on directly — is observed
// without sleeping for a fixed guess at how long it will take.
func (l *safeLog) awaitRecord(t *testing.T, event string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if strings.Contains(l.String(), event) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("no %q record arrived; logs are:\n%s", event, l.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// asyncRunner is restartFreshRunner's synchronised twin: RestartFresh lands on the
// reset goroutine while the test reads it, and it signals so a test can wait for
// the rotation instead of sleeping for it.
type asyncRunner struct {
	baseRunner
	mu       sync.Mutex
	childPID int
	restarts []string
	rotated  chan string
}

func newAsyncRunner(childPID int) *asyncRunner {
	return &asyncRunner{childPID: childPID, rotated: make(chan string, 4)}
}

func (r *asyncRunner) State() sessions.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.childPID == 0 {
		return sessions.State{}
	}
	return sessions.State{Phase: sessions.PhaseRunning, ChildPID: r.childPID}
}

func (r *asyncRunner) RestartFresh(sessionID string) {
	r.mu.Lock()
	r.restarts = append(r.restarts, sessionID)
	r.mu.Unlock()
	r.rotated <- sessionID
}

func (r *asyncRunner) restartCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.restarts)
}

// awaitRotation blocks for the next RestartFresh, failing the test rather than
// hanging the package if the reset never gets there.
func (r *asyncRunner) awaitRotation(t *testing.T) string {
	t.Helper()
	select {
	case id := <-r.rotated:
		return id
	case <-time.After(3 * time.Second):
		t.Fatalf("no rotation within the budget; the reset goroutine never reached startFreshRunner")
		return ""
	}
}

// resetProbe records the reset's own side of the dispatch: which conversations it
// was asked to wrap up, and in what order relative to the rotation.
type resetProbe struct {
	mu        sync.Mutex
	wrappedUp []string
	// gate holds the wrap-up open so a test can assert on the in-progress window.
	gate chan struct{}
}

// starterWithReset builds a starter whose reset seam is driven by the probe rather
// than by a real coordinator: this file's subject is the dispatch, and a real
// coordinator would make the ordering assertion depend on a child that does not
// exist here.
func (p *resetProbe) starter(runner sessions.Runner, logs *safeLog) activeSessionStarter {
	// ONE logger for both, not two over the same Builder: slog serialises writes
	// per handler, so two handlers sharing a writer have two mutexes and no
	// ordering between them — and here the two would be written from the dispatch
	// goroutine and the reset goroutine at once.
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reset := &conversationReset{
		base:     context.Background(),
		deadline: time.Second,
		resolve: func(convID string) (resetTarget, bool) {
			p.mu.Lock()
			p.wrappedUp = append(p.wrappedUp, convID)
			gate := p.gate
			p.mu.Unlock()
			if gate != nil {
				<-gate
			}
			// Unresolvable on purpose: the wrap-up's own body is not this file's
			// subject, and refusing here leaves the routine's fast exit — which is
			// exactly what must still be followed by the rotation.
			return resetTarget{}, false
		},
		log: log,
	}
	return activeSessionStarter{
		currentConv: func() string { return starterConvA },
		resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
			if runner == nil || (convID != starterConvA && convID != starterConvB) {
				return nil, "", "", false
			}
			return runner, sessions.SessionID("session-of-" + convID), "", true
		},
		rotate: func(old sessions.SessionID) (sessions.SessionID, error) {
			return sessions.SessionID("fresh-" + string(old)), nil
		},
		reset: reset,
		log:   log,
	}
}

func (p *resetProbe) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.wrappedUp...)
}

// lateOutcome is the relay.LateSessionStarter callback, instrumented. It counts
// calls as well as carrying values because "exactly once" is half the seam's
// contract and a second call would otherwise be invisible — the channel is
// buffered, so an extra report would sit in it unnoticed rather than deadlocking.
type lateOutcome struct {
	mu    sync.Mutex
	calls int
	ch    chan error
}

func newLateOutcome() *lateOutcome { return &lateOutcome{ch: make(chan error, 4)} }

func (o *lateOutcome) report(err error) {
	o.mu.Lock()
	o.calls++
	o.mu.Unlock()
	o.ch <- err
}

func (o *lateOutcome) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

// await blocks for the reported outcome, failing rather than hanging the package
// when the seam never reports — which is the failure this whole file guards.
func (o *lateOutcome) await(t *testing.T) error {
	t.Helper()
	select {
	case err := <-o.ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("no outcome reported within the budget; the late seam owes exactly one")
		return nil
	}
}

// TestActiveSessionStarter_LiveChildWrapsUpThenRotates is AC 1's dispatch half:
// the wrap-up runs, and the rotation follows it rather than preceding it.
//
// The RETURN is asserted to be prompt, which is the constraint the whole async
// tail exists for: handleNewSession calls this inline on relay's single Run
// dispatch goroutine, so a StartNewSession that waited out the wrap-up would
// freeze frame dispatch for every connection the daemon has.
func TestActiveSessionStarter_LiveChildWrapsUpThenRotates(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{gate: make(chan struct{})}
	var logs safeLog
	s := p.starter(runner, &logs)

	returned := make(chan error, 1)
	go func() { returned <- s.StartNewSession(starterConvB) }()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartNewSession = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("StartNewSession did not return while the wrap-up was still running")
	}
	// The rotation must NOT have happened yet: the wrap-up is parked on the gate.
	if got := runner.restartCount(); got != 0 {
		t.Errorf("rotated %d times before the wrap-up finished, want 0", got)
	}

	close(p.gate)
	if got := runner.awaitRotation(t); got != "fresh-session-of-"+starterConvB {
		t.Errorf("rotated to %q, want the fresh id of the named conversation", got)
	}
	if got := p.seen(); len(got) != 1 || got[0] != starterConvB {
		t.Errorf("wrapped up %v, want exactly the named conversation", got)
	}
}

// TestActiveSessionStarter_SecondResetIsDropped is AC 4 at the dispatch level. The
// second frame must rotate NOTHING — not rotate immediately, and not queue behind
// the first and rotate later.
func TestActiveSessionStarter_SecondResetIsDropped(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{gate: make(chan struct{})}
	var logs safeLog
	s := p.starter(runner, &logs)

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("the first StartNewSession = %v, want nil", err)
	}
	// The first reset is parked on the gate, so the second arrives mid-reset.
	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("the second StartNewSession = %v, want nil (dropped, not an error)", err)
	}
	close(p.gate)
	runner.awaitRotation(t)

	if got := p.seen(); len(got) != 1 {
		t.Errorf("the reset ran %d times (%v), want 1 — the second frame must be dropped", len(got), got)
	}
	// One rotation, and no second one arriving late: the drop is not a deferral.
	select {
	case id := <-runner.rotated:
		t.Errorf("a second rotation landed (%q); the dropped frame must rotate nothing", id)
	case <-time.After(200 * time.Millisecond):
	}
	if !strings.Contains(logs.String(), "v2.new_session.reset_in_progress") {
		t.Errorf("the dropped frame left no record; logs are:\n%s", logs.String())
	}
}

// TestActiveSessionStarter_ResetReleasesForTheNextFrame pins the other half of the
// guard: it is a window, not a latch. A conversation that has finished resetting
// must be resettable again, or one reset would wedge the verb for the daemon's life.
func TestActiveSessionStarter_ResetReleasesForTheNextFrame(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{}
	var logs safeLog
	s := p.starter(runner, &logs)

	for i := 0; i < 2; i++ {
		if err := s.StartNewSession(starterConvB); err != nil {
			t.Fatalf("StartNewSession #%d = %v, want nil", i+1, err)
		}
		runner.awaitRotation(t)
	}
	if got := p.seen(); len(got) != 2 {
		t.Errorf("the reset ran %d times (%v), want 2", len(got), got)
	}
}

// TestActiveSessionStarter_NoLiveChildKeepsTheSynchronousPath is the #2099
// preservation row. A bare frame on a conversation whose child has never spawned
// still rotates, and it does so INLINE — the wrap-up arm is gated on liveness, so
// nothing about that path moves off the caller's goroutine.
func TestActiveSessionStarter_NoLiveChildKeepsTheSynchronousPath(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(0) // no child
	p := &resetProbe{}
	var logs safeLog
	s := p.starter(runner, &logs)

	if err := s.StartNewSession(""); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}
	// Synchronous: the rotation has already happened when the call returns.
	if got := runner.restartCount(); got != 1 {
		t.Errorf("rotated %d times on return, want 1 — the childless bare path must stay inline", got)
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("wrapped up %v, want nothing — there is no child to wrap up", got)
	}
}

// refusingStarter builds the wrap-up-arm starter for the #2443 rows: a live child,
// a recorded workspace, and a spawn-dir validator that refuses it.
func refusingStarter(p *resetProbe, runner *asyncRunner, logs *safeLog) activeSessionStarter {
	s := p.starter(runner, logs)
	s.spawnDirFor = func(string) (string, error) { return "", errors.New("outside $HOME") }
	s.resolveBound = func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
		return runner, sessions.SessionID("session-of-" + convID), "/recorded/cwd", true
	}
	return s
}

// TestActiveSessionStarter_SyncSeamWithholdsTheWorkspaceReply pins the PLAIN
// relay.SessionStarter form on the wrap-up arm. That method must answer before the
// rotation has happened, and #2443's value asserts a rotation that COMPLETED — so
// this form answers nil rather than making a claim the client cannot check. The
// refusal is not lost even here: it is recorded once the rotation lands, and the
// late form below is how a client actually receives it.
func TestActiveSessionStarter_SyncSeamWithholdsTheWorkspaceReply(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{}
	var logs safeLog
	s := refusingStarter(p, runner, &logs)

	err := s.StartNewSession(starterConvB)
	var refused *relay.RotatedWithoutWorkspaceError
	if errors.As(err, &refused) {
		t.Fatalf("StartNewSession replied %v before the rotation happened; "+
			"RotatedWithoutWorkspaceError MUST NOT be returned for a rotation that has not completed", err)
	}
	if err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}
	runner.awaitRotation(t) // the rotation still happens, asynchronously

	// The refusal survives as a record, and only below a rotation that returned.
	logs.awaitRecord(t, "v2.new_session.workspace_refused")
}

// TestActiveSessionStarter_WrapUpArmDeliversTheWorkspaceReplyLate is the row the
// e2e regression on PR #2482 asked for, and the one that reconciles this ticket
// with #2443: the reply is neither faked at dispatch time nor dropped, it is
// DELIVERED when it becomes true.
//
// THE GATE IS THE ASSERTION. While the wrap-up is parked, nothing has rotated and
// nothing may have been reported — a seam that answered here would be answering
// for a rotation ninety seconds away, which is the claim the type's doc forbids.
// Only once the gate opens and RestartFresh has landed may the refusal arrive.
func TestActiveSessionStarter_WrapUpArmDeliversTheWorkspaceReplyLate(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{gate: make(chan struct{})}
	var logs safeLog
	s := refusingStarter(p, runner, &logs)
	out := newLateOutcome()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.StartNewSessionLate(starterConvB, out.report)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("StartNewSessionLate did not return while the wrap-up was still running")
	}

	// Parked on the gate: no rotation, and therefore nothing may have been claimed.
	if got := runner.restartCount(); got != 0 {
		t.Fatalf("rotated %d times before the wrap-up finished, want 0", got)
	}
	if got := out.count(); got != 0 {
		t.Errorf("the seam reported %d outcomes before the rotation happened, want 0 — "+
			"RotatedWithoutWorkspaceError asserts a rotation that COMPLETED", got)
	}

	close(p.gate)
	runner.awaitRotation(t)

	err := out.await(t)
	var refused *relay.RotatedWithoutWorkspaceError
	if !errors.As(err, &refused) {
		t.Fatalf("late outcome = %v, want a *relay.RotatedWithoutWorkspaceError", err)
	}
	if refused.ConversationID != starterConvB {
		t.Errorf("refusal named %q, want the resolved conversation %q", refused.ConversationID, starterConvB)
	}
	if got := out.count(); got != 1 {
		t.Errorf("the seam reported %d outcomes, want exactly 1", got)
	}
}

// TestActiveSessionStarter_LateOutcomeIsPlainErrorWhenTheRotationFails is the
// other half of the late form's contract, and the reason delivering late is not
// the same as delivering optimistically. A rotation that failed reports the plain
// error and makes NO workspace claim — even though the workspace really was
// refused — because "rotated without the workspace" for a rotation that never
// happened is the lie the type's doc forbids outright.
//
// The failure is the ORDINARY one startFreshRunner's own doc names: a concurrent
// new_session won the race, which this ticket's ninety-second window makes likelier
// rather than rarer.
func TestActiveSessionStarter_LateOutcomeIsPlainErrorWhenTheRotationFails(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{}
	var logs safeLog
	s := refusingStarter(p, runner, &logs)
	s.rotate = func(sessions.SessionID) (sessions.SessionID, error) {
		return "", sessions.ErrSessionNotFound
	}
	out := newLateOutcome()

	s.StartNewSessionLate(starterConvB, out.report)

	err := out.await(t)
	var refused *relay.RotatedWithoutWorkspaceError
	if errors.As(err, &refused) {
		t.Fatalf("late outcome = %v after a FAILED rotation; want the plain error, since "+
			"RotatedWithoutWorkspaceError MUST NOT report a rotation that did not happen", err)
	}
	if !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Errorf("late outcome = %v, want the rotate error", err)
	}
}

// TestActiveSessionStarter_LateOutcomeIsNilWhenTheRotationIsClean is the third
// outcome: a wrap-up arm that rotated with its recorded workspace intact owes the
// client nothing, and must say so rather than staying silent — handleNewSession
// answers nil by sending no reply, and a seam that never called back would leave
// the manager holding a frame it can neither answer nor forget.
func TestActiveSessionStarter_LateOutcomeIsNilWhenTheRotationIsClean(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{}
	var logs safeLog
	s := p.starter(runner, &logs) // no spawnDirFor: nothing is refused
	out := newLateOutcome()

	s.StartNewSessionLate(starterConvB, out.report)
	runner.awaitRotation(t)

	if err := out.await(t); err != nil {
		t.Errorf("late outcome = %v, want nil for a clean rotation", err)
	}
	if got := out.count(); got != 1 {
		t.Errorf("the seam reported %d outcomes, want exactly 1", got)
	}
}

// TestActiveSessionStarter_LateSeamReportsInertArmsSynchronously pins that the
// late form defers ONLY the wrap-up arm. Every other arm resolves on the caller's
// goroutine and must report there too — a relay manager that had to wait for a
// callback before it could move on from an inert frame would have gained the
// latency the async tail exists to remove.
func TestActiveSessionStarter_LateSeamReportsInertArmsSynchronously(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		starter func(p *resetProbe, runner *asyncRunner, logs *safeLog) activeSessionStarter
		convID  string
		wantErr error
	}{
		{
			name:    "named conversation with no live child is inert",
			starter: func(p *resetProbe, r *asyncRunner, l *safeLog) activeSessionStarter { return p.starter(r, l) },
			convID:  starterConvB,
		},
		{
			name: "unresolvable conversation is inert",
			starter: func(p *resetProbe, r *asyncRunner, l *safeLog) activeSessionStarter {
				s := p.starter(r, l)
				s.resolveBound = func(string) (sessions.Runner, sessions.SessionID, string, bool) {
					return nil, "", "", false
				}
				return s
			},
			convID: starterConvB,
		},
		{
			name: "a synchronous rotation that fails reports its error",
			starter: func(p *resetProbe, r *asyncRunner, l *safeLog) activeSessionStarter {
				s := p.starter(r, l)
				s.rotate = func(sessions.SessionID) (sessions.SessionID, error) {
					return "", sessions.ErrSessionNotFound
				}
				return s
			},
			convID:  "",
			wantErr: sessions.ErrSessionNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A childless runner: every row here must avoid the wrap-up arm, which is
			// gated on liveness, so none of them may defer.
			runner := newAsyncRunner(0)
			p := &resetProbe{}
			var logs safeLog
			s := tc.starter(p, runner, &logs)
			out := newLateOutcome()

			s.StartNewSessionLate(tc.convID, out.report)

			// Synchronous: reported by the time the call returned, with no wait.
			if got := out.count(); got != 1 {
				t.Fatalf("the seam reported %d outcomes on return, want exactly 1 — "+
					"only the wrap-up arm may defer", got)
			}
			if err := out.await(t); !errors.Is(err, tc.wantErr) {
				t.Errorf("late outcome = %v, want %v", err, tc.wantErr)
			}
			if got := p.seen(); len(got) != 0 {
				t.Errorf("wrapped up %v, want nothing — no row here has a live child", got)
			}
		})
	}
}

// TestActiveSessionStarter_RefusedWorkspaceIsNotRecordedWhenTheRotationFails is the
// other half of the same contract. The record stands in for the reply, so it must
// carry the reply's precondition: a rotation that failed reports the failure and
// says nothing about the workspace, because "rotated without the workspace" for a
// rotation that never happened is the claim the type's doc forbids.
func TestActiveSessionStarter_RefusedWorkspaceIsNotRecordedWhenTheRotationFails(t *testing.T) {
	t.Parallel()

	runner := newAsyncRunner(starterLiveChildPID)
	p := &resetProbe{}
	var logs safeLog
	s := p.starter(runner, &logs)
	s.spawnDirFor = func(string) (string, error) { return "", errors.New("outside $HOME") }
	s.resolveBound = func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
		return runner, sessions.SessionID("session-of-" + convID), "/recorded/cwd", true
	}
	// The ORDINARY failure startFreshRunner's own doc names: a concurrent
	// new_session won the race and the session has moved.
	s.rotate = func(sessions.SessionID) (sessions.SessionID, error) {
		return "", sessions.ErrSessionNotFound
	}

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}

	logs.awaitRecord(t, "v2.new_session.rotate_failed")
	if got := logs.String(); strings.Contains(got, "v2.new_session.workspace_refused") {
		t.Errorf("a rotation that failed reported the workspace refused anyway; logs are:\n%s", got)
	}
}

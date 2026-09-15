package main

import (
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #1548: the runner dispatchers after their PTY arms were deleted ----------
//
// interruptRunner and startFreshRunner each carried a second type-switch arm for
// *supervisor.Supervisor until #1348 deleted that package outright and left them
// type-dead. Nothing pinned their removal: interruptRunner had no test in the repo
// at all (interrupt_routing_test.go went with #1348), and startFreshRunner's only
// driver, rotatingRunner, reaches the surviving RestartFresh arm.
//
// These rows are the pin. The two "exposes the deleted arm's method" rows are RED
// before the deletion — the dead arms matched, so the arm was armSendEsc and both
// stub counters reached 1 — and green after; they are what catches an accidental
// re-add. The two wire-string rows are green either way and freeze the
// operator-facing constant values (#1193).
//
// Deliberately NOT here: a row for the surviving RestartFresh arm.
// TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild already drives
// the real startFreshRunner through it end to end, including the #1330 gate
// ordering, and duplicating it here would only add a shallower copy.

// The stub sentinels name which stub method ran from the failure message alone.
//
// The two stubs for the DELETED arms return a non-nil error on purpose, even
// though nothing should ever call them. Their rows assert err == nil; a stub
// returning nil would leave that assertion green under a regression that calls the
// dead method and still reports the inert arm, collapsing the row onto its counter
// as the sole discriminator. A sentinel makes counter and error independently red.
var (
	errInterruptStub       = errors.New("interruptOnlyRunner.Interrupt")
	errSendEscStub         = errors.New("sendEscOnlyRunner.SendEsc")
	errStartNewSessionStub = errors.New("startNewSessionOnlyRunner.StartNewSession")
)

// interruptOnlyRunner exposes Interrupt and nothing else — the shape of the one
// arm interruptRunner still recognises, which streamRunner has in production.
type interruptOnlyRunner struct {
	baseRunner
	calls int
}

func (r *interruptOnlyRunner) Interrupt() error {
	r.calls++
	return errInterruptStub
}

// sendEscOnlyRunner exposes the method the deleted interrupt arm dispatched on. No
// type in the module has this shape as a sessions.Runner any more; the stub exists
// solely to prove the dispatcher no longer looks for it.
type sendEscOnlyRunner struct {
	baseRunner
	calls int
}

func (r *sendEscOnlyRunner) SendEsc() error {
	r.calls++
	return errSendEscStub
}

// startNewSessionOnlyRunner is sendEscOnlyRunner's new_session twin: the method
// startFreshRunner's deleted arm dispatched on, exposed by nothing in production.
//
// It also offers the OPTIONAL rotation gate, which beginRotationOrNoop would find
// — precisely so the row can assert the gate is never armed for a runner the
// dispatcher does not recognise. Without the gate the stub would take
// beginRotationOrNoop's inert branch and the assertion would be true by
// construction.
type startNewSessionOnlyRunner struct {
	baseRunner
	calls     int
	rotations int
}

func (r *startNewSessionOnlyRunner) StartNewSession() error {
	r.calls++
	return errStartNewSessionStub
}

func (r *startNewSessionOnlyRunner) BeginRotation() func() {
	r.rotations++
	return func() {}
}

func TestInterruptRunner_ActuatesInterruptAndNothingElse(t *testing.T) {
	t.Parallel()

	t.Run("Interrupt runner actuates and reports the interrupt arm", func(t *testing.T) {
		r := &interruptOnlyRunner{}

		arm, err := interruptRunner(r)

		if arm != armInterrupt {
			t.Errorf("arm = %q, want %q", arm, armInterrupt)
		}
		// The chosen method's error must reach the caller verbatim: it is what
		// handleInterrupt Warn-logs, and swallowing it would report a failed
		// actuation as a clean one.
		if !errors.Is(err, errInterruptStub) {
			t.Errorf("err = %v, want %v", err, errInterruptStub)
		}
		if r.calls != 1 {
			t.Errorf("Interrupt called %d times, want 1", r.calls)
		}
		if got := string(armInterrupt); got != "interrupt" {
			t.Errorf("armInterrupt wire string = %q, want %q (operator-facing, #1193)", got, "interrupt")
		}
	})

	t.Run("SendEsc runner is inert and is never actuated", func(t *testing.T) {
		r := &sendEscOnlyRunner{}

		arm, err := interruptRunner(r)

		// Compared against the CONSTANT, not the literal "none": the literal is
		// row 3's job, so a wire-string change reddens exactly one row.
		if arm != armNone {
			t.Errorf("arm = %q, want %q", arm, armNone)
		}
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if r.calls != 0 {
			t.Errorf("SendEsc called %d times, want 0: the SendEsc arm is deleted (#1548)", r.calls)
		}
	})

	t.Run("runner exposing no interrupt method is inert", func(t *testing.T) {
		arm, err := interruptRunner(baseRunner{})

		if arm != armNone {
			t.Errorf("arm = %q, want %q", arm, armNone)
		}
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if got := string(armNone); got != "none" {
			t.Errorf("armNone wire string = %q, want %q (operator-facing, #1193)", got, "none")
		}
	})
}

// TestStartFreshRunner_StartNewSessionRunnerIsInert pins the new_session half.
//
// The two zero-counters below the return value are load-bearing, not second
// opinions on the StartNewSession one. rotate mints the pool-side id, and the line
// above it arms the #1330 rotation gate. Hoisting either above the inert return —
// the obvious reduce-nesting move now that the dispatch is an if-assertion — arms a
// gate for a rotation that never happens, and the inert return skips the abort()
// disarm, so every later WriteUserTurn on that conversation fails with
// ErrNoLiveChild until the next respawn. The frame that reaches here comes from a
// paired phone, so that wedge is remotely reachable.
func TestStartFreshRunner_StartNewSessionRunnerIsInert(t *testing.T) {
	t.Parallel()

	r := &startNewSessionOnlyRunner{}
	rotations := 0
	rotate := func(old sessions.SessionID) (sessions.SessionID, error) {
		rotations++
		return sessions.SessionID("fresh-" + string(old)), nil
	}

	if err := startFreshRunner(r, sessions.SessionID("old-session-id"), "", rotate, nil); err != nil {
		t.Errorf("startFreshRunner = %v, want nil", err)
	}
	if r.calls != 0 {
		t.Errorf("StartNewSession called %d times, want 0: the StartNewSession arm is deleted (#1548)", r.calls)
	}
	if rotations != 0 {
		t.Errorf("rotate called %d times, want 0: an unrecognised runner must mint no pool-side id", rotations)
	}
	if r.rotations != 0 {
		t.Errorf("BeginRotation called %d times, want 0: an unrecognised runner must leave the #1330 gate disarmed", r.rotations)
	}
}

package main

import (
	"errors"
	"testing"
)

// --- #1548: the interrupt dispatcher after its PTY arm was deleted -----------
//
// interruptRunner carried a second type-switch arm for *supervisor.Supervisor
// until #1348 deleted that package outright and left it type-dead. This row pins
// the one surviving actuation and its operator-facing arm string (#1193).
//
// The inert arms this file used to pin (a runner exposing SendEsc, StartNewSession
// or no interrupt method at all) cannot exist since #2592 put Interrupt,
// RestartFresh and BeginRotation on sessions.Runner. The surviving RestartFresh
// dispatch is driven end to end by
// TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild.

// errInterruptStub names the stub method that ran from the failure message alone.
var errInterruptStub = errors.New("interruptOnlyRunner.Interrupt")

// interruptOnlyRunner overrides Interrupt and nothing else — the one method
// interruptRunner actuates, which streamRunner has in production.
type interruptOnlyRunner struct {
	baseRunner
	calls int
}

func (r *interruptOnlyRunner) Interrupt() error {
	r.calls++
	return errInterruptStub
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
}

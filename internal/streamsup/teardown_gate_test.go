package streamsup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- #1513: the teardown gate ------------------------------------------------
//
// The rotation gate above refuses turns while a new_session rotation is in flight.
// These rows pin its sibling: the gate the DELIBERATE-KILL teardowns arm — both
// eviction arms of sessions.Session.runActive and sessions.Pool.UpdateSettings'
// restart branch — whose release rule is the one the rotation gate's cannot be.
//
// Payload markers, distinct literals so a "the refused bytes never reached a
// child" assertion cannot match a later probe's echo by accident.
const (
	teardownRefusedProbe = "teardown-probe-refused"
	teardownLandedProbe  = "teardown-probe-landed"
)

// TestRunner_BeginTeardown_RefusesTurnWhileChildIsLive is AC1: with the teardown
// gate armed, WriteUserTurn refuses with the retryable ErrNoLiveChild and writes
// ZERO BYTES, while Stdin() is still non-nil.
//
// The non-vacuity guard is the whole point, and it is the same one
// TestRunner_BeginRotation_RefusesTurnWhileChildIsLive carries: without asserting
// the handle is live first, an ErrNoLiveChild would prove only that some child was
// absent — the pre-existing refusal, which passes with or without this gate.
//
// "Zero bytes" needs a happens-AFTER edge, not a sleep. The Restart respawn below
// releases the gate (that is this gate's release rule, and the row after this one
// pins it), so the second probe's echo is a deterministic edge: it can only have
// been written after the refusal, and it crosses the same pipe into the same
// buffer. Once it is there, the first probe's absence is a fact rather than a
// timing accident.
func TestRunner_BeginTeardown_RefusesTurnWhileChildIsLive(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 4)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	waitSpawn(t, spawned, "the first spawn")
	// Before the kill below, for waitReadyMarkers' reason: a child killed before it
	// writes its marker never writes it at all, and the count below would read short.
	waitReadyMarkers(t, out, 1)

	if r.Stdin() == nil {
		t.Fatal("Stdin() is nil before the arm; the refusal below would be the pre-existing no-live-child one, which passes with or without the teardown gate")
	}
	r.BeginTeardown()

	if err := r.WriteUserTurn(context.Background(), "c1", []byte(teardownRefusedProbe)); !errors.Is(err, ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn with the teardown gate armed = %v, want ErrNoLiveChild; a nil return is what msgqueue reads as a confirmed commit, so it would drop the queue head and the message would die unread in the pipe of a child the daemon is about to kill", err)
	}

	// The respawn releases the gate, and its echo is the happens-after edge the
	// absence assertion needs.
	r.Restart(nil)
	waitSpawn(t, spawned, "the Restart respawn")
	waitReadyMarkers(t, out, 2)

	if err := r.WriteUserTurn(context.Background(), "c1", []byte(teardownLandedProbe)); err != nil {
		t.Fatalf("WriteUserTurn after the respawn = %v, want nil; a teardown gate that outlives its own teardown wedges the conversation, which is a worse outcome than the loss it fixes", err)
	}
	waitForContains(t, out, teardownLandedProbe, 5*time.Second)

	if got := out.String(); strings.Contains(got, teardownRefusedProbe) {
		t.Errorf("the refused turn reached a child after all — the refusal returned the right error but still wrote bytes; stdout:\n%s", got)
	}
}

// TestRunner_BeginTeardown_ReleasedByAnyBindWhileRotationArmStands is AC2, and it
// is the reason this gate cannot reuse the rotation flag. Both properties are
// asserted about ONE bind — the Restart-driven respawn, whose freshSeq snapshot
// reads EQUAL to armFreshSeq:
//
//   - that bind RELEASES the teardown gate. Its release rule is childGeneration,
//     which setStdin bumps on every bind, so any successor satisfies it.
//   - that same bind LEAVES THE ROTATION ARM STANDING (#1482). Its release rule is
//     a strictly-greater freshSeq, which an equal snapshot does not meet.
//
// Arming the teardowns from the rotation flag would therefore wedge the session:
// every turn would refuse until some later RestartFresh landed. One bind, two
// rules, opposite outcomes — which is only observable if both arms are placed on
// the same runner and measured across the same respawn.
//
// The rotation arm is read through its abort rather than a second respawn:
// turnTargetWithGate reports gateRotation first, so with both armed the teardown
// gate's state is masked. Dropping the rotation arm unmasks it, and gateOpen then
// says the bind had already released the teardown gate.
//
// echo_lines stays alive until it is killed, so no crash-respawn can race either
// arm: both are strictly before the respawn's beginSpawn by program order.
func TestRunner_BeginTeardown_ReleasedByAnyBindWhileRotationArmStands(t *testing.T) {
	t.Parallel()
	out, stderr := &safeBuffer{}, &safeBuffer{}
	cfg := helperRunCfg(t, "echo_lines", out, stderr)
	spawned := make(chan struct{}, 4)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	waitSpawn(t, spawned, "the first spawn")
	waitReadyMarkers(t, out, 1)

	abortRotation := r.BeginRotation()
	r.BeginTeardown()
	if _, gate := r.turnTargetWithGate(); gate != gateRotation {
		t.Fatalf("turnTargetWithGate = %v with both arms placed, want gateRotation; the assertions below could not tell a release from the initial state", gate)
	}

	// The bind under test. Restart kills child 1 and relaunches at once, and that
	// respawn's beginSpawn runs before any RestartFresh, so its snapshot cannot
	// exceed the rotation arm's threshold.
	r.Restart(nil)
	waitSpawn(t, spawned, "the Restart respawn")
	waitReadyMarkers(t, out, 2)

	if r.Stdin() == nil {
		t.Fatal("Stdin() is nil after the Restart respawn; both assertions below would collapse into the pre-existing no-live-child refusal, which passes with or without either release rule")
	}
	if _, gate := r.turnTargetWithGate(); gate != gateRotation {
		t.Fatalf("turnTargetWithGate = %v after an equal-freshSeq bind, want gateRotation; the Restart respawn disarmed the ROTATION gate, so a turn accepted on the strength of the already-fired session_transition{clear} would be written into the child RestartFresh is about to kill (#1482)", gate)
	}

	abortRotation()
	if _, gate := r.turnTargetWithGate(); gate != gateOpen {
		t.Fatalf("turnTargetWithGate = %v once the rotation arm is dropped, want gateOpen; the Restart respawn did NOT release the teardown gate, so a teardown that ends in a Restart-driven or crash respawn wedges the session — every turn refuses until some later RestartFresh lands", gate)
	}
	if err := r.WriteUserTurn(context.Background(), "c1", []byte(teardownLandedProbe)); err != nil {
		t.Fatalf("WriteUserTurn with both gates down = %v, want nil", err)
	}
}

package sessions

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- #1513: the teardown gate is armed before the kill -----------------------
//
// Three deliberate-kill paths tear a live child down: both eviction arms of
// Session.runActive (the idle timer, and the cap/force evictCh that Pool.Remove
// drives through Session.Evict) and Pool.UpdateSettings' restart branch. Each must
// arm the runner's write-refusal gate FIRST, so a delivery racing the teardown is
// refused and retried against the successor rather than written into the pipe of a
// child that is about to die — a write that returns nil, which msgqueue reads as a
// confirmed commit before dropping the queue head.

// Event names in teardownRecorder's ordered log.
const (
	evArm      = "begin_teardown"
	evRestart  = "restart"
	evRunEnded = "run_ended"
)

// teardownRecorder is a Runner double that records the ORDER of the arm and the
// teardown it is supposed to precede. fakeRunner supplies every method the three
// paths do not exercise, so this double carries only the three that matter.
//
// One shared, ordered slice rather than a counter or a per-method slice apiece: the
// property under test is a SEQUENCE. A test that only counted arms would stay green
// with the arm placed after the kill, which is the whole defect.
type teardownRecorder struct {
	fakeRunner
	mu     sync.Mutex
	events []string
}

func (r *teardownRecorder) record(ev string) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

// log deep-copies under r.mu: the lifecycle goroutine appends while the test
// goroutine reads.
func (r *teardownRecorder) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *teardownRecorder) BeginTeardown() { r.record(evArm) }

// Restart is UpdateSettings' teardown: the kill and relaunch the restart branch
// drives for a setting with no in-band form.
func (r *teardownRecorder) Restart(args []string) { r.record(evRestart) }

// Run is the eviction arms' teardown, observed from the runner's side: both arms
// tear the child down by cancelling the context Run was started on, so Run
// returning IS the kill from this double's vantage point.
func (r *teardownRecorder) Run(ctx context.Context) error {
	<-ctx.Done()
	r.record(evRunEnded)
	return ctx.Err()
}

// assertArmedBefore fails unless the log holds the arm and then the teardown, in
// that order. Both halves are checked, so a missing arm and a late arm produce
// different diagnostics.
func assertArmedBefore(t *testing.T, got []string, teardown, path string) {
	t.Helper()
	arm, kill := -1, -1
	for i, ev := range got {
		if ev == evArm && arm == -1 {
			arm = i
		}
		if ev == teardown && kill == -1 {
			kill = i
		}
	}
	if kill == -1 {
		t.Fatalf("%s: the teardown %q never happened; events = %v — the ordering below would be vacuous", path, teardown, got)
	}
	if arm == -1 {
		t.Fatalf("%s: the teardown gate was never armed; events = %v. A delivery racing this teardown is written into the dying child, returns nil, and msgqueue drops the queue head as committed — the message is lost with no retry, no session_error, and queue_state reporting it delivered", path, got)
	}
	if arm > kill {
		t.Fatalf("%s: the gate was armed AFTER the teardown (events = %v); an arm placed past the kill closes nothing — the whole race it exists for has already run", path, got)
	}
}

// helperTeardownPool builds a Pool whose sessions are backed by teardownRecorder.
// ClaudeBin is never executed: the factory replaces the runner outright, so no
// child is spawned and the three paths are driven purely through the lifecycle.
func helperTeardownPool(t *testing.T, idle time.Duration) (*Pool, *teardownRecorder) {
	t.Helper()
	rec := &teardownRecorder{}
	pool, err := New(Config{
		RunnerFactory: func(RunnerConfig) (Runner, error) { return rec, nil },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Bootstrap: SessionConfig{
			ClaudeBin:      "/nonexistent/claude",
			IdleTimeout:    idle,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   time.Second,
		},
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, rec
}

// TestSession_IdleEviction_ArmsTeardownGateBeforeKill is AC3's idle-timer arm. The
// filed scenario reaches it whenever an operator sets -pyry-idle-timeout: a queued
// message passes the delivery seam, lands in the pipe, and the timer fires before
// the child reads stdin.
func TestSession_IdleEviction_ArmsTeardownGateBeforeKill(t *testing.T) {
	t.Parallel()
	pool, rec := helperTeardownPool(t, 100*time.Millisecond)
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	// Wait on the recorder's OWN kill event, not on stateEvicted. beginEvict flips
	// lcState and releases lcMu BEFORE runActive's idle arm reaches cancelSup, while
	// teardownRecorder.Run can only append evRunEnded after it — so the state edge is
	// ordered strictly EARLIER than the event asserted below, and waiting on it would
	// put pollUntil's 10 ms sleep where a happens-after relation belongs. Unloaded that
	// passes; under CPU contention it reds this row with a log holding the arm and no
	// kill, which is a flake against every PR sharing the gate rather than a defect in
	// any of them. The sibling below needs no such care: Evict blocks on evictedCh,
	// which endEvict closes after drainSup — a real edge, and the shape copied here.
	if !pollUntil(t, 3*time.Second, func() bool {
		return slices.Contains(rec.log(), evRunEnded)
	}) {
		t.Fatalf("the idle timer did not tear the child down within 3s; state=%v, events = %v", sess.LifecycleState(), rec.log())
	}
	assertArmedBefore(t, rec.log(), evRunEnded, "idle eviction")
}

// TestSession_ForcedEviction_ArmsTeardownGateBeforeKill is AC3's evictCh arm — the
// cap/force path, which Pool.Remove also drives through Session.Evict. Cap eviction
// is uncorrelated with the victim's own deliveries, which is why ordinary
// multi-conversation use hits this window rather than a contrived interleaving.
func TestSession_ForcedEviction_ArmsTeardownGateBeforeKill(t *testing.T) {
	t.Parallel()
	pool, rec := helperTeardownPool(t, 0) // idle disabled: only Evict can fire
	sess := pool.Default()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	if !pollUntil(t, 3*time.Second, func() bool {
		return sess.LifecycleState() == stateActive
	}) {
		t.Fatalf("session did not become active within 3s; state=%v", sess.LifecycleState())
	}
	if err := sess.Evict(ctx); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if !pollUntil(t, 3*time.Second, func() bool {
		return sess.LifecycleState() == stateEvicted
	}) {
		t.Fatalf("session did not evict within 3s; state=%v", sess.LifecycleState())
	}
	assertArmedBefore(t, rec.log(), evRunEnded, "forced eviction")
}

// TestPool_UpdateSettings_ArmsTeardownGateBeforeRestart is AC3's settings arm, and
// the one path of the three that is live under DEFAULT config.
//
// The update clears Model to "" deliberately: since #2066 that — a Model or Effort
// explicitly cleared to "run at claude's own default" — is what inBandDeliverable
// still refuses and therefore the only shape that reaches Restart. An escalation or
// an ordinary model change now travels in band and would take the other branch, so a
// test written around one would assert nothing about this arm.
func TestPool_UpdateSettings_ArmsTeardownGateBeforeRestart(t *testing.T) {
	t.Parallel()
	pool, rec := helperTeardownPool(t, 0)
	id := pool.Default().ID()

	// A model must be SET before clearing it means anything: UpdateSettings returns
	// early when the merged settings equal the stored ones, so clearing an already-empty
	// Model would reach neither branch and the assertions below would be vacuous. This
	// first update takes the in-band branch and records nothing.
	set := "opus"
	if err := pool.UpdateSettings(id, SettingsUpdate{Model: &set}); err != nil {
		t.Fatalf("UpdateSettings (set): %v", err)
	}
	cleared := ""
	if err := pool.UpdateSettings(id, SettingsUpdate{Model: &cleared}); err != nil {
		t.Fatalf("UpdateSettings (clear): %v", err)
	}

	got := rec.log()
	assertArmedBefore(t, got, evRestart, "settings restart")
	// The in-band branch must not arm: it kills nothing, and a gate armed there would
	// refuse turns for a delivery that tears no child down.
	if n := strings.Count(strings.Join(got, " "), evArm); n != 1 {
		t.Errorf("settings restart: %d arms, want exactly 1; events = %v", n, got)
	}
}

// TestPool_UpdateSettings_InBandBranchDoesNotArm is that last clause from the other
// side: an update WITH an in-band form delivers live and must leave the gate alone.
// Without this row the arm could be hoisted above UpdateSettings' branch split and
// every assertion above would stay green, while every in-band settings change began
// refusing turns for a teardown that never happens.
func TestPool_UpdateSettings_InBandBranchDoesNotArm(t *testing.T) {
	t.Parallel()
	pool, rec := helperTeardownPool(t, 0)
	id := pool.Default().ID()

	next := "sonnet"
	if err := pool.UpdateSettings(id, SettingsUpdate{Model: &next}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	got := rec.log()
	for _, ev := range got {
		if ev == evArm {
			t.Fatalf("the in-band branch armed the teardown gate; events = %v — no child is torn down here, so every live settings change would refuse turns until the next respawn", got)
		}
		if ev == evRestart {
			t.Fatalf("the in-band branch restarted the child; events = %v", got)
		}
	}
}

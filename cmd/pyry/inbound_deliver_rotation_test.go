package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// --- #1295: a turn queued ACROSS a new_session rotation -----------------------
//
// TestRelayV2_StreamNewSessionRotatesAndRestartsFresh has failed twice at its M4
// milestone with one shape: the rotation succeeds, the daemon ACCEPTS the
// following turn (ack #2 received=true), and then zero bytes reach any child for
// the whole 20 s deadline (stdin log 99 → 99). It reproduces at roughly 1-in-N
// over weeks, so "run it more" is unbounded work. This file replaces that
// sampling with a FORCED ordering one layer below e2e, at the delivery seam
// itself (newInboundDeliver, main.go).
//
// WHICH NOT-READY ARM IS FORCED: row 2 of the ticket's three-row table,
// busy.waitIdleForDelivery(ctx, convID, hold). The seam has exactly three points
// at which a delivery can sit, and this is the only one consistent with every
// feature of the recorded failure:
//
//   - Activate (bounded by inboundActivateTimeout, 30 s) is silent and does
//     outlast the e2e's 20 s window, but the pre-rotation turn had already been
//     delivered through this same conversation — the e2e's M1 echoed it — so its
//     session was live, and Activate on a live session is a near-no-op. A wedged
//     respawn is not the recorded shape.
//   - waitIdleForDelivery (bounded by streamTurnHoldTimeout, 15 min) sits AFTER
//     the route resolved and the message was enqueued, which is why ack #2 came
//     back true; it sits BEFORE WriteUserTurn, which is why zero bytes moved; its
//     bound is far outside the 20 s window, so nothing expires inside it; and it
//     emits NOTHING while parked, matching a record that carries no 1 Hz
//     "msgqueue: delivery failed, will retry" Warn.
//   - WriteUserTurn takes the raw lifecycle ctx and is deliberately unbounded,
//     but on the stream path it returns at once — streamsup.WriteTurn maps a nil
//     Stdin() to the retryable ErrNoLiveChild without blocking.
//
// So: park the delivery in the hold deterministically, then run the rotation, and
// assert that the rotation's clear is what releases it and that the turn lands in
// the POST-rotation child and in no other. Zero production code is touched; the
// seam is already injectable (newInboundDeliver takes resolve, busy and hold as
// parameters) and msgqueue.Config.Deliver is a DeliverFunc.
//
// Neither test in this file calls t.Parallel(), unlike every other test in
// inbound_deliver_test.go, and the omission is deliberate: the claim under test is
// DETERMINISM, measured with -count=50 -race, and running 50 race-instrumented
// copies concurrently reintroduces machine load as a confound on the
// recvStringWithin budgets. Serialised, the whole 50-iteration loop is a few
// seconds and every wait keeps orders of magnitude of headroom.

// fakeChild is one claude child's stdin pipe. Every child ever installed is
// retained by rotatingWriter, so an assertion can name WHICH child received a
// payload rather than only whether somebody did — the distinction AC1 requires
// ("bytes into the dead child's pipe must not read as delivered").
type fakeChild struct {
	sessionID string
	dead      bool
	stdin     []string
}

// childSnapshot is a race-free copy of a fakeChild for assertions and failure
// messages. It carries the dead flag because a misroute's whole signature is
// "the payload is in a child that is no longer the one behind Stdin()".
type childSnapshot struct {
	sessionID string
	dead      bool
	stdin     []string
}

// baseRunner is the six trivial sessions.Runner methods in package main, so a
// test stub can embed it and add only the one method under test. Moved here from
// the interrupt-routing tests when #1348 deleted them; this file is its last
// caller. The lifecycle is never driven, so Run returns nil.
type baseRunner struct{}

func (baseRunner) State() sessions.State { return sessions.State{} }
func (baseRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return nil
}
func (baseRunner) WaitForPTY(ctx context.Context) error { return nil }
func (baseRunner) Run(ctx context.Context) error        { return nil }
func (baseRunner) Restart(args []string)                {}
func (baseRunner) SetSpawnArgs(args []string)           {}
func (baseRunner) SetModel(model string) error          { return nil }
func (baseRunner) SetSpawnPermissionMode(string)        {}
func (baseRunner) SetPermissionMode(mode string) error  { return nil }

func (c childSnapshot) String() string {
	return fmt.Sprintf("{session=%s dead=%t stdin=%q}", c.sessionID, c.dead, c.stdin)
}

// rotatingWriter is a handlers.TurnWriter that models the production indirection
// boundSession.WriteUserTurn → *sessions.Session → streamRunner →
// *streamsup.Runner.WriteUserTurn → WriteTurn(ctx, r.Stdin(), payload), across a
// new_session rotation.
//
// THE LOAD-BEARING MODELLING DECISION: it resolves its target child at WRITE
// time, not at resolve time. That mirrors production exactly — RestartFresh
// rotates the runner's internal id and re-spawns IN PLACE, so the
// *streamsup.Runner identity survives the rotation while the child behind Stdin()
// does not. A fixture that snapshotted the child when the seam resolved the
// writer would make "did the turn reach the post-rotation child?" true by
// construction in the wrong direction; this one keeps it a real question about
// the seam.
//
// It deliberately has NO "refuse to write to a DEAD child" branch. Refusing there
// would mask a misroute behind an error and let msgqueue retry it away; appending
// leaves the misroute observable, which is what the "no other child holds the
// payload" assertion below is for. The control test proves that path is reachable.
//
// #1330 AMENDS that decision rather than deleting it, because it governs one case
// and production grew another. The dead-child case is unchanged: a write to a
// child that has already gone still appends, still reads as a misroute. What is
// new is the ROTATION GATE (rotating/rotateGen below) — a refusal production now
// genuinely makes, in (*streamsup.Runner).WriteUserTurn, for a child that is very
// much alive but doomed. Modelling it is not the same as asserting it: the fixture
// only OFFERS the capability, and whether the real startFreshRunner arms it is
// the question TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild
// asks.
//
// All three signal channels are buffered and sent to non-blockingly, mirroring
// exitFor/sinkFor's discipline: these methods run on the drain's own goroutine and
// must never wedge it, and a dropped duplicate costs nothing because each barrier
// receives exactly once. The mutex is a leaf lock, never held across a channel op.
type rotatingWriter struct {
	mu       sync.Mutex
	current  *fakeChild
	children []*fakeChild

	// rotating/rotateGen are the fixture twin of the runner's rotation gate,
	// including its generation-stamped disarm. Guarded by the SAME mu that guards
	// current, because the property being modelled is that "is a rotation armed?"
	// and "which child?" are answered under one acquisition. spawn clears the arm
	// (the twin of setStdin); beginRestart deliberately does not (the twin of
	// takeStdin) — the gate spans the teardown.
	rotating  bool
	rotateGen uint64

	// activated fires from Activate — the statement IMMEDIATELY BEFORE
	// waitIdleForDelivery, on the same goroutine. Receiving it therefore
	// establishes "the drain has entered this delivery and is now at the hold" by
	// program order, with no sleep.
	activated chan string
	// noChild fires when a write finds no live child (mid-restart).
	noChild chan string
	// wrote fires with the session id of the child that received the payload.
	wrote chan string
}

func newRotatingWriter() *rotatingWriter {
	return &rotatingWriter{
		activated: make(chan string, 8),
		noChild:   make(chan string, 8),
		wrote:     make(chan string, 8),
	}
}

// rotationSignal delivers one fixture signal without ever blocking its caller.
func rotationSignal(ch chan string, v string) {
	select {
	case ch <- v:
	default:
	}
}

// Activate models Pool.Activate on an already-live session: a near-no-op.
func (w *rotatingWriter) Activate(context.Context) error {
	rotationSignal(w.activated, "activate")
	return nil
}

// BeginRotation arms the fixture's rotation gate and returns the generation-stamped
// disarm, modelling (*streamsup.Runner).BeginRotation. Offering the method is what
// lets the REAL startFreshRunner decide whether to use it.
func (w *rotatingWriter) BeginRotation() func() {
	w.mu.Lock()
	w.rotating = true
	w.rotateGen++
	gen := w.rotateGen
	w.mu.Unlock()

	return func() {
		w.mu.Lock()
		if w.rotateGen == gen {
			w.rotating = false
		}
		w.mu.Unlock()
	}
}

// WriteUserTurn appends to whichever child is current AT WRITE TIME, or returns
// streamsup.ErrNoLiveChild verbatim when there is none — the sentinel
// streamsup.WriteTurn returns for a nil Stdin(), which msgqueue classifies as a
// retryable delivery failure and re-attempts on the same head.
//
// An armed rotation takes the same refusal, read under the SAME acquisition as
// current (modelling turnTarget), and returns the same sentinel: production
// deliberately mints no new error, because ErrNoLiveChild is already the retryable
// classification msgqueue and cmd/pyry agree on.
func (w *rotatingWriter) WriteUserTurn(_ context.Context, _ string, payload []byte) error {
	w.mu.Lock()
	child := w.current
	if w.rotating {
		child = nil // gated: the live child is doomed, so it is not a target
	} else if child != nil {
		child.stdin = append(child.stdin, string(payload))
	}
	w.mu.Unlock()

	if child == nil {
		rotationSignal(w.noChild, string(payload))
		return streamsup.ErrNoLiveChild
	}
	rotationSignal(w.wrote, child.sessionID)
	return nil
}

// beginRestart models RestartFresh's teardown half: the old child dies and
// Stdin() returns nil until the fresh spawn binds. It leaves the rotation gate
// armed, as takeStdin does — the teardown is the middle of that window, not its
// end.
//
// It is driven as its OWN step rather than from the fixture's RestartFresh,
// because in production RestartFresh returns after cancel() and the kill, the Wait
// and the respawn all run later on the Run goroutine. A fixture that killed
// synchronously would make "the woken delivery found the child already gone" a
// race, which is precisely the thing under test.
func (w *rotatingWriter) beginRestart() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		w.current.dead = true
		w.current = nil
	}
}

// spawn installs a fresh live child as current, retaining it in arrival order, and
// disarms the rotation gate in the same acquisition — the fixture twin of setStdin,
// which is production's single release point.
func (w *rotatingWriter) spawn(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := &fakeChild{sessionID: sessionID}
	w.children = append(w.children, c)
	w.current = c
	w.rotating = false
}

// stdinOf returns everything written to the child spawned under sessionID.
func (w *rotatingWriter) stdinOf(sessionID string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var got []string
	for _, c := range w.children {
		if c.sessionID == sessionID {
			got = append(got, c.stdin...)
		}
	}
	return got
}

// snapshot copies every child ever installed, in arrival order. It subsumes a
// bare id list: the misroute assertion must iterate ALL children (so a future
// third child cannot hide a misroute) and the failure message must print each
// child's id, dead flag and stdin contents.
func (w *rotatingWriter) snapshot() []childSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]childSnapshot, 0, len(w.children))
	for _, c := range w.children {
		out = append(out, childSnapshot{
			sessionID: c.sessionID,
			dead:      c.dead,
			stdin:     append([]string(nil), c.stdin...),
		})
	}
	return out
}

// rotatingSessionOwner is a MUTABLE conversationForSession. stubBusyResolve takes
// a fixed map and therefore cannot express a rotation, which is the one thing this
// file needs.
//
// rekey ADDS the new id and KEEPS the old one, because that is what RebindSession
// does: CurrentSessionID moves to the new id and the old id is appended to
// SessionHistory, and conversationForSession matches either.
// Keeping the old id is what leaves the child-exit lane's clear — keyed to the
// runner's CONSTRUCTION-time session id, which RestartFresh does not rotate —
// resolvable after a rotation; dropping it would turn that lane into a
// Debug-level silent skip.
type rotatingSessionOwner struct {
	mu    sync.Mutex
	owned map[string]struct{}
}

func newRotatingSessionOwner(sessionIDs ...string) *rotatingSessionOwner {
	o := &rotatingSessionOwner{owned: make(map[string]struct{}, len(sessionIDs))}
	for _, sid := range sessionIDs {
		o.owned[sid] = struct{}{}
	}
	return o
}

func (o *rotatingSessionOwner) resolve(sessionID string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.owned[sessionID]; !ok {
		return "", false
	}
	return testConvID, true
}

func (o *rotatingSessionOwner) rekey(oldID, newID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = oldID // kept, not removed — see the type doc (SessionHistory).
	o.owned[newID] = struct{}{}
}

const (
	rotationOldSID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	rotationNewSID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

// rotatingRunner adapts a *rotatingWriter to sessions.Runner so the REAL
// startFreshRunner can dispatch to it. It is streamRunner-shaped in the two ways
// that matter: RestartFresh(string) selects the direct stream-json arm, and
// BeginRotation() func() offers the rotation gate as an OPTIONAL capability — the
// same shape production's optional assertion looks for.
//
// It exposes both unconditionally. Whether the gate is ARMED is the dispatch's
// decision, not the fixture's, which is what leaves the test a real question:
// against a startFreshRunner that never calls BeginRotation, the gate stays open
// and the woken delivery writes into the outgoing child.
//
// RestartFresh records the id and does NOT tear the child down — see beginRestart's
// doc for why the teardown is a separate step.
type rotatingRunner struct {
	baseRunner
	w *rotatingWriter

	restartFreshIDs []string
}

func (r *rotatingRunner) RestartFresh(newID string) {
	r.restartFreshIDs = append(r.restartFreshIDs, newID)
}

func (r *rotatingRunner) BeginRotation() func() { return r.w.BeginRotation() }

// TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild is #1330's
// primary proof, and it is the third test in this file rather than an edit to
// either existing one because it decides a window they deliberately leave open.
//
// WHAT MAKES IT DIFFERENT FROM ITS TWO SIBLINGS:
//
//   - TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild inverts
//     steps 6 and 7 relative to production — teardown BEFORE the clear — and says
//     so in its own doc, naming the un-inverted order as "a DISTINCT window this test
//     deliberately does not decide". This test runs the PRODUCTION order: the
//     clear fires while the outgoing child is still live.
//   - The control below never begins a rotation at all.
//
// And it drives the REAL startFreshRunner rather than open-coding the sequence.
// That is what makes it RED on an unmodified tree: the fixture offers a gate, and
// an unmodified startFreshRunner never arms it, so the woken delivery finds the
// still-live outgoing child and the write SUCCEEDS into the child that is about to
// die — which is the defect, verbatim.
//
// WHERE THE DETERMINISM COMES FROM — the sibling's four facts still hold (the busy
// mark happens-before the Enqueue; waitIdleForDelivery's single-acquisition
// re-check; the clear is the sole release; Activate immediately precedes the hold),
// plus a fifth this ordering needs: the outgoing child stays LIVE across the whole
// delivery attempt, because the teardown is step 7 and the test does not run it
// until it has observed step 6's outcome. So "the write found no live child"
// cannot happen by luck here — on an unmodified tree it cannot happen at all, and
// the red is a misroute rather than a timeout race.
func TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild(t *testing.T) {
	owner := newRotatingSessionOwner(rotationOldSID)
	w := newRotatingWriter()
	w.spawn(rotationOldSID)
	tr := newTurnBusyTracker(owner.resolve, discardLogger())

	gaveUp := make(chan string, 1)
	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(
			func(string) (handlers.TurnWriter, error) { return w, nil },
			tr,
			streamTurnHoldTimeout,
		),
		RetryInterval: 10 * time.Millisecond,
		OnGiveUp: func(convID, reason string) {
			rotationSignal(gaveUp, convID+": "+reason)
		},
		Logger: inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	// Step 1 — the pre-rotation turn is running (the e2e's M1). Non-vacuity guard:
	// without a standing mark nothing is held and every step below proves nothing.
	tr.observe(rotationOldSID, turnevent.TextChunk{MessageID: "m1", Text: "working"})
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after the pre-rotation turn opened; nothing would be held and the rest of this test is vacuous")
	}

	// Step 2 — the subsequent turn is accepted (the e2e's ack #2); receiving
	// w.activated puts the drain AT the hold, by program order.
	q.Enqueue(testConvID, "turn-two")
	recvStringWithin(t, w.activated, "the drain to enter delivery (Activate precedes the hold)")

	// Step 3 — parked, zero bytes. Fires only if the hold did not engage.
	assertNoWriteWithin(t, w.wrote, 50*time.Millisecond)

	// Step 4 — the rotation, IN PRODUCTION ORDER, driven through the real dispatch.
	// rotate models Pool.RotateForNewSession: re-key the binding, then fire the
	// ReasonClear fan-out — which reaches clearForSession and releases the parked
	// delivery — all while the outgoing child is still alive and no fresh child
	// exists. startFreshRunner, not this test, decides whether a gate was armed
	// first.
	rr := &rotatingRunner{w: w}
	rotate := func(gotOld sessions.SessionID) (sessions.SessionID, error) {
		if string(gotOld) != rotationOldSID {
			t.Errorf("rotate received oldID %q, want the bound pre-rotation id %q", gotOld, rotationOldSID)
		}
		owner.rekey(rotationOldSID, rotationNewSID)
		tr.clearForSession(rotationNewSID)
		return sessions.SessionID(rotationNewSID), nil
	}
	if err := startFreshRunner(rr, rotationOldSID, rotate); err != nil {
		t.Fatalf("startFreshRunner: %v", err)
	}
	if !slices.Equal(rr.restartFreshIDs, []string{rotationNewSID}) {
		t.Fatalf("RestartFresh calls = %v, want exactly one with the rotated id %q; the dispatch did not take the stream arm and the rest of this test would measure the wrong path",
			rr.restartFreshIDs, rotationNewSID)
	}

	// Step 5 — the woken delivery attempts its write and is REFUSED. Written as a
	// three-way select rather than recvStringWithin so the two red modes are
	// DISCRIMINATED in the record instead of collapsing into one timeout: a write
	// that landed is #1330's defect, and no attempt at all is a release failure.
	select {
	case <-w.noChild:
		// Refused: the gate was armed before rotate() fired the clear.
	case sid := <-w.wrote:
		t.Fatalf("the woken delivery WROTE the turn into session %q instead of being refused. startFreshRunner did not "+
			"arm the rotation gate before rotate(), so the turn went into the child RestartFresh is about to kill — "+
			"msgqueue reads that successful write as a commit and drops the head, so the fresh child never receives it "+
			"and never echoes it (#1330). children = %v", sid, w.snapshot())
	case <-time.After(2 * time.Second):
		t.Fatalf("the woken delivery neither wrote nor was refused within 2s of the rotation's clear. Two readings, and "+
			"neither is #1330's window: the clear did not release the parked delivery, or it resolved to no "+
			"conversation because the rebind in rotate() did not land (a fixture bug — production skips that case at "+
			"Debug level, invisibly). children = %v, Busy = %t", w.snapshot(), tr.Busy(testConvID))
	}

	// Step 6 — the asynchronous teardown finally lands (production's Run goroutine
	// reaping the cancelled child). Kept for ordering fidelity, and the assertion is
	// a no-bytes window check rather than a discriminator: once current is nil a
	// gated and an ungated writer refuse alike, so this step cannot distinguish
	// them. The property it models — the gate SURVIVES the teardown, because
	// takeStdin does not clear it — is pinned one layer down, in
	// TestRunner_BeginRotation_FreshChildClearsTheGate.
	w.beginRestart()
	assertNoWriteWithin(t, w.wrote, 50*time.Millisecond)

	// Step 7 — the fresh child binds stdin (setStdin), which is the gate's sole
	// release point; msgqueue re-attempts the same head.
	w.spawn(rotationNewSID)
	if got := recvStringWithin(t, w.wrote, "the retried delivery to reach the fresh child"); got != rotationNewSID {
		t.Fatalf("the turn was written to session %q, want the post-rotation session %q", got, rotationNewSID)
	}

	// Step 8 — the turn is in the post-rotation child, and in NO other.
	if got := w.stdinOf(rotationNewSID); !slices.Equal(got, []string{"turn-two"}) {
		t.Errorf("post-rotation child stdin = %v, want [turn-two]; children = %v", got, w.snapshot())
	}
	for _, c := range w.snapshot() {
		if c.sessionID == rotationNewSID {
			continue
		}
		if slices.Contains(c.stdin, "turn-two") {
			t.Errorf("the turn was delivered into a child that is not the post-rotation one (%v); "+
				"bytes into a doomed child's pipe are not a delivery. children = %v", c, w.snapshot())
		}
	}
	if !tr.Busy(testConvID) {
		t.Error("Busy = false after the post-rotation write; the mark that precedes the write was undone or never placed")
	}
	select {
	case reason := <-gaveUp:
		t.Errorf("msgqueue gave up on the head (%s); the retry ladder ran to its bound, which is a finding rather than a timeout", reason)
	default:
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild forces the
// ordering the M4 failure is suspected of: a turn is queued while the
// conversation's turn is running, so the drain parks in waitIdleForDelivery; the
// new_session rotation then re-keys the binding, tears the child down and fires
// the transition's clear; and the turn must land in the POST-rotation child.
//
// WHERE THE DETERMINISM COMES FROM — four facts, each checkable rather than
// trusted:
//
//  1. The busy mark (step 2) happens-before the Enqueue (step 3) in test-goroutine
//     program order, so the drain cannot slip past the gate before it closes.
//  2. waitIdleForDelivery re-checks membership and captures the generation
//     channel under ONE lock acquisition (setBusy's close-and-replace protocol),
//     so it can neither miss a wakeup nor return while the mark stands.
//  3. The only setBusy(conv, false) anywhere in this test is step 7's
//     clearForSession. There is no TurnEnd, no second tracker feed and no
//     eviction — so the release is unique and named.
//  4. Activate immediately precedes the hold on the drain goroutine, so receiving
//     w.activated proves the drain is AT the hold. No sleep "lets it get there".
//
// STEPS 6 AND 7 RUN IN THE OPPOSITE ORDER TO PRODUCTION, and the cost is stated
// rather than hidden. Production runs rotate() — which includes the whole
// transition fan-out and therefore the clear — to completion, and only then calls
// RestartFresh (`startFreshRunner`). Left in that order, whether the woken delivery
// finds the doomed child still live or already nil is a RACE, and AC2 forbids
// deciding this by luck. Pulling the teardown ahead of the clear removes the race
// while preserving the property under test: the clear is still the sole release,
// and the fresh child still arrives strictly after the wake. The consequence — a
// parked turn can, in production, wake into a still-live doomed child and write
// into it — is a DISTINCT window this test deliberately does not decide; it is
// recorded in the spec's Outcome section, and the M4 record argues against it
// being the M4 stall, since that path writes bytes and the shared stdin log
// showed none.
//
// The hold is the PRODUCTION streamTurnHoldTimeout (15 min), not a shortened one.
// A short hold converts a stuck-busy defect from "silent park" into "error → 1 Hz
// retry → give-up at 2 min" — a different failure signature from the one on
// record, which is silent. The bound is never reached here: if the clear fails to
// release, this test fails on its own 2 s recvStringWithin budget first. So the
// production value costs nothing in wall-clock and buys a faithful signature.
func TestInboundDeliver_RotationReleasesHeldTurn_DeliversToFreshChild(t *testing.T) {
	owner := newRotatingSessionOwner(rotationOldSID)
	w := newRotatingWriter()
	w.spawn(rotationOldSID)
	tr := newTurnBusyTracker(owner.resolve, discardLogger())

	// Reaching give-up would mean the retry ladder ran ~2 minutes of attempts;
	// that is a finding in its own right, not a timeout, so it is reported as one.
	gaveUp := make(chan string, 1)
	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(
			func(string) (handlers.TurnWriter, error) { return w, nil },
			tr,
			streamTurnHoldTimeout,
		),
		// Only the post-wake ErrNoLiveChild re-attempt rides this; GiveUpAfter keeps
		// its default because the test must never reach it.
		RetryInterval: 10 * time.Millisecond,
		OnGiveUp: func(convID, reason string) {
			rotationSignal(gaveUp, convID+": "+reason)
		},
		Logger: inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	// Step 2 — the pre-rotation turn is running (the e2e's M1). The assertion is
	// the non-vacuity guard: without a standing mark nothing would be held and
	// every step below would prove nothing.
	tr.observe(rotationOldSID, turnevent.TextChunk{MessageID: "m1", Text: "working"})
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after the pre-rotation turn opened; nothing would be held and the rest of this test is vacuous")
	}

	// Step 3 — the subsequent turn is accepted (the e2e's ack #2) and the drain
	// enters the delivery. Receiving w.activated puts it at the hold.
	q.Enqueue(testConvID, "turn-two")
	recvStringWithin(t, w.activated, "the drain to enter delivery (Activate precedes the hold)")

	// Step 4 — "not ready at the moment the drain attempts the turn": parked in
	// the hold, zero bytes. This window can never fail a correct held
	// implementation; it fires only if the hold did not engage.
	assertNoWriteWithin(t, w.wrote, 50*time.Millisecond)

	// Step 5 — rebindConversation / RebindSession: the conversation's binding
	// moves to the new id and the old id survives in SessionHistory.
	owner.rekey(rotationOldSID, rotationNewSID)

	// Step 6 — RestartFresh's teardown: the old child dies, Stdin() goes nil.
	w.beginRestart()

	// Step 7 — the transition observer's clear, keyed to the NEW session id
	// (transitionClearsTurn returns NewSessionID), which resolves only because
	// step 5 ran first. This is the sole release of the parked delivery, and the
	// woken delivery finds no live child.
	tr.clearForSession(rotationNewSID)
	recvStringWithin(t, w.noChild,
		"the parked delivery to attempt a write, 2s after the rotation's clear — either it is still inside "+
			"waitIdleForDelivery because the clear did not release the hold (the defect under investigation); or the "+
			"clear resolved to no conversation because the rebind in step 5 did not land (a fixture bug — production "+
			"skips that case at Debug level, invisibly); or the seam returned without attempting a write at all")

	// Step 8 — the fresh child binds stdin; msgqueue re-attempts the same head.
	w.spawn(rotationNewSID)
	if got := recvStringWithin(t, w.wrote, "the retried delivery to reach the fresh child"); got != rotationNewSID {
		t.Fatalf("the turn was written to session %q, want the post-rotation session %q", got, rotationNewSID)
	}

	// Step 9 — the turn is in the post-rotation child, and in NO other.
	if got := w.stdinOf(rotationNewSID); !slices.Equal(got, []string{"turn-two"}) {
		t.Errorf("post-rotation child stdin = %v, want [turn-two]; children = %v", got, w.snapshot())
	}
	for _, c := range w.snapshot() {
		if c.sessionID == rotationNewSID {
			continue
		}
		if slices.Contains(c.stdin, "turn-two") {
			t.Errorf("the turn was delivered into a child that is not the post-rotation one (%v); "+
				"bytes into a dead child's pipe are not a delivery. children = %v", c, w.snapshot())
		}
	}
	// The successful write opened the next turn: openForDelivery precedes the
	// write and was not undone.
	if !tr.Busy(testConvID) {
		t.Error("Busy = false after the post-rotation write; the mark that precedes the write was undone or never placed")
	}
	select {
	case reason := <-gaveUp:
		t.Errorf("msgqueue gave up on the head (%s); the retry ladder ran to its bound, which is a finding rather than a timeout", reason)
	default:
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestInboundDeliver_RotationWithoutHold_WritesIntoThePreRotationChild is the
// control, and it is not decoration. It builds the SAME fixture with a nil tracker
// — PTY mode, where the hold is a documented no-op — so the delivery does not park
// and writes straight into the still-live pre-rotation child.
//
// That makes the primary test's "no other child holds the payload" assertion
// non-vacuous PERMANENTLY rather than only at mutation time: it proves the fixture
// has a reachable path into the pre-rotation child, so the primary test's silence
// about that child is a result and not a structural impossibility.
//
// It stops at the pre-rotation write. There is no tracker to feed a running turn
// into and no hold to park in, so the rotation steps would order nothing.
func TestInboundDeliver_RotationWithoutHold_WritesIntoThePreRotationChild(t *testing.T) {
	w := newRotatingWriter()
	w.spawn(rotationOldSID)

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(
			func(string) (handlers.TurnWriter, error) { return w, nil },
			nil, // PTY mode: no tracker, so no hold
			streamTurnHoldTimeout,
		),
		RetryInterval: 10 * time.Millisecond,
		Logger:        inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	q.Enqueue(testConvID, "turn-two")
	recvStringWithin(t, w.activated, "the drain to enter delivery (Activate)")
	if got := recvStringWithin(t, w.wrote, "the unheld write to land"); got != rotationOldSID {
		t.Fatalf("the turn was written to session %q, want the pre-rotation session %q", got, rotationOldSID)
	}
	if got := w.stdinOf(rotationOldSID); !slices.Equal(got, []string{"turn-two"}) {
		t.Errorf("pre-rotation child stdin = %v, want [turn-two]; children = %v", got, w.snapshot())
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

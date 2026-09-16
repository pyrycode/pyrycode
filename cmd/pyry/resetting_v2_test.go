package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #2478 the resetting producer ---
//
// The subject here is the WIRE: which frames a reset puts on it, in what order, and
// carrying what. What the wrap-up turn itself does is session_reset_test.go's
// subject; which dispatch arm a frame takes is new_session_reset_test.go's. Both
// files' doubles are reused rather than re-cut, so the `written` token below is
// proved by a note the #2477 coordinator actually stored.
//
// resettingBcast rather than fakeInteractiveBcast, and not by preference: that
// double's own doc says it carries no mutex because its emitter spawns no
// goroutine. This one is written from whatever goroutine a reset tail runs on.

// resettingBcast is the mutex-guarded interactiveBroadcaster double. It answers one
// fixed conn set — a reset's three edges are three separate fan-outs over the same
// set, so a scripted per-call sequence would only obscure which edge saw what — and
// records every push in arrival order.
type resettingBcast struct {
	mu      sync.Mutex
	conns   []relay.ActiveConn
	pushErr map[string]error
	pushes  []resettingPush
}

type resettingPush struct {
	connID  string
	env     protocol.Envelope
	payload protocol.ResettingPayload
}

func newResettingBcast(conns ...relay.ActiveConn) *resettingBcast {
	return &resettingBcast{conns: conns}
}

// interactiveConns is the ordinary case: one interactive phone.
func interactiveConns() []relay.ActiveConn {
	return []relay.ActiveConn{{ConnID: "conn-interactive", Interactive: true}}
}

func (b *resettingBcast) ActiveConns(ctx context.Context) []relay.ActiveConn {
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]relay.ActiveConn(nil), b.conns...)
}

func (b *resettingBcast) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.pushErr[connID]; err != nil {
		return err
	}
	// Decoded HERE rather than in each assertion: a frame whose body is not a
	// ResettingPayload is a bug this double should surface at the push, not one that
	// reads as a zero-valued payload three assertions later.
	var payload protocol.ResettingPayload
	if env.Type == protocol.TypeResetting {
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return err
		}
	}
	b.pushes = append(b.pushes, resettingPush{connID: connID, env: env, payload: payload})
	return nil
}

func (b *resettingBcast) recorded() []resettingPush {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]resettingPush(nil), b.pushes...)
}

func (b *resettingBcast) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pushes)
}

// edge is one expected frame, spelled the way the wire spells it.
type edge struct {
	active  bool
	phase   string
	handoff string
}

func wireEdges(pushes []resettingPush) []edge {
	out := make([]edge, 0, len(pushes))
	for _, p := range pushes {
		out = append(out, edge{active: p.payload.Active, phase: p.payload.Phase, handoff: p.payload.Handoff})
	}
	return out
}

func assertEdges(t *testing.T, got []resettingPush, want ...edge) {
	t.Helper()
	have := wireEdges(got)
	if len(have) != len(want) {
		t.Fatalf("frames: got %d %+v, want %d %+v", len(have), have, len(want), want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Errorf("frame %d: got %+v, want %+v", i, have[i], want[i])
		}
	}
}

// quietLogger is the sink these tests write records into. None of them asserts on a
// record — the emitter's whole contract is what reaches the WIRE — but slog panics
// on a nil logger, so an unread sink is what the doubles get.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&safeLog{}, nil))
}

// attachedEmitter is the emitter as production has it: built over the daemon ctx in
// main.go, then handed the broadcaster from the relay leg.
func attachedEmitter(bcast interactiveBroadcaster) *resettingEmitterV2 {
	e := newResettingEmitterV2(context.Background(), quietLogger())
	e.attach(bcast)
	return e
}

// --- the emitter's own frames -------------------------------------------------

// TestResettingEmitter_EdgeShapes pins what each of the three methods puts on the
// wire, the falling edge's two empty strings included — the shape a client gates on
// Active for, rather than adding a fourth token to either closed set.
func TestResettingEmitter_EdgeShapes(t *testing.T) {
	t.Parallel()

	bcast := newResettingBcast(interactiveConns()...)
	e := attachedEmitter(bcast)

	e.wrappingUp(resetConvA)
	e.restarting(resetConvA, true)
	e.restarting(resetConvA, false)
	e.done(resetConvA)

	got := bcast.recorded()
	assertEdges(t, got,
		edge{active: true, phase: protocol.ResetPhaseWrappingUp, handoff: protocol.ResetHandoffPending},
		edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: protocol.ResetHandoffWritten},
		edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: protocol.ResetHandoffSkipped},
		edge{active: false, phase: "", handoff: ""},
	)
	for i, p := range got {
		if p.env.Type != protocol.TypeResetting {
			t.Errorf("frame %d type: got %q, want %q", i, p.env.Type, protocol.TypeResetting)
		}
		if p.payload.ConversationID != resetConvA {
			t.Errorf("frame %d conversation_id: got %q, want %q", i, p.payload.ConversationID, resetConvA)
		}
		if p.env.EventID != nil {
			t.Errorf("frame %d carries an EventID; resetting is ephemeral status and joins no replay ring", i)
		}
		if p.env.TS.IsZero() {
			t.Errorf("frame %d has no timestamp", i)
		}
		if p.env.ID == 0 {
			t.Errorf("frame %d has no envelope id", i)
		}
	}
}

// TestResettingEmitter_InteractiveGate is AC-5's first half: a conn that never
// negotiated interactive receives no resetting frame, while one that did, on the
// same snapshot, receives every edge.
func TestResettingEmitter_InteractiveGate(t *testing.T) {
	t.Parallel()

	bcast := newResettingBcast(
		relay.ActiveConn{ConnID: "conn-coarse", Interactive: false},
		relay.ActiveConn{ConnID: "conn-interactive", Interactive: true},
	)
	e := attachedEmitter(bcast)

	e.wrappingUp(resetConvA)
	e.restarting(resetConvA, false)
	e.done(resetConvA)

	for _, p := range bcast.recorded() {
		if p.connID != "conn-coarse" {
			continue
		}
		t.Fatalf("a resetting frame reached the non-interactive conn: %+v", p.payload)
	}
	if got := bcast.count(); got != 3 {
		t.Fatalf("the interactive conn received %d frames, want 3", got)
	}
}

// TestResettingEmitter_InertWithoutBroadcaster covers every state in which the
// emitter has nobody to fan out to: a nil receiver (the PTY posture, where
// activeSessionStarter's optional field is simply omitted), the window before
// startRelayV2 attaches, and the detach that ends the relay leg.
func TestResettingEmitter_InertWithoutBroadcaster(t *testing.T) {
	t.Parallel()

	var nilEmitter *resettingEmitterV2
	nilEmitter.wrappingUp(resetConvA) // must not panic
	nilEmitter.restarting(resetConvA, true)
	nilEmitter.done(resetConvA)
	nilEmitter.attach(newResettingBcast())
	nilEmitter.detach()

	unattached := newResettingEmitterV2(context.Background(), quietLogger())
	unattached.wrappingUp(resetConvA) // must not panic

	bcast := newResettingBcast(interactiveConns()...)
	e := attachedEmitter(bcast)
	e.wrappingUp(resetConvA)
	e.detach()
	e.done(resetConvA)
	if got := bcast.count(); got != 1 {
		t.Fatalf("got %d frames, want 1: only the pre-detach edge should reach the wire", got)
	}
}

// TestResettingEmitter_PushErrorDoesNotAbortTheFanOut: a conn that closed between
// the snapshot and the push must not cost the other conns their frame. There is no
// re-sync path to fall back on — the frame is live-only.
func TestResettingEmitter_PushErrorDoesNotAbortTheFanOut(t *testing.T) {
	t.Parallel()

	bcast := newResettingBcast(
		relay.ActiveConn{ConnID: "conn-gone", Interactive: true},
		relay.ActiveConn{ConnID: "conn-live", Interactive: true},
	)
	bcast.pushErr = map[string]error{"conn-gone": errors.New("conn not found")}
	e := attachedEmitter(bcast)

	e.wrappingUp(resetConvA)

	got := bcast.recorded()
	if len(got) != 1 || got[0].connID != "conn-live" {
		t.Fatalf("got %+v, want the live conn's frame alone", got)
	}
}

// TestResettingEmitter_CancelledContextFansOutToNobody is the teardown posture: with
// the daemon ctx captured at construction cancelled, a late edge reaches nobody
// rather than pushing into a winding-down manager.
func TestResettingEmitter_CancelledContextFansOutToNobody(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bcast := newResettingBcast(interactiveConns()...)
	e := newResettingEmitterV2(ctx, quietLogger())
	e.attach(bcast)

	e.done(resetConvA)

	if got := bcast.count(); got != 0 {
		t.Fatalf("got %d frames after ctx cancellation, want 0", got)
	}
}

// TestResettingEmitter_ConcurrentConversations is why the counter and the late-bound
// broadcaster are guarded at all: two conversations reset at once, on two
// goroutines, and -race is the assertion.
func TestResettingEmitter_ConcurrentConversations(t *testing.T) {
	t.Parallel()

	bcast := newResettingBcast(interactiveConns()...)
	e := attachedEmitter(bcast)

	var wg sync.WaitGroup
	for _, conv := range []string{resetConvA, resetConvB} {
		wg.Add(1)
		go func(convID string) {
			defer wg.Done()
			e.wrappingUp(convID)
			e.restarting(convID, false)
			e.done(convID)
		}(conv)
	}
	wg.Wait()

	if got := bcast.count(); got != 6 {
		t.Fatalf("got %d frames, want 6 (three per conversation)", got)
	}
}

// --- the edges as one whole reset produces them -------------------------------

// resettingTail is the reset tail under test: a starter carrying a REAL #2477
// coordinator (so the handoff token is decided by the same code production decides
// it with) and an emitter wired to a recording broadcaster. The three counters are
// snapshots of how many frames stood at three moments, which is how "after the
// rotation" and "before the release" become assertions rather than claims.
type resettingTail struct {
	starter     activeSessionStarter
	bcast       *resettingBcast
	runner      *asyncRunner
	atWrite     int
	atRotate    int
	atRelease   int
	rotateCalls int
}

// newResettingTail wires the tail over the coordinator fixture `build` produces.
// rotateErr != nil makes the rotation fail, which is AC-3's second row.
func newResettingTail(t *testing.T, build func(*resettingTail) *resetFixture, rotateErr error) *resettingTail {
	t.Helper()
	tail := &resettingTail{bcast: newResettingBcast(interactiveConns()...), runner: newAsyncRunner(4242)}
	f := build(tail)
	// Only the four seams resetThenRotate actually reads. currentConv and
	// resolveBound belong to start, which these rows deliberately bypass: the tail is
	// called directly so every frame lands on the test goroutine before the
	// assertions read it, with no polling and nothing in flight.
	tail.starter = activeSessionStarter{
		rotate: func(old sessions.SessionID) (sessions.SessionID, error) {
			tail.atRotate = tail.bcast.count()
			tail.rotateCalls++
			if rotateErr != nil {
				return "", rotateErr
			}
			return sessions.SessionID("fresh-" + string(old)), nil
		},
		reset:     f.reset,
		resetting: attachedEmitter(tail.bcast),
		log:       quietLogger(),
	}
	return tail
}

func (tail *resettingTail) run() {
	release := func() { tail.atRelease = tail.bcast.count() }
	tail.starter.resetThenRotate(release, nil, tail.runner, sessions.SessionID("old"), resetConvA, "", false)
}

// resetThatWritesANote is #2477's happy path, copied from
// session_reset_test.go's ordering row: an interrupt whose child goes idle
// asynchronously, then a reply that storeNote admits and writes. It is the only
// shape that produces `written`, so the token is proved by a note the coordinator
// actually stored rather than by a bool a test injected.
func resetThatWritesANote(t *testing.T, tail *resettingTail) *resetFixture {
	t.Helper()
	var f *resetFixture
	f = newResetFixture(t, resetOptions{
		startBusy: true,
		runner: func(capture *wrapUpCapture, steps *resetSteps) sessions.Runner {
			return &resetRunner{capture: capture, steps: steps, onInterrupt: func() {
				go func() {
					time.Sleep(20 * time.Millisecond)
					f.goIdle()
				}()
			}}
		},
		answerOnWrite: func(fx *resetFixture) {
			// Runs inside the write, on the tail's goroutine: the rising edge must
			// already be on the wire by the time the wrap-up turn is delivered.
			tail.atWrite = tail.bcast.count()
			fx.answer(resetReplyText, true)
		},
	})
	return f
}

// resetThatWritesNothing is the unresolvable arm — one of AC-2's skip reasons, and
// the cheapest of them: the binding moved, so there is nobody left to ask.
func resetThatWritesNothing(t *testing.T, _ *resettingTail) *resetFixture {
	t.Helper()
	return newResetFixture(t, resetOptions{unresolvable: true})
}

// TestResetThenRotate_EmitsTheThreeEdgesInOrder is AC-1 and AC-2's two ends: one
// reset delivers a rising wrapping_up/pending, a rising restarting carrying the
// outcome the wrap-up actually reached, then a falling edge with both fields empty —
// all three naming the conversation's canonical id.
func TestResetThenRotate_EmitsTheThreeEdgesInOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		build   func(*testing.T, *resettingTail) *resetFixture
		handoff string
	}{
		{name: "a note was written", build: resetThatWritesANote, handoff: protocol.ResetHandoffWritten},
		{name: "no note was written", build: resetThatWritesNothing, handoff: protocol.ResetHandoffSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tail := newResettingTail(t, func(tl *resettingTail) *resetFixture { return tc.build(t, tl) }, nil)
			tail.run()

			assertEdges(t, tail.bcast.recorded(),
				edge{active: true, phase: protocol.ResetPhaseWrappingUp, handoff: protocol.ResetHandoffPending},
				edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: tc.handoff},
				edge{active: false, phase: "", handoff: ""},
			)
			for _, p := range tail.bcast.recorded() {
				if p.payload.ConversationID != resetConvA {
					t.Errorf("conversation_id: got %q, want the canonical %q", p.payload.ConversationID, resetConvA)
				}
			}
		})
	}
}

// TestResetThenRotate_FallingEdgeFollowsTheRotation is AC-3 on the success path: both
// rising edges are on the wire before the rotation is attempted and the falling edge
// is not, so a client never sees the indicator clear while claude is still being
// respawned.
func TestResetThenRotate_FallingEdgeFollowsTheRotation(t *testing.T) {
	tail := newResettingTail(t, func(tl *resettingTail) *resetFixture { return resetThatWritesANote(t, tl) }, nil)
	tail.run()

	if tail.atWrite != 1 {
		t.Errorf("the wrap-up turn was delivered with %d frames on the wire, want 1 (wrapping_up alone)", tail.atWrite)
	}
	if tail.atRotate != 2 {
		t.Errorf("the rotation was attempted with %d frames on the wire, want 2 (both rising edges, no falling one)", tail.atRotate)
	}
	// AC-5's second half, the part this ticket must not disturb: the rotation still
	// happens exactly once, so the one session_transition it broadcasts still follows.
	if tail.rotateCalls != 1 || tail.runner.restartCount() != 1 {
		t.Fatalf("rotations=%d restarts=%d, want exactly 1 of each", tail.rotateCalls, tail.runner.restartCount())
	}
	got := tail.bcast.recorded()
	if last := got[len(got)-1]; last.payload.Active {
		t.Fatalf("the last frame is still a rising edge: %+v", last.payload)
	}
}

// TestResetThenRotate_FallingEdgeAfterAFailedRotation is AC-3's other half: a
// rotation that errors still closes the sequence, so a client's indicator clears
// without a timeout of its own.
func TestResetThenRotate_FallingEdgeAfterAFailedRotation(t *testing.T) {
	tail := newResettingTail(t,
		func(tl *resettingTail) *resetFixture { return resetThatWritesNothing(t, tl) },
		errors.New("rotate refused"))
	tail.run()

	assertEdges(t, tail.bcast.recorded(),
		edge{active: true, phase: protocol.ResetPhaseWrappingUp, handoff: protocol.ResetHandoffPending},
		edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: protocol.ResetHandoffSkipped},
		edge{active: false, phase: "", handoff: ""},
	)
	if tail.runner.restartCount() != 0 {
		t.Fatalf("the rotation failed but the runner restarted %d times", tail.runner.restartCount())
	}
}

// TestResetThenRotate_FallingEdgePrecedesTheRelease pins the LIFO ordering the two
// defers rest on. Reversed, release would admit a second reset whose own wrapping_up
// went out ahead of this one's active:false — and a client clearing its indicator on
// that late falling edge would strand the second reset's, the one ordering error the
// falling-edge guarantee cannot recover from.
func TestResetThenRotate_FallingEdgePrecedesTheRelease(t *testing.T) {
	tail := newResettingTail(t, func(tl *resettingTail) *resetFixture { return resetThatWritesNothing(t, tl) }, nil)
	tail.run()

	if tail.atRelease != 3 {
		t.Fatalf("release saw %d frames; the falling edge must already be on the wire (want 3)", tail.atRelease)
	}
}

// --- the arms that emit nothing -----------------------------------------------

// TestStartNewSession_InertArmsEmitNoResettingFrame is AC-4: a new_session that runs
// no wrap-up puts nothing on this wire at all. Every row returns from start before
// the tail's goroutine exists, so the count is read with nothing in flight.
func TestStartNewSession_InertArmsEmitNoResettingFrame(t *testing.T) {
	newStarterFor := func(t *testing.T, bcast *resettingBcast, mutate func(*testing.T, *activeSessionStarter)) activeSessionStarter {
		t.Helper()
		s := activeSessionStarter{
			currentConv: func() string { return resetConvA },
			resolveBound: func(string) (sessions.Runner, sessions.SessionID, string, bool) {
				return newAsyncRunner(4242), sessions.SessionID("old"), "", true
			},
			rotate: func(old sessions.SessionID) (sessions.SessionID, error) {
				return sessions.SessionID("fresh-" + string(old)), nil
			},
			reset: &conversationReset{
				base:    context.Background(),
				resolve: func(string) (resetTarget, bool) { return resetTarget{}, false },
				log:     quietLogger(),
			},
			resetting: attachedEmitter(bcast),
			log:       quietLogger(),
		}
		mutate(t, &s)
		return s
	}

	for _, tc := range []struct {
		name   string
		convID string
		mutate func(*testing.T, *activeSessionStarter)
	}{
		{
			name:   "no active conversation",
			convID: "",
			mutate: func(_ *testing.T, s *activeSessionStarter) { s.currentConv = func() string { return "" } },
		},
		{
			name:   "named id is not canonical",
			convID: "not-a-conversation-id",
			mutate: func(_ *testing.T, s *activeSessionStarter) {},
		},
		{
			name:   "conversation has no bound session",
			convID: resetConvA,
			mutate: func(_ *testing.T, s *activeSessionStarter) {
				s.resolveBound = func(string) (sessions.Runner, sessions.SessionID, string, bool) {
					return nil, "", "", false
				}
			},
		},
		{
			name:   "bound runner cannot restart",
			convID: resetConvA,
			mutate: func(_ *testing.T, s *activeSessionStarter) {
				s.resolveBound = func(string) (sessions.Runner, sessions.SessionID, string, bool) {
					return &plainRunner{}, sessions.SessionID("old"), "", true
				}
			},
		},
		{
			name:   "named conversation has no live child",
			convID: resetConvA,
			mutate: func(_ *testing.T, s *activeSessionStarter) {
				s.resolveBound = func(string) (sessions.Runner, sessions.SessionID, string, bool) {
					return newAsyncRunner(0), sessions.SessionID("old"), "", true
				}
			},
		},
		{
			name:   "a reset is already in flight",
			convID: resetConvA,
			mutate: func(t *testing.T, s *activeSessionStarter) {
				if _, ok := s.reset.begin(resetConvA); !ok {
					t.Fatal("could not claim the conversation for the first reset")
				}
			},
		},
		{
			name:   "no reset wired: the synchronous pre-#2477 rotation",
			convID: "",
			mutate: func(_ *testing.T, s *activeSessionStarter) { s.reset = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bcast := newResettingBcast(interactiveConns()...)
			s := newStarterFor(t, bcast, tc.mutate)
			if err := s.StartNewSession(tc.convID); err != nil {
				t.Fatalf("StartNewSession: %v", err)
			}
			if got := bcast.count(); got != 0 {
				t.Fatalf("got %d resetting frames on an arm that runs no wrap-up, want 0: %+v",
					got, wireEdges(bcast.recorded()))
			}
		})
	}
}

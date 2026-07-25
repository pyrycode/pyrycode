package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/turncommit"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// gatingWriter is a handlers.TurnWriter that models "claude busy mid-turn": its
// WriteUserTurn blocks on the release channel until the test lets one turn
// commit, then records the delivered text in order. It signals delivery start on
// entered and commit on completed so tests synchronise without sleeps. Activate
// is a no-op (the session is already live on the unit path). Closing release up
// front makes every delivery commit immediately (claude idle).
type gatingWriter struct {
	mu        sync.Mutex
	delivered []string
	release   chan struct{}
	entered   chan string
	completed chan string
}

func newGatingWriter() *gatingWriter {
	return &gatingWriter{
		release:   make(chan struct{}),
		entered:   make(chan string, 16),
		completed: make(chan string, 16),
	}
}

func (w *gatingWriter) Activate(ctx context.Context) error { return nil }

func (w *gatingWriter) WriteUserTurn(ctx context.Context, convID string, payload []byte) error {
	text := string(payload)
	w.entered <- text
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	w.delivered = append(w.delivered, text)
	w.mu.Unlock()
	w.completed <- text
	return nil
}

func (w *gatingWriter) deliveredOrder() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.delivered...)
}

func inboundTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func recvStringWithin(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// TestInboundDeliver_EnqueueWhileBusy_OrderedDrain is the live-wiring proof: a
// real msgqueue.Queue wired through newInboundDeliver to a gating writer. Three
// messages enqueued while a turn is in flight are held behind the busy head and
// drain in enqueue order, one at a time (never more than one in flight) as each
// turn is released — the #704 behaviour, now live (AC#2/AC#4).
func TestInboundDeliver_EnqueueWhileBusy_OrderedDrain(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve, nil, streamTurnHoldTimeout),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	// Enqueue three to one conversation. The gate is closed, so the head enters
	// delivery and blocks; the other two stay queued behind it.
	q.Enqueue("conv", "m1")
	q.Enqueue("conv", "m2")
	q.Enqueue("conv", "m3")

	if got := recvStringWithin(t, w.entered, "first delivery entry"); got != "m1" {
		t.Fatalf("first entered = %q, want m1", got)
	}
	// Single-in-flight: nothing else enters delivery while the head is gated.
	select {
	case got := <-w.entered:
		t.Fatalf("a second delivery entered before the gate opened: %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	// Release one turn at a time → in-order commit, one at a time.
	for _, want := range []string{"m1", "m2", "m3"} {
		w.release <- struct{}{}
		if got := recvStringWithin(t, w.completed, "commit"); got != want {
			t.Fatalf("commit = %q, want %q", got, want)
		}
	}

	if got := w.deliveredOrder(); len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Errorf("delivered order = %v, want [m1 m2 m3]", got)
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestInboundDeliver_Idle_DrainsPromptly covers the idle path: with the gate
// open (claude idle, every delivery commits immediately), a single enqueue
// drains without any further signal.
func TestInboundDeliver_Idle_DrainsPromptly(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	close(w.release) // gate open: deliveries commit immediately
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve, nil, streamTurnHoldTimeout),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- q.Run(ctx) }()

	q.Enqueue("conv", "only")
	if got := recvStringWithin(t, w.completed, "commit"); got != "only" {
		t.Fatalf("commit = %q, want only", got)
	}

	cancel()
	if err := recvErrWithin(t, runDone, "queue Run exit"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

func recvErrWithin(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// --- #1199: the stream-path mid-turn hold -------------------------------------

// funcWriter is a handlers.TurnWriter whose write body each test supplies. It
// covers the cases gatingWriter's fixed body cannot: a write that fails, a write
// that inspects the tracker from inside, and a write that must be proven never to
// run at all. Activate is a no-op — the seam's Activate leg is unchanged by #1199
// and is covered by the two tests above.
type funcWriter struct {
	write func(ctx context.Context, convID string, payload []byte) error
}

func (w funcWriter) Activate(context.Context) error { return nil }

func (w funcWriter) WriteUserTurn(ctx context.Context, convID string, payload []byte) error {
	return w.write(ctx, convID, payload)
}

// commitClaimingWriter mirrors streamsup.WriteTurn's structure on the stream path:
// it claims the turncommit gate carried on ctx and only then records the write,
// returning turncommit.ErrDropped on a denied claim. gatingWriter deliberately
// does not claim, so the drop test below uses this one instead — the claim is what
// sets msgqueue's committing flag, and therefore what makes "the held head is
// still removable" an assertion about the hold rather than a property of a fake
// that never claims at all.
type commitClaimingWriter struct {
	mu        sync.Mutex
	delivered []string
	entered   chan string
}

func newCommitClaimingWriter() *commitClaimingWriter {
	return &commitClaimingWriter{entered: make(chan string, 16)}
}

func (w *commitClaimingWriter) Activate(context.Context) error { return nil }

func (w *commitClaimingWriter) WriteUserTurn(ctx context.Context, convID string, payload []byte) error {
	if gate := turncommit.From(ctx); gate != nil && !gate() {
		return turncommit.ErrDropped
	}
	text := string(payload)
	w.mu.Lock()
	w.delivered = append(w.delivered, text)
	w.mu.Unlock()
	w.entered <- text
	return nil
}

func (w *commitClaimingWriter) deliveredOrder() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.delivered...)
}

// holdTestTracker builds a tracker whose sole session "sess-a" owns testConvID, so
// a test can end the running turn the way production does — a TurnEnd on the
// fan-in — rather than by reaching for the mark directly.
func holdTestTracker() *turnBusyTracker {
	return newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger())
}

func endHeldTurn(tr *turnBusyTracker) {
	tr.observe("sess-a", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
}

// waitBacklog polls until convID's backlog texts equal want. Polling (not a
// barrier) because the transition it waits for — the drain advancing past a
// delivered head — has no seam a unit test can subscribe to; the flake direction
// is safe, since a correct implementation reaches the state in microseconds and
// only machine slowness costs extra iterations.
func waitBacklog(t *testing.T, q *msgqueue.Queue, convID string, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for {
		got = nil
		for _, m := range q.Snapshot(convID) {
			got = append(got, m.Text)
		}
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog for %s = %v, want %v", convID, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertNoWriteWithin asserts nothing enters the write path for a grace window.
// The window can never fail a correct held implementation (a held delivery never
// writes); it fires only when the hold did not engage.
func assertNoWriteWithin(t *testing.T, entered <-chan string, d time.Duration) {
	t.Helper()
	select {
	case got := <-entered:
		t.Fatalf("%q was written while the conversation's turn was still running; the mid-turn hold did not engage", got)
	case <-time.After(d):
	}
}

// #1199 AC4 (PTY unchanged, explicitly): with a nil tracker the seam holds
// nothing. Two back-to-back sends both deliver with no turn-end signal of any
// kind, which is the pre-#1199 body's behaviour verbatim.
//
// Non-vacuous because the SAME shape with a tracker wired is exactly the hold
// asserted below: the second send would park until a TurnEnd that never comes, and
// this test would time out. PTY mode never constructs a tracker, so this is the
// path the daemon's default mode runs on every delivery.
func TestInboundDeliver_NilTracker_NoHold(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	close(w.release) // every write commits immediately
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve, nil, streamTurnHoldTimeout),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	q.Enqueue(testConvID, "m1")
	q.Enqueue(testConvID, "m2")

	for _, want := range []string{"m1", "m2"} {
		if got := recvStringWithin(t, w.completed, "commit"); got != want {
			t.Fatalf("commit = %q, want %q", got, want)
		}
	}
}

// #1199 AC1: the hold engages, and it is a GUARANTEE rather than a better race.
// A send submitted while the conversation's turn is running stays in the backlog
// for the whole turn and is written only once the turn ends; several held sends
// are written in submission order.
//
// The determinism is structural, not timing-based: openForDelivery marks on the
// drain's own goroutine inside the same deliver call that then writes, so m1's
// mark happens-before m1's delivery returns, which happens-before m2's delivery
// starts. The fake writer here returns as soon as it is entered — modelling the
// stream path's write-and-return exactly — so before this ticket m2 and m3 would
// both have drained within microseconds of m1.
func TestInboundDeliver_StreamHold_HoldsMidTurnSends(t *testing.T) {
	t.Parallel()
	w := newGatingWriter()
	close(w.release) // the stream write returns as soon as the envelope is written
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }
	tr := holdTestTracker()

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve, tr, 10*time.Second),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	// m1 opens the turn. The mark precedes the write, so once m1's write has
	// completed the conversation reads busy.
	q.Enqueue(testConvID, "m1")
	if got := recvStringWithin(t, w.entered, "m1 write entry"); got != "m1" {
		t.Fatalf("first write = %q, want m1", got)
	}
	if got := recvStringWithin(t, w.completed, "m1 commit"); got != "m1" {
		t.Fatalf("first commit = %q, want m1", got)
	}
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after the first delivery; nothing would be held and the rest of this test would be vacuous")
	}

	// Two sends submitted mid-turn.
	q.Enqueue(testConvID, "m2")
	q.Enqueue(testConvID, "m3")

	assertNoWriteWithin(t, w.entered, 100*time.Millisecond)
	// ...and they are still in the backlog, which is what the queued-backlog UI
	// snapshots. Before this ticket the drain had emptied it.
	waitBacklog(t, q, testConvID, []string{"m2", "m3"})

	// Each turn ending releases exactly one held send, in submission order — the
	// next write re-marks the conversation, so m3 stays held until a second TurnEnd.
	for _, want := range []string{"m2", "m3"} {
		endHeldTurn(tr)
		if got := recvStringWithin(t, w.completed, "commit after turn end"); got != want {
			t.Fatalf("commit after turn end = %q, want %q", got, want)
		}
	}

	if got := w.deliveredOrder(); !slices.Equal(got, []string{"m1", "m2", "m3"}) {
		t.Errorf("delivered order = %v, want [m1 m2 m3]", got)
	}
}

// #1199 AC2: the drop-before-drain control removes a HELD message. The dropped
// message is never written, and the backlog loses exactly that entry while the
// drain advances to the one behind it.
//
// The writer claims the turncommit gate as streamsup.WriteTurn does, so the head
// is un-droppable from the instant the write begins. That is what makes
// Remove-returns-true an assertion about the hold: the window in which a head is
// draining && !committing was effectively zero before this ticket, and the hold is
// what widens it to the length of the running turn.
func TestInboundDeliver_StreamHold_HeldMessageIsDroppable(t *testing.T) {
	t.Parallel()
	w := newCommitClaimingWriter()
	resolve := func(string) (handlers.TurnWriter, error) { return w, nil }
	tr := holdTestTracker()

	q, err := msgqueue.New(msgqueue.Config{
		Deliver: newInboundDeliver(resolve, tr, 10*time.Second),
		Logger:  inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	q.Enqueue(testConvID, "m1")
	if got := recvStringWithin(t, w.entered, "m1 write"); got != "m1" {
		t.Fatalf("first write = %q, want m1", got)
	}
	if !tr.Busy(testConvID) {
		t.Fatal("Busy = false after the first delivery; nothing would be held")
	}

	m2ID := q.Enqueue(testConvID, "m2")
	q.Enqueue(testConvID, "m3")
	if m2ID == 0 {
		t.Fatal("Enqueue rejected m2 (backlog full?); there is nothing to drop")
	}
	assertNoWriteWithin(t, w.entered, 100*time.Millisecond)

	if !q.Remove(testConvID, m2ID) {
		t.Fatal("Remove of the held head returned false; the head was un-droppable, which is the regression this ticket fixes")
	}
	waitBacklog(t, q, testConvID, []string{"m3"})

	// The drain advanced to m3, which parks on the same hold until the turn ends.
	assertNoWriteWithin(t, w.entered, 100*time.Millisecond)
	endHeldTurn(tr)
	if got := recvStringWithin(t, w.entered, "m3 write"); got != "m3" {
		t.Fatalf("write after turn end = %q, want m3", got)
	}

	if got := w.deliveredOrder(); !slices.Equal(got, []string{"m1", "m3"}) {
		t.Errorf("delivered order = %v, want [m1 m3]; the dropped message must never reach claude", got)
	}
}

// #1199 AC3 (first half): a turn that never ends bounds the attempt rather than
// holding the conversation forever, and it does so having written NOTHING — so
// msgqueue's retry is a clean re-attempt and can never duplicate a turn.
//
// The seam is called directly here: the classification (errors.Is against
// context.DeadlineExceeded) is the property, and going through the queue would
// only expose it as text in the give-up reason.
func TestInboundDeliver_StreamHold_TimesOutWithoutWriting(t *testing.T) {
	t.Parallel()

	var writes atomic.Int64
	w := funcWriter{write: func(context.Context, string, []byte) error {
		writes.Add(1)
		return nil
	}}
	tr := holdTestTracker()
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "working"}) // a turn that never ends

	deliver := newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, tr, 50*time.Millisecond)
	err := deliver(context.Background(), testConvID, []byte("held"))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deliver on a never-ending turn = %v, want an error satisfying context.DeadlineExceeded", err)
	}
	if got := writes.Load(); got != 0 {
		t.Errorf("writes = %d, want 0; a timed-out hold must write nothing, or its retry duplicates the turn", got)
	}
}

// #1199 AC3 (second half): that bounded failure surfaces through the EXISTING
// give-up path — OnGiveUp, which the daemon routes to the typed
// session_error/CodeSessionBlocked frame — rather than leaving the conversation
// held forever. The bounds are shrunk to keep the test fast; the production
// arithmetic against them is recorded on streamTurnHoldTimeout.
func TestInboundDeliver_StreamHold_NeverEndingTurnGivesUp(t *testing.T) {
	t.Parallel()

	w := funcWriter{write: func(context.Context, string, []byte) error { return nil }}
	tr := holdTestTracker()
	tr.observe("sess-a", turnevent.TextChunk{MessageID: "m1", Text: "working"})

	gaveUp := make(chan string, 1)
	q, err := msgqueue.New(msgqueue.Config{
		Deliver:       newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, tr, 50*time.Millisecond),
		RetryInterval: 10 * time.Millisecond,
		GiveUpAfter:   60 * time.Millisecond,
		OnGiveUp: func(convID, _ string) {
			select {
			case gaveUp <- convID:
			default:
			}
		},
		Logger: inboundTestLogger(t),
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	q.Enqueue(testConvID, "held")
	if got := recvStringWithin(t, gaveUp, "give-up notification"); got != testConvID {
		t.Errorf("OnGiveUp convID = %q, want %q", got, testConvID)
	}
}

// #1199 AC1: a write that FAILS after the mark leaves no stale mark behind.
// Without the undo the conversation would read busy forever — no event closes a
// turn that never started — and every later send on it would hold until the bound.
// The error is returned verbatim so msgqueue's errors.Is classification of
// ErrNoLiveSession / ErrTrustModalPending / turncommit.ErrDropped is untouched.
func TestInboundDeliver_StreamHold_UndoesMarkOnWriteFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("no live child")
	w := funcWriter{write: func(context.Context, string, []byte) error { return wantErr }}
	tr := holdTestTracker()

	deliver := newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, tr, time.Second)
	err := deliver(context.Background(), testConvID, []byte("m1"))

	if !errors.Is(err, wantErr) {
		t.Errorf("deliver = %v, want the write error verbatim (%v)", err, wantErr)
	}
	if tr.Busy(testConvID) {
		t.Error("Busy = true after a failed write; the mark outlived the attempt and would hold every later send")
	}
}

// #1199 AC1 (the ordering teeth): the mark is placed BEFORE the write, asserted
// from inside the write itself. A mark-after-write implementation fails here, and
// only here — it satisfies every other assertion in this file while leaving a
// stale mark on a fast child, whose TurnEnd can land and clear before the marking
// statement runs.
func TestInboundDeliver_StreamHold_MarksBeforeTheWrite(t *testing.T) {
	t.Parallel()

	tr := holdTestTracker()
	var busyAtWrite bool
	w := funcWriter{write: func(_ context.Context, convID string, _ []byte) error {
		busyAtWrite = tr.Busy(convID)
		return nil
	}}

	deliver := newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, tr, time.Second)
	if err := deliver(context.Background(), testConvID, []byte("m1")); err != nil {
		t.Fatalf("deliver = %v, want nil", err)
	}

	if !busyAtWrite {
		t.Error("the conversation read idle on entry to the write; the mark must precede the write")
	}
	if !tr.Busy(testConvID) {
		t.Error("Busy = false after a successful write; the turn the write started must be marked open")
	}
}

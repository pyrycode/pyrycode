package msgqueue

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// The #2038 attachment shape, the one case where a message's queued text and its
// delivered payload differ. deliveredHostPath is the needle every AC-2 assertion
// searches for: it appears ONLY in the composed delivery payload, never in the
// text a client reads back, so finding it anywhere on the seam's side of the
// boundary is the leak.
const (
	deliveredText     = "look at this screenshot"
	deliveredHostPath = "/Users/operator/.pyry/instances/default/attachments/9f2/photo.png"
	deliveredPayload  = "The user attached a file at " + deliveredHostPath + "\n\n" + deliveredText
	deliveredMsgID    = "client-minted-7"
)

// deliveredRecorder captures every OnDelivered invocation. The seam fires from a
// drain goroutine, so the mutex is load-bearing; fired lets a test wait for the
// first call without polling.
type deliveredRecorder struct {
	mu    sync.Mutex
	convs []string
	msgs  []QueuedMessage
	fired chan struct{}
}

func newDeliveredRecorder() *deliveredRecorder {
	return &deliveredRecorder{fired: make(chan struct{}, 16)}
}

func (r *deliveredRecorder) onDelivered(convID string, msg QueuedMessage) {
	r.mu.Lock()
	r.convs = append(r.convs, convID)
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	r.fired <- struct{}{}
}

func (r *deliveredRecorder) calls() (convs []string, msgs []QueuedMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.convs...), append([]QueuedMessage(nil), r.msgs...)
}

// AC 1, AC 2, AC 5's payload source: a confirmed delivery hands the seam the
// QUEUED TEXT and the client's own message id — never the delivered payload,
// which for an attachment-bearing message names an on-host path
// (docs/protocol-mobile.md § Error codes forbids putting that on the wire, and
// this record is served to paired devices by #2116).
//
// The fakeDeliver order assertion is the non-vacuity control: it proves the
// delivery really did carry deliveredHostPath, so "the seam's text does not"
// compares two genuinely different values rather than passing because nothing
// ever held the path.
func TestQueue_OnDelivered_CarriesQueuedTextNotDeliveryPayload(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	rec := newDeliveredRecorder()
	q, err := New(Config{Deliver: f.deliver, OnDelivered: rec.onDelivered})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	id := q.EnqueueDelivery("conv-a", deliveredMsgID, deliveredText, deliveredPayload)
	if id != 1 {
		t.Fatalf("EnqueueDelivery id = %d, want 1", id)
	}
	recvWithin(t, rec.fired, "the delivered seam to fire")

	if got := f.deliveredOrder(); !equalStrings(got, []string{deliveredPayload}) {
		t.Fatalf("claude received %q, want the composed delivery payload %q", got, deliveredPayload)
	}

	convs, msgs := rec.calls()
	if len(msgs) != 1 {
		t.Fatalf("OnDelivered fired %d times, want 1", len(msgs))
	}
	if convs[0] != "conv-a" {
		t.Errorf("convID = %q, want %q", convs[0], "conv-a")
	}
	if msgs[0].Text != deliveredText {
		t.Errorf("Text = %q, want the queued text %q", msgs[0].Text, deliveredText)
	}
	if strings.Contains(msgs[0].Text, deliveredHostPath) {
		t.Errorf("Text leaked the on-host path %q", deliveredHostPath)
	}
	if msgs[0].MessageID != deliveredMsgID {
		t.Errorf("MessageID = %q, want the client's own id %q", msgs[0].MessageID, deliveredMsgID)
	}
	if msgs[0].ID != 1 {
		t.Errorf("ID = %d, want the queued id 1", msgs[0].ID)
	}
	if msgs[0].AttachmentIDs != nil {
		t.Errorf("AttachmentIDs = %q, want nil for a message enqueued without ids", msgs[0].AttachmentIDs)
	}
}

// #2596: EnqueueAttached stores the client's attachment ids on the record and
// the delivered projection carries them, in the order passed. The ids are copied
// on entry: the caller rewriting its slice after the call returns must not reach
// the record the history producer later reads.
func TestQueue_OnDelivered_CarriesAttachmentIDsCopiedOnEntry(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	rec := newDeliveredRecorder()
	q, err := New(Config{Deliver: f.deliver, OnDelivered: rec.onDelivered})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Enqueue BEFORE Run so the drain cannot fire until the caller's slice has
	// been rewritten — the alias, if there were one, is observable every time.
	ids := []string{"att-2", "att-1"}
	if id := q.EnqueueAttached("conv-a", deliveredMsgID, deliveredText, deliveredPayload, ids); id != 1 {
		t.Fatalf("EnqueueAttached id = %d, want 1", id)
	}
	ids[0] = "mutated"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()
	recvWithin(t, rec.fired, "the delivered seam to fire")

	if got := f.deliveredOrder(); !equalStrings(got, []string{deliveredPayload}) {
		t.Fatalf("claude received %q, want the composed delivery payload %q", got, deliveredPayload)
	}
	_, msgs := rec.calls()
	if len(msgs) != 1 {
		t.Fatalf("OnDelivered fired %d times, want 1", len(msgs))
	}
	if want := []string{"att-2", "att-1"}; !equalStrings(msgs[0].AttachmentIDs, want) {
		t.Errorf("AttachmentIDs = %q, want %q as enqueued", msgs[0].AttachmentIDs, want)
	}
	if msgs[0].Text != deliveredText {
		t.Errorf("Text = %q, want the queued text %q", msgs[0].Text, deliveredText)
	}
}

// AC 4: the drain retries the same head after a failure, and only the attempt
// that is CONFIRMED fires the seam — one entry per message, never one per
// attempt.
func TestQueue_OnDelivered_FiresOnceAcrossRetries(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.failTimes["conv-a"] = 2
	rec := newDeliveredRecorder()
	q, err := New(Config{Deliver: f.deliver, OnDelivered: rec.onDelivered, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	q.Enqueue("conv-a", "retried message")
	recvWithin(t, rec.fired, "the delivered seam to fire")
	waitSnapshotEmpty(t, q, "conv-a")

	if _, msgs := rec.calls(); len(msgs) != 1 {
		t.Fatalf("OnDelivered fired %d times across 2 failures + 1 success, want 1", len(msgs))
	}
}

// AC 3, give-up half: a head abandoned after persistent delivery failure was
// never written to claude, so it is not history and produces no entry.
func TestQueue_OnDelivered_SilentOnGiveUp(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.setPermaFail("conv-a", true)
	rec := newDeliveredRecorder()
	gaveUp := make(chan string, 1)
	q, err := New(Config{
		Deliver:       f.deliver,
		OnDelivered:   rec.onDelivered,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   10 * time.Millisecond,
		OnGiveUp:      func(convID, _ string) { gaveUp <- convID },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	q.Enqueue("conv-a", "never lands")
	recvWithin(t, gaveUp, "the give-up seam to fire")

	if _, msgs := rec.calls(); len(msgs) != 0 {
		t.Fatalf("OnDelivered fired %d times on an abandoned head, want 0", len(msgs))
	}
}

// AC 3, cancellation half: a head dropped while it is still WAITING for claude
// to go idle is un-committed by construction — commitGate refuses the claim and
// the seam aborts without writing — so it was never said and produces no entry.
func TestQueue_OnDelivered_SilentWhenHeadRemovedBeforeCommit(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.gates["conv-a"] = make(chan struct{}) // hold the delivery in its idle-gate wait
	rec := newDeliveredRecorder()
	q, err := New(Config{Deliver: f.deliver, OnDelivered: rec.onDelivered, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	id := q.Enqueue("conv-a", "cancelled before it commits")
	recvWithin(t, f.entered, "the delivery to start")

	// Droppable: the head is draining but not committing. Remove cancels the
	// delivery ctx, so the gated deliver returns ctx.Err() without ever claiming.
	if !q.Remove("conv-a", id) {
		t.Fatal("Remove of the waiting head returned false; want true")
	}
	waitSnapshotEmpty(t, q, "conv-a")

	if _, msgs := rec.calls(); len(msgs) != 0 {
		t.Fatalf("OnDelivered fired %d times on a head dropped before commit, want 0", len(msgs))
	}
}

// The design decision the plan records: the seam fires on the CONFIRMED WRITE,
// not on the queue's advance, so it deliberately does NOT copy the neighbouring
// `if advanced` guard on q.notify. That guard is about the backlog; history is
// about what was said, and once the seam confirms the write the text has reached
// claude's stdin and will be answered.
//
// Production cannot distinguish the two: WriteUserTurn always claims commitGate,
// so err == nil implies the claim won, implies a racing Remove no-opped, implies
// advanced. Only a seam that confirms WITHOUT claiming — as here — makes the
// choice observable, which is what stops a later `if advanced` from landing green.
func TestQueue_OnDelivered_FiresWhenRemoveRacedConfirmedDelivery(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	deliver := func(_ context.Context, _ string, _ []byte) error {
		entered <- struct{}{}
		<-release
		return nil // confirmed, and the commit gate was never claimed
	}
	rec := newDeliveredRecorder()
	q, err := New(Config{Deliver: deliver, OnDelivered: rec.onDelivered, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()

	id := q.Enqueue("conv-a", "removed, but claude already has it")
	recvWithin(t, entered, "the delivery to start")
	if !q.Remove("conv-a", id) {
		t.Fatal("Remove of the unclaimed head returned false; want true")
	}
	close(release)
	recvWithin(t, rec.fired, "the delivered seam to fire despite the racing Remove")

	if _, msgs := rec.calls(); len(msgs) != 1 {
		t.Fatalf("OnDelivered fired %d times, want 1 — a confirmed write is history even when the advance found the head gone", len(msgs))
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// staticSnapshot returns a snapshot func that hands back items verbatim for any
// convID (the single-conversation scenarios don't key on convID).
func staticSnapshot(items []msgqueue.QueuedMessage) func(string) []msgqueue.QueuedMessage {
	return func(string) []msgqueue.QueuedMessage { return items }
}

func decodeQueueState(t *testing.T, env protocol.Envelope) protocol.QueueStatePayload {
	t.Helper()
	if env.Type != protocol.TypeQueueState {
		t.Fatalf("env type = %q, want %q", env.Type, protocol.TypeQueueState)
	}
	var p protocol.QueueStatePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode queue_state payload: %v", err)
	}
	return p
}

func oneInteractiveConn(connID string) *fakeInteractiveBcast {
	return &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: connID, Interactive: true}}},
	}
}

// AC-1 + AC-4: an enqueue (snapshot with the backlog) produces exactly one
// queue_state carrying the expected ordered backlog to the interactive conn.
func TestQueueStateEmitterV2_Broadcast_FansBacklog(t *testing.T) {
	t.Parallel()
	ts1 := time.Now().UTC()
	ts2 := ts1.Add(time.Second)
	snap := staticSnapshot([]msgqueue.QueuedMessage{
		{ID: 1, Text: "first", TS: ts1},
		{ID: 2, Text: "second", TS: ts2},
	})
	bcast := oneInteractiveConn("c1")
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")

	if len(bcast.pushes) != 1 {
		t.Fatalf("want 1 push, got %d", len(bcast.pushes))
	}
	if bcast.pushes[0].connID != "c1" {
		t.Errorf("pushed to %q, want c1", bcast.pushes[0].connID)
	}
	got := decodeQueueState(t, bcast.pushes[0].env)
	if got.ConversationID != "conv-A" {
		t.Errorf("conversation_id = %q, want conv-A", got.ConversationID)
	}
	if len(got.Queued) != 2 {
		t.Fatalf("queued len = %d, want 2", len(got.Queued))
	}
	if got.Queued[0].QueuedMsgID != 1 || got.Queued[0].Text != "first" || !got.Queued[0].TS.Equal(ts1) {
		t.Errorf("queued[0] = %+v, want {1 first %v}", got.Queued[0], ts1)
	}
	if got.Queued[1].QueuedMsgID != 2 || got.Queued[1].Text != "second" || !got.Queued[1].TS.Equal(ts2) {
		t.Errorf("queued[1] = %+v, want {2 second %v}", got.Queued[1], ts2)
	}
}

// AC-1: a drain-advance (a shorter snapshot on the next change) produces a fresh
// queue_state reflecting the reduced backlog.
func TestQueueStateEmitterV2_Broadcast_DrainAdvanceUpdates(t *testing.T) {
	t.Parallel()
	backlog := []msgqueue.QueuedMessage{
		{ID: 1, Text: "a", TS: time.Now().UTC()},
		{ID: 2, Text: "b", TS: time.Now().UTC()},
	}
	snap := func(string) []msgqueue.QueuedMessage { return backlog }
	bcast := oneInteractiveConn("c1")
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")
	backlog = backlog[1:] // head confirmed-delivered → backlog shrinks
	e.broadcast(context.Background(), bcast, "conv-A")

	if len(bcast.pushes) != 2 {
		t.Fatalf("want 2 pushes, got %d", len(bcast.pushes))
	}
	if first := decodeQueueState(t, bcast.pushes[0].env); len(first.Queued) != 2 {
		t.Errorf("first queued len = %d, want 2", len(first.Queued))
	}
	second := decodeQueueState(t, bcast.pushes[1].env)
	if len(second.Queued) != 1 || second.Queued[0].QueuedMsgID != 2 {
		t.Errorf("second queued = %+v, want single id=2", second.Queued)
	}
}

// AC-1: an empty backlog must marshal to "queued":[] (non-nil slice), not null,
// so the phone can clear its view. Guards the make([]…,0,…) requirement.
func TestQueueStateEmitterV2_Broadcast_EmptyBacklogIsEmptyArray(t *testing.T) {
	t.Parallel()
	snap := staticSnapshot(nil) // unknown/empty conv → Snapshot returns nil
	bcast := oneInteractiveConn("c1")
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")

	if len(bcast.pushes) != 1 {
		t.Fatalf("want 1 push, got %d", len(bcast.pushes))
	}
	raw := bcast.pushes[0].env.Payload
	if !bytes.Contains(raw, []byte(`"queued":[]`)) {
		t.Errorf("payload = %s, want a non-null empty queued array", raw)
	}
	got := decodeQueueState(t, bcast.pushes[0].env)
	if got.Queued == nil {
		t.Error("queued is nil; want a non-nil empty slice")
	}
	if len(got.Queued) != 0 {
		t.Errorf("queued len = %d, want 0", len(got.Queued))
	}
}

// AC-2 + AC-4: queue_state fans only to interactive conns; a non-interactive
// conn in the same snapshot receives none.
func TestQueueStateEmitterV2_Broadcast_SkipsNonInteractive(t *testing.T) {
	t.Parallel()
	snap := staticSnapshot([]msgqueue.QueuedMessage{{ID: 1, Text: "x", TS: time.Now().UTC()}})
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "interactive", Interactive: true},
		{ConnID: "plain", Interactive: false},
	}}}
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")

	if len(bcast.pushes) != 1 {
		t.Fatalf("want 1 push, got %d", len(bcast.pushes))
	}
	if bcast.pushes[0].connID != "interactive" {
		t.Errorf("pushed to %q, want interactive only", bcast.pushes[0].connID)
	}
	if got := len(pushesFor(bcast.pushes, "plain")); got != 0 {
		t.Errorf("non-interactive conn received %d pushes, want 0", got)
	}
}

// AC-3 + AC-4: a queue_state for conv-A carries only conv-A's items; another
// conversation's queued text never enters the payload.
func TestQueueStateEmitterV2_Broadcast_ScopesToConversation(t *testing.T) {
	t.Parallel()
	snap := func(convID string) []msgqueue.QueuedMessage {
		switch convID {
		case "conv-A":
			return []msgqueue.QueuedMessage{{ID: 1, Text: "alpha-only", TS: time.Now().UTC()}}
		case "conv-B":
			return []msgqueue.QueuedMessage{{ID: 1, Text: "bravo-secret", TS: time.Now().UTC()}}
		default:
			return nil
		}
	}
	bcast := oneInteractiveConn("c1")
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")

	if len(bcast.pushes) != 1 {
		t.Fatalf("want 1 push, got %d", len(bcast.pushes))
	}
	if bytes.Contains(bcast.pushes[0].env.Payload, []byte("bravo-secret")) {
		t.Fatal("conv-A payload leaked conv-B's queued text")
	}
	got := decodeQueueState(t, bcast.pushes[0].env)
	if got.ConversationID != "conv-A" {
		t.Errorf("conversation_id = %q, want conv-A", got.ConversationID)
	}
	if len(got.Queued) != 1 || got.Queued[0].Text != "alpha-only" {
		t.Errorf("queued = %+v, want only conv-A's item", got.Queued)
	}
}

// A per-conn Push error must not abort the fan-out: the remaining interactive
// conns still receive the queue_state.
func TestQueueStateEmitterV2_Broadcast_PushErrorContinues(t *testing.T) {
	t.Parallel()
	snap := staticSnapshot([]msgqueue.QueuedMessage{{ID: 1, Text: "x", TS: time.Now().UTC()}})
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "bad", Interactive: true},
			{ConnID: "good", Interactive: true},
		}},
		pushErr: map[string]error{"bad": context.DeadlineExceeded},
	}
	e := newQueueStateEmitterV2(nil, snap, discardLogger())

	e.broadcast(context.Background(), bcast, "conv-A")

	if got := pushTypes(bcast.pushes); len(got) != 2 {
		t.Fatalf("want 2 push attempts (loop continued past the failing conn), got %v", got)
	}
	if len(pushesFor(bcast.pushes, "good")) != 1 {
		t.Error("the healthy conn did not receive the queue_state after a sibling Push failed")
	}
}

// Pure mapping seam: ID → QueuedMsgID, FIFO order preserved, empty → non-nil.
func TestToQueueStatePayload(t *testing.T) {
	t.Parallel()
	ts1 := time.Now().UTC()
	ts2 := ts1.Add(time.Minute)
	tests := []struct {
		name   string
		convID string
		items  []msgqueue.QueuedMessage
		want   []protocol.QueuedItem
	}{
		{
			name:   "empty yields non-nil slice",
			convID: "c",
			items:  nil,
			want:   []protocol.QueuedItem{},
		},
		{
			name:   "maps id and preserves order",
			convID: "c",
			items: []msgqueue.QueuedMessage{
				{ID: 7, MessageID: "mid-7", Text: "first", TS: ts1},
				{ID: 9, MessageID: "mid-9", Text: "second", TS: ts2},
			},
			want: []protocol.QueuedItem{
				{QueuedMsgID: 7, MessageID: "mid-7", Text: "first", TS: ts1},
				{QueuedMsgID: 9, MessageID: "mid-9", Text: "second", TS: ts2},
			},
		},
		{
			// A message enqueued through the two-argument Enqueue shim carries no
			// client id, and the mapping must relay that empty value rather than
			// substituting anything (#2092 AC 3).
			name:   "empty message_id maps across unchanged",
			convID: "c",
			items: []msgqueue.QueuedMessage{
				{ID: 1, MessageID: "", Text: "no client id", TS: ts1},
			},
			want: []protocol.QueuedItem{
				{QueuedMsgID: 1, MessageID: "", Text: "no client id", TS: ts1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := toQueueStatePayload(tt.convID, tt.items)
			if got.ConversationID != tt.convID {
				t.Errorf("conversation_id = %q, want %q", got.ConversationID, tt.convID)
			}
			if got.Queued == nil {
				t.Fatal("queued is nil; want non-nil")
			}
			if len(got.Queued) != len(tt.want) {
				t.Fatalf("queued len = %d, want %d", len(got.Queued), len(tt.want))
			}
			for i := range got.Queued {
				if got.Queued[i].QueuedMsgID != tt.want[i].QueuedMsgID ||
					got.Queued[i].MessageID != tt.want[i].MessageID ||
					got.Queued[i].Text != tt.want[i].Text ||
					!got.Queued[i].TS.Equal(tt.want[i].TS) {
					t.Errorf("queued[%d] = %+v, want %+v", i, got.Queued[i], tt.want[i])
				}
			}
		})
	}
}

// queueStateNotify must never block on a full channel (the ChangeFunc
// MUST-NOT-BLOCK contract): a send to a full buffer drops and logs queue_full.
func TestQueueStateNotify_DropOnFullDoesNotBlock(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ch := make(chan string, 1)
	notify := queueStateNotify(ch, logger)

	notify("conv-A") // fills the cap-1 buffer

	done := make(chan struct{})
	go func() {
		notify("conv-B") // buffer full → must drop, not block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queueStateNotify blocked on a full channel")
	}

	if got := <-ch; got != "conv-A" { // the first (buffered) send survived
		t.Errorf("buffered convID = %q, want conv-A", got)
	}
	if !strings.Contains(buf.String(), "queue_full") {
		t.Errorf("missing queue_full warn; logs = %q", buf.String())
	}
}

// queueStateNotify must never leak the untrusted convID's *text* — it only ever
// receives a convID, and logs only the convID, never message content.
func TestQueueStateNotify_DeliversConvID(t *testing.T) {
	t.Parallel()
	ch := make(chan string, queueStateQueueSize)
	notify := queueStateNotify(ch, discardLogger())

	notify("conv-X")

	select {
	case got := <-ch:
		if got != "conv-X" {
			t.Errorf("delivered convID = %q, want conv-X", got)
		}
	default:
		t.Fatal("notify did not deliver the convID to the channel")
	}
}

// startQueueStateStreamV2's cleanup joins the Run goroutine on ctx-cancel and is
// idempotent.
func TestStartQueueStateStreamV2_CleanupJoinsOnCancel(t *testing.T) {
	t.Parallel()
	ch := make(chan string, queueStateQueueSize)
	qse := newQueueStateEmitterV2(ch, staticSnapshot(nil), discardLogger())
	bcast := oneInteractiveConn("c1")

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startQueueStateStreamV2(ctx, qse, bcast)

	cancel()
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return after ctx cancel")
	}
	cleanup() // idempotent
}

// End-to-end through the channel: a notification drains through Run and produces
// a queue_state on the interactive conn.
func TestQueueStateEmitterV2_Run_DeliversFromChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan string, queueStateQueueSize)
	snap := staticSnapshot([]msgqueue.QueuedMessage{{ID: 1, Text: "x", TS: time.Now().UTC()}})
	qse := newQueueStateEmitterV2(ch, snap, discardLogger())

	pushed := make(chan struct{}, 1)
	bcast := &notifyingBcast{
		inner:  oneInteractiveConn("c1"),
		pushed: pushed,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startQueueStateStreamV2(ctx, qse, bcast)
	t.Cleanup(func() {
		cancel()  // unblock Run on ctx.Done...
		cleanup() // ...then join it (cleanup waits on <-done)
	})

	queueStateNotify(ch, discardLogger())("conv-A")

	select {
	case <-pushed:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not push a queue_state for the notified conversation")
	}
}

// #878: outstandingQueues adapts a live queue's SnapshotAll to the relay
// reconcile seam — one QueueStatePayload per non-empty conversation, mapped via
// toQueueStatePayload, and none for a drained-empty conversation. Enqueue-only
// (no Run) keeps both backlogs intact for the two-conversation assertion.
func TestOutstandingQueues_OnePayloadPerNonEmptyConversation(t *testing.T) {
	t.Parallel()
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q.Enqueue("conv-A", "alpha-1")
	q.Enqueue("conv-A", "alpha-2")
	q.Enqueue("conv-B", "bravo-1")

	got := outstandingQueues(q)()
	if len(got) != 2 {
		t.Fatalf("outstandingQueues returned %d payloads, want 2 (one per non-empty conversation)", len(got))
	}

	byConv := map[string]protocol.QueueStatePayload{}
	for _, p := range got {
		if _, dup := byConv[p.ConversationID]; dup {
			t.Fatalf("conversation_id %q appeared in more than one payload", p.ConversationID)
		}
		byConv[p.ConversationID] = p
	}

	a, ok := byConv["conv-A"]
	if !ok {
		t.Fatal("missing payload for conv-A")
	}
	if len(a.Queued) != 2 ||
		a.Queued[0].QueuedMsgID != 1 || a.Queued[0].Text != "alpha-1" ||
		a.Queued[1].QueuedMsgID != 2 || a.Queued[1].Text != "alpha-2" {
		t.Errorf("conv-A queued = %+v, want ids [1 2] texts [alpha-1 alpha-2] in order", a.Queued)
	}
	b, ok := byConv["conv-B"]
	if !ok {
		t.Fatal("missing payload for conv-B")
	}
	if len(b.Queued) != 1 || b.Queued[0].QueuedMsgID != 1 || b.Queued[0].Text != "bravo-1" {
		t.Errorf("conv-B queued = %+v, want single {1 bravo-1}", b.Queued)
	}
}

// #878 AC3: a conversation whose backlog fully drained produces no payload — the
// omission propagates from SnapshotAll through the adapter. A conversation held
// non-empty (its head gated in delivery) proves the emptiness, not an empty queue.
func TestOutstandingQueues_OmitsDrainedConversation(t *testing.T) {
	t.Parallel()
	// held blocks in deliver forever; gone delivers immediately and drains empty.
	release := make(chan struct{})
	deliver := func(_ context.Context, convID string, _ []byte) error {
		if convID == "held" {
			<-release
		}
		return nil
	}
	q, err := msgqueue.New(msgqueue.Config{Deliver: deliver, RetryInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(release)
	go func() { _ = q.Run(ctx) }()

	q.Enqueue("held", "h1")
	q.Enqueue("gone", "g1")

	// Wait for gone to drain empty (its convQueue is retained with items == nil).
	deadline := time.Now().Add(2 * time.Second)
	for len(q.Snapshot("gone")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("conversation \"gone\" did not drain empty")
		}
		time.Sleep(time.Millisecond)
	}

	got := outstandingQueues(q)()
	if len(got) != 1 {
		t.Fatalf("outstandingQueues returned %d payloads, want 1 (drained conv omitted)", len(got))
	}
	if got[0].ConversationID != "held" {
		t.Errorf("payload conversation_id = %q, want held (the still-backlogged one)", got[0].ConversationID)
	}
}

// #878: an empty queue yields an empty (non-nil) payload slice — the nil-safe
// posture the relay seam short-circuits on (len == 0 ⇒ no reconcile).
func TestOutstandingQueues_EmptyQueue(t *testing.T) {
	t.Parallel()
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := outstandingQueues(q)()
	if len(got) != 0 {
		t.Errorf("outstandingQueues on an empty queue returned %d payloads, want 0", len(got))
	}
}

// notifyingBcast wraps fakeInteractiveBcast and signals after each Push so a
// test driving the Run goroutine can await delivery without polling. Push is
// only ever called from the single Run goroutine, so the embedded recorder needs
// no extra synchronisation.
type notifyingBcast struct {
	inner  *fakeInteractiveBcast
	pushed chan struct{}
}

func (n *notifyingBcast) ActiveConns(ctx context.Context) []relay.ActiveConn {
	return n.inner.ActiveConns(ctx)
}

func (n *notifyingBcast) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	err := n.inner.Push(ctx, connID, env)
	select {
	case n.pushed <- struct{}{}:
	default:
	}
	return err
}

// Race coverage: OnChange fires from many goroutines while Run drains. nextID is
// touched only by Run, the channel is concurrency-safe, and bcast is read only by
// Run — so -race must report nothing.
func TestQueueStateEmitterV2_Run_ConcurrentOnChange(t *testing.T) {
	t.Parallel()
	ch := make(chan string, queueStateQueueSize)
	snap := staticSnapshot([]msgqueue.QueuedMessage{{ID: 1, Text: "x", TS: time.Now().UTC()}})
	qse := newQueueStateEmitterV2(ch, snap, discardLogger())
	bcast := oneInteractiveConn("c1")
	notify := queueStateNotify(ch, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startQueueStateStreamV2(ctx, qse, bcast)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				notify("conv-A") // drop-on-full is fine; we assert no race/panic
			}
		}()
	}
	wg.Wait()

	cancel()
	cleanup()
}

// TestQueueState_ComposedDeliveryNeverReachesTheWire covers #2038 AC 4 on BOTH
// read-back paths a client has: the per-change queue_state push (Snapshot →
// toQueueStatePayload) and the connect-time reconcile (outstandingQueues). A
// message naming attachments is delivered to claude as a composed prompt naming
// their on-host paths, and neither path may carry one — docs/protocol-mobile.md
// § Error codes forbids disclosing the daemon's layout from the other side, and a
// success path leaking what the failure path is guarded against would undo that
// mitigation.
//
// The queue is deliberately never Run, so nothing drains and the backlog stays
// readable. The delivery payload differs from the text, or a build that projected
// the wrong field would pass.
func TestQueueState_ComposedDeliveryNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	const (
		convID   = "conv-attach"
		userText = "what does this say?"
		hostPath = "/home/u/.pyry/inst/conversations/conv-attach/attachments/aaaaaaaa-aaaa-4aaa-8aaa-000000000001/report.pdf"
	)
	composed := userText + "\n\nAttached files (use the Read tool to view each):\n" + hostPath

	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if id := q.EnqueueDelivery(convID, "mid-attach", userText, composed); id != 1 {
		t.Fatalf("EnqueueDelivery id = %d, want 1", id)
	}

	// Both arms read the same msgqueue snapshot through the same mapping, so both
	// are asserted here rather than trusting one to stand for the other.
	arms := map[string]protocol.QueueStatePayload{
		"queue_state push":       toQueueStatePayload(convID, q.Snapshot(convID)),
		"connect-time reconcile": {},
	}
	reconciled := outstandingQueues(q)()
	if len(reconciled) != 1 {
		t.Fatalf("outstandingQueues returned %d payloads, want 1", len(reconciled))
	}
	arms["connect-time reconcile"] = reconciled[0]

	for name, payload := range arms {
		if payload.ConversationID != convID {
			t.Errorf("%s: ConversationID = %q, want %q", name, payload.ConversationID, convID)
		}
		if len(payload.Queued) != 1 {
			t.Fatalf("%s: Queued len = %d, want 1", name, len(payload.Queued))
		}
		if payload.Queued[0].Text != userText {
			t.Errorf("%s: Text = %q, want the user's own words %q", name, payload.Queued[0].Text, userText)
		}
		// Marshalled, because the wire is what AC 4 is about — a leak through any
		// field of the payload, not just Text, has to fail this.
		wire, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: marshal payload: %v", name, err)
		}
		if strings.Contains(string(wire), hostPath) {
			t.Errorf("%s: wire payload discloses the host path:\n%s", name, wire)
		}
	}
}

// TestQueueState_ClientMessageIDReachesBothArms covers #2092 AC 1 and AC 2
// together, on the two read-back paths a client has: the change-driven
// queue_state push (Snapshot → toQueueStatePayload) and the connect-time
// reconcile (outstandingQueues → SnapshotAll → the same mapping). Both must name
// the identical client-minted message_id.
//
// Asserting both is the point rather than belt-and-braces. The arms diverge
// upstream of the shared mapping — Snapshot and SnapshotAll are two separate
// projection loops in msgqueue — so a test exercising only the push passes
// against a SnapshotAll that drops the field, and the client that would notice is
// exactly the one reconnecting mid-backlog, which is the case the merge has to
// survive.
//
// The queue is deliberately never Run, so nothing drains and the backlog stays
// readable.
func TestQueueState_ClientMessageIDReachesBothArms(t *testing.T) {
	t.Parallel()
	const (
		convID    = "conv-merge"
		messageID = "3f2a9c14-7b6e-4d51-9a08-2e5c1b7d4f60"
		userText  = "queued while claude was busy"
	)

	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if id := q.EnqueueDelivery(convID, messageID, userText, userText); id != 1 {
		t.Fatalf("EnqueueDelivery id = %d, want 1", id)
	}

	reconciled := outstandingQueues(q)()
	if len(reconciled) != 1 {
		t.Fatalf("outstandingQueues returned %d payloads, want 1", len(reconciled))
	}
	arms := map[string]protocol.QueueStatePayload{
		"queue_state push":       toQueueStatePayload(convID, q.Snapshot(convID)),
		"connect-time reconcile": reconciled[0],
	}

	for name, payload := range arms {
		if payload.ConversationID != convID {
			t.Errorf("%s: ConversationID = %q, want %q", name, payload.ConversationID, convID)
		}
		if len(payload.Queued) != 1 {
			t.Fatalf("%s: Queued len = %d, want 1", name, len(payload.Queued))
		}
		if payload.Queued[0].MessageID != messageID {
			t.Errorf("%s: MessageID = %q, want the client's own id %q",
				name, payload.Queued[0].MessageID, messageID)
		}
		// The daemon-side id keeps addressing the item; message_id is a
		// correlation key and replaces nothing (#2092 AC 4).
		if payload.Queued[0].QueuedMsgID != 1 {
			t.Errorf("%s: QueuedMsgID = %d, want 1", name, payload.Queued[0].QueuedMsgID)
		}
		// Marshalled, because the wire is what the ACs are about: a mapping that
		// dropped the tag, or an omitempty that elided an empty id, has to fail
		// here and not only in the struct comparison above.
		wire, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: marshal payload: %v", name, err)
		}
		if !strings.Contains(string(wire), `"message_id":"`+messageID+`"`) {
			t.Errorf("%s: wire payload does not name the client's message_id:\n%s", name, wire)
		}
	}
}

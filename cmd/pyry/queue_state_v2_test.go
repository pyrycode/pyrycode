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
				{ID: 7, Text: "first", TS: ts1},
				{ID: 9, Text: "second", TS: ts2},
			},
			want: []protocol.QueuedItem{
				{QueuedMsgID: 7, Text: "first", TS: ts1},
				{QueuedMsgID: 9, Text: "second", TS: ts2},
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

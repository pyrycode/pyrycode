package msgqueue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSendNow records every send-now write and fails while err is set.
type fakeSendNow struct {
	mu       sync.Mutex
	err      error
	payloads []string
}

func (f *fakeSendNow) send(_ context.Context, _ string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.payloads = append(f.payloads, string(payload))
	return nil
}

func (f *fakeSendNow) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.payloads...)
}

// changeCounter counts OnChange notifications.
type changeCounter struct {
	mu sync.Mutex
	n  int
}

func (c *changeCounter) onChange(string) { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *changeCounter) count() int      { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// sendNowQueue starts a queue whose drain blocks on conv "c" (a running turn)
// until the returned release is called, with three messages queued and the head
// waiting inside the delivery seam.
func sendNowQueue(t *testing.T, sn *fakeSendNow) (q *Queue, f *fakeDeliver, rec *deliveredRecorder, ch *changeCounter, release func()) {
	t.Helper()
	f = newFakeDeliver()
	gate := make(chan struct{})
	f.gates["c"] = gate
	rec = newDeliveredRecorder()
	ch = &changeCounter{}
	q, err := New(Config{Deliver: f.deliver, SendNow: sn.send, OnDelivered: rec.onDelivered, OnChange: ch.onChange})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = q.Run(ctx) }()
	for _, text := range []string{"one", "two", "three"} {
		q.EnqueueDelivery("c", "m-"+text, text, "deliver-"+text)
	}
	recvWithin(t, f.entered, "the head to wait in the delivery seam")
	var once sync.Once
	return q, f, rec, ch, func() { once.Do(func() { close(gate) }) }
}

func backlogIDs(q *Queue, convID string) []uint64 {
	var ids []uint64
	for _, m := range q.Snapshot(convID) {
		ids = append(ids, m.ID)
	}
	return ids
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueue_SendNow_TakesMessageOutOfOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		id        uint64
		want      string
		remaining []uint64
		drained   []string
	}{
		{"waiting head", 1, "deliver-one", []uint64{2, 3}, []string{"deliver-two", "deliver-three"}},
		{"middle", 2, "deliver-two", []uint64{1, 3}, []string{"deliver-one", "deliver-three"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sn := &fakeSendNow{}
			q, f, rec, ch, release := sendNowQueue(t, sn)
			before := ch.count()

			if !q.SendNow("c", tc.id) {
				t.Fatalf("SendNow(%d) = false, want true", tc.id)
			}
			if got := sn.written(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("send-now wrote %q, want [%q]", got, tc.want)
			}
			if got := backlogIDs(q, "c"); !equalIDs(got, tc.remaining) {
				t.Fatalf("backlog = %v, want %v", got, tc.remaining)
			}
			if ch.count() <= before {
				t.Errorf("OnChange did not fire for the shrunk backlog")
			}
			recvWithin(t, rec.fired, "OnDelivered for the send-now message")
			_, msgs := rec.calls()
			if len(msgs) != 1 || msgs[0].ID != tc.id || !msgs[0].SentNow {
				t.Fatalf("OnDelivered = %+v, want one call for id %d with SentNow", msgs, tc.id)
			}

			release()
			// rec.fired, not f.completed: the fake signals completed inside deliver,
			// before the drain fires OnDelivered for that message.
			for range tc.drained {
				recvWithin(t, rec.fired, "OnDelivered for the rest of the backlog")
			}
			if got := f.deliveredOrder(); !equalStrings(got, tc.drained) {
				t.Fatalf("drained %q, want %q (FIFO order of the rest)", got, tc.drained)
			}
			_, msgs = rec.calls()
			if len(msgs) != 3 {
				t.Fatalf("OnDelivered fired %d times, want 3", len(msgs))
			}
			for _, m := range msgs[1:] {
				if m.SentNow {
					t.Errorf("idle delivery of id %d marked SentNow", m.ID)
				}
			}
		})
	}
}

func TestQueue_SendNow_SeamErrorLeavesMessageInPlace(t *testing.T) {
	t.Parallel()
	for _, id := range []uint64{1, 2, 3} {
		sn := &fakeSendNow{err: errors.New("turn is idle")}
		q, f, rec, _, release := sendNowQueue(t, sn)

		if q.SendNow("c", id) {
			t.Fatalf("SendNow(%d) = true with a failing seam", id)
		}
		if got := backlogIDs(q, "c"); !equalIDs(got, []uint64{1, 2, 3}) {
			t.Fatalf("id %d: backlog = %v, want [1 2 3] unchanged", id, got)
		}
		release()
		for range 3 {
			recvWithin(t, rec.fired, "the backlog to drain at idle")
		}
		if got := f.deliveredOrder(); !equalStrings(got, []string{"deliver-one", "deliver-two", "deliver-three"}) {
			t.Fatalf("id %d: drained %q, want original FIFO order", id, got)
		}
		_, msgs := rec.calls()
		for _, m := range msgs {
			if m.SentNow {
				t.Errorf("id %d: a failed send-now reported SentNow on id %d", id, m.ID)
			}
		}
	}
}

// A refused send-now of the waiting head cancels its attempt and puts it back
// before the drain re-locks. The drain must book that as a clean drop: no
// failure Warn, and no give-up streak that a later single transient failure,
// after a turn longer than GiveUpAfter, would trip into abandoning the head.
func TestQueue_SendNow_RefusedHeadStartsNoGiveUpStreak(t *testing.T) {
	t.Parallel()
	const giveUpAfter = 50 * time.Millisecond
	sendNowReturned := make(chan struct{})
	entered := make(chan struct{}, 1)
	var attempts atomic.Int32
	deliver := func(ctx context.Context, _ string, _ []byte) error {
		switch attempts.Add(1) {
		case 1:
			// Waiting for idle until the send-now take cancels this attempt, then
			// returning only after the failed send-now has put the head back.
			entered <- struct{}{}
			<-ctx.Done()
			<-sendNowReturned
			return ctx.Err()
		case 2:
			// A turn that outlasts the give-up bound, then one transient failure.
			time.Sleep(3 * giveUpAfter)
			return errors.New("no live child")
		default:
			return nil
		}
	}
	var buf syncBuffer
	gaveUp := make(chan struct{}, 1)
	delivered := make(chan struct{}, 1)
	sn := &fakeSendNow{err: errors.New("not a claude session")}
	q, err := New(Config{
		Deliver:       deliver,
		SendNow:       sn.send,
		RetryInterval: time.Millisecond,
		GiveUpAfter:   giveUpAfter,
		Logger:        slog.New(slog.NewTextHandler(&buf, nil)),
		OnGiveUp:      func(string, string) { gaveUp <- struct{}{} },
		OnDelivered:   func(string, QueuedMessage) { delivered <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()
	q.Enqueue("c", "one")
	recvWithin(t, entered, "the head to wait for idle")

	if q.SendNow("c", 1) {
		t.Fatal("SendNow with a refusing seam = true")
	}
	close(sendNowReturned)

	select {
	case <-delivered:
	case <-gaveUp:
		t.Fatal("the head was abandoned: the refused send-now started its give-up streak")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the head to be delivered")
	}
	cancel()
	<-runErr
	if out := buf.String(); strings.Contains(out, "context canceled") {
		t.Fatalf("the cancelled attempt was logged as a delivery failure:\n%s", out)
	}
}

// syncBuffer is a bytes.Buffer safe for the drain goroutine to write while the
// test later reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestQueue_SendNow_NoOps(t *testing.T) {
	t.Parallel()
	sn := &fakeSendNow{}
	q, _, _, _, _ := sendNowQueue(t, sn)
	for _, tc := range []struct {
		name string
		conv string
		id   uint64
	}{
		{"unknown id", "c", 99},
		{"unknown conversation", "other", 1},
	} {
		if q.SendNow(tc.conv, tc.id) {
			t.Errorf("%s: SendNow = true, want false", tc.name)
		}
	}
	if got := sn.written(); len(got) != 0 {
		t.Fatalf("no-op SendNow wrote %q", got)
	}
	if got := backlogIDs(q, "c"); !equalIDs(got, []uint64{1, 2, 3}) {
		t.Fatalf("backlog = %v, want [1 2 3] unchanged", got)
	}
}

func TestQueue_SendNow_NilSeamIsInert(t *testing.T) {
	t.Parallel()
	q, err := New(Config{Deliver: newFakeDeliver().deliver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q.Enqueue("c", "one")
	if q.SendNow("c", 1) {
		t.Fatal("SendNow with no seam = true, want false")
	}
	if got := backlogIDs(q, "c"); !equalIDs(got, []uint64{1}) {
		t.Fatalf("backlog = %v, want [1]", got)
	}
}

// A head whose idle delivery has already claimed it (committing) wins over a
// send-now take, exactly as it wins over Remove.
func TestQueue_SendNow_NoOpsOnCommittingHead(t *testing.T) {
	t.Parallel()
	f := newFakeDeliver()
	f.commitGates["c"] = make(chan struct{})
	sn := &fakeSendNow{}
	q, err := New(Config{Deliver: f.deliver, SendNow: sn.send})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = q.Run(ctx) }()
	q.Enqueue("c", "one")
	q.Enqueue("c", "two")
	recvWithin(t, f.claimed, "the head to be claimed")

	if q.SendNow("c", 1) {
		t.Fatal("SendNow on the committing head = true, want false")
	}
	if got := sn.written(); len(got) != 0 {
		t.Fatalf("SendNow wrote %q over a committing head", got)
	}
	if got := backlogIDs(q, "c"); !equalIDs(got, []uint64{1, 2}) {
		t.Fatalf("backlog = %v, want [1 2]", got)
	}
	f.commitGates["c"] <- struct{}{}
	recvWithin(t, f.completed, "the committing head to complete")
	// The refusal left the rest of the backlog draining in order.
	f.commitGates["c"] <- struct{}{}
	recvWithin(t, f.completed, "the second message to complete")
}

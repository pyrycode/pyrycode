package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// pushCall records one (*assistantTurnEmitterV2).broadcast → Push attempt:
// the addressed conn, the envelope it carried, and the error the stub
// returned for it (nil on success). Captured in call order.
type pushCall struct {
	connID string
	env    protocol.Envelope
	err    error
}

// stubV2Broadcaster is a test double for the V2SessionManager surface the
// v2 emitter consumes (ActiveConnIDs + Push). It returns a scripted
// sequence of open-conn snapshots (the last entry is reused once the
// sequence is exhausted, modelling a steady set) and can inject a per-conn
// Push error. Every Push attempt is recorded and mirrored onto a buffered
// channel so a test can wait for N attempts without sleeping.
type stubV2Broadcaster struct {
	mu sync.Mutex

	snapshots [][]string // one entry consumed per ActiveConnIDs call
	callIdx   int
	pushErr   map[string]error // connID → error Push returns for it

	recorded []pushCall
	pushed   chan pushCall
}

func newStubV2Broadcaster(snapshots ...[]string) *stubV2Broadcaster {
	return &stubV2Broadcaster{
		snapshots: snapshots,
		pushErr:   map[string]error{},
		pushed:    make(chan pushCall, 64),
	}
}

func (s *stubV2Broadcaster) ActiveConnIDs(ctx context.Context) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 {
		return nil
	}
	idx := s.callIdx
	if idx >= len(s.snapshots) {
		idx = len(s.snapshots) - 1 // steady-state: reuse the last snapshot
	}
	s.callIdx++
	out := make([]string, len(s.snapshots[idx]))
	copy(out, s.snapshots[idx])
	return out
}

func (s *stubV2Broadcaster) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	s.mu.Lock()
	err := s.pushErr[connID]
	call := pushCall{connID: connID, env: env, err: err}
	s.recorded = append(s.recorded, call)
	s.mu.Unlock()
	s.pushed <- call
	return err
}

// drainPushes collects up to want Push attempts from the stub's channel,
// returning early on deadline. Order matches Push call order (broadcast is
// serial on the Run goroutine).
func drainPushes(t *testing.T, ch <-chan pushCall, want int, deadline time.Duration) []pushCall {
	t.Helper()
	out := make([]pushCall, 0, want)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < want {
		select {
		case c := <-ch:
			out = append(out, c)
		case <-timer.C:
			return out
		}
	}
	return out
}

func msgIDOf(t *testing.T, env protocol.Envelope) string {
	t.Helper()
	var p protocol.MessagePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode message payload: %v", err)
	}
	return p.MessageID
}

// Cursor empty → no push. Mirrors v1 DropsWhenCursorEmpty.
func TestAssistantTurnEmitterV2_DropsWhenCursorEmpty(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-x"})
	cur := &stubCursor{} // empty cursor
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte(testChunk))

	got := drainPushes(t, bc.pushed, 1, 50*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("expected no pushes when cursor is empty, got %d", len(got))
	}

	cancel()
	<-done
}

// Fan-out to every open conn (AC#1, AC#2 fan-out).
func TestAssistantTurnEmitterV2_FansOutToAllOpenConns(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-a", "conn-b"})
	cur := &stubCursor{}
	cur.set(testConvID)
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte(testChunk))

	pushes := drainPushes(t, bc.pushed, 2, time.Second)
	if len(pushes) != 2 {
		t.Fatalf("expected 2 pushes, got %d", len(pushes))
	}
	seen := map[string]bool{}
	for _, pc := range pushes {
		seen[pc.connID] = true
		if pc.env.Type != protocol.TypeMessage {
			t.Errorf("env.Type: got %q, want %q", pc.env.Type, protocol.TypeMessage)
		}
		if pc.env.InReplyTo != nil {
			t.Errorf("env.InReplyTo: got %v, want nil (server-initiated)", pc.env.InReplyTo)
		}
		var p protocol.MessagePayload
		if err := json.Unmarshal(pc.env.Payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if p.ConversationID != testConvID {
			t.Errorf("ConversationID: got %q, want %q", p.ConversationID, testConvID)
		}
		if p.Role != "assistant" {
			t.Errorf("Role: got %q, want %q", p.Role, "assistant")
		}
		if p.Text != testChunk {
			t.Errorf("Text: got %q, want %q", p.Text, testChunk)
		}
		if !conversations.ValidID(p.MessageID) {
			t.Errorf("MessageID %q is not a valid UUIDv4", p.MessageID)
		}
	}
	if !seen["conn-a"] || !seen["conn-b"] {
		t.Errorf("expected both conns pushed, got %v", seen)
	}

	cancel()
	<-done
}

// Per-conn Push failure does not abort the turn (AC#2 "dropped is skipped").
func TestAssistantTurnEmitterV2_PushFailureDoesNotAbortTurn(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-a", "conn-b", "conn-c"})
	bc.pushErr["conn-b"] = relay.ErrConnNotFound
	cur := &stubCursor{}
	cur.set(testConvID)
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte(testChunk))

	// 3 attempts: a (ok), b (ErrConnNotFound), c (ok). The failed b must
	// not abort the turn — a and c still get their envelopes.
	pushes := drainPushes(t, bc.pushed, 3, time.Second)
	if len(pushes) != 3 {
		t.Fatalf("expected 3 push attempts, got %d", len(pushes))
	}
	got := map[string]error{}
	for _, pc := range pushes {
		got[pc.connID] = pc.err
	}
	if got["conn-a"] != nil {
		t.Errorf("conn-a push: got err %v, want nil", got["conn-a"])
	}
	if !errors.Is(got["conn-b"], relay.ErrConnNotFound) {
		t.Errorf("conn-b push: got err %v, want ErrConnNotFound", got["conn-b"])
	}
	if got["conn-c"] != nil {
		t.Errorf("conn-c push: got err %v, want nil (turn must continue past the failed conn-b)", got["conn-c"])
	}

	cancel()
	<-done
}

// Envelope-ID counter increments; per-chunk MessageID is shared across the
// chunk's conns and distinct between chunks (Envelope-ID policy; mirrors v1
// one-message-ID-per-chunk minting).
func TestAssistantTurnEmitterV2_EnvelopeIDIncrements(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-a", "conn-b"})
	cur := &stubCursor{}
	cur.set(testConvID)
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte("chunk-one"))
	em.Enqueue([]byte("chunk-two"))

	pushes := drainPushes(t, bc.pushed, 4, time.Second)
	if len(pushes) != 4 {
		t.Fatalf("expected 4 pushes, got %d", len(pushes))
	}

	// Caller-side env.ID counter: strictly increasing in delivery order.
	var prev uint64
	for i, pc := range pushes {
		if pc.env.ID <= prev {
			t.Errorf("push %d: env.ID %d not greater than previous %d", i, pc.env.ID, prev)
		}
		prev = pc.env.ID
	}

	// pushes[0..1] are chunk-one (conn-a, conn-b), pushes[2..3] chunk-two.
	id0, id1 := msgIDOf(t, pushes[0].env), msgIDOf(t, pushes[1].env)
	id2, id3 := msgIDOf(t, pushes[2].env), msgIDOf(t, pushes[3].env)
	for _, id := range []string{id0, id1, id2, id3} {
		if !conversations.ValidID(id) {
			t.Errorf("MessageID %q is not a valid UUIDv4", id)
		}
	}
	if id0 != id1 {
		t.Errorf("chunk-one MessageIDs differ across conns: %q vs %q", id0, id1)
	}
	if id2 != id3 {
		t.Errorf("chunk-two MessageIDs differ across conns: %q vs %q", id2, id3)
	}
	if id0 == id2 {
		t.Errorf("MessageID not distinct between chunks: both %q", id0)
	}

	cancel()
	<-done
}

// Enqueue copies the chunk (mirrors v1 EnqueueCopiesChunk).
func TestAssistantTurnEmitterV2_EnqueueCopiesChunk(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-x"})
	cur := &stubCursor{}
	cur.set(testConvID)
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	buf := []byte("first-text")
	em.Enqueue(buf)
	// Mutate the caller buffer immediately — the supervisor reuses its read
	// buffer, so Enqueue must copy.
	for i := range buf {
		buf[i] = 'X'
	}

	pushes := drainPushes(t, bc.pushed, 1, time.Second)
	if len(pushes) != 1 {
		t.Fatalf("expected 1 push, got %d", len(pushes))
	}
	var p protocol.MessagePayload
	if err := json.Unmarshal(pushes[0].env.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Text != "first-text" {
		t.Errorf("Text: got %q, want %q (Enqueue did not copy)", p.Text, "first-text")
	}

	cancel()
	<-done
}

// ctx-cancel stops Run (deterministic — gated on a done channel, no sleeps).
func TestAssistantTurnEmitterV2_CtxCancelStopsRun(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-x"})
	cur := &stubCursor{}
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// A phone that connects mid-turn is included in subsequent fan-outs only
// (AC#2 "connects mid-turn"): conn-b appears in the second snapshot, so it
// receives chunk-two but not chunk-one.
func TestAssistantTurnEmitterV2_MidTurnConnectIncludedNextChunk(t *testing.T) {
	t.Parallel()

	bc := newStubV2Broadcaster([]string{"conn-a"}, []string{"conn-a", "conn-b"})
	cur := &stubCursor{}
	cur.set(testConvID)
	em := newAssistantTurnEmitterV2(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte("chunk-one"))
	em.Enqueue([]byte("chunk-two"))

	// chunk-one → 1 push (conn-a); chunk-two → 2 pushes (conn-a, conn-b).
	pushes := drainPushes(t, bc.pushed, 3, time.Second)
	if len(pushes) != 3 {
		t.Fatalf("expected 3 pushes, got %d", len(pushes))
	}
	var bPushes []pushCall
	for _, pc := range pushes {
		if pc.connID == "conn-b" {
			bPushes = append(bPushes, pc)
		}
	}
	if len(bPushes) != 1 {
		t.Fatalf("conn-b pushes: got %d, want 1 (included only after it connects)", len(bPushes))
	}
	var p protocol.MessagePayload
	if err := json.Unmarshal(bPushes[0].env.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Text != "chunk-two" {
		t.Errorf("conn-b Text: got %q, want %q (mid-turn join misses the first chunk)", p.Text, "chunk-two")
	}

	cancel()
	<-done
}

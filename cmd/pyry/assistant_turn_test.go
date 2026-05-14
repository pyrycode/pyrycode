package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	testConvID = "11111111-1111-4111-8111-111111111111"
	testChunk  = "assistant-text-marker"
)

type stubCursor struct{ id atomic.Value }

func (s *stubCursor) CurrentConversation() string {
	v := s.id.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

func (s *stubCursor) set(id string) { s.id.Store(id) }

type stubBroadcaster struct{ conns []*dispatch.Conn }

func (s *stubBroadcaster) ActiveConns() []*dispatch.Conn { return s.conns }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// drainOutbound returns all envelopes published to outbound up to deadline.
func drainOutbound(t *testing.T, outbound <-chan protocol.RoutingEnvelope, want int, deadline time.Duration) []protocol.RoutingEnvelope {
	t.Helper()
	out := make([]protocol.RoutingEnvelope, 0, want)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(out) < want {
		select {
		case env := <-outbound:
			out = append(out, env)
		case <-timer.C:
			return out
		}
	}
	return out
}

func TestAssistantTurnEmitter_DropsWhenCursorEmpty(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn("conn-x", outbound, nil)
	cur := &stubCursor{} // empty cursor
	bc := &stubBroadcaster{conns: []*dispatch.Conn{c}}
	em := newAssistantTurnEmitter(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte(testChunk))

	// Allow the broadcast goroutine a chance to process the chunk and (drop).
	got := drainOutbound(t, outbound, 1, 50*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("expected no envelopes when cursor is empty, got %d", len(got))
	}

	cancel()
	<-done
}

func TestAssistantTurnEmitter_FansOutToAllConns(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 4)
	cA := dispatch.NewTestConn("conn-a", outbound, nil)
	cB := dispatch.NewTestConn("conn-b", outbound, nil)
	cur := &stubCursor{}
	cur.set(testConvID)
	bc := &stubBroadcaster{conns: []*dispatch.Conn{cA, cB}}
	em := newAssistantTurnEmitter(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	em.Enqueue([]byte(testChunk))

	frames := drainOutbound(t, outbound, 2, time.Second)
	if len(frames) != 2 {
		t.Fatalf("expected 2 envelopes, got %d", len(frames))
	}

	seenConn := map[string]bool{}
	for _, f := range frames {
		seenConn[f.ConnID] = true
		var env protocol.Envelope
		if err := json.Unmarshal(f.Frame, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.Type != protocol.TypeMessage {
			t.Errorf("env.Type: got %q, want %q", env.Type, protocol.TypeMessage)
		}
		if env.InReplyTo != nil {
			t.Errorf("env.InReplyTo: got %v, want nil (server-initiated)", env.InReplyTo)
		}
		if env.ID != 1 {
			// Each NewTestConn starts at 0 → first NextID()=1.
			t.Errorf("env.ID: got %d, want 1 for fresh test conn", env.ID)
		}
		var p protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
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
	if !seenConn["conn-a"] || !seenConn["conn-b"] {
		t.Errorf("expected both conns in outbound, got %v", seenConn)
	}

	cancel()
	<-done
}

func TestAssistantTurnEmitter_EnqueueCopiesChunk(t *testing.T) {
	t.Parallel()

	outbound := make(chan protocol.RoutingEnvelope, 1)
	c := dispatch.NewTestConn("conn-x", outbound, nil)
	cur := &stubCursor{}
	cur.set(testConvID)
	bc := &stubBroadcaster{conns: []*dispatch.Conn{c}}
	em := newAssistantTurnEmitter(cur, bc, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { em.Run(ctx); close(done) }()

	buf := []byte("first-text")
	em.Enqueue(buf)
	// Mutate the caller buffer immediately. The chunk seen downstream
	// must reflect the original bytes — Enqueue is required to copy.
	for i := range buf {
		buf[i] = 'X'
	}

	frames := drainOutbound(t, outbound, 1, time.Second)
	if len(frames) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(frames))
	}
	var env protocol.Envelope
	if err := json.Unmarshal(frames[0].Frame, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var p protocol.MessagePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Text != "first-text" {
		t.Errorf("Text: got %q, want %q (Enqueue did not copy)", p.Text, "first-text")
	}

	cancel()
	<-done
}

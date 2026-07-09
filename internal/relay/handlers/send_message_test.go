package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	sendMsgRequestID     = uint64(8)
	sendMsgNextID        = uint64(2)
	sendMsgConvID        = "C1"
	sendMsgMessageID     = "M1"
	sendMsgText          = "hi there"
	sendMsgConnIDForTest = "c-send-msg"
)

// stubTurnWriter is a TurnWriter that records Activate / WriteUserTurn call
// counts. Since #721 the handler enqueues and never drives the write surface, so
// it is used only to prove both counters stay 0 on the enqueue and reject paths
// (delivery moved to the daemon drain, covered in cmd/pyry).
type stubTurnWriter struct {
	activateCalls int
	calls         int
}

func (s *stubTurnWriter) Activate(ctx context.Context) error {
	s.activateCalls++
	return nil
}

func (s *stubTurnWriter) WriteUserTurn(ctx context.Context, id string, payload []byte) error {
	s.calls++
	return nil
}

// stubSessionRouter is the test double for SessionRouter. It records every
// conversationID it was asked to route. When multi is non-nil it maps each id
// to its own writer (the AC#2 independence shape); otherwise it returns tw/err
// for any id. A non-nil err short-circuits before tw is consulted, so a reject
// test can also set tw to prove the writer is never reached.
type stubSessionRouter struct {
	tw     TurnWriter
	err    error
	multi  map[string]TurnWriter
	gotIDs []string
}

func (s *stubSessionRouter) Route(conversationID string) (TurnWriter, error) {
	s.gotIDs = append(s.gotIDs, conversationID)
	if s.err != nil {
		return nil, s.err
	}
	if s.multi != nil {
		w, ok := s.multi[conversationID]
		if !ok {
			return nil, conversations.ErrConversationNotFound
		}
		return w, nil
	}
	return s.tw, nil
}

// routeTo wraps a single TurnWriter in a stubSessionRouter that returns it for
// any conversationID — a uniform routing shim for tests that assert handler
// conduct independent of which conversation resolved.
func routeTo(w TurnWriter) SessionRouter {
	return &stubSessionRouter{tw: w}
}

// enqueueCall records one (conversationID, text) pair the handler appended.
type enqueueCall struct {
	convID string
	text   string
}

// fakeEnqueuer is the test double for Enqueuer. It records every (convID, text)
// the handler enqueues and hands back a monotonic stub id, so a test can assert
// the handler enqueued exactly the routed conversation's verbatim text — and, on
// the routing-reject paths, that it enqueued nothing. When reject is set it still
// records the call (the handler DID reach the enqueue point) but returns 0, the
// backlog-full sentinel, so the cap-reject branch can be exercised.
type fakeEnqueuer struct {
	calls  []enqueueCall
	nextID uint64
	reject bool
}

func (f *fakeEnqueuer) Enqueue(convID, text string) uint64 {
	f.calls = append(f.calls, enqueueCall{convID: convID, text: text})
	if f.reject {
		return 0
	}
	f.nextID++
	return f.nextID
}

func sendMsgLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sendMsgCapturingLogger returns a logger that writes to buf so a test can assert
// what a handler branch logged. The handler runs synchronously in the test
// goroutine, so reading buf after the call returns is race-free.
func sendMsgCapturingLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// newSendMsgConn mirrors register_push_token_test.go's newTestConn, with
// a distinct conn id constant so a parallel-run test crash log is easy
// to attribute. NextID is advanced once before the handler runs so the
// first reply observes id=2 (mimicking the gate's hello_ack accounting).
func newSendMsgConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope, <-chan protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	dev := &devices.Device{
		TokenHash: devices.HashToken("plain-token"),
		Name:      "phone",
	}
	c := dispatch.NewTestConn(sendMsgConnIDForTest, out, dev)
	_ = c.NextID()
	recv := func() protocol.RoutingEnvelope {
		t.Helper()
		select {
		case env := <-out:
			return env
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for outbound envelope")
			return protocol.RoutingEnvelope{}
		}
	}
	return c, recv, out
}

func sendMsgRequest(t *testing.T, payload any) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      sendMsgRequestID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertSendMsgEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != sendMsgConnIDForTest {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, sendMsgConnIDForTest)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != sendMsgNextID {
		t.Errorf("ID = %d, want %d", env.ID, sendMsgNextID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != sendMsgRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, sendMsgRequestID)
	}
	return env
}

// TestSendMessage_AckOnEnqueue covers AC#1+AC#3: a routable send routes the
// frame's ConversationID (the validate + cursor-stamp moment), enqueues the
// text exactly once under that id, and acks on ENQUEUE — without ever driving
// the write surface (the resolved writer is discarded; delivery is the drain's
// job).
func TestSendMessage_AckOnEnqueue(t *testing.T) {
	t.Parallel()
	bound := &stubTurnWriter{}
	router := &stubSessionRouter{tw: bound}
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(router, q, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

	if got := router.gotIDs; len(got) != 1 || got[0] != sendMsgConvID {
		t.Errorf("router routed %v, want [%q]", got, sendMsgConvID)
	}
	if len(q.calls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(q.calls))
	}
	if q.calls[0].convID != sendMsgConvID || q.calls[0].text != sendMsgText {
		t.Errorf("Enqueue(%q, %q), want (%q, %q)", q.calls[0].convID, q.calls[0].text, sendMsgConvID, sendMsgText)
	}
	if bound.activateCalls != 0 || bound.calls != 0 {
		t.Errorf("write surface reached on enqueue path: activate=%d write=%d, want 0/0 (writer is discarded)", bound.activateCalls, bound.calls)
	}
}

// TestSendMessage_TwoConversations_EachEnqueuesIndependently covers AC#2: two
// sends to different conversations enqueue under their own ids with their own
// text — neither conversation's enqueue carries the other's bytes. (Ordered,
// independent DRAIN across conversations is the engine's property, proven live
// in cmd/pyry's inbound-deliver test.)
func TestSendMessage_TwoConversations_EachEnqueuesIndependently(t *testing.T) {
	t.Parallel()
	const (
		convA = "conv-A"
		convB = "conv-B"
		textA = "message for A"
		textB = "message for B"
	)
	writerA := &stubTurnWriter{}
	writerB := &stubTurnWriter{}
	router := &stubSessionRouter{multi: map[string]TurnWriter{
		convA: writerA,
		convB: writerB,
	}}
	q := &fakeEnqueuer{}

	send := func(t *testing.T, convID, text string) {
		t.Helper()
		c, recv, _ := newSendMsgConn(t)
		req := sendMsgRequest(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      sendMsgMessageID,
			Text:           text,
		})
		h := SendMessage(router, q, sendMsgLogger(t))
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler(%s): %v", convID, err)
		}
		assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
	}

	send(t, convA, textA)
	send(t, convB, textB)

	want := []enqueueCall{{convID: convA, text: textA}, {convID: convB, text: textB}}
	if len(q.calls) != len(want) {
		t.Fatalf("Enqueue calls = %d, want %d", len(q.calls), len(want))
	}
	for i, w := range want {
		if q.calls[i] != w {
			t.Errorf("Enqueue[%d] = %+v, want %+v", i, q.calls[i], w)
		}
	}
}

// TestSendMessage_UnknownConversation_RejectedBeforeEnqueue covers AC#4's first
// arm: a router that fails resolution with ErrConversationNotFound makes the
// handler reply conversation.not_found (not retryable) before any enqueue — the
// backlog and the write surface are both untouched.
func TestSendMessage_UnknownConversation_RejectedBeforeEnqueue(t *testing.T) {
	t.Parallel()
	bound := &stubTurnWriter{}
	// tw is set to prove it is never invoked once err short-circuits Route.
	router := &stubSessionRouter{tw: bound, err: conversations.ErrConversationNotFound}
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: "unknown",
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(router, q, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeConversationNotFound {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeConversationNotFound)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if len(q.calls) != 0 {
		t.Errorf("Enqueue calls = %d, want 0 (reject must precede enqueue)", len(q.calls))
	}
	if bound.activateCalls != 0 || bound.calls != 0 {
		t.Errorf("writer reached on routing reject: activate=%d write=%d, want 0/0", bound.activateCalls, bound.calls)
	}
}

// TestSendMessage_NoBoundSession_RejectedBeforeEnqueue covers AC#4's second arm:
// a router error that is NOT ErrConversationNotFound (the conversation exists but
// has no live bound session) maps to a retryable server.binary_offline reply
// before any enqueue. This is the asymmetric arm: at enqueue we have a live
// phone to tell "retry", so the unbound case rejects synchronously (a transient
// unbind AFTER ack is instead absorbed by the drain, covered in cmd/pyry).
func TestSendMessage_NoBoundSession_RejectedBeforeEnqueue(t *testing.T) {
	t.Parallel()
	bound := &stubTurnWriter{}
	router := &stubSessionRouter{tw: bound, err: errors.New("conversation has no bound session")}
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(router, q, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeServerBinaryOffline)
	}
	if !payload.Retryable {
		t.Errorf("Retryable = false, want true (no-bound-session is transient)")
	}
	if len(q.calls) != 0 {
		t.Errorf("Enqueue calls = %d, want 0 (reject must precede enqueue)", len(q.calls))
	}
	if bound.activateCalls != 0 || bound.calls != 0 {
		t.Errorf("writer reached on no-bound-session reject: activate=%d write=%d, want 0/0", bound.activateCalls, bound.calls)
	}
}

// TestSendMessage_BacklogFull_RetryableReject covers #869 AC#3: a routable send
// whose Enqueue returns 0 (backlog at the per-conversation cap) replies with the
// retryable server-busy envelope and does NOT ack. Unlike the routing-reject
// arms, Enqueue WAS called once (and returned 0) — the reject is at the enqueue
// point, not before it. The reject log carries conversation_id + message_id but
// never payload.Text (untrusted, phone-controlled content).
func TestSendMessage_BacklogFull_RetryableReject(t *testing.T) {
	t.Parallel()
	bound := &stubTurnWriter{}
	router := &stubSessionRouter{tw: bound}
	q := &fakeEnqueuer{reject: true} // backlog full: Enqueue returns 0
	logger, logs := sendMsgCapturingLogger(t)
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(router, q, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeServerBinaryBusy {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeServerBinaryBusy)
	}
	if !payload.Retryable {
		t.Errorf("Retryable = false, want true (backlog full is transient)")
	}

	// Enqueue was reached exactly once and returned 0; the reject is at (not
	// before) the enqueue point.
	if len(q.calls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 (cap reject is at the enqueue point)", len(q.calls))
	}

	// Never ack on reject: the only reply was the error envelope above.
	if bound.activateCalls != 0 || bound.calls != 0 {
		t.Errorf("writer reached on backlog-full reject: activate=%d write=%d, want 0/0", bound.activateCalls, bound.calls)
	}

	// SECURITY: the reject log must never leak payload.Text; it must carry the
	// opaque ids for diagnosis.
	logged := logs.String()
	if strings.Contains(logged, sendMsgText) {
		t.Errorf("reject log leaked payload.Text %q; logs = %q", sendMsgText, logged)
	}
	if !strings.Contains(logged, sendMsgConvID) {
		t.Errorf("reject log missing conversation_id %q; logs = %q", sendMsgConvID, logged)
	}
	if !strings.Contains(logged, sendMsgMessageID) {
		t.Errorf("reject log missing message_id %q; logs = %q", sendMsgMessageID, logged)
	}
}

func TestSendMessage_MalformedPayload_RejectedBeforeEnqueue(t *testing.T) {
	t.Parallel()
	router := routeTo(&stubTurnWriter{})
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := protocol.Envelope{
		ID:      sendMsgRequestID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: []byte("not-json"),
	}

	h := SendMessage(router, q, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if len(q.calls) != 0 {
		t.Errorf("Enqueue calls = %d, want 0 (malformed payload must not reach the backlog)", len(q.calls))
	}
}

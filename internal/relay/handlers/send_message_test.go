package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// enqueueCall records one (conversationID, messageID, text, delivery) tuple the
// handler appended. delivery is what reaches claude; text is what a client reads
// back, and the two differ only for a message naming attachments (#2038).
// messageID is the client's own id for the message, relayed verbatim onto the
// queued record so a client can merge the queued row with its optimistic echo
// (#2092); the handler neither validates nor mints it.
type enqueueCall struct {
	convID    string
	messageID string
	text      string
	delivery  string
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

func (f *fakeEnqueuer) EnqueueDelivery(convID, messageID, text, delivery string) uint64 {
	f.calls = append(f.calls, enqueueCall{convID: convID, messageID: messageID, text: text, delivery: delivery})
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

	h := SendMessage(router, q, nil, sendMsgLogger(t))
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
	// A message naming no attachments is delivered byte-for-byte as it is queued
	// (#2038 AC 2) — the composition seam is inert when there is nothing to name.
	if q.calls[0].delivery != sendMsgText {
		t.Errorf("delivery = %q, want the queued text %q verbatim", q.calls[0].delivery, sendMsgText)
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
		h := SendMessage(router, q, nil, sendMsgLogger(t))
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler(%s): %v", convID, err)
		}
		assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
	}

	send(t, convA, textA)
	send(t, convB, textB)

	// Both sends carry the same client message_id, since `send` builds one payload
	// shape. That is legal and inert: the id addresses nothing, so it neither
	// merges the two conversations' enqueues nor deduplicates them (#2092 AC 4).
	want := []enqueueCall{
		{convID: convA, messageID: sendMsgMessageID, text: textA, delivery: textA},
		{convID: convB, messageID: sendMsgMessageID, text: textB, delivery: textB},
	}
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

	h := SendMessage(router, q, nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, nil, logger)
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

	h := SendMessage(router, q, nil, sendMsgLogger(t))
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

// --- #2038: naming a message's stored attachments ---------------------------

const (
	attachPath1 = "/home/u/.pyry/inst/conversations/c/attachments/a1/report.pdf"
	attachPath2 = "/home/u/.pyry/inst/conversations/c/attachments/a2/_.._.._etc_passwd"
)

// attachID builds a canonical-shaped attachment id. The handler never validates
// the shape (attachments.ResolvePath owns that check), but using the real shape
// keeps a fixture from reading as permission to send any short string.
func attachID(n int) string {
	return fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", n)
}

// repeatID builds a list naming one id n times — the shape that makes the bound's
// count-elements-not-distinct-ids rule observable.
func repeatID(id string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = id
	}
	return out
}

// resolveCall records one (conversationID, attachmentID) pair the handler asked
// the resolver about. The conversation half is the one that matters: it must be
// the id Route already validated, never anything else, or ResolvePath's
// confinement precondition is not discharged.
type resolveCall struct {
	convID string
	id     string
}

// fakeAttachmentResolver is the test double for AttachmentResolver. An id absent
// from paths does not resolve — the single comma-ok refusal ResolvePath answers
// for an unknown id, a non-canonical id and a foreign-conversation id alike.
type fakeAttachmentResolver struct {
	paths map[string]string
	calls []resolveCall
}

func (f *fakeAttachmentResolver) resolve(convID, id string) (string, bool) {
	f.calls = append(f.calls, resolveCall{convID: convID, id: id})
	p, ok := f.paths[id]
	return p, ok
}

// sendMsgRawRequest builds a send_message envelope from RAW payload JSON. It
// exists for the three empty wire forms: SendMessagePayload's omitempty elides a
// nil and an empty slice identically, so a marshalled struct cannot express
// "attachment_ids": null or "attachment_ids": [] at all.
func sendMsgRawRequest(t *testing.T, payloadJSON string) protocol.Envelope {
	t.Helper()
	if !json.Valid([]byte(payloadJSON)) {
		t.Fatalf("fixture payload is not valid JSON: %s", payloadJSON)
	}
	return protocol.Envelope{
		ID:      sendMsgRequestID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payloadJSON),
	}
}

// wantPrompt builds the delivery payload the handler is expected to compose, so
// every assertion below is an EXACT string compare rather than a substring probe
// that would tolerate a stray path or a mangled separator.
func wantPrompt(text string, paths ...string) string {
	block := attachmentPromptHeader + "\n" + strings.Join(paths, "\n")
	if text == "" {
		return block
	}
	return text + "\n\n" + block
}

func sendMsgErrorPayload(t *testing.T, env protocol.Envelope) protocol.ErrorPayload {
	t.Helper()
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	return payload
}

// TestSendMessage_NoAttachments_DeliveredVerbatim covers AC 2 across ALL THREE
// empty wire forms. SendMessagePayload.UnmarshalJSON collapses them to one
// value, so a build that branched on which arrived would have to do it against a
// distinction the decoder already erased — these rows are what prove it did.
func TestSendMessage_NoAttachments_DeliveredVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
	}{
		{"key absent", `{"conversation_id":"C1","message_id":"M1","text":"hi there"}`},
		{"null", `{"conversation_id":"C1","message_id":"M1","text":"hi there","attachment_ids":null}`},
		{"empty array", `{"conversation_id":"C1","message_id":"M1","text":"hi there","attachment_ids":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			res := &fakeAttachmentResolver{}
			c, recv, _ := newSendMsgConn(t)

			h := SendMessage(router, q, res.resolve, sendMsgLogger(t))
			if err := h(context.Background(), c, sendMsgRawRequest(t, tt.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

			if len(q.calls) != 1 {
				t.Fatalf("Enqueue calls = %d, want 1", len(q.calls))
			}
			if q.calls[0].text != sendMsgText || q.calls[0].delivery != sendMsgText {
				t.Errorf("queued (text, delivery) = (%q, %q), want both %q byte-for-byte",
					q.calls[0].text, q.calls[0].delivery, sendMsgText)
			}
			if len(res.calls) != 0 {
				t.Errorf("resolver called %d times for a message naming nothing, want 0", len(res.calls))
			}
		})
	}
}

// TestSendMessage_ComposesPromptFromAttachments covers AC 1 and the dedup half of
// AC 6: each named attachment's on-host path reaches claude in the order the
// client listed it, the queued text stays the user's own words, and a repeated id
// is resolved once and named once at its FIRST-occurrence position.
func TestSendMessage_ComposesPromptFromAttachments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		text        string
		ids         []string
		wantPaths   []string
		wantResolve []string // the ids the resolver should have been asked about, in order
	}{
		{
			name:        "one attachment",
			text:        sendMsgText,
			ids:         []string{attachID(1)},
			wantPaths:   []string{attachPath1},
			wantResolve: []string{attachID(1)},
		},
		{
			name:        "two attachments keep the client's order",
			text:        sendMsgText,
			ids:         []string{attachID(2), attachID(1)},
			wantPaths:   []string{attachPath2, attachPath1},
			wantResolve: []string{attachID(2), attachID(1)},
		},
		{
			// The dedup row. A build that named every occurrence answers the path
			// twice; one that deduped on LAST occurrence answers them reversed.
			name:        "a repeated id is resolved once and named once, in first-occurrence order",
			text:        sendMsgText,
			ids:         []string{attachID(1), attachID(2), attachID(1)},
			wantPaths:   []string{attachPath1, attachPath2},
			wantResolve: []string{attachID(1), attachID(2)},
		},
		{
			// Empty text is a real message — a person attaching a file and saying
			// nothing. The block stands alone, with no leading blank line.
			name:        "empty text carries the block alone",
			text:        "",
			ids:         []string{attachID(1)},
			wantPaths:   []string{attachPath1},
			wantResolve: []string{attachID(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			res := &fakeAttachmentResolver{paths: map[string]string{
				attachID(1): attachPath1,
				attachID(2): attachPath2,
			}}
			c, recv, _ := newSendMsgConn(t)
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      sendMsgMessageID,
				Text:           tt.text,
				AttachmentIDs:  tt.ids,
			})

			h := SendMessage(router, q, res.resolve, sendMsgLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

			if len(q.calls) != 1 {
				t.Fatalf("Enqueue calls = %d, want 1", len(q.calls))
			}
			if q.calls[0].text != tt.text {
				t.Errorf("queued text = %q, want the user's own words %q", q.calls[0].text, tt.text)
			}
			if want := wantPrompt(tt.text, tt.wantPaths...); q.calls[0].delivery != want {
				t.Errorf("delivery =\n%q\nwant\n%q", q.calls[0].delivery, want)
			}

			if len(res.calls) != len(tt.wantResolve) {
				t.Fatalf("resolver asked %d times (%+v), want %d", len(res.calls), res.calls, len(tt.wantResolve))
			}
			for i, wantID := range tt.wantResolve {
				if res.calls[i].id != wantID {
					t.Errorf("resolve[%d] id = %q, want %q", i, res.calls[i].id, wantID)
				}
				// Confinement: every resolve is asked against the conversation Route
				// already validated, never a second id from anywhere else.
				if res.calls[i].convID != sendMsgConvID {
					t.Errorf("resolve[%d] conversation = %q, want the routed %q", i, res.calls[i].convID, sendMsgConvID)
				}
			}
		})
	}
}

// TestSendMessage_UnresolvedAttachment_RejectedBeforeEnqueue covers AC 3: a named
// id that does not resolve under this conversation puts NO path in a prompt and
// nothing in the backlog, and the client is told attachment.not_found with a
// static message that does not say which id failed.
func TestSendMessage_UnresolvedAttachment_RejectedBeforeEnqueue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver AttachmentResolver
		res      *fakeAttachmentResolver
	}{
		{
			name: "one of two ids does not resolve",
			res:  &fakeAttachmentResolver{paths: map[string]string{attachID(1): attachPath1}},
		},
		{
			// Fail-closed: with no resolver wired, a message naming attachments is
			// refused rather than silently delivered without them.
			name: "nil resolver refuses every id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			var resolve AttachmentResolver
			if tt.res != nil {
				resolve = tt.res.resolve
			}
			c, recv, _ := newSendMsgConn(t)
			ids := []string{attachID(1), attachID(9)}
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      sendMsgMessageID,
				Text:           sendMsgText,
				AttachmentIDs:  ids,
			})

			h := SendMessage(router, q, resolve, sendMsgLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
			payload := sendMsgErrorPayload(t, env)
			if payload.Code != protocol.CodeAttachmentNotFound {
				t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeAttachmentNotFound)
			}
			if payload.Retryable {
				t.Errorf("Retryable = true, want false (re-listing the conversation is the repair)")
			}
			// The oracle guard: naming WHICH of the ids failed would turn one
			// send_message into a batch existence probe for up to 32 ids.
			for _, id := range ids {
				if strings.Contains(payload.Message, id) {
					t.Errorf("reply message %q names attachment id %q; it must be static", payload.Message, id)
				}
			}
			if strings.Contains(payload.Message, attachPath1) {
				t.Errorf("reply message %q discloses a host path", payload.Message)
			}
			if len(q.calls) != 0 {
				t.Errorf("Enqueue calls = %d, want 0 (a partially-attached turn must never reach claude)", len(q.calls))
			}
		})
	}
}

// TestSendMessage_AttachmentIDBound covers the enforcement half of AC 6. The
// bound is protocol.MaxAttachmentIDsPerMessage, published as UNCHECKED with
// enforcement named as this ticket's, and it counts ELEMENTS rather than distinct
// ids — the repeated-id row is what makes that ordering non-vacuous, since a
// build that deduped before counting accepts it.
func TestSendMessage_AttachmentIDBound(t *testing.T) {
	t.Parallel()
	atBound := make([]string, protocol.MaxAttachmentIDsPerMessage)
	overBound := make([]string, protocol.MaxAttachmentIDsPerMessage+1)
	for i := range overBound {
		overBound[i] = attachID(i)
		if i < len(atBound) {
			atBound[i] = attachID(i)
		}
	}
	repeated := repeatID(attachID(0), protocol.MaxAttachmentIDsPerMessage+1)

	tests := []struct {
		name        string
		ids         []string
		wantType    string
		wantCode    string
		wantEnqueue int
	}{
		{"exactly at the bound is accepted", atBound, protocol.TypeAck, "", 1},
		{"one past the bound is refused", overBound, protocol.TypeError, protocol.CodeProtocolMalformed, 0},
		{"one past the bound as repeats of ONE id is still refused", repeated, protocol.TypeError, protocol.CodeProtocolMalformed, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			paths := map[string]string{}
			for i := range overBound {
				paths[attachID(i)] = fmt.Sprintf("/home/u/.pyry/inst/conversations/c/attachments/a%d/f.txt", i)
			}
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			res := &fakeAttachmentResolver{paths: paths}
			c, recv, _ := newSendMsgConn(t)
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      sendMsgMessageID,
				Text:           sendMsgText,
				AttachmentIDs:  tt.ids,
			})

			h := SendMessage(router, q, res.resolve, sendMsgLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertSendMsgEnvelopeShape(t, recv(), tt.wantType)
			if tt.wantCode != "" {
				payload := sendMsgErrorPayload(t, env)
				if payload.Code != tt.wantCode {
					t.Errorf("Code = %q, want %q", payload.Code, tt.wantCode)
				}
				if payload.Retryable {
					t.Errorf("Retryable = true, want false (resending the same frame reproduces it)")
				}
				// The bound is a pure frame-shape rule, so it is answered before
				// Route stamps the active-conversation cursor and before a single
				// directory is read. Both counters at 0 is that ordering.
				if len(router.gotIDs) != 0 {
					t.Errorf("router routed %v on an over-bound frame, want none (the cursor must not be stamped)", router.gotIDs)
				}
				if len(res.calls) != 0 {
					t.Errorf("resolver asked %d times on an over-bound frame, want 0", len(res.calls))
				}
			}
			if len(q.calls) != tt.wantEnqueue {
				t.Errorf("Enqueue calls = %d, want %d", len(q.calls), tt.wantEnqueue)
			}
		})
	}
}

// TestSendMessage_RouteBeforeResolve pins the other half of the ordering, and it
// is the security-load-bearing half: attachments.ResolvePath's precondition is
// that its conversationID is one the authenticated session is already on, and
// Route validating the client-asserted id against the registry binding is what
// discharges it. A build that resolved first would resolve against an id nothing
// had checked.
func TestSendMessage_RouteBeforeResolve(t *testing.T) {
	t.Parallel()
	router := &stubSessionRouter{err: errors.New("conversation has no bound session")}
	q := &fakeEnqueuer{}
	res := &fakeAttachmentResolver{paths: map[string]string{attachID(1): attachPath1}}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
		AttachmentIDs:  []string{attachID(1)},
	})

	h := SendMessage(router, q, res.resolve, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
	if payload := sendMsgErrorPayload(t, env); payload.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("Code = %q, want %q (the routing reject wins over a resolvable attachment)", payload.Code, protocol.CodeServerBinaryOffline)
	}
	if len(res.calls) != 0 {
		t.Errorf("resolver asked %d times against an unvalidated conversation, want 0", len(res.calls))
	}
	if len(q.calls) != 0 {
		t.Errorf("Enqueue calls = %d, want 0", len(q.calls))
	}
}

// TestSendMessage_AttachmentPathsNeverLogged covers AC 5 on all three new
// branches. The non-vacuity guard is the positive half: on the success path the
// test asserts the handler DID hold the path (it composed it into the delivery)
// and still did not log it, so the assertion cannot pass by the handler simply
// never having the value.
func TestSendMessage_AttachmentPathsNeverLogged(t *testing.T) {
	t.Parallel()
	const filename = "quarterly_earnings_draft.pdf"
	path := "/home/u/.pyry/inst/conversations/c/attachments/" + attachID(1) + "/" + filename

	tests := []struct {
		name string
		ids  []string
		// wantComposed marks the one branch that reaches the composer, where the
		// test can prove the handler HELD the path before asserting it stayed
		// unlogged.
		wantComposed bool
	}{
		{"success path composes and stays silent", []string{attachID(1)}, true},
		{"not-found path", []string{attachID(1), attachID(9)}, false},
		{"over-bound path", repeatID(attachID(1), protocol.MaxAttachmentIDsPerMessage+1), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ids := tt.ids
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			res := &fakeAttachmentResolver{paths: map[string]string{attachID(1): path}}
			logger, logs := sendMsgCapturingLogger(t)
			c, recv, _ := newSendMsgConn(t)
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      sendMsgMessageID,
				Text:           sendMsgText,
				AttachmentIDs:  ids,
			})

			h := SendMessage(router, q, res.resolve, logger)
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}
			recv()

			// Non-vacuity: prove the handler actually held the path on the one
			// branch where it should have, so the absence below is a real silence.
			if tt.wantComposed {
				if len(q.calls) != 1 || !strings.Contains(q.calls[0].delivery, path) {
					t.Fatalf("delivery = %+v, want it to carry %q — the log assertion is vacuous otherwise", q.calls, path)
				}
			}

			got := logs.String()
			for _, banned := range []string{path, filename, attachID(1)} {
				if strings.Contains(got, banned) {
					t.Errorf("log carries %q, which AC 5 forbids at any level:\n%s", banned, got)
				}
			}
		})
	}
}

// TestSendMessage_RelaysClientMessageIDVerbatim covers #2092 AC 3 at the handler:
// the id the client sent is handed to the enqueue seam byte-for-byte. Not
// trimmed, not lower-cased, not re-encoded, and never minted when the client
// sent none — an empty message_id is legal (the field is non-omitempty and the
// handler validates nothing about it) and must arrive empty rather than filled
// in.
//
// Each row would survive a build that normalised in a DIFFERENT way, so the set
// separates trimming, case-folding and empty-defaulting instead of trusting one
// representative value to stand for all three. The hostile-shaped row is the
// #2038 posture applied here: a client-chosen string is opaque transit, so shape
// is not the handler's business either way.
func TestSendMessage_RelaysClientMessageIDVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		messageID string
	}{
		{"uuid", "3f2a9c14-7b6e-4d51-9a08-2e5c1b7d4f60"},
		{"empty is legal and never minted", ""},
		{"surrounding whitespace survives", "  M1\t"},
		{"case is preserved", "MiXeD-CaSe"},
		{"hostile shape is transit, not syntax", "a\"b\nc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := &stubSessionRouter{tw: &stubTurnWriter{}}
			q := &fakeEnqueuer{}
			c, recv, _ := newSendMsgConn(t)
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      tt.messageID,
				Text:           sendMsgText,
			})

			h := SendMessage(router, q, nil, sendMsgLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

			if len(q.calls) != 1 {
				t.Fatalf("Enqueue calls = %d, want 1", len(q.calls))
			}
			if q.calls[0].messageID != tt.messageID {
				t.Errorf("enqueued messageID = %q, want the client's bytes %q", q.calls[0].messageID, tt.messageID)
			}
			// The id must not have been sourced from, or clobbered, the text.
			if q.calls[0].text != sendMsgText {
				t.Errorf("enqueued text = %q, want %q", q.calls[0].text, sendMsgText)
			}
		})
	}
}

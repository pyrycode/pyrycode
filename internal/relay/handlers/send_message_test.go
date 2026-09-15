package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
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

	h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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
		h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, nil, nil, "", nil, logger)
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

	h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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

			h := SendMessage(router, q, res.resolve, nil, "", nil, sendMsgLogger(t))
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

			h := SendMessage(router, q, res.resolve, nil, "", nil, sendMsgLogger(t))
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

			h := SendMessage(router, q, resolve, nil, "", nil, sendMsgLogger(t))
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

			h := SendMessage(router, q, res.resolve, nil, "", nil, sendMsgLogger(t))
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

	h := SendMessage(router, q, res.resolve, nil, "", nil, sendMsgLogger(t))
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

			h := SendMessage(router, q, res.resolve, nil, "", nil, logger)
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

			h := SendMessage(router, q, nil, nil, "", nil, sendMsgLogger(t))
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

// --- #2159: a new chat names itself from its first message -------------------

const (
	autoNameCwd = "/work/autoname"
	// autoNameExisting is the name a seeded row already carries in the
	// never-overwrite tests — deliberately unlike anything deriveConversationName
	// would produce from sendMsgText, so a build that overwrote it is visible in
	// the diff of the assertion rather than hidden behind a coincidence.
	autoNameExisting = "operator's own title"
	autoNameLabel    = "Client work"
)

// autoNameSeedTime is the seeded row's LastUsedAt — a fixed instant, so the
// "a rejected send does not bump last_used_at" assertions compare against a
// known value rather than against a window.
//
// Since #2438 it is no longer what an ACCEPTED send leaves behind: the send is
// itself a use and re-stamps the field, so the accepted rows bracket the call
// with the clock instead. Naming still does not bump — the announced record
// simply carries the instant the send that provoked it just wrote.
var autoNameSeedTime = time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)

// newAutoNameReg returns a registry backed by a throwaway path (so the eager Save
// writes to a temp dir) seeded with one conversation at sendMsgConvID whose Name
// is name — nil for the unnamed row every auto-naming test starts from.
func newAutoNameReg(t *testing.T, name *string) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(sendMsgConvID),
		Name:       name,
		Cwd:        autoNameCwd,
		LastUsedAt: autoNameSeedTime,
	})
	return reg, path
}

// capturingAnnouncer returns a ConversationAnnouncer that appends every pushed
// record, plus the slice it appends to. The handler runs synchronously in the
// test goroutine, so reading the slice after the call returns is race-free.
func capturingAnnouncer() (ConversationAnnouncer, *[]protocol.ConversationUpdatedPayload) {
	var got []protocol.ConversationUpdatedPayload
	return func(p protocol.ConversationUpdatedPayload) { got = append(got, p) }, &got
}

// storedName reads a conversation's name straight off the registry.
func storedName(t *testing.T, reg *conversations.Registry, id string) *string {
	t.Helper()
	conv, ok := reg.Get(conversations.ConversationID(id))
	if !ok {
		t.Fatalf("conversation %q not in registry", id)
	}
	return conv.Name
}

// TestSendMessage_AutoNamesUnnamedConversation covers AC 2 and AC 3 on the happy
// path: an accepted send over a nil-name row stores the derived title, persists
// it, and pushes exactly one record carrying the stored name, the workspace's
// label, an unchanged last_used_at and no in_reply_to.
func TestSendMessage_AutoNamesUnnamedConversation(t *testing.T) {
	t.Parallel()
	reg, path := newAutoNameReg(t, nil)
	label := autoNameLabel
	reg.SetWorkspaceLabel(autoNameCwd, &label)
	announce, pushed := capturingAnnouncer()
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(routeTo(&stubTurnWriter{}), q, nil, reg, path, announce, sendMsgLogger(t))
	before := time.Now()
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	after := time.Now()

	// The ack is unchanged — auto-naming is a side effect of an accepted message.
	assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

	got := storedName(t, reg, sendMsgConvID)
	if got == nil {
		t.Fatalf("stored Name = nil, want %q", sendMsgText)
	}
	if *got != sendMsgText {
		t.Errorf("stored Name = %q, want %q", *got, sendMsgText)
	}

	// Persisted eagerly: a fresh registry loaded off the same path sees the name,
	// which is the property that makes it survive a daemon restart.
	reloaded, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if n := storedName(t, reloaded, sendMsgConvID); n == nil || *n != sendMsgText {
		t.Errorf("reloaded Name = %v, want pointer to %q", n, sendMsgText)
	}

	if len(*pushed) != 1 {
		t.Fatalf("announce calls = %d, want 1", len(*pushed))
	}
	p := (*pushed)[0]
	if p.ID != sendMsgConvID {
		t.Errorf("pushed ID = %q, want %q", p.ID, sendMsgConvID)
	}
	if p.Name == nil || *p.Name != sendMsgText {
		t.Errorf("pushed Name = %v, want pointer to %q", p.Name, sendMsgText)
	}
	if p.Cwd != autoNameCwd {
		t.Errorf("pushed Cwd = %q, want %q", p.Cwd, autoNameCwd)
	}
	if p.WorkspaceLabel == nil || *p.WorkspaceLabel != autoNameLabel {
		t.Errorf("pushed WorkspaceLabel = %v, want pointer to %q", p.WorkspaceLabel, autoNameLabel)
	}
	// The announced record carries the instant the SEND just stamped, not the
	// seeded one (#2438). Naming is still not a use — it reads the row's own value
	// under the lock — but the send that provoked it is, and it wrote first, so a
	// record carrying the seed would be stale the moment it left the daemon.
	if p.LastUsedAt.Before(before) || p.LastUsedAt.After(after) {
		t.Errorf("pushed LastUsedAt = %v, want an instant within [%v, %v] (the send is a use)", p.LastUsedAt, before, after)
	}
}

// TestSendMessage_AutoNamePushesNullLabelWhenUnlabelled pins the nullable half of
// the workspace_label projection: presence comes from the registry accessor's
// second return, never from a label != "" compare.
func TestSendMessage_AutoNamePushesNullLabelWhenUnlabelled(t *testing.T) {
	t.Parallel()
	reg, path := newAutoNameReg(t, nil)
	announce, pushed := capturingAnnouncer()
	c, _, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText})

	h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, reg, path, announce, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if len(*pushed) != 1 {
		t.Fatalf("announce calls = %d, want 1", len(*pushed))
	}
	if lbl := (*pushed)[0].WorkspaceLabel; lbl != nil {
		t.Errorf("pushed WorkspaceLabel = %q, want nil", *lbl)
	}
}

// TestSendMessage_AutoNameNeverOverwrites covers AC 2's never-rename rule: a row
// that already carries a name — from create, rename, promote, or this path on an
// earlier message — is left alone and announces nothing.
func TestSendMessage_AutoNameNeverOverwrites(t *testing.T) {
	t.Parallel()
	existing := autoNameExisting
	reg, path := newAutoNameReg(t, &existing)
	announce, pushed := capturingAnnouncer()
	q := &fakeEnqueuer{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText})

	h := SendMessage(routeTo(&stubTurnWriter{}), q, nil, reg, path, announce, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
	if n := storedName(t, reg, sendMsgConvID); n == nil || *n != autoNameExisting {
		t.Errorf("stored Name = %v, want the untouched %q", n, autoNameExisting)
	}
	if len(*pushed) != 0 {
		t.Errorf("announce calls = %d, want 0 (an already-named row announces nothing)", len(*pushed))
	}
	// The message itself is unaffected: naming is a side effect, not a gate.
	if len(q.calls) != 1 {
		t.Errorf("Enqueue calls = %d, want 1", len(q.calls))
	}
}

// TestSendMessage_AutoNameSkipsRejectedSends covers AC 2's reject rule across
// every branch that returns before the hook point. None of them may write a name
// or push a record — a refused message names nothing.
func TestSendMessage_AutoNameSkipsRejectedSends(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		router  SessionRouter
		reject  bool
		payload protocol.SendMessagePayload
	}{
		{
			name:    "unknown conversation",
			router:  &stubSessionRouter{err: conversations.ErrConversationNotFound},
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
		{
			name:    "no bound session",
			router:  &stubSessionRouter{err: errors.New("no bound session")},
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
		{
			name:   "too many attachments",
			router: routeTo(&stubTurnWriter{}),
			payload: protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				Text:           sendMsgText,
				AttachmentIDs:  repeatID(attachID(1), protocol.MaxAttachmentIDsPerMessage+1),
			},
		},
		{
			name:    "backlog full",
			router:  routeTo(&stubTurnWriter{}),
			reject:  true,
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, path := newAutoNameReg(t, nil)
			announce, pushed := capturingAnnouncer()
			c, recv, _ := newSendMsgConn(t)

			h := SendMessage(tc.router, &fakeEnqueuer{reject: tc.reject}, nil, reg, path, announce, sendMsgLogger(t))
			if err := h(context.Background(), c, sendMsgRequest(t, tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}

			assertSendMsgEnvelopeShape(t, recv(), protocol.TypeError)
			if n := storedName(t, reg, sendMsgConvID); n != nil {
				t.Errorf("stored Name = %q, want nil (a rejected send names nothing)", *n)
			}
			if len(*pushed) != 0 {
				t.Errorf("announce calls = %d, want 0", len(*pushed))
			}
		})
	}
}

// TestSendMessage_AutoNameFromTextNotComposedPrompt covers AC 2's last sentence
// and AC 1's empty case together, on the two shapes a message with attachments
// takes. The composed prompt names on-host paths; a title cut from it would put a
// host filesystem path into a name pushed to every paired client.
func TestSendMessage_AutoNameFromTextNotComposedPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		text     string
		wantName *string
	}{
		{
			// Text plus an attachment: the title is the TEXT alone. A build that
			// derived from the delivery string names this chat "hi there Attached"
			// or worse, the path's first component.
			name:     "text with an attachment names the chat from the text",
			text:     sendMsgText,
			wantName: &[]string{sendMsgText}[0],
		},
		{
			// Attachment-only: the text normalises to empty, so nothing is written
			// and nothing is pushed — even though the delivery string is a long,
			// non-empty, daemon-authored block naming a path.
			name: "an attachment-only message names nothing",
			text: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, path := newAutoNameReg(t, nil)
			announce, pushed := capturingAnnouncer()
			res := &fakeAttachmentResolver{paths: map[string]string{attachID(1): attachPath1}}
			q := &fakeEnqueuer{}
			c, _, _ := newSendMsgConn(t)
			req := sendMsgRequest(t, protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				Text:           tc.text,
				AttachmentIDs:  []string{attachID(1)},
			})

			h := SendMessage(routeTo(&stubTurnWriter{}), q, res.resolve, reg, path, announce, sendMsgLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			// The delivery really did carry the path — otherwise this test could
			// pass against a build that dropped attachments entirely.
			if len(q.calls) != 1 {
				t.Fatalf("Enqueue calls = %d, want 1", len(q.calls))
			}
			if want := wantPrompt(tc.text, attachPath1); q.calls[0].delivery != want {
				t.Fatalf("delivery = %q, want %q", q.calls[0].delivery, want)
			}

			got := storedName(t, reg, sendMsgConvID)
			switch {
			case tc.wantName == nil && got != nil:
				t.Errorf("stored Name = %q, want nil", *got)
			case tc.wantName != nil && (got == nil || *got != *tc.wantName):
				t.Errorf("stored Name = %v, want pointer to %q", got, *tc.wantName)
			}
			wantPushes := 0
			if tc.wantName != nil {
				wantPushes = 1
			}
			if len(*pushed) != wantPushes {
				t.Errorf("announce calls = %d, want %d", len(*pushed), wantPushes)
			}
		})
	}
}

// TestSendMessage_AutoNameLogsWithoutTitleOrText covers AC 4: the one new event
// carries conversation_id and neither the message text nor the derived title —
// which is a prefix of that text and therefore the same untrusted user content.
func TestSendMessage_AutoNameLogsWithoutTitleOrText(t *testing.T) {
	t.Parallel()
	reg, path := newAutoNameReg(t, nil)
	logger, buf := sendMsgCapturingLogger(t)
	c, _, _ := newSendMsgConn(t)
	// A distinctive text so a leak cannot hide behind a word the log would carry
	// for another reason, and long enough that the title is a strict prefix.
	const secret = "zqxjvbrit confidential merger memo for the board tomorrow"
	req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: secret})

	h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, reg, path, nil, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "event=send_message.autonamed") {
		t.Errorf("log does not carry event=send_message.autonamed; got:\n%s", out)
	}
	if !strings.Contains(out, "conversation_id="+sendMsgConvID) {
		t.Errorf("log does not carry conversation_id=%s; got:\n%s", sendMsgConvID, out)
	}
	if strings.Contains(out, "zqxjvbrit") {
		t.Errorf("log carries the message text (or the title cut from it); got:\n%s", out)
	}
}

// TestSendMessage_AutoNameNilSeamsStillAck covers the two fail-closed shapes: a
// nil registry names nothing, and a nil announcer pushes nothing. Both still ack,
// which is the contract that keeps every caller with no relay leg working.
func TestSendMessage_AutoNameNilSeamsStillAck(t *testing.T) {
	t.Parallel()
	t.Run("nil registry names nothing", func(t *testing.T) {
		t.Parallel()
		announce, pushed := capturingAnnouncer()
		c, recv, _ := newSendMsgConn(t)
		req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText})

		h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, nil, "", announce, sendMsgLogger(t))
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler: %v", err)
		}
		assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
		if len(*pushed) != 0 {
			t.Errorf("announce calls = %d, want 0 (no registry, nothing to announce)", len(*pushed))
		}
	})

	t.Run("nil announcer still names and acks", func(t *testing.T) {
		t.Parallel()
		reg, path := newAutoNameReg(t, nil)
		c, recv, _ := newSendMsgConn(t)
		req := sendMsgRequest(t, protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText})

		h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, reg, path, nil, sendMsgLogger(t))
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler: %v", err)
		}
		assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
		if n := storedName(t, reg, sendMsgConvID); n == nil || *n != sendMsgText {
			t.Errorf("stored Name = %v, want pointer to %q", n, sendMsgText)
		}
	})
}

// ackWatchingNamer wraps a real registry and records how many envelopes were
// already queued outbound the first time the auto-naming step touched it. The
// embedded *conversations.Registry supplies Save and WorkspaceLabel unchanged, so
// the double still satisfies ConversationAutoNamer and the step under test does
// real work rather than running against a stub.
type ackWatchingNamer struct {
	*conversations.Registry
	out     <-chan protocol.RoutingEnvelope
	queued  int
	touched bool
}

func (n *ackWatchingNamer) Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool {
	n.touched = true
	n.queued = len(n.out)
	return n.Registry.Update(id, fn)
}

// TestSendMessage_AcksBeforeAutoNaming pins the ordering the naming step must
// keep: the ack is on its way out BEFORE any of the naming work begins.
//
// It is a real wire property and not bookkeeping. The step costs an fsync and a
// fan-out, and EnqueueDelivery has already handed the turn to the drain, which
// runs on its own goroutine — so time spent here is time in which the drain can
// deliver the turn and the child can provoke frames of its own. Run ahead of the
// ack, those frames reach the sender BEFORE the ack it is still waiting for.
// #2159's first cut did exactly that and reordered the wire under two stream-json
// e2e specs; nothing in the unit suite noticed, which is why this test exists.
//
// The assertion is on the OUTBOUND QUEUE DEPTH observed from inside the step,
// which is deterministic: the handler runs synchronously on this goroutine, so
// one queued envelope means the ack was written first and zero means it was not.
func TestSendMessage_AcksBeforeAutoNaming(t *testing.T) {
	t.Parallel()
	reg, path := newAutoNameReg(t, nil)
	c, recv, out := newSendMsgConn(t)
	namer := &ackWatchingNamer{Registry: reg, out: out}
	announce, pushed := capturingAnnouncer()
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, namer, path, announce, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !namer.touched {
		t.Fatal("the auto-naming step never touched the registry; the ordering assertion below would pass vacuously")
	}
	if namer.queued != 1 {
		t.Errorf("outbound envelopes queued when auto-naming began = %d, want 1 (the ack, already sent)", namer.queued)
	}
	// And the ack itself is unchanged by the move.
	assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
	if len(*pushed) != 1 {
		t.Errorf("announce calls = %d, want 1", len(*pushed))
	}
}

// --- #2438: an accepted send keeps its conversation out of the idle sweep ----

// storedLastUsed reads a conversation's LastUsedAt straight off the registry,
// the sibling of storedName.
func storedLastUsed(t *testing.T, reg *conversations.Registry, id string) time.Time {
	t.Helper()
	conv, ok := reg.Get(conversations.ConversationID(id))
	if !ok {
		t.Fatalf("conversation %q not in registry", id)
	}
	return conv.LastUsedAt
}

// TestSendMessage_BumpsLastUsedAtOnAcceptedSendOnly covers AC 1: an ACCEPTED
// send stamps the conversation's LastUsedAt with the accept time and persists
// it, and every branch that refuses the message leaves the field alone.
//
// The reject rows are the same four the auto-naming table enumerates, and for
// the same structural reason: the bump sits past the last reject branch, so
// "a rejected send is not a use" is a property of where the step is rather
// than of a guard inside it. Two of them — an unresolvable attachment aside —
// pass Route and are still refused, which is why the step hangs off ACCEPTANCE
// and not off Route succeeding.
//
// Every row seeds an ALREADY-NAMED row so the auto-naming step declines and the
// only registry write under test is the bump.
func TestSendMessage_BumpsLastUsedAtOnAcceptedSendOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		router   SessionRouter
		reject   bool
		payload  protocol.SendMessagePayload
		wantBump bool
	}{
		{
			name:   "accepted",
			router: routeTo(&stubTurnWriter{}),
			payload: protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				MessageID:      sendMsgMessageID,
				Text:           sendMsgText,
			},
			wantBump: true,
		},
		{
			name:    "unknown conversation",
			router:  &stubSessionRouter{err: conversations.ErrConversationNotFound},
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
		{
			name:    "no bound session",
			router:  &stubSessionRouter{err: errors.New("no bound session")},
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
		{
			name:   "too many attachments",
			router: routeTo(&stubTurnWriter{}),
			payload: protocol.SendMessagePayload{
				ConversationID: sendMsgConvID,
				Text:           sendMsgText,
				AttachmentIDs:  repeatID(attachID(1), protocol.MaxAttachmentIDsPerMessage+1),
			},
		},
		{
			name:    "backlog full",
			router:  routeTo(&stubTurnWriter{}),
			reject:  true,
			payload: protocol.SendMessagePayload{ConversationID: sendMsgConvID, Text: sendMsgText},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			existing := autoNameExisting
			reg, path := newAutoNameReg(t, &existing)
			c, recv, _ := newSendMsgConn(t)

			h := SendMessage(tc.router, &fakeEnqueuer{reject: tc.reject}, nil, reg, path, nil, sendMsgLogger(t))
			before := time.Now()
			if err := h(context.Background(), c, sendMsgRequest(t, tc.payload)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			after := time.Now()

			wantType := protocol.TypeError
			if tc.wantBump {
				wantType = protocol.TypeAck
			}
			assertSendMsgEnvelopeShape(t, recv(), wantType)

			got := storedLastUsed(t, reg, sendMsgConvID)
			if !tc.wantBump {
				if !got.Equal(autoNameSeedTime) {
					t.Errorf("stored LastUsedAt = %v, want the untouched %v (a rejected send is not a use)", got, autoNameSeedTime)
				}
				return
			}

			// A window rather than an instant: the handler reads the clock itself,
			// which is the whole point — nothing else in the daemon knows when the
			// message was accepted.
			if got.Before(before) || got.After(after) {
				t.Errorf("stored LastUsedAt = %v, want an instant within [%v, %v]", got, before, after)
			}

			// Persisted eagerly, so the bump survives a daemon restart — without
			// that, a registry reloaded at startup carries the stale instant and
			// the sweep deletes the conversation anyway.
			reloaded, err := conversations.Load(path)
			if err != nil {
				t.Fatalf("reload registry: %v", err)
			}
			if persisted := storedLastUsed(t, reloaded, sendMsgConvID); !persisted.Equal(got) {
				t.Errorf("reloaded LastUsedAt = %v, want the stored %v", persisted, got)
			}
		})
	}
}

// TestSendMessage_BumpSparesIdleConversationFromSweep covers AC 2 end to end
// through the predicate that owns the decision: a conversation created 31 days
// ago whose last accepted message is one day old is NOT swept.
//
// ShouldArchive takes now as a parameter, so "one day old" is expressed by
// asking the predicate a day after the send rather than by faking the clock the
// handler reads.
func TestSendMessage_BumpSparesIdleConversationFromSweep(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := autoNameExisting
	seed := conversations.Conversation{
		ID:         conversations.ConversationID(sendMsgConvID),
		Name:       &name,
		Cwd:        autoNameCwd,
		LastUsedAt: time.Now().Add(-31 * 24 * time.Hour),
	}
	reg.Create(seed)

	// Non-vacuous first: as seeded, this row is exactly what the sweep deletes.
	// Without this line the assertion below would also pass against a handler
	// that never touched the registry at all.
	if !conversations.ShouldArchive(seed, time.Now()) {
		t.Fatalf("seeded row (LastUsedAt = %v) is not sweep-eligible; the assertion below would be vacuous", seed.LastUsedAt)
	}

	c, recv, _ := newSendMsgConn(t)
	h := SendMessage(routeTo(&stubTurnWriter{}), &fakeEnqueuer{}, nil, reg, path, nil, sendMsgLogger(t))
	if err := h(context.Background(), c, sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)

	stored, ok := reg.Get(conversations.ConversationID(sendMsgConvID))
	if !ok {
		t.Fatalf("conversation %q not in registry", sendMsgConvID)
	}
	if conversations.ShouldArchive(stored, time.Now().Add(24*time.Hour)) {
		t.Errorf("ShouldArchive = true one day after an accepted message (LastUsedAt = %v); "+
			"a conversation in daily use must outlive the 30-day sweep", stored.LastUsedAt)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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

// stubTurnWriter records Activate / WriteUserTurn arguments and returns
// the preconfigured errors. The captured payload is detached from the
// caller's slice so the recorded bytes are immune to later in-place
// mutation. callOrder lets tests assert Activate-before-WriteUserTurn.
type stubTurnWriter struct {
	err            error
	activateErr    error
	calls          int
	activateCalls  int
	gotID          string
	gotPayload     []byte
	callOrder      []string
}

func (s *stubTurnWriter) Activate(ctx context.Context) error {
	s.activateCalls++
	s.callOrder = append(s.callOrder, "activate")
	return s.activateErr
}

func (s *stubTurnWriter) WriteUserTurn(ctx context.Context, id string, payload []byte) error {
	s.calls++
	s.gotID = id
	s.gotPayload = append([]byte(nil), payload...)
	s.callOrder = append(s.callOrder, "write")
	return s.err
}

func sendMsgLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

func TestSendMessage_Success_EmitsAck(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(stub, sendMsgLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertSendMsgEnvelopeShape(t, recv(), protocol.TypeAck)
	var ack protocol.AckPayload
	if err := json.Unmarshal(env.Payload, &ack); err != nil {
		t.Fatalf("unmarshal ack payload: %v", err)
	}

	if stub.calls != 1 {
		t.Errorf("WriteUserTurn calls = %d, want 1", stub.calls)
	}
	if stub.activateCalls != 1 {
		t.Errorf("Activate calls = %d, want 1", stub.activateCalls)
	}
	if got := stub.callOrder; len(got) != 2 || got[0] != "activate" || got[1] != "write" {
		t.Errorf("callOrder = %v, want [activate write]", got)
	}
	if stub.gotID != sendMsgConvID {
		t.Errorf("WriteUserTurn id = %q, want %q", stub.gotID, sendMsgConvID)
	}
	if string(stub.gotPayload) != sendMsgText {
		t.Errorf("WriteUserTurn payload = %q, want %q", string(stub.gotPayload), sendMsgText)
	}
}

// TestSendMessage_ActivateTimeout_EmitsBinaryOffline covers the #396
// failure mode: Activate returns context.DeadlineExceeded (e.g. claude
// failed to respawn within the 30s window). The handler must not call
// WriteUserTurn, must emit a server.binary_offline error envelope, and
// must return nil so the dispatcher keeps the conn alive for retry.
func TestSendMessage_ActivateTimeout_EmitsBinaryOffline(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{activateErr: context.DeadlineExceeded}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(stub, sendMsgLogger(t))
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
		t.Errorf("Retryable = false, want true (binary-offline is transient)")
	}
	if stub.activateCalls != 1 {
		t.Errorf("Activate calls = %d, want 1", stub.activateCalls)
	}
	if stub.calls != 0 {
		t.Errorf("WriteUserTurn calls = %d, want 0 (Activate failure must short-circuit)", stub.calls)
	}
}

// TestSendMessage_HandlerCtxCanceled_PropagatesError covers the
// conn-closing branch: when the dispatcher's ctx is cancelled, Activate
// returns context.Canceled and the handler propagates the error so the
// dispatcher's per-conn unwind runs. No wire reply is produced.
func TestSendMessage_HandlerCtxCanceled_PropagatesError(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{activateErr: context.Canceled}
	c, _, out := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ensure ctx.Err() is non-nil at handler entry

	h := SendMessage(stub, sendMsgLogger(t))
	err := h(ctx, c, req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("handler err = %v, want context.Canceled", err)
	}
	if stub.calls != 0 {
		t.Errorf("WriteUserTurn calls = %d, want 0", stub.calls)
	}

	select {
	case env := <-out:
		t.Fatalf("unexpected outbound envelope on ctx-cancel branch: %+v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSendMessage_ConversationNotFound_EmitsErrorEnvelope(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{err: conversations.ErrConversationNotFound}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: "unknown",
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(stub, sendMsgLogger(t))
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
	if payload.Message == "" {
		t.Errorf("Message empty, want non-empty")
	}
	if stub.calls != 1 {
		t.Errorf("WriteUserTurn calls = %d, want 1 (handler must invoke writer before mapping sentinel)", stub.calls)
	}
}

// TestSendMessage_DeliveryFailure_EmitsBinaryOffline covers AC #2's loud-
// failure contract: a default-class WriteUserTurn error (e.g. the supervisor's
// wrapped no-live-session / not-committed failure) must now produce a
// retryable server.binary_offline envelope so the phone learns the turn was
// not delivered — instead of the old silent error-propagation that produced no
// wire reply (and, upstream, a false ack on the silent-drop path). This
// inverts the prior TestSendMessage_WrappedError_PassesThroughNoWireReply.
func TestSendMessage_DeliveryFailure_EmitsBinaryOffline(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{err: errors.New("supervisor: write user turn: no live session")}
	c, recv, _ := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	h := SendMessage(stub, sendMsgLogger(t))
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
		t.Errorf("Retryable = false, want true (delivery failure is transient)")
	}
	if stub.calls != 1 {
		t.Errorf("WriteUserTurn calls = %d, want 1", stub.calls)
	}
}

// TestSendMessage_DeliveryCtxCanceled_PropagatesError covers the conn-closing
// branch on the delivery phase: when the parent ctx is cancelled and
// WriteUserTurn returns context.Canceled, the handler propagates the error for
// the dispatcher's per-conn unwind and emits no doomed wire reply. (A deliver
// timeout returns DeadlineExceeded, not Canceled, so it lands in the
// binary_offline arm instead — covered above.)
func TestSendMessage_DeliveryCtxCanceled_PropagatesError(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{err: context.Canceled}
	c, _, out := newSendMsgConn(t)
	req := sendMsgRequest(t, protocol.SendMessagePayload{
		ConversationID: sendMsgConvID,
		MessageID:      sendMsgMessageID,
		Text:           sendMsgText,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // parent ctx.Err() non-nil → propagate, don't reply

	h := SendMessage(stub, sendMsgLogger(t))
	err := h(ctx, c, req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("handler err = %v, want context.Canceled", err)
	}

	select {
	case env := <-out:
		t.Fatalf("unexpected outbound envelope on delivery ctx-cancel branch: %+v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSendMessage_MalformedPayload_EmitsProtocolMalformed(t *testing.T) {
	t.Parallel()
	stub := &stubTurnWriter{}
	c, recv, _ := newSendMsgConn(t)
	req := protocol.Envelope{
		ID:      sendMsgRequestID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: []byte("not-json"),
	}

	h := SendMessage(stub, sendMsgLogger(t))
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
	if stub.calls != 0 {
		t.Errorf("WriteUserTurn calls = %d, want 0 (malformed payload must not reach writer)", stub.calls)
	}
}

package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// prolificHandler returns a dispatch.Handler that emits req.Count reply
// envelopes (TypeConversations) in one invocation, each carrying a
// monotonically increasing {"index":i} payload so a test can assert
// emission order and count. The reply count is taken from the request
// payload so a single registered handler can serve both the many-reply
// wedge frame and a subsequent one-reply frame on the same conn.
func prolificHandler() dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var req struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return fmt.Errorf("prolific handler decode: %w", err)
		}
		for i := 0; i < req.Count; i++ {
			payload := json.RawMessage(fmt.Sprintf(`{"index":%d}`, i))
			if err := c.Reply(ctx, env, protocol.TypeConversations, payload); err != nil {
				return err
			}
		}
		return nil
	}
}

// appFrameCount seals an app frame of TypeListConversations whose payload
// requests count replies, then feeds it to the manager's Frames channel.
func appFrameCount(t *testing.T, sess *openSession, reqID uint64, count int) {
	t.Helper()
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(fmt.Sprintf(`{"count":%d}`, count)),
	})
}

// TestV2Session_OpenState_ProlificHandler_NoDeadlock is the #909
// regression guard (AC #1, #4). A handler registered in the shared
// Handlers table emits 2*handlerOutboundBuf replies in one invocation —
// more than the per-frame outbound buffer can hold. Against the previous
// drain-after-Route implementation the handler's Conn.Send fills the
// buffer and blocks forever on the Run goroutine, wedging the whole
// manager; waitForOutboundCount then times out and fails the test.
// After the concurrent-drain fix all replies are sealed and forwarded,
// and — crucially — a SECOND frame on the same conn is still serviced,
// proving the manager did not wedge.
func TestV2Session_OpenState_ProlificHandler_NoDeadlock(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: prolificHandler(),
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	const prolific = 2 * handlerOutboundBuf // 16 > buffer of 8

	// First frame: the prolific handler emits `prolific` replies. Expect
	// noise_resp (envs[0]) + all prolific replies to arrive as sealed
	// outbound. Against drain-after-Route this deadlocks and the deadline
	// fires.
	appFrameCount(t, sess, 17, prolific)
	envs := waitForOutboundCount(t, rec, 1+prolific, 2*time.Second)
	if len(envs) < 1+prolific {
		t.Fatalf("outbound after prolific frame: got %d, want >= %d", len(envs), 1+prolific)
	}

	// Second frame on the SAME conn: a single-reply invocation. If the
	// manager wedged on the prolific handler this never arrives. Its
	// reply pushes the outbound count to 1+prolific+1. (Single-conn
	// coverage is sufficient here per the spec: a wedge is a wedge
	// regardless of conn_id, and the harness hardcodes v2TestConnID.)
	const secondReqID uint64 = 18
	appFrameCount(t, sess, secondReqID, 1)
	envs = waitForOutboundCount(t, rec, 1+prolific+1, 2*time.Second)

	// Decrypt every reply in send-counter order: initRecv advances one
	// receive counter per Decrypt, so the replies must be validated in
	// emission order. The final reply is the second frame's — its
	// InReplyTo of secondReqID proves the manager serviced a fresh frame
	// after the prolific handler returned, i.e. it did not wedge.
	replies := envs[1:]
	if len(replies) != prolific+1 {
		t.Fatalf("replies: got %d, want %d", len(replies), prolific+1)
	}
	var lastInner protocol.Envelope
	for i, env := range replies {
		if env.CloseCode != 0 {
			t.Errorf("reply %d CloseCode = %d, want 0 (no close)", i, env.CloseCode)
		}
		lastInner = decryptAppFrame(t, env, sess.initRecv)
		if lastInner.Type != protocol.TypeConversations {
			t.Errorf("reply %d type = %q, want %q", i, lastInner.Type, protocol.TypeConversations)
		}
	}
	if lastInner.InReplyTo == nil || *lastInner.InReplyTo != secondReqID {
		t.Errorf("second-frame reply InReplyTo = %v, want pointer to %d", lastInner.InReplyTo, secondReqID)
	}

	// Manager is still open and servicing after both frames.
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after prolific dispatch", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after prolific dispatch = %v, want V2StateOpen", got)
	}
}

// TestV2Session_OpenState_ProlificHandler_EmissionOrder pins AC #2 (no
// drop, no reorder) and, under -race, AC #3 (s.send.Encrypt stays on the
// Run goroutine). The handler emits 2*handlerOutboundBuf replies with
// monotonically-indexed payloads; each is AEAD-sealed under s.send on the
// Run goroutine and decrypted here in send-counter order via initRecv. A
// dropped or reordered seal would surface as an AEAD/order mismatch in
// decryptAppFrame, so this test doubles as a send-counter-integrity
// check: if s.send.Encrypt ever ran off the Run goroutine (concurrent
// with the spawned Route goroutine), -race would fire and the ordered
// decrypt below would break.
func TestV2Session_OpenState_ProlificHandler_EmissionOrder(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: prolificHandler(),
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	const prolific = 2 * handlerOutboundBuf

	appFrameCount(t, sess, 17, prolific)
	envs := waitForOutboundCount(t, rec, 1+prolific, 2*time.Second)

	// envs[0] is the noise_resp; envs[1:1+prolific] are the sealed
	// replies in send-counter order. Decrypt each under initRecv (which
	// advances its own receive counter per Decrypt) and assert the
	// payload indices are 0,1,...,prolific-1 with none missing.
	replies := envs[1 : 1+prolific]
	for i, env := range replies {
		inner := decryptAppFrame(t, env, sess.initRecv)
		if inner.Type != protocol.TypeConversations {
			t.Fatalf("reply %d type = %q, want %q", i, inner.Type, protocol.TypeConversations)
		}
		var got struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(inner.Payload, &got); err != nil {
			t.Fatalf("reply %d decode payload: %v", i, err)
		}
		if got.Index != i {
			t.Fatalf("reply %d index = %d, want %d (reorder or drop through concurrent drain)", i, got.Index, i)
		}
	}
}

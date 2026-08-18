package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
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

// blockingHandler returns a dispatch.Handler that records each invocation's
// request ID on entered (when non-nil), then blocks until it receives on
// release or ctx is cancelled, and finally emits one TypeConversations reply.
// The ctx.Done arms are load-bearing: the manager's per-conn worker (#965)
// runs the handler, and a test's sess.stop / mgr Run-exit cancels runCtx to
// unblock a still-parked handler at cleanup so no worker goroutine leaks. A
// nil release channel blocks forever (until ctx), modelling a permanently
// wedged spawn.
func blockingHandler(entered chan<- uint64, release <-chan struct{}) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		if entered != nil {
			select {
			case entered <- env.ID:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(`{}`))
	}
}

// waitForConnNoiseMsg polls until connID has at least n captured noise_msg
// envelopes or the deadline expires. The per-conn twin of waitForOutboundCount
// used by the multi-conn #965 tests.
func waitForConnNoiseMsg(t *testing.T, rec *v2Recorder, connID string, n int) []protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if msgs := noiseMsgsForConn(t, rec, connID); len(msgs) >= n {
			return msgs
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("conn %q: only got %d noise_msg, want >= %d", connID, len(noiseMsgsForConn(t, rec, connID)), n)
	return nil
}

// waitForCloseCode polls until an outbound envelope addressed to connID
// carries the given WS close code, or the deadline expires.
func waitForCloseCode(t *testing.T, rec *v2Recorder, connID string, code uint16) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, env := range rec.snapshot() {
			if env.ConnID == connID && env.CloseCode == code {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("conn %q: no outbound with close code %d within deadline", connID, code)
}

// TestV2Session_SlowHandler_DoesNotStallOtherConn is AC-1(a): a handler
// blocked on conn A must not stall a frame arriving on a different conn B. B's
// reply is sealed and emitted without waiting for A's handler to be released.
// Against a Run loop that blocks on the handler (pre-#965) B's reply never
// arrives and waitForConnNoiseMsg times out.
func TestV2Session_SlowHandler_DoesNotStallOtherConn(t *testing.T) {
	t.Parallel()

	const (
		connA = "c-v2-A" // its handler blocks
		connB = "c-v2-B" // must be serviced regardless
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	release := make(chan struct{})
	handlers := map[string]dispatch.Handler{
		protocol.TypeSendMessage:       blockingHandler(nil, release),
		protocol.TypeListConversations: prolificHandler(),
	}

	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	})
	t.Cleanup(stop)

	aSend, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, nil)
	bSend, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, nil)

	// Conn A: a frame whose handler blocks. A's worker parks; Run stays free.
	frames <- sealAppFrameConn(t, aSend, connA, protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	// Conn B: a fast frame. Its reply must seal + emit without waiting for A.
	frames <- sealAppFrameConn(t, bSend, connB, protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"count":1}`),
	})

	// B's reply arrives while A is still blocked (A is never released before
	// this assertion). A stalled Run would fail here.
	msgs := waitForConnNoiseMsg(t, rec, connB, 1)
	inner := decryptAppFrame(t, msgs[0], bRecv)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("conn B reply type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != 2 {
		t.Errorf("conn B reply InReplyTo = %v, want 2", inner.InReplyTo)
	}

	// Now release A; its reply arrives too — the offload did not lose it.
	close(release)
	aMsgs := waitForConnNoiseMsg(t, rec, connA, 1)
	aReply := decryptAppFrame(t, aMsgs[0], aRecv)
	if aReply.InReplyTo == nil || *aReply.InReplyTo != 1 {
		t.Errorf("conn A reply InReplyTo = %v, want 1", aReply.InReplyTo)
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

// TestV2Session_SlowHandler_ModalTimeoutStillFires is AC-1(b): with an
// application handler blocked on conn A's worker, the modal deny-on-timeout
// timer must still fire on schedule — Run keeps servicing m.modalTimeout. The
// safe-deny's ResolveTimeout call and the modal_dismissed to A both land while
// A's handler is parked.
func TestV2Session_SlowHandler_ModalTimeoutStillFires(t *testing.T) {
	// Not t.Parallel: mutates the package-level modalDenyTimeout var.
	prev := modalDenyTimeout
	modalDenyTimeout = 20 * time.Millisecond
	t.Cleanup(func() { modalDenyTimeout = prev })

	const (
		connA      = "c-v2-A"
		modalID    = "modal-during-block"
		wantOutT   = "denied_timeout"
		wantSource = "timeout"
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	fake := &fakeModalResolver{
		timeoutOKFor:     modalID,
		timeoutDismissal: ModalDismissal{Outcome: wantOutT, Source: wantSource},
	}

	release := make(chan struct{})
	handlers := map[string]dispatch.Handler{
		protocol.TypeSendMessage: blockingHandler(nil, release),
	}

	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:        frames,
		Outbound:      rec.outbound,
		StaticPriv:    respPriv,
		Devices:       reg,
		ServerID:      v2TestServerID,
		Logger:        silentLogger(),
		Handlers:      handlers,
		ModalResolver: fake,
	})
	t.Cleanup(stop)

	aSend, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})

	// Block conn A's worker on a never-released handler.
	frames <- sealAppFrameConn(t, aSend, connA, protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	// Arm the deny-on-timeout while A's handler is blocked. Run must still
	// service m.modalTimeout — the safe-deny fires on schedule.
	mgr.ArmModalTimeout(context.Background(), modalID)

	// ResolveTimeout fires and the modal_dismissed reaches A, proving the timer
	// was serviced despite A's blocked handler. Wait for the dismissal to be
	// recorded (it is A's only noise_msg — the blocked handler hasn't replied).
	waitForConnNoiseMsg(t, rec, connA, 1)
	assertModalDismissed(t, rec, connA, aRecv, modalID, wantOutT, wantSource)

	close(release)
}

// TestV2Session_SlowHandler_RekeyStillEmits is AC-1's rekey clause: with an
// application handler blocked on conn A's worker, a manual rekey still routes
// through m.manualRekey and emits. Rekey funnels onto Run and blocks on its
// reply; a Run stalled behind the handler would make it hang until the ctx
// deadline. It returns promptly, and the rekey_request lands on the wire.
func TestV2Session_SlowHandler_RekeyStillEmits(t *testing.T) {
	t.Parallel()

	const connA = "c-v2-A"

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	release := make(chan struct{})
	handlers := map[string]dispatch.Handler{
		protocol.TypeSendMessage: blockingHandler(nil, release),
	}

	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	})
	t.Cleanup(stop)

	aSend, _ := openModalConn(t, mgr, frames, rec, respPub, connA, nil)

	// Block conn A's worker.
	frames <- sealAppFrameConn(t, aSend, connA, protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	// A manual rekey must complete while A's handler is blocked. handleManualRekey
	// returns nil only after emitRekeyRequest has sealed and sent the frame.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.Rekey(ctx, connA); err != nil {
		t.Fatalf("Rekey while handler blocked: %v", err)
	}

	// The rekey_request rode the wire (sealed as a noise_msg under s.send) even
	// though A's handler is still parked — its reply has not been emitted.
	waitForConnNoiseMsg(t, rec, connA, 1)

	close(release)
}

// TestV2Session_AppFrame_PerConnSerialization is AC-2: application frames on
// one connection are handled strictly in arrival order — no two handlers for
// the same conn run concurrently — and their sealed replies emit in that order.
// A gated handler proves frame N+1 does not enter until frame N exits, and the
// two replies decrypt (in send-counter order via initRecv) with in_reply_to in
// arrival order.
func TestV2Session_AppFrame_PerConnSerialization(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	entered := make(chan uint64, 4)
	release := make(chan struct{}) // unbuffered: one token releases one handler
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			select {
			case entered <- env.ID:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(`{}`))
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 4)
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

	// Two app frames on the SAME conn, in order.
	appFrameCount(t, sess, 100, 1)
	appFrameCount(t, sess, 101, 1)

	// Frame 100 enters first.
	if got := <-entered; got != 100 {
		t.Fatalf("first handler entry ID = %d, want 100", got)
	}
	// Frame 101 must NOT enter while 100 is still blocked — the worker runs one
	// frame at a time (per-conn serialization). A concurrent dispatch would push
	// 101 onto entered here.
	select {
	case got := <-entered:
		t.Fatalf("second handler entered (ID %d) before the first exited — same-conn handlers ran concurrently", got)
	case <-time.After(100 * time.Millisecond):
	}
	// Release 100; it replies and exits, then the worker dequeues 101.
	release <- struct{}{}
	if got := <-entered; got != 101 {
		t.Fatalf("second handler entry ID = %d, want 101", got)
	}
	release <- struct{}{}

	// Both replies sealed in arrival order. envs[0] is the noise_resp.
	envs := waitForOutboundCount(t, rec, 1+2, 2*time.Second)
	replies := envs[1:]
	first := decryptAppFrame(t, replies[0], sess.initRecv)
	second := decryptAppFrame(t, replies[1], sess.initRecv)
	if first.InReplyTo == nil || *first.InReplyTo != 100 {
		t.Errorf("first reply InReplyTo = %v, want 100", first.InReplyTo)
	}
	if second.InReplyTo == nil || *second.InReplyTo != 101 {
		t.Errorf("second reply InReplyTo = %v, want 101", second.InReplyTo)
	}
}

// TestV2Session_AppFrame_QueueOverflow_ClosesConn proves the inbound-queue
// backpressure bound: a conn that pipelines more than appFrameQueueDepth app
// frames while its worker is parked on a wedged handler is torn down at 4421,
// and a different conn is unaffected. The overflow is self-inflicted per conn
// (frames are AEAD-decrypted under s.recv before reaching the queue).
func TestV2Session_AppFrame_QueueOverflow_ClosesConn(t *testing.T) {
	// Not t.Parallel: shrinks the package-level appFrameQueueDepth var.
	prev := appFrameQueueDepth
	appFrameQueueDepth = 4
	t.Cleanup(func() { appFrameQueueDepth = prev })

	const (
		connA = "c-v2-A" // flooded; must be torn down
		connB = "c-v2-B" // must stay healthy
	)

	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	handlers := map[string]dispatch.Handler{
		protocol.TypeSendMessage:       blockingHandler(nil, nil), // never released
		protocol.TypeListConversations: prolificHandler(),
	}

	frames := make(chan protocol.RoutingEnvelope, 64)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	})
	t.Cleanup(stop)

	aSend, _ := openModalConn(t, mgr, frames, rec, respPub, connA, nil)
	bSend, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, nil)

	// Flood connA with far more than appFrameQueueDepth blocking frames. The
	// first parks the worker; the rest fill s.appFrames until the non-blocking
	// enqueue overflows and closeWith(4421) fires. The exact overflow index is
	// timing-dependent (the worker may hold one in-flight), so send a generous
	// surplus to make overflow certain.
	for i := 0; i < 3*appFrameQueueDepth+2; i++ {
		frames <- sealAppFrameConn(t, aSend, connA, protocol.Envelope{
			ID:      uint64(1000 + i),
			Type:    protocol.TypeSendMessage,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
	}

	// connA is torn down with 4421.
	waitForCloseCode(t, rec, connA, uint16(StatusProtocolMismatch))

	// connB is unaffected: a fresh frame still gets its reply.
	frames <- sealAppFrameConn(t, bSend, connB, protocol.Envelope{
		ID:      7,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"count":1}`),
	})
	msgs := waitForConnNoiseMsg(t, rec, connB, 1)
	inner := decryptAppFrame(t, msgs[0], bRecv)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("conn B reply type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != 7 {
		t.Errorf("conn B reply InReplyTo = %v, want 7", inner.InReplyTo)
	}
}

// --- app-reply transport-down gate tests (#1525) ---

// dropLinesContaining returns every log line in buf holding substr.
func dropLinesContaining(buf *syncLogBuffer, substr string) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return out
}

// TestV2Session_AppReply_TransportDown_BurnsNoNonce is AC1/AC2/AC3. A handler
// reply that returns to Run while the relay leg is down must consume no Noise
// send-nonce: the guard probes transportDown() BEFORE s.send.Encrypt, because
// the post-send Outbound error arrives after the nonce is already spent (#874,
// #912).
//
// The nonce oracle is decryptAppFrame under sess.initRecv, as used by
// TestV2Session_Push_ReconnectWhileDownDoesNotSeal (#874) and the #912
// rekey-deferral test. Against the unguarded function the down-window reply
// seals at nonce 0 and is then dropped by Outbound, the recovery reply seals at
// nonce 1, and initRecv still expects 0 — so the final decrypt MAC-fails. A
// clean decrypt proves zero seals happened while down. Run under -race.
func TestV2Session_AppReply_TransportDown_BurnsNoNonce(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder()
	// attempts counts every Outbound call, up OR down. gatedRecorder.outbound
	// records nothing while down, so "the recorder observed zero envelopes" is
	// also true of a wrong implementation that skips the seal but still hands
	// the raw unsealed reply.Frame to send — asserting on gated.rec alone would
	// make AC2 vacuous. The counter sits outside the up/down branch and is what
	// actually pins the #446 never-emit-unsealed rule.
	var attempts atomic.Int64
	outbound := func(env protocol.RoutingEnvelope) error {
		attempts.Add(1)
		return gated.outbound(env)
	}

	entered := make(chan uint64, 1)
	release := make(chan struct{})
	// Two distinct types so the recovery frame is served by a handler the first
	// frame's release did not already unblock.
	handlers := map[string]dispatch.Handler{
		protocol.TypeSendMessage:       blockingHandler(entered, release),
		protocol.TypeListConversations: prolificHandler(),
	}

	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 4)
	// Handshake with the transport UP so the session reaches open and initRecv
	// is live.
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
		Handlers:   handlers,
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	baseline := attempts.Load()
	if baseline != 1 {
		t.Fatalf("post-handshake Outbound attempts = %d, want 1 (the noise_resp)", baseline)
	}

	// Frame 1 parks its handler on the per-conn worker; Run stays free.
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking handler never entered")
	}

	// The leg drops mid-handler — the ticket's failure scenario in miniature —
	// then the handler returns its reply into Run's m.appReply arm.
	gated.up.Store(false)
	close(release)

	// The drop emits no envelope, so the log line is the only observable edge.
	// AC3.
	waitForLogContains(t, logBuf, "event=v2.app_reply.dropped_transport_down")
	lines := dropLinesContaining(logBuf, "v2.app_reply.dropped_transport_down")
	if len(lines) != 1 {
		t.Fatalf("drop log lines = %d, want exactly 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "conn_id="+v2TestConnID) {
		t.Errorf("drop line missing conn_id=%s: %s", v2TestConnID, lines[0])
	}
	// Content-free: no payload, plaintext, ciphertext, or key bytes, matching
	// the package's no-AEAD-bytes-in-logs discipline.
	for _, forbidden := range []string{"in_reply_to", "payload", "frame=", "ciphertext", "type="} {
		if strings.Contains(lines[0], forbidden) {
			t.Errorf("drop line carries %q: %s", forbidden, lines[0])
		}
	}

	// AC2: nothing was handed to Outbound on the down path.
	if got := attempts.Load(); got != baseline {
		t.Errorf("Outbound attempts = %d, want %d (nothing forwarded on the down path)", got, baseline)
	}

	// AC1: recover, then emit. The recovery reply is the FIRST seal, so it
	// lands at the nonce initRecv still expects.
	gated.up.Store(true)
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"count":1}`),
	})

	msgs := waitForConnNoiseMsg(t, sess.rec, v2TestConnID, 1)
	inner := decryptAppFrame(t, msgs[0], sess.initRecv)
	if inner.InReplyTo == nil || *inner.InReplyTo != 2 {
		t.Errorf("recovery reply InReplyTo = %v, want 2", inner.InReplyTo)
	}
}

// TestV2Session_AppReply_NotOpenPrecedesTransportDown is AC5: the pre-existing
// V2StateOpen drop keeps precedence over the new transport-down check, so a
// reply for a session torn down mid-handler is still dropped on the not-open
// branch with its existing debug log, whether the leg is up or down.
//
// A direct call rather than a driven teardown, deliberately: driving it would
// mean closing the session mid-handler, and forwardToRun's select has an s.done
// arm alongside the m.appReply send — with s.done already closed Go picks among
// ready cases at random, so the reply might never reach forwardAppReply at all.
// The direct call has no concurrency and discriminates exactly which of the two
// checks runs first. Run is never started, so no single-owner invariant is
// touched; the function reads only s.state and s.connID on this branch, so the
// hand-built session's nil s.send is never dereferenced.
func TestV2Session_AppReply_NotOpenPrecedesTransportDown(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	var attempts atomic.Int64
	logger, logBuf := bufferLogger()
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames: make(chan protocol.RoutingEnvelope),
		Outbound: func(protocol.RoutingEnvelope) error {
			attempts.Add(1)
			return nil
		},
		Connected:  func() bool { return false },
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}

	s := &V2Session{connID: v2TestConnID, state: V2StateClosed}
	mgr.forwardAppReply(s, protocol.RoutingEnvelope{
		ConnID: v2TestConnID,
		Frame:  json.RawMessage(`{"reply":true}`),
	})

	out := logBuf.String()
	if !strings.Contains(out, "relay: v2 app reply dropped; session not open") {
		t.Errorf("missing the pre-existing not-open drop line; got:\n%s", out)
	}
	if strings.Contains(out, "v2.app_reply.dropped_transport_down") {
		t.Errorf("transport-down branch ran ahead of the V2StateOpen gate; got:\n%s", out)
	}
	if got := attempts.Load(); got != 0 {
		t.Errorf("Outbound attempts = %d, want 0", got)
	}
}

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- transport-down HOLD tests (#874) ---

// errTransportDown models a (*relay.Connection).Send hitting
// transport.ErrNotConnected while the daemon↔relay leg is down.
var errTransportDown = errors.New("test: relay transport down")

// gatedRecorder wraps a v2Recorder with an atomic transport-up flag so a test
// can flip the relay leg down and back up (#874). When up, outbound records the
// frame and returns nil; when down it records nothing and returns
// errTransportDown — the shape (*relay.Connection).Send exposes when the
// underlying conn is dropped. connected() reads the same flag and wires
// V2SessionConfig.Connected, so the drain's pre-seal probe and the send path
// agree on transport state.
type gatedRecorder struct {
	rec *v2Recorder
	up  atomic.Bool
}

func newGatedRecorder() *gatedRecorder {
	g := &gatedRecorder{rec: &v2Recorder{}}
	g.up.Store(true)
	return g
}

func (g *gatedRecorder) outbound(env protocol.RoutingEnvelope) error {
	if !g.up.Load() {
		return errTransportDown
	}
	return g.rec.outbound(env)
}

func (g *gatedRecorder) connected() bool { return g.up.Load() }

// queueLen returns the depth of connID's push buffer under the leaf lock, or -1
// if no queue exists. Safe to call while Run is active (pushMu guards m.queues).
func queueLen(mgr *V2SessionManager, connID string) int {
	mgr.pushMu.Lock()
	defer mgr.pushMu.Unlock()
	q, ok := mgr.queues[connID]
	if !ok {
		return -1
	}
	return len(q.items)
}

// assertHeldQueued verifies the drain is HOLDING while the transport is down:
// after a settle window (long enough for the Run goroutine to service the
// drainCh wake and hit the hold guard) the head is still queued at wantQueued —
// un-popped and unsealed, so no nonce is burned — and nothing beyond the
// handshake resp reached the recorder. In the pre-fix behaviour the drain would
// have popped and seal-dropped the head, so the queue would read empty here.
func assertHeldQueued(t *testing.T, mgr *V2SessionManager, rec *v2Recorder, connID string, wantQueued int) {
	t.Helper()
	// A negative assertion ("the drain did NOT pop"): give Run ample time to
	// process the wake, then assert the head stayed put. drainCh is signalled
	// synchronously by Push, so a held drain settles well within this window.
	time.Sleep(100 * time.Millisecond)
	if got := queueLen(mgr, connID); got != wantQueued {
		t.Fatalf("queue depth = %d, want %d (head held un-popped while transport down)", got, wantQueued)
	}
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("recorded %d envelopes while down, want 1 (only the handshake resp; drain sealed nothing)", len(got))
	}
}

// assertQueueDrains polls until connID's push buffer empties — the transport-up
// path popping and forwarding the head (a positive assertion, so poll-until is
// deterministic and fast).
func assertQueueDrains(t *testing.T, mgr *V2SessionManager, connID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if queueLen(mgr, connID) == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("queue for %s did not drain to empty (head not popped on transport-up path)", connID)
}

// TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous pins AC1/AC3/AC4:
// a control envelope pushed while the relay transport is down is held (not
// sealed, not lost); once the transport recovers it is delivered exactly once,
// in FIFO order, and the Noise send-nonce sequence stays contiguous across the
// down→up window (both surviving frames decrypt under the phone's recv state — a
// burned/gapped nonce would MAC-fail here). Run under -race.
func TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// The handshake runs with the transport UP so the session reaches open and
	// the noise_resp is emitted.
	gated := newGatedRecorder()
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Flip the transport DOWN, then push a control envelope. The drain must hold
	// it: un-popped, unsealed, no nonce burned.
	gated.up.Store(false)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "held-while-down")); err != nil {
		t.Fatalf("Push while down: %v", err)
	}
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 1)

	// Recover the transport, then push a second control envelope. This
	// re-signals drainCh (the lazy flush): the held head drains first, then the
	// new one, both in FIFO order.
	gated.up.Store(true)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 2, "after-recover")); err != nil {
		t.Fatalf("Push after recover: %v", err)
	}

	// Exactly two new frames beyond the handshake resp — no duplicate, no loss.
	envs := waitForEnvelopes(t, gated.rec, 3)
	if len(envs) != 3 {
		t.Fatalf("recorded %d envelopes, want 3 (noise_resp + 2 pushes; no duplicate, no loss)", len(envs))
	}
	first := decryptAppFrame(t, envs[1], sess.initRecv)
	second := decryptAppFrame(t, envs[2], sess.initRecv)
	if first.ID != 1 || second.ID != 2 {
		t.Errorf("delivered order = [%d %d], want [1 2] (FIFO: held head before post-recover push)", first.ID, second.ID)
	}
}

// TestV2Session_Push_HoldGatedOnProbeNotSendError pins AC2: the drain HOLDS iff
// the pre-seal Connected() probe reports down. A send that fails while the probe
// still reports UP is NOT held — the head is popped and forwarded, and its
// transport error dropped (the pre-#874 drop-on-send posture). Hold is thus
// distinguished from session-level failure by control-flow position (the probe
// gates the pop, upstream of the seal), not by inspecting the send error.
func TestV2Session_Push_HoldGatedOnProbeNotSendError(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// probeUp drives the pre-seal Connected() gate; sendFail drives whether
	// Outbound errors. They are independent so the test can set probe=up +
	// send=fail to prove a send failure alone never holds.
	var probeUp, sendFail atomic.Bool
	probeUp.Store(true)
	// sends counts completed send DECISIONS, incremented after the sendFail read
	// on both branches. drainOnce commits the pop under pushMu and releases the
	// lock before forwardEnvelope seals and reaches this closure, so an assertion
	// synchronised on the queue emptying returns inside that gap. Observing
	// sends >= N is program-ordered after envelope N's sendFail read, so it is a
	// real happens-before — no duration to tune.
	var sends atomic.Int64
	rec := &v2Recorder{}
	outbound := func(env protocol.RoutingEnvelope) error {
		if sendFail.Load() {
			sends.Add(1)
			return errTransportDown
		}
		err := rec.outbound(env)
		sends.Add(1)
		return err
	}
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   outbound,
		Connected:  probeUp.Load,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Sub-case (b): probe UP but the send fails. The drain must NOT hold — it
	// pops and forwards; the transport error is dropped. The head is consumed.
	sendFail.Store(true)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "up-send-fails")); err != nil {
		t.Fatalf("Push (probe up, send fails): %v", err)
	}
	// Wait for THIS push's send decision (the handshake resp is decision #1)
	// before asserting anything or touching either flag. Synchronising on the
	// pop alone leaves the already-popped envelope in flight to outbound: the
	// sendFail flip below would then land before its sendFail read, the success
	// branch would record a second envelope, and sub-case (a)'s assertHeldQueued
	// would fail with "recorded 2 envelopes while down, want 1". It also makes
	// the recorder check that follows non-vacuous — today it can pass simply
	// because the send has not happened yet.
	deadline := time.Now().Add(2 * time.Second)
	for sends.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("send decisions = %d, want >= 2 (handshake resp + the probe-up push)", sends.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}
	assertQueueDrains(t, sess.mgr, v2TestConnID)
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("recorded %d, want 1 (send failed, nothing delivered; head still consumed not held)", len(got))
	}

	// Sub-case (a): probe DOWN. Now the drain holds — head un-popped, unsealed.
	// sendFail is irrelevant while held (the seal/send never runs).
	probeUp.Store(false)
	sendFail.Store(false)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 2, "probe-down-holds")); err != nil {
		t.Fatalf("Push (probe down): %v", err)
	}
	assertHeldQueued(t, sess.mgr, rec, v2TestConnID, 1)
}

// TestV2Session_Push_FlushesOnReconnectSignal pins AC3 (#875): a control
// envelope enqueued while the relay transport is down is delivered after the
// reconnect signal fires with NO intervening Push; multiple held envelopes
// preserve FIFO order and stay on a contiguous Noise send-nonce (both decrypt
// under the phone's recv state — a burned/gapped nonce MAC-fails). Mirrors
// TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous but drives the
// flush from the reconnect edge instead of a follow-up Push. Run under -race —
// exercises the reconnect → Run → drainCh → drainOnce cross-goroutine path.
func TestV2Session_Push_FlushesOnReconnectSignal(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder()
	reconnect := make(chan struct{}, 1)
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		Reconnect:  reconnect,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Transport DOWN: push two control envelopes. Both held — un-popped,
	// unsealed, nothing delivered beyond the handshake resp.
	gated.up.Store(false)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "held-1")); err != nil {
		t.Fatalf("Push #1 while down: %v", err)
	}
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 2, "held-2")); err != nil {
		t.Fatalf("Push #2 while down: %v", err)
	}
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 2)

	// Recover the transport and fire the reconnect edge — NO further Push. The
	// arm re-signals drainCh; the held head drains FIFO, self-perpetuating via
	// drainOnce's more→drainCh re-signal until the queue empties.
	gated.up.Store(true)
	reconnect <- struct{}{}

	envs := waitForEnvelopes(t, gated.rec, 3)
	if len(envs) != 3 {
		t.Fatalf("recorded %d envelopes, want 3 (noise_resp + 2 held; flushed by reconnect alone)", len(envs))
	}
	first := decryptAppFrame(t, envs[1], sess.initRecv)
	second := decryptAppFrame(t, envs[2], sess.initRecv)
	if first.ID != 1 || second.ID != 2 {
		t.Errorf("delivered order = [%d %d], want [1 2] (FIFO held flush on reconnect)", first.ID, second.ID)
	}
}

// TestV2Session_Push_NilReconnectInert pins AC1 (#875): with a nil Reconnect
// seam Run gains no new wake source — a held envelope stays held until a
// subsequent Push re-signals the drain (pre-#875 behaviour). Confirms "when
// nil, behaves exactly as before."
func TestV2Session_Push_NilReconnectInert(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder()
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		Reconnect:  nil, // explicit: no reconnect wake source
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Transport DOWN: push one control envelope → held.
	gated.up.Store(false)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "held-nil-reconnect")); err != nil {
		t.Fatalf("Push while down: %v", err)
	}
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 1)

	// Recover the transport. With no reconnect seam nothing wakes the drain — the
	// head is still held (there is no reconnect edge to re-signal drainCh).
	gated.up.Store(true)
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 1)

	// A subsequent Push re-signals the drain: both flush FIFO (pre-#875 path).
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 2, "after-recover")); err != nil {
		t.Fatalf("Push after recover: %v", err)
	}
	envs := waitForEnvelopes(t, gated.rec, 3)
	if len(envs) != 3 {
		t.Fatalf("recorded %d envelopes, want 3 (flush driven by Push, not reconnect)", len(envs))
	}
	first := decryptAppFrame(t, envs[1], sess.initRecv)
	second := decryptAppFrame(t, envs[2], sess.initRecv)
	if first.ID != 1 || second.ID != 2 {
		t.Errorf("delivered order = [%d %d], want [1 2]", first.ID, second.ID)
	}
}

// TestV2Session_Push_ReconnectWhileDownDoesNotSeal guards the "seals nothing
// itself" contract (#875): the reconnect arm only WAKES the drain; drainOnce
// still consults Connected before the pop. Firing the reconnect edge while the
// transport is still down leaves the head held — no nonce burned into a dead
// link.
func TestV2Session_Push_ReconnectWhileDownDoesNotSeal(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder()
	reconnect := make(chan struct{}, 1)
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		Reconnect:  reconnect,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Transport DOWN: push one control envelope → held.
	gated.up.Store(false)
	if err := sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "still-down")); err != nil {
		t.Fatalf("Push while down: %v", err)
	}
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 1)

	// Fire the reconnect edge while STILL down. The arm wakes the drain, but
	// drainOnce's transportDown() gate holds the pop — nothing sealed, nothing
	// delivered, no nonce burned.
	reconnect <- struct{}{}
	assertHeldQueued(t, sess.mgr, gated.rec, v2TestConnID, 1)
}

// --- rekey-emit transport-down gate tests (#912) ---

// TestV2Session_RekeyScheduled_DeferredWhileTransportDown_EmitsOnRecovery pins
// AC1/AC2 and the AC5 scheduled-up regression: while the relay transport is
// down the scheduled rekey wake seals nothing (no send-nonce burned) and
// re-arms a short retry; once the transport recovers the retry fires and the
// rekey_request is emitted normally. The nonce oracle is decryptAppFrame under
// sess.initRecv — a burned nonce during the down window would gap the sequence
// and MAC-fail here, so a clean decrypt after recovery proves zero seals
// happened while down. Run under -race.
func TestV2Session_RekeyScheduled_DeferredWhileTransportDown_EmitsOnRecovery(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval / rekeyRetryInterval /
	// rekeyReplyTimeout vars read by other tests' dispatch goroutines.
	prevInterval := rekeyInterval
	rekeyInterval = 60 * time.Millisecond
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevRetry := rekeyRetryInterval
	rekeyRetryInterval = 40 * time.Millisecond
	t.Cleanup(func() { rekeyRetryInterval = prevRetry })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 2 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// Handshake runs with the transport UP so the session reaches open and the
	// scheduled rekeyTimer is armed.
	gated := newGatedRecorder()
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	// Flip the transport DOWN before the first scheduled fire, then let several
	// scheduled + retry cycles elapse. Each must defer (seal nothing, arm no
	// reply window) — so the session must NOT be torn down.
	gated.up.Store(false)
	time.Sleep(250 * time.Millisecond)

	// Recover the transport. The pending retry timer fires and emits the
	// deferred rekey_request normally.
	gated.up.Store(true)
	envs := waitForEnvelopes(t, gated.rec, 2)
	if len(envs) != 2 {
		t.Fatalf("recorded %d envelopes, want 2 (noise_resp + one emit on recovery; no blind emit while down)", len(envs))
	}
	emit := envs[1]
	if emit.CloseCode != 0 {
		t.Errorf("recovery emit CloseCode = %d, want 0", emit.CloseCode)
	}
	// Nonce oracle: a clean decrypt under initRecv (nonce 0) proves s.send's
	// nonce never advanced during the down window.
	inner := decryptAppFrame(t, emit, sess.initRecv)
	if inner.Type != protocol.TypeRekeyRequest {
		t.Errorf("recovery emit inner type = %q, want %q", inner.Type, protocol.TypeRekeyRequest)
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode recovery emit payload: %v", err)
	}
	if payload.Reason != "scheduled" {
		t.Errorf("recovery emit reason = %q, want %q", payload.Reason, "scheduled")
	}

	// After stop: the session survived the down window (still open) and the
	// recovery emit set awaitingRekeyReply.
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing — it was torn down while transport was down", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after recovery = %v, want V2StateOpen", got)
	}
	if !s.awaitingRekeyReply {
		t.Errorf("awaitingRekeyReply = false after recovery emit, want true")
	}
}

// TestV2Session_RekeyScheduled_TransportDown_NoRekeyFailed_NoClose pins AC3:
// a transport-down rekey boundary produces no noise.rekey_failed warning and
// does not close the session with StatusHandshakeFailure, even after more than
// rekeyReplyTimeout has elapsed. It directly refutes the status-quo tear-down
// (a blind emit would arm the reply window and, on its expiry, close 4426).
func TestV2Session_RekeyScheduled_TransportDown_NoRekeyFailed_NoClose(t *testing.T) {
	// Not t.Parallel: mutates package-level rekey vars.
	prevInterval := rekeyInterval
	rekeyInterval = 30 * time.Millisecond
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevRetry := rekeyRetryInterval
	rekeyRetryInterval = 30 * time.Millisecond
	t.Cleanup(func() { rekeyRetryInterval = prevRetry })
	// Short reply window: with the status-quo bug, the down-boundary blind emit
	// would arm this and fire a 4426 close well within the settle sleep below.
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 40 * time.Millisecond
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
	gated := newGatedRecorder()
	// Drop the relay leg the instant the handshake resp is on the wire.
	// handleNoiseInit runs m.send(resp) → s.state = V2StateOpen → armRekeyTimer
	// sequentially on the Run goroutine and m.send never consults
	// transportDown(), so this store is program-ordered before the scheduled
	// rekey timer is even armed: the FIRST scheduled boundary is guaranteed to
	// find the transport down. Dropping it after driveToOpen returns instead
	// leaves a window of real time in which that first boundary emits for real,
	// arms the reply window and tears the session down at 4426 — the exact
	// outcome this test refutes, so under load the test manufactured its own
	// failure. The store is idempotent; no one-shot guard is needed. The
	// gatedRecorder lockstep is preserved — after the flip, outbound errors and
	// connected() reads false together, so "the recorder saw exactly one
	// envelope" still means "exactly one frame was sealed under s.send".
	outbound := func(env protocol.RoutingEnvelope) error {
		err := gated.outbound(env)
		gated.up.Store(false)
		return err
	}
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	// Positive assertion: the defer path must have actually run (proves this is
	// the fix, not just an unarmed timer). Wait until it is observed rather than
	// sleeping a fixed span, so a starved scheduler delays this test instead of
	// failing it.
	waitForLogContains(t, logBuf, "event=v2.rekey.emit.deferred_transport_down")

	// The negative below is a real settle window, but anchored to the CONFIRMED
	// deferral above rather than to test start, so starvation lengthens it
	// instead of consuming it. 5× rekeyReplyTimeout is ample for a blind emit's
	// reply window to expire and produce the noise.rekey_failed + 4426 teardown.
	time.Sleep(5 * rekeyReplyTimeout)

	out := logBuf.String()
	if strings.Contains(out, "noise.rekey_failed") {
		t.Errorf("log contains noise.rekey_failed — a transport blip was mislabelled a rekey failure; got:\n%s", out)
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing — it was closed while transport was down (want it kept open)", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state = %v, want V2StateOpen (no StatusHandshakeFailure teardown)", got)
	}
}

// TestV2Session_RekeyManual_TransportDown_ReturnsErrTransportDown pins AC4: a
// manual rekey while the transport is down returns a distinct ErrTransportDown
// (not collapsed into ErrSessionNotOpen), burns no nonce, arms no reply window,
// and leaves the scheduled 1-hour timer untouched.
func TestV2Session_RekeyManual_TransportDown_ReturnsErrTransportDown(t *testing.T) {
	// Not t.Parallel: mutates package-level rekey vars. Long intervals so the
	// scheduled timer cannot fire during the test window.
	prevInterval := rekeyInterval
	rekeyInterval = 10 * time.Second
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 10 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder()
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	gated.up.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := sess.mgr.Rekey(ctx, v2TestConnID)
	if !errors.Is(err, ErrTransportDown) {
		t.Fatalf("Rekey while down: err = %v, want ErrTransportDown", err)
	}
	if errors.Is(err, ErrSessionNotOpen) {
		t.Errorf("ErrTransportDown must be distinct from ErrSessionNotOpen; err = %v", err)
	}

	// After stop: no emit side-effect (awaitingRekeyReply stays false, no reply
	// timer armed) and the scheduled cadence is preserved (rekeyTimer untouched).
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after a deferred manual rekey", v2TestConnID)
	}
	if s.awaitingRekeyReply {
		t.Errorf("awaitingRekeyReply = true, want false (no emit while down)")
	}
	if s.rekeyReplyTimer != nil {
		t.Errorf("rekeyReplyTimer armed, want nil (no reply window on a deferred manual rekey)")
	}
	if s.rekeyTimer == nil {
		t.Errorf("rekeyTimer = nil, want the scheduled cadence preserved (it must not be stopped on the down path)")
	}
}

// TestV2Session_RekeyManual_TransportUp_GateInert pins AC5 for the manual path
// with the Connected seam explicitly wired but UP: the gate is inert and the
// manual rekey emits exactly as before — rekey_request with reason "manual",
// awaitingRekeyReply set, reply timer armed.
func TestV2Session_RekeyManual_TransportUp_GateInert(t *testing.T) {
	// Not t.Parallel: mutates package-level rekey vars. Long intervals so only
	// the manual emit produces a second envelope.
	prevInterval := rekeyInterval
	rekeyInterval = 10 * time.Second
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 10 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	gated := newGatedRecorder() // up by default
	frames := make(chan protocol.RoutingEnvelope, 2)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated.outbound,
		Connected:  gated.connected,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, gated.rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.Rekey(ctx, v2TestConnID); err != nil {
		t.Fatalf("Rekey while up: %v", err)
	}

	envs := waitForEnvelopes(t, gated.rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs after manual rekey: got %d, want exactly 2 (noise_resp + manual emit)", len(envs))
	}
	inner := decryptAppFrame(t, envs[1], sess.initRecv)
	if inner.Type != protocol.TypeRekeyRequest {
		t.Errorf("manual emit inner type = %q, want %q", inner.Type, protocol.TypeRekeyRequest)
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode manual emit payload: %v", err)
	}
	if payload.Reason != "manual" {
		t.Errorf("manual emit reason = %q, want %q", payload.Reason, "manual")
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after manual rekey", v2TestConnID)
	}
	if !s.awaitingRekeyReply {
		t.Errorf("awaitingRekeyReply = false, want true (gate inert on transport-up path)")
	}
	if s.rekeyReplyTimer == nil {
		t.Errorf("rekeyReplyTimer = nil, want armed (gate inert on transport-up path)")
	}
}

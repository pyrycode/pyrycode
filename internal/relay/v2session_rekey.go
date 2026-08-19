package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the Noise re-key lifecycle — the periodic
// timer-driven re-key plus the operator-triggered manual re-key —
// carved out of v2session.go (#1022). Pure move: same package, no
// behaviour change. The V2Session / V2SessionManager structs, the Run
// loop, handleWake, and the shared sentinels stay in v2session.go; #1021
// carved out the initiator handshake before this slice.

// rekeyInterval is the scheduled re-key cadence on each open v2 session
// (docs/protocol-mobile.md § Re-key — the 1-hour rule). Exposed as a
// package var (lowercase) so tests can substitute a sub-second value
// via a t.Cleanup save-and-restore idiom; not part of the public API.
var rekeyInterval = 1 * time.Hour

// rekeyReplyTimeout is the bounded window between emitting a
// rekey_request and observing the phone's fresh noise_init. On expiry
// the conn is closed at StatusHandshakeFailure (4426) and a
// noise.rekey_failed log line is emitted. Exposed as a package var
// (lowercase) so tests can substitute a sub-second value; not part of
// the public API.
var rekeyReplyTimeout = 30 * time.Second

// rekeyRetryInterval is the short, bounded re-arm cadence used when a
// scheduled rekey wake fires while transportDown() reports the relay leg
// down (#912). Much shorter than the 1-hour rekeyInterval so a transient
// relay blip at the rekey boundary is retried soon after the transport
// recovers; comfortably longer than the relay reconnect backoff ceiling
// (~30s) so a single retry usually lands on a recovered transport; well
// under idleTimeout (15m) so a genuinely-gone phone is reaped by the idle
// sweep rather than by an endless retry spin. Exposed as a package var
// (lowercase) so tests can substitute a sub-second value; not part of the
// public API.
var rekeyRetryInterval = 1 * time.Minute

// manualRekeyReq is enqueued by (*V2SessionManager).Rekey and dequeued
// by Run on the manual-rekey channel arm. The reply channel is
// per-request (cap=1) so the manager's reply send is non-blocking even
// if the caller's ctx fires between enqueue and reply.
type manualRekeyReq struct {
	connID string
	reply  chan error
}

// handleRekeyInit runs the responder side of a phone-initiated re-key
// handshake. Same shape as the initial handshake (NewResponder →
// ReadInit → WriteResp → emit noise_resp) with three differences:
//
//  1. Early-data is empty on both directions (docs/protocol-mobile.md
//     § Re-key). Hello validation and token re-check are skipped — they
//     ran at initial handshake. The per-rekey identity gate is the
//     peer-static continuity check below, not the token.
//  2. Peer-static continuity is enforced: the new initiator's static
//     pub (from resp.PeerStatic() after ReadInit) MUST equal
//     s.peerStatic captured at initial handshake. Mismatch closes at
//     4426, the same code the initial handshake uses for IK-related
//     failure.
//  3. CipherState swap on success: a SINGLE tuple assignment
//     `s.send, s.recv = newSend, newRecv` on the manager's single
//     dispatch goroutine. No half-mixed state where one direction uses
//     new keys and the other uses old. State stays V2StateOpen;
//     s.device and s.peerStatic are preserved. The old *CipherState
//     pointers are dropped from the struct; Go's GC reclaims the
//     underlying memory. An explicit Wipe() of the key bytes is NOT
//     exposed (would require touching internal/noise's surface —
//     deferred); the single-owner-goroutine invariant means no code
//     path observes the old state after the swap.
//
// Failure paths reuse the initial handshake's close code (4426) and
// closeWith primitive — no new close code is introduced. closeWith
// removes the session entry, so the next inbound frame on the same
// conn_id lazy-creates a fresh V2StateAwaitingInit session.
func (m *V2SessionManager) handleRekeyInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	resp, err := noise.NewResponder(m.cfg.StaticPriv)
	if err != nil {
		// Realistically unreachable: StaticPriv length is validated at
		// NewV2SessionManager. Close at 4426 anyway — without a Responder
		// we cannot proceed.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_responder_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Re-key noise_init carries empty early-data per spec; discard the
	// returned slice unconditionally.
	if _, err := resp.ReadInit(inner.Data); err != nil {
		// MAC failure, malformed IK message 1, or wrong responder static.
		// No re-key CipherStates exist; close-only at 4426.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_read_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Peer-static continuity check: pins the post-rekey AEAD channel to
	// the same peer identity as the initial handshake (Threat #3
	// residual-risk claim). bytes.Equal is intentional and variable-time
	// acceptable — both operands are public keys (live peer's static
	// from resp.PeerStatic() and the stored s.peerStatic), so timing
	// leakage carries no secret. device_name is intentionally omitted
	// from the reject log line: the re-key initiator's identity is
	// unknown / hostile, and logging the captured-at-initial-handshake
	// device-name on a rejected re-key would be an anti-enumeration
	// signal.
	if !bytes.Equal(resp.PeerStatic(), s.peerStatic) {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_peer_static_mismatch")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Re-key noise_resp carries empty early-data per spec.
	respMsg, newSend, newRecv, err := resp.WriteResp(nil)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_write_resp_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "rekey_marshal_noise_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Atomic swap on the single dispatch goroutine. The old
	// *CipherState pointers are dropped from the struct here; the GC
	// reclaims the underlying memory once no further reference exists.
	// State stays V2StateOpen; s.device and s.peerStatic are preserved.
	s.send, s.recv = newSend, newRecv

	m.cfg.Logger.Info("relay: v2 rekey accept",
		"event", "v2.rekey.accept",
		"conn_id", s.connID,
		"device_name", s.device.Name)
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
	s.rekeyComplete(m, ctx)
}

// rekeyComplete is the seam that bridges responder-side swap completion
// back to initiator-side cadence. Called from the success tail of
// (*V2SessionManager).handleRekeyInit after the atomic s.send / s.recv
// swap and the v2.rekey.accept log emission.
//
// Behaviour:
//   - Clears awaitingRekeyReply (no-op if not set — a spontaneous
//     phone-initiated re-key that the binary did not request still
//     re-bases the 1-hour cadence; any successful swap is a fresh-key
//     moment).
//   - Stops and nils rekeyReplyTimer (no-op if nil).
//   - Stops and replaces rekeyTimer with a fresh one armed
//     rekeyInterval from now.
//
// Runs on the manager's single dispatch goroutine (the same goroutine
// that owns s.send / s.recv after the swap). Inherits the no-mutex /
// no-atomic invariant from the rest of the package.
//
// The (m, ctx) argument shape carries the dependencies needed to
// re-arm the 1-hour timer — m for the wake channel, ctx (Run-derived
// runCtx) for the AfterFunc callback's escape arm.
func (s *V2Session) rekeyComplete(m *V2SessionManager, ctx context.Context) {
	s.awaitingRekeyReply = false
	if s.rekeyReplyTimer != nil {
		s.rekeyReplyTimer.Stop()
		s.rekeyReplyTimer = nil
	}
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
	}
	s.rekeyTimer = m.armRekeyTimer(ctx, s)
}

// handleRekeyRequest classifies an inbound v2 rekey_request control
// envelope and emits a single structured log line. The binary is always
// the IK responder (ADR 024); a rekey_request from the phone takes NO
// transport action — no close, no outbound frame, no state change. The
// phone re-keys by sending noise_init directly, not by signalling via
// rekey_request.
//
// payload.reason is matched against the closed set {scheduled, manual,
// compromise} from docs/protocol-mobile.md § Re-key:
//   - recognised values log at INFO,
//   - empty, unknown, or non-string values log at WARN (forward-compat:
//     mobile may add a reason value before the binary catches up).
//
// ctx is accepted for parity with dispatchAppFrame / handleNoiseMsg but
// is unused: the method does no work that needs cancellation. Runs on
// the manager's single dispatch goroutine (same as the rest of this
// file); no new concurrency invariant.
func (m *V2SessionManager) handleRekeyRequest(_ context.Context, s *V2Session, env protocol.Envelope) {
	var payload struct {
		Reason string `json:"reason"`
	}
	// Decode failures are tolerated and treated as empty reason; emitting
	// a sealed protocol.malformed reply for a broken control payload
	// would be a surprising behaviour change since the envelope itself
	// took no transport action either way.
	_ = json.Unmarshal(env.Payload, &payload)

	switch payload.Reason {
	case "scheduled", "manual", "compromise":
		m.cfg.Logger.Info("relay: v2 rekey request received",
			"event", "v2.rekey.request.received",
			"conn_id", s.connID,
			"reason", payload.Reason)
	default:
		m.cfg.Logger.Warn("relay: v2 rekey request received",
			"event", "v2.rekey.request.received",
			"conn_id", s.connID,
			"reason", payload.Reason)
	}
}

// emitRekeyRequest builds an AEAD-sealed rekey_request envelope under
// s.send, wraps it as a noise_msg inner frame, forwards via m.send,
// then sets s.awaitingRekeyReply=true and arms s.rekeyReplyTimer.
// Called on the manager's single dispatch goroutine from two sites:
// handleWake's wakeRekeyEmit arm passes reason="scheduled" (timer-
// driven), and handleManualRekey passes reason="manual" (operator-
// triggered via the control socket — docs/protocol-mobile.md § Re-key).
// The "compromise" reason is reserved for a future caller.
//
// PRECONDITION (#912): callers MUST verify transportDown() is false before
// calling. Sealing a rekey_request while the relay leg is down burns a Noise
// send-nonce on a frame m.send silently drops and arms a doomed reply window;
// both current callers (handleWake's wakeRekeyEmit arm and handleManualRekey)
// gate on transportDown() first, and a future "compromise" caller must too.
// The check is not enforced inside here: the two callers diverge after it
// (scheduled re-arms a retry timer, manual returns ErrTransportDown), which a
// guard in this primitive could not signal back.
//
// Envelope ID is fixed at 1: there is no rekey_ack response that would
// correlate by InReplyTo (the spec is explicit — the next successful
// AEAD round-trip under the new keys is the implicit ack).
//
// AEAD-seal failure is realistically unreachable under correct
// flynn/noise (same posture as sealError): the only trigger
// s.send.Encrypt can reach from here is ErrMaxNonce — send-nonce
// exhaustion — and the three marshal branches encode closed structs.
// On seal or marshal failure the frame is dropped and a WARN line
// emitted; the conn is NOT closed, and the session remains in
// V2StateOpen. But none of the four failure branches arms anything, so
// the scheduled cadence ends there:
//
//   - Scheduled: no further scheduled emit is armed. s.rekeyTimer still
//     holds the one-shot that delivered this wake — non-nil, but fired
//     and inert, so a re-arm guarded on s.rekeyTimer == nil would never
//     fire on this path.
//   - Manual: handleManualRekey stopped and nil'd s.rekeyTimer before
//     calling in, so a failed manual emit likewise leaves no cadence.
//     Re-running pyry rekey is the operator's recovery step, not a
//     fallback the cadence provides.
//
// Recovery either way is a phone-initiated re-key (handleRekeyInit →
// rekeyComplete), which re-arms the 1-hour cadence
// (docs/protocol-mobile.md § Re-key). The idle sweep is untouched, but
// idleTimer re-arms on every inbound frame: it reaps a session that
// goes quiet, not the key lifetime of one that stays busy. The
// awaitingRekeyReply skip below is not one of these paths — it returns
// with a rekeyReplyTimer in flight, which either closes the conn or is
// cleared by rekeyComplete.
func (m *V2SessionManager) emitRekeyRequest(ctx context.Context, s *V2Session, reason string) {
	// Defensive: a wakeRekeyEmit arriving while already awaiting a
	// reply would re-emit. Skip — the in-flight emit's reply window is
	// still ticking. (Should not happen under normal operation;
	// rekeyTimer is one-shot and only re-armed by rekeyComplete, which
	// clears the bool first.)
	if s.awaitingRekeyReply {
		m.cfg.Logger.Warn("relay: v2 rekey emit skipped",
			"event", "v2.rekey.emit.skipped_already_awaiting",
			"conn_id", s.connID)
		return
	}

	reqPayload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	envelope := protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeRekeyRequest,
		TS:      time.Now().UTC(),
		Payload: reqPayload,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit seal failed",
			"event", "v2.rekey.emit.seal_failed",
			"conn_id", s.connID)
		return
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 rekey emit marshal failed",
			"event", "v2.rekey.emit.marshal_failed",
			"conn_id", s.connID)
		return
	}
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
	s.awaitingRekeyReply = true
	s.rekeyReplyTimer = m.armRekeyReplyTimer(ctx, s)
	m.cfg.Logger.Info("relay: v2 rekey emit",
		"event", "v2.rekey.emit",
		"conn_id", s.connID,
		"reason", reason)
}

// armRekeyTimer arms the 1-hour scheduled re-key timer. The callback
// runs on a fresh runtime goroutine (time.AfterFunc semantics); it
// pushes a wakeRekeyEmit signal onto m.wake under blocking-send +
// ctx.Done semantics. ctx is the manager's Run-derived runCtx;
// cancelled on Run exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armRekeyTimer(ctx context.Context, s *V2Session) *time.Timer {
	interval := rekeyInterval
	if m.cfg.RekeyInterval > 0 {
		interval = m.cfg.RekeyInterval
	}
	return time.AfterFunc(interval, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyEmit}:
		case <-ctx.Done():
		}
	})
}

// armRekeyReplyTimer arms the 30s reply-window timer. Same shape as
// armRekeyTimer but with the wakeRekeyReplyTimeout kind and the
// shorter cadence.
func (m *V2SessionManager) armRekeyReplyTimer(ctx context.Context, s *V2Session) *time.Timer {
	timeout := rekeyReplyTimeout
	if m.cfg.RekeyReplyTimeout > 0 {
		timeout = m.cfg.RekeyReplyTimeout
	}
	return time.AfterFunc(timeout, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyReplyTimeout}:
		case <-ctx.Done():
		}
	})
}

// armRekeyRetryTimer arms the short bounded re-arm used when a scheduled
// rekey wake fires while the relay transport is down (#912). Same callback
// shape as armRekeyTimer — it pushes a wakeRekeyEmit signal onto m.wake
// under blocking-send + ctx.Done semantics — only the cadence differs
// (rekeyRetryInterval, not rekeyInterval). Re-entering the same
// wakeRekeyEmit arm makes the deferral a self-healing loop: each fire
// re-checks transportDown() and either emits (recovered) or re-arms (still
// down). A dedicated helper rather than a duration parameter on
// armRekeyTimer keeps that function's two happy-path callers untouched.
func (m *V2SessionManager) armRekeyRetryTimer(ctx context.Context, s *V2Session) *time.Timer {
	interval := rekeyRetryInterval
	if m.cfg.RekeyRetryInterval > 0 {
		interval = m.cfg.RekeyRetryInterval
	}
	return time.AfterFunc(interval, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyEmit}:
		case <-ctx.Done():
		}
	})
}

// Rekey satisfies control.Rekeyer. It funnels the request onto Run's
// dispatch goroutine via m.manualRekey so the lookup + emit sequence
// runs under the single-owner-goroutine invariant on s.send / s.state /
// s.rekeyTimer — no new lock or atomic is introduced.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound, so the
// dispatcher's errors.Is check maps to ErrCodeConnNotFound on the
// wire), ErrSessionNotOpen for sessions not in V2StateOpen or already
// awaiting a rekey reply, ErrTransportDown when the session is eligible but
// the relay transport is currently down (#912), ctx.Err() on caller
// cancellation, or any transport-layer error surfaced by the emit path (no
// such error is returned today — seal failures are logged and dropped per
// emitRekeyRequest's documented posture, which leaves a nil return alongside
// a session with no scheduled cadence until a phone-initiated re-key).
//
// Production wire-up of *V2SessionManager into the cmd/pyry daemon
// lands in a separate ticket; until then this method is reachable
// only from internal/relay tests.
var _ control.Rekeyer = (*V2SessionManager)(nil)

func (m *V2SessionManager) Rekey(ctx context.Context, connID string) error {
	req := manualRekeyReq{connID: connID, reply: make(chan error, 1)}
	select {
	case m.manualRekey <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleManualRekey runs on Run's dispatch goroutine. It validates the
// session is eligible for a manual rekey, stops the scheduled 1-hour
// timer so a manual emit at T+45min does not also trigger a scheduled
// emit at T+60min (rekeyComplete re-arms a fresh timer on the responder
// reply), and reuses the existing emitRekeyRequest machinery with
// reason="manual".
//
// Sessions awaiting a prior rekey reply collapse into ErrSessionNotOpen
// — the same sentinel as "not in V2StateOpen" — because from the
// operator's perspective both states mean "the conn is not ready for
// a fresh manual rekey." This also imposes a natural per-conn rate
// limit of one manual rekey per rekeyReplyTimeout (30s in production).
func (m *V2SessionManager) handleManualRekey(ctx context.Context, connID string) error {
	s, ok := m.sessions[connID]
	if !ok {
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	if s.awaitingRekeyReply {
		return ErrSessionNotOpen
	}
	// #912: gate the manual emit on the relay transport, AFTER the
	// eligibility checks (so a missing or ineligible conn still gets its
	// precise error) but BEFORE the scheduled-timer Stop below. A
	// rekey_request sealed while the transport is down burns a Noise
	// send-nonce on a frame that cannot reach the phone. Return a distinct
	// ErrTransportDown so the operator sees "retry later", and leave the
	// scheduled 1-hour timer untouched so the session keeps its cadence.
	if m.transportDown() {
		return ErrTransportDown
	}
	// Stop the scheduled 1-hour timer before emitting; rekeyComplete
	// arms a fresh one on the responder reply. Stop()'s bool return is
	// intentionally ignored: a stale wakeRekeyEmit signal already
	// queued onto m.wake is caught by emitRekeyRequest's defensive
	// awaitingRekeyReply skip (the manual emit below sets the bool
	// before Run picks up the stale wake).
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
		s.rekeyTimer = nil
	}
	m.emitRekeyRequest(ctx, s, "manual")
	return nil
}

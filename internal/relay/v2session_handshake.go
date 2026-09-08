package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound Noise session-establishment path — the
// initiator handshake and hello/capability negotiation — carved out of
// v2session.go (#1021). Pure move: same package, no behaviour change.
// The app-frame router, re-key responder, and manager core stay in
// v2session.go; later #964 slices own those concerns.

// maxNoisePayloadBytes is the Noise framework's per-message limit on the
// decoded `data` field of an InnerFrameV2 (docs/protocol-mobile.md
// § Wire shapes). Enforced at the JSON-decode boundary so oversized
// payloads never reach Responder.ReadInit.
const maxNoisePayloadBytes = 65535

// decodeInnerFrameV2 JSON-decodes raw as an InnerFrameV2 and validates
// the discriminator shape. Returns ErrMalformedInnerFrame on any
// structural failure (bad JSON, wrong version, missing type, oversized
// data after base64 decode).
func decodeInnerFrameV2(raw json.RawMessage) (InnerFrameV2Decoded, error) {
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		return InnerFrameV2Decoded{}, fmt.Errorf("decode inner frame: %w", err)
	}
	if inner.Version != protocol.V2Version {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame: version %d, want %d",
			inner.Version, protocol.V2Version)
	}
	switch inner.Type {
	case protocol.TypeNoiseInit, protocol.TypeNoiseResp, protocol.TypeNoiseMsg:
	default:
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame: unknown type %q", inner.Type)
	}
	data, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame data base64: %w", err)
	}
	if len(data) > maxNoisePayloadBytes {
		return InnerFrameV2Decoded{}, fmt.Errorf("inner frame data: %d bytes > %d cap",
			len(data), maxNoisePayloadBytes)
	}
	return InnerFrameV2Decoded{Type: inner.Type, Data: data}, nil
}

// InnerFrameV2Decoded is the post-validation form of an InnerFrameV2:
// Type matches one of the discriminator constants and Data has been
// base64-decoded with the size cap enforced.
type InnerFrameV2Decoded struct {
	Type string
	Data []byte
}

// supportedV2Capabilities is the daemon's authoritative capability set. The
// negotiation output is built from THESE entries only — never from the phone's
// advertised set — so an unsupported/spoofed advertisement can never be echoed
// in the hello_ack or recorded on the session.
//
// Membership grants nothing on its own: every capability gate in this package
// reads s.interactive, which handleNoiseInit derives with a value-specific
// slices.Contains against protocol.CapabilityInteractive rather than from the
// negotiated slice being non-empty. That distinction became load-bearing with
// protocol.CapabilityQuestion (#2020), the first member a client can be granted
// while remaining non-interactive; reducing it to a len() > 0 test would hand a
// question-only client the whole interactive stream.
// protocol.CapabilityModelList (#2172) is the second such member and is pinned
// by its own row for that reason rather than riding the question one.
//
// New members are APPENDED, never inserted. negotiateCapabilities emits in this
// slice's order and both of its test tables compare with slices.Equal, so the
// position of an existing member is what every row's expectation is written
// against; inserting ahead of one silently rewrites all of them.
//
// Read-only after package init and read on the manager's Run goroutine; it is a
// var only because a slice cannot be const. Nothing may assign to it or to its
// backing array at runtime.
var supportedV2Capabilities = []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}

// negotiateCapabilities returns the phone's advertised set ∩
// supportedV2Capabilities, in supported-set order. It iterates the supported
// set (not the advertised one), so the result is a subset of supported by
// construction: duplicates collapse, an unsupported/spoofed advertisement is
// dropped, and an advertise-nothing / only-unsupported set yields nil (the
// omitempty ack field then drops the key, preserving v1 byte-stability, and
// the recorded interactive flag fails closed to false).
func negotiateCapabilities(advertised []string) []string {
	var out []string
	for _, name := range supportedV2Capabilities {
		if slices.Contains(advertised, name) {
			out = append(out, name)
		}
	}
	return out
}

// handleNoiseInit processes an inbound noise_init frame. The initial
// handshake path is documented step-by-step in
// docs/specs/architecture/445-internal-relay-v2-inner-frame-handshake-token-gating.md.
// A noise_init arriving in V2StateOpen is a phone-initiated re-key
// (docs/protocol-mobile.md § Re-key); it is routed to handleRekeyInit,
// which runs IK responder again and atomically swaps s.send / s.recv.
// noise_init in V2StateHandshakeComplete remains a state-machine
// violation (CipherStates are held but uncommitted; a fresh handshake
// at that point is indistinguishable from a wire-protocol violation).
func (m *V2SessionManager) handleNoiseInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	switch s.state {
	case V2StateOpen:
		m.handleRekeyInit(ctx, s, inner)
		return
	case V2StateAwaitingInit:
		// fall through to the initial-handshake body below
	default: // V2StateHandshakeComplete; V2StateClosed is filtered earlier in handleFrame.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_init_in_handshake_complete")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	// Lazy-construct the responder on first noise_init for this conn.
	resp, err := noise.NewResponder(m.cfg.StaticPriv)
	if err != nil {
		// Realistically unreachable: StaticPriv length is validated at
		// NewV2SessionManager and key derivation is deterministic. Close
		// at 4426 anyway — without a Responder we cannot proceed.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "responder_init_failed")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	s.resp = resp

	earlyData, err := s.resp.ReadInit(inner.Data)
	if err != nil {
		// MAC failure, malformed IK message 1, wrong static pubkey. No
		// AEAD channel exists; close-only at 4426.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure))
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	// Pin the initiator's static pub at the earliest authenticated
	// point — ReadInit success means flynn has MAC-verified and
	// decrypted the static. Consumed by the re-key responder's
	// peer-continuity check (#453); inert in this slice.
	s.peerStatic = s.resp.PeerStatic()

	var helloEnv protocol.Envelope
	if err := json.Unmarshal(earlyData, &helloEnv); err != nil {
		// Malformed early-data envelope: handshake-layer protocol
		// violation. CipherStates don't exist yet, so close-only at 4421
		// (cannot AEAD-seal an error envelope without WriteResp).
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "early_data_not_envelope")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}
	if helloEnv.Type != protocol.TypeHello {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "early_data_not_hello")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}
	var helloPayload protocol.HelloClientPayload
	if err := json.Unmarshal(helloEnv.Payload, &helloPayload); err != nil {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "hello_payload_decode")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	// Negotiate capabilities: intersect the phone's advertised set with the
	// daemon's authoritative supported set. The result is echoed in the
	// hello_ack on every handshake and (on the token-OK branch only) recorded
	// as s.interactive below. Computed before the ack literal so the same
	// negotiated slice is the single source of truth for both the echo and the
	// flag — ack and flag can never disagree.
	negotiated := negotiateCapabilities(helloPayload.Capabilities)

	// Build and AEAD-seal hello_ack via WriteResp's early-data slot. The
	// hello_ack carries InReplyTo=hello.ID to mirror v1's request/response
	// pairing convention.
	helloID := helloEnv.ID
	ackPayload, err := json.Marshal(protocol.HelloAckPayload{
		ProtocolVersion: "v2",
		ServerID:        m.cfg.ServerID,
		ConnID:          s.connID,
		Capabilities:    negotiated, // omitempty: nil/empty → key absent
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "ack_payload_marshal")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	ackEnv := protocol.Envelope{
		ID:        1,
		Type:      protocol.TypeHelloAck,
		TS:        time.Now().UTC(),
		Payload:   ackPayload,
		InReplyTo: &helloID,
	}
	ackJSON, err := json.Marshal(ackEnv)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "ack_envelope_marshal")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	respMsg, sendCS, recvCS, err := s.resp.WriteResp(ackJSON)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "write_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}
	s.send = sendCS
	s.recv = recvCS
	// State transitions to handshakeComplete BEFORE token validation —
	// observably distinct from open. The gating test pins this.
	s.state = V2StateHandshakeComplete

	respFrame, err := marshalInnerFrameV2(protocol.TypeNoiseResp, respMsg)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.ik_failure",
			"conn_id", s.connID,
			"close_code", int(StatusHandshakeFailure),
			"reason", "marshal_noise_resp")
		m.closeWith(ctx, s, StatusHandshakeFailure, nil)
		return
	}

	// Reload the on-disk registry so a device paired after daemon startup
	// authenticates without a restart (#782). Fail closed on a read error:
	// log path + a static reason and proceed to Validate against the retained
	// in-memory set — the accept set is not widened and no loaded device is
	// lost. SECURITY: never log the wrapped err; a corrupt devices.json can
	// carry file bytes (a token_hash) in a json.Unmarshal error.
	if m.cfg.DevicesPath != "" {
		if err := m.cfg.Devices.Reload(m.cfg.DevicesPath); err != nil {
			m.cfg.Logger.Warn("relay: v2 devices reload failed",
				"event", "v2.devices.reload_failed",
				"conn_id", s.connID,
				"path", m.cfg.DevicesPath)
		}
	}

	device, tokenResult := m.cfg.Devices.Validate(helloPayload.Token)
	if tokenResult != devices.ValidateAccepted {
		// Token-failure path: emit AEAD-sealed error envelope and the
		// 4401 close in a SINGLE routing envelope so the phone observes
		// the error frame before the WS close (spec § Failure modes,
		// line 436; matches v1 dispatcher's atomicity pattern).
		//
		// An elapsed redemption window (#1529) and an unrecognised token
		// share this body wholesale — same sealed auth.invalid_token, same
		// MsgInvalidToken, same 4401, same frame order. Only the log event
		// below tells them apart, and it does so server-side only: a
		// client-visible difference would turn the handshake into an oracle
		// confirming that a photographed token was once real. Selecting a
		// string here rather than branching into a second reject body is
		// what keeps that identity structural.
		rejectEvent := "v2.handshake.reject.invalid_token"
		if tokenResult == devices.ValidateWindowElapsed {
			rejectEvent = "v2.handshake.reject.redemption_window_elapsed"
		}
		errFrame, sealErr := m.sealError(s, protocol.CodeAuthInvalidToken,
			MsgInvalidToken, helloID)
		// SECURITY: the line carries neither the plain token nor its hash —
		// nor the deadline, which would narrow when the token was minted for
		// anyone reading logs. The event name is the whole discriminator.
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", rejectEvent,
			"conn_id", s.connID,
			"close_code", int(StatusUnauthorized))
		// Best-effort: send noise_resp first (so the AEAD channel exists
		// on the wire), then the error+close combined envelope.
		m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
		if sealErr != nil {
			// AEAD seal failed — drop the error frame, still emit 4401.
			m.cfg.Logger.Warn("relay: v2 seal error failed; close-only",
				"conn_id", s.connID, "err", sealErr)
			m.closeWith(ctx, s, StatusUnauthorized, nil)
			return
		}
		m.closeWith(ctx, s, StatusUnauthorized, errFrame)
		return
	}

	// Success: emit noise_resp, advance to open. Capture the matched
	// device snapshot so dispatchAppFrame can surface it via *dispatch.Conn
	// for handlers that consult c.Auth().
	m.cfg.Logger.Info("relay: v2 handshake accept",
		"event", "v2.handshake.accept",
		"conn_id", s.connID,
		"device_name", device.Name)
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: respFrame})
	// Durably record this token's first redemption (#1528): clear the
	// deadline `pyry pair` stamped and persist, so a redeemed device stays
	// distinguishable from a never-scanned one across a restart. No-op for a
	// record with no deadline and for an unwired DevicesPath; never fails the
	// handshake. Placed AFTER the accept envelope so the phone never waits on
	// the devices lock, and BEFORE the V2StateOpen transition below so that
	// transition — the edge every enumeration and every test synchronises on —
	// also orders the write.
	m.recordRedemption(s.connID, device)
	// s.device deliberately keeps the pre-clear snapshot: it records what
	// authentication observed, and no reader consults RedeemBy off the session
	// (#1529 enforces the deadline at the registry).
	s.device = &device
	// Record the negotiated interactive decision from the same slice the ack
	// echoed (single source of truth) BEFORE the session becomes enumerable.
	// A spoofed/unsupported advertisement never appears in negotiated, so it
	// can never flag the session.
	s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)
	// Retain what the client reported about ITSELF, for the session's appended
	// system prompt (#2148). Recorded here, on the accept path and before the
	// session becomes enumerable, for s.interactive's reason: an unauthenticated
	// peer's strings must never be observable through ActiveConns.
	//
	// The two fields are copied BY VALUE and helloPayload is not retained. That is
	// deliberate: the same payload carries Token, plaintext credential material
	// HelloClientPayload marks MUST-NOT-log, and parking the struct on the session
	// would keep the token alive for the session's lifetime and one %+v from a log
	// line. Length is the only property judged here — see ActiveConn on why the
	// character set is internal/sessions' decision, not this package's.
	s.clientName = retainedClientField(helloPayload.DeviceName, maxRetainedClientNameBytes)
	s.clientVersion = retainedClientField(helloPayload.ClientVersion, maxRetainedClientVersionBytes)
	s.state = V2StateOpen
	// Create the per-session push buffer now that the session is authenticated
	// and enumerable. queue-exists ⟺ V2StateOpen; an off-Run Push finds this
	// queue under pushMu without reading Run-owned m.sessions. Mutating the map
	// here (on Run) and in closeWith (on Run) keeps the key set Run-owned.
	m.pushMu.Lock()
	m.queues[s.connID] = &pushQueue{}
	m.pushMu.Unlock()
	// Spawn this conn's app-frame worker (#965) now that the session is open:
	// s.device is set and s.state == V2StateOpen, so the `go` here is the
	// happens-before edge past which the worker may safely read those
	// immutable fields. dispatchAppFrame's non-blocking enqueue onto
	// s.appFrames feeds it; the worker runs handlers off Run so a slow
	// handler cannot stall the Run loop. ctx is runCtx; closeWith closes
	// s.done to stop the worker on teardown. This tail runs once per session
	// (V2StateOpen noise_init routes to handleRekeyInit above, not here), so
	// exactly one worker is spawned per conn.
	s.appFrames = make(chan appFrameJob, appFrameQueueDepth)
	s.done = make(chan struct{})
	go m.appFrameWorker(ctx, s)
	s.rekeyTimer = m.armRekeyTimer(ctx, s)
	// Arm the idle sweep (#774) alongside the rekey timer. lastActivityAt
	// was stamped by this noise_init's handleFrame, so the timer fires
	// ~idleTimeout from now unless a further inbound frame re-stamps it. No
	// inbound frame within the window tears the session down via closeWith,
	// bounding the CipherStates' lifetime under connect/disconnect churn.
	s.idleTimer = m.armIdleTimer(ctx, s, idleTimeout)

	// Connect-time modal reconcile (#877): re-send the still-outstanding
	// modal_shown set to this conn so a phone that connected/reconnected after a
	// permission prompt was raised is brought to current modal truth, rather than
	// letting the prompt silently ride the deny-on-timeout window unseen. No-op
	// for a non-interactive conn or an unwired seam. Ordering relative to
	// replayMissed is immaterial: the reconcile's modal_shown lands in m.queues
	// (held behind any reconnect-replay tail by #777, then drains), while
	// replayMissed enqueues into the separate replayQueue. Placed first to
	// document intent — surface the time-sensitive prompt ahead of replayed
	// history.
	m.reconcileModals(ctx, s)

	// Connect-time question reconcile (#1979): re-send the still-outstanding
	// question_shown batches to this conn — the fourth Mode B instance — so a client
	// that connected/reconnected after claude asked a clarifying question can answer
	// it instead of leaving it to run out its window unseen. No-op for a
	// non-interactive conn or an unwired seam. Correctness does not depend on where in
	// this tail the call lands: the four reconciles carry distinct payload types and
	// none reads another's effect. Placed beside the modal reconcile rather than after
	// the model-list one because an outstanding batch rides the daemon's approval
	// window — the same time-sensitivity that put the modal reconcile first and the
	// model-list reconcile last.
	m.reconcileQuestions(ctx, s)

	// Connect-time queue reconcile (#878): re-send the current queue_state for each
	// non-empty conversation to this conn — the queue twin of the modal reconcile
	// above — so a phone that connected/reconnected between backlog changes sees
	// current queue truth instead of the stale/empty view #722's push-on-change
	// leaves. No-op for a non-interactive conn or an unwired seam. Ordering relative
	// to reconcileModals and replayMissed is immaterial (distinct payload types);
	// placed after the modal reconcile to surface the time-sensitive permission
	// prompt ahead of the backlog.
	m.reconcileQueues(ctx, s)

	// Connect-time model-list reconcile (#1863): send the retained model_list for
	// each session holding one to this conn — the third Mode B instance alongside
	// the two reconciles above — so a client attaching later can populate its model
	// menu without sending a message first. The live turn lane never reaches such a
	// client (and on the bootstrap session reaches none), for the three loss points
	// reconcileModelLists names. No-op for a non-interactive conn or an unwired
	// seam. Ordering relative to the other two and to replayMissed is immaterial
	// (distinct payload types); placed third to keep the time-sensitive permission
	// prompt first, the same reason the queue reconcile is already second.
	m.reconcileModelLists(ctx, s)

	// Connect-time slash-command-list reconcile (#2006): send the retained
	// slash_command_list for each session holding one to this conn — the fifth Mode B
	// instance alongside the four reconciles above — so a client attaching later gets
	// a correct command menu without waiting for a turn that may never come. The live
	// turn lane never reaches such a client, for the three loss points
	// reconcileSlashCommandLists names. No-op for a non-interactive conn or an unwired
	// seam. Correctness does not depend on the position: the five carry distinct
	// payload types, none reads another's effect, and none of the five frames is
	// droppable (pushQueue.enqueue marks only TypeAssistantDelta so). This tail is
	// ordered by time-sensitivity — permission prompt first — and a command menu is the
	// least time-sensitive of the five, so it goes last.
	m.reconcileSlashCommandLists(ctx, s)

	// Connect-time background-task-roster reconcile (#2078): send the retained
	// background_task_roster for each session holding one to this conn — the sixth
	// Mode B instance alongside the five reconciles above — so a client attaching to a
	// long-running daemon shows what is still running instead of an empty panel. The
	// live turn lane reaches only whoever is connected when claude CHANGES the roster,
	// and Mode A cannot help a client that advertises no last_event_id. No-op for a
	// non-interactive conn or an unwired seam (this slice ships it unwired; #2079 fills
	// it). Correctness does not depend on the position: the six carry distinct payload
	// types, none reads another's effect, and none of the six frames is droppable
	// (pushQueue.enqueue marks only TypeAssistantDelta so). This tail is ordered by
	// time-sensitivity — permission prompt first — and a roster snapshot is not
	// time-sensitive in that sense, so it goes last.
	m.reconcileBackgroundTaskRosters(ctx, s)

	// Mid-turn-reconnect replay (#647): if the phone advertised where it left
	// off, replay the conversation's missed tail (or emit a resync marker) on
	// this conn before Run returns to its select to service the live stream
	// (AC-2's "before the live stream resumes"). The hook is at the very tail
	// of the success path — noise_resp is sent (the phone's recv CipherState
	// exists), s.state is V2StateOpen (forwardEnvelope's gate passes), and the
	// push queue exists — so replay frames seal under the fresh session keys as
	// the first AEAD-transport frames. last_event_id is untrusted remote input:
	// replayMissed range/ring-bounds it and scopes it to the daemon-resolved
	// conversation (never one the phone names).
	if helloPayload.LastEventID != nil {
		m.replayMissed(ctx, s, *helloPayload.LastEventID)
	}
}

// handleNoiseMsg processes an inbound noise_msg frame. The
// handshakeComplete branch is the gating invariant: a noise_msg
// arriving while we hold CipherStates but have not yet validated the
// token is rejected as auth.invalid_token. The open branch AEAD-decrypts
// the payload and dispatches the inner v1-shaped envelope through the
// existing handler chain; AEAD failures close the conn at 4421.
func (m *V2SessionManager) handleNoiseMsg(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	switch s.state {
	case V2StateAwaitingInit:
		// No CipherStates yet; close-only at 4421.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_msg_before_handshake")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	case V2StateHandshakeComplete:
		// Gating invariant: try to AEAD-decrypt and decode. If the frame
		// decrypts cleanly to a non-hello envelope, take the
		// auth.invalid_token path; otherwise reject at 4421.
		plaintext, err := s.recv.Decrypt(inner.Data)
		if err != nil {
			m.cfg.Logger.Warn("relay: v2 state reject",
				"event", "v2.state.reject",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch),
				"reason", "noise_msg_decrypt_failed")
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plaintext, &env); err != nil {
			m.cfg.Logger.Warn("relay: v2 state reject",
				"event", "v2.state.reject",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch),
				"reason", "noise_msg_envelope_decode")
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		// Decrypted cleanly: token was not validated; reject as
		// auth.invalid_token regardless of envelope type. The handler
		// chain MUST NOT be reached from handshakeComplete (AC #4).
		errFrame, sealErr := m.sealError(s, protocol.CodeAuthInvalidToken,
			MsgInvalidToken, env.ID)
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.invalid_token",
			"conn_id", s.connID,
			"close_code", int(StatusUnauthorized),
			"reason", "noise_msg_in_handshake_complete")
		if sealErr != nil {
			m.cfg.Logger.Warn("relay: v2 seal error failed; close-only",
				"conn_id", s.connID, "err", sealErr)
			m.closeWith(ctx, s, StatusUnauthorized, nil)
			return
		}
		m.closeWith(ctx, s, StatusUnauthorized, errFrame)
		return
	case V2StateOpen:
		plaintext, err := s.recv.Decrypt(inner.Data)
		if err != nil {
			// Tampered, replayed, or truncated frame. flynn/noise leaves
			// the receive counter unchanged on Decrypt failure, but the
			// channel is no longer trustworthy: close at 4421 and drop the
			// session entry so a subsequent noise_init for the same
			// conn_id starts a fresh awaitingInit (AC #2, #3). Do NOT log
			// the underlying error text — the AEAD ciphertext and counter
			// indices are not operator-actionable and stay out of the log
			// channel per the spec's security review.
			m.cfg.Logger.Warn("relay: v2 aead fail",
				"event", "v2.aead.fail",
				"conn_id", s.connID,
				"close_code", int(StatusProtocolMismatch))
			m.closeWith(ctx, s, StatusProtocolMismatch, nil)
			return
		}
		m.dispatchAppFrame(ctx, s, plaintext)
		return
	}
}

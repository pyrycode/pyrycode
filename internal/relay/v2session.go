package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Mobile Protocol v2 close codes (docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
	// StatusProtocolMismatch is the WS close code the binary asks the
	// relay to apply when a phone sends an inner frame that violates the
	// v2 inner-frame shape, the state machine, or the discriminator
	// table. Wire spec: docs/protocol-mobile.md § Error codes, close-code
	// row 4421.
	StatusProtocolMismatch websocket.StatusCode = 4421

	// StatusHandshakeFailure is the WS close code the binary asks the
	// relay to apply when the Noise_IK handshake fails before
	// CipherStates exist (e.g. MAC failure on IK message 1, wrong static
	// pubkey). No AEAD-sealed error envelope can be sent — the close
	// code is the only signal. Wire spec: docs/protocol-mobile.md
	// § Error codes, close-code row 4426.
	StatusHandshakeFailure websocket.StatusCode = 4426
)

// maxNoisePayloadBytes is the Noise framework's per-message limit on the
// decoded `data` field of an InnerFrameV2 (docs/protocol-mobile.md
// § Wire shapes). Enforced at the JSON-decode boundary so oversized
// payloads never reach Responder.ReadInit.
const maxNoisePayloadBytes = 65535

// handlerOutboundBuf is the buffer size for the per-frame dispatch.Conn
// outbound channel allocated by dispatchAppFrame. The three production
// handlers (send_message, list_conversations, register_push_token) emit
// exactly one reply per invocation; Route emits at most one error reply.
// 8 is a generous safety margin and is documented as the
// synchronous-handler assumption in V2SessionConfig.Handlers.
const handlerOutboundBuf = 8

// V2SessionState is the externally-observable lifecycle state of a
// per-conn V2Session. The handshakeComplete substate is distinct from
// open even though both can be set inside the same noise_init handler:
// the field exists so the gating test pins the
// "handler chain unreachable from handshakeComplete" invariant
// deterministically (AC #1 / #4).
type V2SessionState int

const (
	V2StateAwaitingInit V2SessionState = iota
	V2StateHandshakeComplete
	V2StateOpen
	V2StateClosed
)

// V2Session is the per-conn_id state held by V2SessionManager. Mutation
// is serialised by the manager's single dispatch goroutine (the loop is
// the lock); there is no mutex because flynn/noise's CipherStates are
// not safe for concurrent use and the manager guarantees a single
// writer per conn_id.
type V2Session struct {
	connID string
	state  V2SessionState
	resp   *noise.Responder
	send   *noise.CipherState
	recv   *noise.CipherState

	// device is the matched device snapshot from the handshake's
	// token-accept branch. Surfaced into the per-frame *dispatch.Conn as
	// auth so handlers can call c.Auth(). Set exactly once in
	// handleNoiseInit's token-OK path before state advances to
	// V2StateOpen. Nil before the token check completes.
	device *devices.Device
}

// State returns the externally-observable state. Called from the same
// goroutine that mutates today; a cross-goroutine reader (e.g. a
// broadcast layer added in a later slice) would need atomic.Int32 or a
// small mutex — tracked in spec § Open questions.
func (s *V2Session) State() V2SessionState { return s.state }

// V2SessionConfig parameterises V2SessionManager. All fields are
// required; NewV2SessionManager validates and panics or errors on
// missing required values per the documentation below.
//
// SECURITY: StaticPriv is the binary's 32-byte X25519 static private
// key. It MUST NOT be logged, wrapped into an error message, or emitted
// on any wire surface. internal/keys and internal/noise document the
// same contract for the same bytes; this struct extends the contract
// to the manager's holding site.
type V2SessionConfig struct {
	// Frames is the inbound RoutingEnvelope stream from a relay.Connection
	// (or an in-memory channel in tests). Run consumes until Frames
	// closes or ctx is done.
	Frames <-chan protocol.RoutingEnvelope

	// Outbound forwards a single binary→relay RoutingEnvelope. Production
	// wiring passes (*relay.Connection).Send. Non-nil errors are logged
	// at debug and dropped — the relay leg's reconnect handles recovery
	// (mirrors v1's cmd/pyry/relay.go forwarder posture).
	Outbound func(protocol.RoutingEnvelope) error

	// StaticPriv is the binary's 32-byte X25519 static private key.
	StaticPriv []byte

	// Devices is the token-validation predicate for hello.Token.
	Devices *devices.Registry

	// ServerID is surfaced into the hello_ack early-data payload.
	ServerID string

	// Logger receives lifecycle and reject events. Token, key bytes,
	// payload bytes, AEAD ciphertext, and base64 forms thereof MUST NOT
	// appear in any logged field.
	Logger *slog.Logger

	// Handlers is the application-layer envelope-type → handler table
	// used for v2 open-state dispatch. Optional: nil or empty map means
	// no app handlers are registered, and every open-state envelope falls
	// through to a sealed protocol.unsupported reply via dispatch.Route.
	// Mirror v1's internal/dispatch.Dispatcher.Register registration
	// shape — production wires Handlers via the daemon, same handlers as
	// v1.
	//
	// SECURITY: handlers run on the manager's single dispatch goroutine
	// (same goroutine that mutates s.send / s.recv). Handlers MUST be
	// synchronous and MUST NOT spawn long-lived background goroutines
	// that retain a reference to the *dispatch.Conn passed in — the
	// conn's outbound channel is per-frame and is drained before
	// dispatchAppFrame returns; sends from a forked goroutine after that
	// drain are silently lost (the channel is leaked but bounded by its
	// capacity, and reclaimed by GC).
	Handlers map[string]dispatch.Handler
}

// V2SessionManager owns the per-conn_id v2 state machine. Construct with
// NewV2SessionManager; drive with Run. The manager is single-shot — Run
// returns when Frames closes or ctx is done, and the manager must not
// be reused.
//
// Concurrency: Run is the only goroutine the manager owns. sessions is
// mutated exclusively by Run; no lock. This is intentionally simpler
// than internal/dispatch.Dispatcher (which spins one goroutine per
// conn_id); v2 in this slice runs no application handlers, every frame
// processes synchronously (~100µs Noise call or sub-µs JSON decode), so
// single-goroutine fan-in is correct and obviously safe.
type V2SessionManager struct {
	cfg      V2SessionConfig
	sessions map[string]*V2Session
}

// NewV2SessionManager validates cfg and returns a ready manager. Panics
// on missing Frames or Logger (matching internal/dispatch.New style for
// programmer errors). Returns an error on missing Outbound, missing
// Devices, missing ServerID, or wrong-length StaticPriv.
func NewV2SessionManager(cfg V2SessionConfig) (*V2SessionManager, error) {
	if cfg.Frames == nil {
		panic("relay: V2SessionManager Frames is required")
	}
	if cfg.Logger == nil {
		panic("relay: V2SessionManager Logger is required")
	}
	if cfg.Outbound == nil {
		return nil, fmt.Errorf("relay: V2SessionManager Outbound is required")
	}
	if cfg.Devices == nil {
		return nil, fmt.Errorf("relay: V2SessionManager Devices is required")
	}
	if cfg.ServerID == "" {
		return nil, fmt.Errorf("relay: V2SessionManager ServerID is required")
	}
	if len(cfg.StaticPriv) != noise.KeyLen {
		return nil, fmt.Errorf("relay: V2SessionManager StaticPriv must be %d bytes, got %d",
			noise.KeyLen, len(cfg.StaticPriv))
	}
	return &V2SessionManager{
		cfg:      cfg,
		sessions: make(map[string]*V2Session),
	}, nil
}

// Run drives the state machine until Frames closes or ctx is cancelled.
// Returns ctx.Err() on cancellation, nil on Frames close. Every per-conn
// session is released on return; no goroutines outlive Run.
func (m *V2SessionManager) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case env, ok := <-m.cfg.Frames:
			if !ok {
				return nil
			}
			m.handleFrame(ctx, env)
		}
	}
}

// handleFrame dispatches one inbound routing envelope to its
// per-conn_id session, creating the session lazily on first frame.
func (m *V2SessionManager) handleFrame(ctx context.Context, env protocol.RoutingEnvelope) {
	if env.ConnID == "" {
		// Binary-direct frames (e.g. hello_ack from the relay during the
		// binary↔relay handshake) are owned by relay.Connection, not by
		// the v2 manager. Connection.handshake consumes hello_ack before
		// frames flow through Frames(); any binary-direct frame that
		// reaches us is unexpected and silently dropped.
		return
	}
	s, ok := m.sessions[env.ConnID]
	if !ok {
		s = &V2Session{connID: env.ConnID, state: V2StateAwaitingInit}
		m.sessions[env.ConnID] = s
	}
	if s.state == V2StateClosed {
		// Late frame on a torn-down conn; drop silently.
		return
	}

	inner, err := decodeInnerFrameV2(env.Frame)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "malformed_inner_frame")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
		return
	}

	switch inner.Type {
	case protocol.TypeNoiseInit:
		m.handleNoiseInit(ctx, s, inner)
	case protocol.TypeNoiseMsg:
		m.handleNoiseMsg(ctx, s, inner)
	case protocol.TypeNoiseResp:
		// noise_resp from a phone is never valid — only the binary writes
		// noise_resp. Treat as state-machine violation.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_resp_from_phone")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
	default:
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", env.ConnID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "unknown_inner_type")
		m.closeWith(ctx, s, StatusProtocolMismatch, nil)
	}
}

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

// handleNoiseInit processes an inbound noise_init frame. The handshake
// path is documented step-by-step in
// docs/specs/architecture/445-internal-relay-v2-inner-frame-handshake-token-gating.md.
func (m *V2SessionManager) handleNoiseInit(ctx context.Context, s *V2Session, inner InnerFrameV2Decoded) {
	if s.state != V2StateAwaitingInit {
		// noise_init while handshakeComplete or open: rekey lives in #435,
		// not here. Treat as state-machine violation per the spec table.
		m.cfg.Logger.Warn("relay: v2 state reject",
			"event", "v2.state.reject",
			"conn_id", s.connID,
			"close_code", int(StatusProtocolMismatch),
			"reason", "noise_init_after_handshake")
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

	// Build and AEAD-seal hello_ack via WriteResp's early-data slot. The
	// hello_ack carries InReplyTo=hello.ID to mirror v1's request/response
	// pairing convention (auth.go's buildResponse).
	helloID := helloEnv.ID
	ackPayload, err := json.Marshal(protocol.HelloAckPayload{
		ProtocolVersion: "v2",
		ServerID:        m.cfg.ServerID,
		ConnID:          s.connID,
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

	device, tokenOK := m.cfg.Devices.Validate(helloPayload.Token)
	if !tokenOK {
		// Token-failure path: emit AEAD-sealed error envelope and the
		// 4401 close in a SINGLE routing envelope so the phone observes
		// the error frame before the WS close (spec § Failure modes,
		// line 436; matches v1 dispatcher's atomicity pattern).
		errFrame, sealErr := m.sealError(s, protocol.CodeAuthInvalidToken,
			MsgInvalidToken, helloID)
		m.cfg.Logger.Warn("relay: v2 handshake reject",
			"event", "v2.handshake.reject.invalid_token",
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
	s.device = &device
	s.state = V2StateOpen
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

// dispatchAppFrame runs dispatch.Route on a per-frame *dispatch.Conn,
// drains any reply envelopes the handler emitted, AEAD-seals each one
// under s.send, wraps as noise_msg, and forwards via m.send.
// Synchronous; returns only after Route has returned and all replies
// have been drained.
//
// Assumes handlers are synchronous and do not spawn long-lived
// goroutines that retain a reference to conn; the per-frame outbound
// buffer (handlerOutboundBuf) is large enough to absorb a handler's
// synchronous replies without blocking c.Send. The channel is
// deliberately NOT closed — a misbehaving handler that forks a sender
// after Route returns writes into a leaked but capacity-bounded channel
// that the GC reclaims once the goroutine exits; closing here would
// panic such a sender.
func (m *V2SessionManager) dispatchAppFrame(ctx context.Context, s *V2Session, plaintext []byte) {
	outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)
	conn := dispatch.NewConn(s.connID, outbound, s.device)
	dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)
	for {
		select {
		case reply := <-outbound:
			ciphertext, err := s.send.Encrypt(reply.Frame)
			if err != nil {
				// Realistically unreachable under correct flynn/noise.
				// Drop the reply rather than emit the unencrypted frame.
				m.cfg.Logger.Warn("relay: v2 seal app reply failed; reply dropped",
					"conn_id", s.connID)
				continue
			}
			frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
			if err != nil {
				m.cfg.Logger.Warn("relay: v2 marshal app reply failed; reply dropped",
					"conn_id", s.connID)
				continue
			}
			// The reply's CloseCode is ignored: handlers do not signal
			// closes through c.Send/c.Reply; the close-code field on the
			// routing envelope is reserved for the manager's own
			// close-intent emissions (closeWith).
			m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
		default:
			return
		}
	}
}

// sealError builds a TypeError envelope, AEAD-seals it under s.send,
// and returns the wrapped noise_msg inner-frame JSON ready for the
// Frame slot of a RoutingEnvelope. Returns a non-nil error only if the
// AEAD seal itself failed; JSON marshal failures of static
// well-typed values are wrapped but practically unreachable.
func (m *V2SessionManager) sealError(s *V2Session, code, message string, inReplyTo uint64) (json.RawMessage, error) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: false,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal error payload: %w", err)
	}
	envelope := protocol.Envelope{
		ID:        2,
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal error envelope: %w", err)
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		return nil, fmt.Errorf("aead seal error envelope: %w", err)
	}
	return marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
}

// marshalInnerFrameV2 wraps rawBytes as an InnerFrameV2 of the given
// type, base64-encoding rawBytes for the wire.
func marshalInnerFrameV2(frameType string, rawBytes []byte) (json.RawMessage, error) {
	out, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    frameType,
		Data:    base64.StdEncoding.EncodeToString(rawBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal inner frame %s: %w", frameType, err)
	}
	return out, nil
}

// closeWith transitions s to V2StateClosed, deletes the session from
// the manager, and emits a single routing envelope carrying Frame (when
// non-nil) and CloseCode. The atomic Frame+CloseCode is what guarantees
// the spec's ordering MUST: phone observes the error frame before the
// WS close (spec § Error handling, line 436). Honors ctx by checking
// before the send; the Outbound call itself is synchronous.
func (m *V2SessionManager) closeWith(ctx context.Context, s *V2Session, code websocket.StatusCode, frame json.RawMessage) {
	if s.state == V2StateClosed {
		return
	}
	s.state = V2StateClosed
	delete(m.sessions, s.connID)
	env := protocol.RoutingEnvelope{
		ConnID:    s.connID,
		Frame:     frame,
		CloseCode: uint16(code),
	}
	if ctx.Err() != nil {
		return
	}
	m.send(env)
}

// send forwards env via cfg.Outbound and logs any transport error at
// debug. Mirrors v1's cmd/pyry/relay.go forwarder posture: a non-nil
// error means the relay leg is currently unhealthy; the transport
// reconnect handles recovery and the frame is lost (consistent with the
// v1 protocol contract that frames sent while disconnected are dropped).
func (m *V2SessionManager) send(env protocol.RoutingEnvelope) {
	if err := m.cfg.Outbound(env); err != nil {
		// transport.ErrDisconnected / ErrNotConnected are expected during
		// reconnect; log at debug to keep the warn channel clean.
		m.cfg.Logger.Debug("relay: v2 outbound drop",
			"conn_id", env.ConnID,
			"close_code", env.CloseCode,
			"err", err)
	}
}


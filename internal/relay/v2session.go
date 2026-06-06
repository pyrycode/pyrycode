package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/control"
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

// rekeyInterval is the scheduled re-key cadence on each open v2 session
// (docs/protocol-mobile.md § Re-key — the 1-hour rule). Exposed as a
// package var (lowercase) so tests can substitute a sub-second value
// via the same t.Cleanup save-and-restore idiom handshakeTimeout uses;
// not part of the public API.
var rekeyInterval = 1 * time.Hour

// rekeyReplyTimeout is the bounded window between emitting a
// rekey_request and observing the phone's fresh noise_init. On expiry
// the conn is closed at StatusHandshakeFailure (4426) and a
// noise.rekey_failed log line is emitted. Exposed as a package var
// (lowercase) so tests can substitute a sub-second value; not part of
// the public API.
var rekeyReplyTimeout = 30 * time.Second

// ErrConnNotFound is returned by (*V2SessionManager).Rekey when connID
// is not currently registered in the manager's sessions map. Wraps
// control.ErrConnNotFound so the control dispatcher's
// errors.Is(err, control.ErrConnNotFound) check in handleRekey
// continues to map to ErrCodeConnNotFound on the wire without further
// plumbing.
var ErrConnNotFound = fmt.Errorf("relay: conn not found: %w", control.ErrConnNotFound)

// ErrSessionNotOpen is returned by (*V2SessionManager).Rekey when the
// named session exists but is not eligible for a manual rekey — either
// not in V2StateOpen (still handshaking, or already torn down), or
// already awaiting a rekey reply from a prior emit. The control
// dispatcher surfaces this verbatim through Response.Error with no
// ErrorCode (slice A defines no wire code for this state yet).
var ErrSessionNotOpen = errors.New("relay: session not open")

// wakeKind enumerates the per-session timer events the manager's Run
// goroutine handles on its wake channel. The values are internal and
// MUST NOT be exposed across the package boundary.
type wakeKind int

const (
	wakeRekeyEmit wakeKind = iota
	wakeRekeyReplyTimeout
)

// wakeSignal is the value the per-session timer-callback goroutines
// (spawned by time.AfterFunc) push onto V2SessionManager.wake. The Run
// goroutine pops these signals and performs the actual work
// (emitRekeyRequest or closeWith) under the single-owner-goroutine
// invariant for s.send / s.recv.
type wakeSignal struct {
	s    *V2Session
	kind wakeKind
}

// manualRekeyReq is enqueued by (*V2SessionManager).Rekey and dequeued
// by Run on the manual-rekey channel arm. The reply channel is
// per-request (cap=1) so the manager's reply send is non-blocking even
// if the caller's ctx fires between enqueue and reply.
type manualRekeyReq struct {
	connID string
	reply  chan error
}

// pushReq is enqueued by (*V2SessionManager).Push and dequeued by Run on
// the push channel arm. reply is per-request (cap=1) so Run's reply send
// is non-blocking even if the caller's ctx fires between enqueue and
// reply. Mirrors manualRekeyReq.
type pushReq struct {
	connID string
	env    protocol.Envelope
	reply  chan error
}

// wakeBufferSize sizes the manager's wake channel. The 1-hour rekey
// cadence makes concurrent fires across sessions vanishingly rare; 16
// is a generous safety margin that absorbs the realistic worst case
// (every session times out simultaneously while Run is busy in a slow
// handler invocation) without forcing the timer-callback goroutine to
// block. cap=1 would also be correct.
const wakeBufferSize = 16

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
// writer per conn_id. The re-key responder (handleRekeyInit) atomically
// swaps s.send / s.recv in a single tuple assignment on this same
// goroutine; old *CipherState pointers are dropped from the struct and
// reclaimed by GC. No explicit Wipe() of the key bytes is exposed — the
// single-owner-goroutine invariant means no code path reads the old
// state after the swap, which is the practical zeroisation property.
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

	// peerStatic is the initiator's 32-byte X25519 static public key
	// captured at the initial handshake (immediately after
	// Responder.ReadInit returns nil). The field is set exactly once
	// per V2Session and pins the original peer's identity for the
	// session's entire lifetime — a successful re-key (added in #453)
	// MUST NOT overwrite this value; the re-key responder reads
	// s.peerStatic and rejects mismatches at WS close code 4426.
	//
	// SECURITY: this is a public key (not a secret), but it is
	// identity-bearing. It MUST NOT appear in any logged field; the
	// package's no-key-in-logs discipline extends to per-session
	// identity pins. Not persisted to disk; lifetime is the V2Session.
	peerStatic []byte

	// rekeyTimer fires rekeyInterval after the session entered
	// V2StateOpen (initial handshake) or after the last successful
	// responder-side CipherState swap (rekeyComplete). On fire the
	// timer's AfterFunc callback delivers a wakeRekeyEmit signal onto
	// m.wake; the manager's Run goroutine then calls emitRekeyRequest
	// under the single-owner-goroutine invariant for s.send. Stopped
	// (and replaced with nil) by closeWith on any close path. Re-armed
	// by rekeyComplete. Nil before initial open; nil after closeWith.
	rekeyTimer *time.Timer

	// rekeyReplyTimer fires rekeyReplyTimeout after emitRekeyRequest
	// sent a rekey_request. On fire the AfterFunc callback delivers a
	// wakeRekeyReplyTimeout signal onto m.wake; the manager's Run
	// goroutine then closes the conn via closeWith(StatusHandshakeFailure
	// /* 4426 */, nil) and emits the noise.rekey_failed log line.
	// rekeyComplete stops this timer (and clears awaitingRekeyReply)
	// when the phone's fresh noise_init lands in handleRekeyInit
	// before the timeout elapses. Nil unless awaitingRekeyReply is
	// true.
	rekeyReplyTimer *time.Timer

	// awaitingRekeyReply is true between an emitRekeyRequest emit and
	// either rekeyComplete (success) or the wakeRekeyReplyTimeout
	// branch (failure). The bool is the canonical "are we awaiting a
	// fresh noise_init" predicate; rekeyReplyTimer non-nil-ness is the
	// concrete machinery but is not consulted as state — Stop() on a
	// fired timer can race with the wake delivery, and the bool is the
	// stable signal that rekeyComplete already won.
	awaitingRekeyReply bool
}

// State returns the externally-observable state. Called from the same
// goroutine that mutates today; a cross-goroutine reader (e.g. a
// broadcast layer added in a later slice) would need atomic.Int32 or a
// small mutex — tracked in spec § Open questions.
func (s *V2Session) State() V2SessionState { return s.state }

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
// single-goroutine fan-in is correct and obviously safe. The re-key
// responder's CipherState swap (s.send, s.recv = newSend, newRecv)
// inherits this property: a single tuple assignment on this goroutine
// cannot be observed half-applied by any other code path, so the spec's
// atomic-switchover requirement is structural.
type V2SessionManager struct {
	cfg      V2SessionConfig
	sessions map[string]*V2Session

	// wake is the wake-up signal channel for per-session timers. Both
	// the 1-hour rekey-emit timer and the 30s rekey-reply-timeout
	// timer use time.AfterFunc callbacks (which run on fresh runtime
	// goroutines, NOT on the dispatch goroutine that owns
	// s.send / s.recv); the callbacks send a wakeSignal onto this
	// channel so the manager's Run goroutine can do the actual work
	// (emitRekeyRequest or closeWith) under the single-owner-
	// goroutine invariant. Buffered (wakeBufferSize) so timer
	// callbacks don't block on a busy Run goroutine; callbacks also
	// honour the Run-derived ctx so they unblock cleanly on Run exit
	// and leak no goroutines.
	wake chan wakeSignal

	// manualRekey funnels (*V2SessionManager).Rekey calls onto Run's
	// dispatch goroutine so the lookup + emit sequence runs under the
	// single-owner-goroutine invariant for s.send / s.state /
	// s.rekeyTimer. Unbuffered: backpressure is correct — if Run is
	// busy, Rekey waits; the caller's ctx is the escape arm in Rekey.
	// Not closed by the manager on Run exit; in-flight callers unblock
	// via ctx.Done.
	manualRekey chan manualRekeyReq

	// push funnels (*V2SessionManager).Push calls onto Run's dispatch
	// goroutine so the lookup + seal-under-s.send + forward sequence runs
	// under the single-owner-goroutine invariant. Unbuffered: backpressure
	// is correct — if Run is busy, Push waits; the caller's ctx is the
	// escape arm. Not closed by the manager on Run exit; in-flight callers
	// unblock via ctx.Done.
	push chan pushReq
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
		cfg:         cfg,
		sessions:    make(map[string]*V2Session),
		wake:        make(chan wakeSignal, wakeBufferSize),
		manualRekey: make(chan manualRekeyReq),
		push:        make(chan pushReq),
	}, nil
}

// Run drives the state machine until Frames closes or ctx is cancelled.
// Returns ctx.Err() on cancellation, nil on Frames close. Every per-conn
// session is released on return; no goroutines outlive Run.
//
// runCtx is a derived-cancel context so per-session timer-callback
// goroutines (spawned by armRekeyTimer / armRekeyReplyTimer via
// time.AfterFunc) unblock cleanly when Run exits. The callbacks select
// on (m.wake, runCtx.Done) so a fired-but-undelivered wake completes
// via the ctx branch on shutdown and leaves no goroutine behind.
func (m *V2SessionManager) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	for {
		select {
		case <-runCtx.Done():
			return ctx.Err()
		case env, ok := <-m.cfg.Frames:
			if !ok {
				return nil
			}
			m.handleFrame(runCtx, env)
		case w := <-m.wake:
			m.handleWake(runCtx, w)
		case req := <-m.manualRekey:
			req.reply <- m.handleManualRekey(runCtx, req.connID)
		case req := <-m.push:
			req.reply <- m.handlePush(runCtx, req.connID, req.env)
		}
	}
}

// handleWake routes a per-session timer wake to its handler under the
// single-owner-goroutine invariant. A wake arriving on a session that
// has transitioned out of V2StateOpen (e.g. closeWith ran between the
// timer fire and the wake delivery) is dropped silently. The state-
// check is read-only on s.state from the dispatch goroutine — no race
// because the same goroutine that would have mutated s.state is the
// goroutine doing this read.
func (m *V2SessionManager) handleWake(ctx context.Context, w wakeSignal) {
	if w.s.state != V2StateOpen {
		return
	}
	switch w.kind {
	case wakeRekeyEmit:
		m.emitRekeyRequest(ctx, w.s, "scheduled")
	case wakeRekeyReplyTimeout:
		if !w.s.awaitingRekeyReply {
			// rekeyComplete already cleared the awaiting state — the
			// phone's fresh noise_init landed before the timeout fired
			// and the swap succeeded. Ignore stale wake.
			return
		}
		m.cfg.Logger.Warn("relay: v2 rekey reply timeout",
			"event", "noise.rekey_failed",
			"conn_id", w.s.connID,
			"close_code", int(StatusHandshakeFailure))
		m.closeWith(ctx, w.s, StatusHandshakeFailure, nil)
	}
}

// armRekeyTimer arms the 1-hour scheduled re-key timer. The callback
// runs on a fresh runtime goroutine (time.AfterFunc semantics); it
// pushes a wakeRekeyEmit signal onto m.wake under blocking-send +
// ctx.Done semantics. ctx is the manager's Run-derived runCtx;
// cancelled on Run exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armRekeyTimer(ctx context.Context, s *V2Session) *time.Timer {
	return time.AfterFunc(rekeyInterval, func() {
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
	return time.AfterFunc(rekeyReplyTimeout, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeRekeyReplyTimeout}:
		case <-ctx.Done():
		}
	})
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
	s.rekeyTimer = m.armRekeyTimer(ctx, s)
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
	// v2 control-envelope discriminator: a successful JSON decode whose
	// type matches a v2 control type is routed away from the v1
	// application dispatch chain. The probe is intentionally a re-decode;
	// dispatch.Route below decodes the same plaintext a second time. Cost
	// is one small JSON parse per application frame, well below the
	// per-frame AEAD cost. A JSON decode failure here deliberately falls
	// through so dispatch.Route's malformed-envelope branch emits the
	// sealed protocol.malformed reply established by #446 unchanged.
	var probeEnv protocol.Envelope
	if err := json.Unmarshal(plaintext, &probeEnv); err == nil && probeEnv.Type == protocol.TypeRekeyRequest {
		m.handleRekeyRequest(ctx, s, probeEnv)
		return
	}

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
// Envelope ID is fixed at 1: there is no rekey_ack response that would
// correlate by InReplyTo (the spec is explicit — the next successful
// AEAD round-trip under the new keys is the implicit ack).
//
// AEAD-seal failure is realistically unreachable under correct
// flynn/noise (same posture as sealError). On seal or marshal failure
// the frame is dropped and a WARN line emitted; the conn is NOT
// closed — the session remains in V2StateOpen. On the scheduled path
// the next 1-hour cadence will attempt another emit; on the manual
// path the operator can re-run pyry rekey to retry.
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
	// Stop per-session timers to free runtime timer-heap entries and
	// ensure no pending callback goroutine blocks indefinitely on
	// m.wake. The callback's ctx.Done arm is the load-bearing teardown
	// (Run-derived runCtx cancels on Run exit); these Stop() calls are
	// the defensive belt and are safe to call on a fired timer
	// (returns false, no-op).
	if s.rekeyTimer != nil {
		s.rekeyTimer.Stop()
		s.rekeyTimer = nil
	}
	if s.rekeyReplyTimer != nil {
		s.rekeyReplyTimer.Stop()
		s.rekeyReplyTimer = nil
	}
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

// Rekey satisfies control.Rekeyer. It funnels the request onto Run's
// dispatch goroutine via m.manualRekey so the lookup + emit sequence
// runs under the single-owner-goroutine invariant on s.send / s.state /
// s.rekeyTimer — no new lock or atomic is introduced.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound, so the
// dispatcher's errors.Is check maps to ErrCodeConnNotFound on the
// wire), ErrSessionNotOpen for sessions not in V2StateOpen or already
// awaiting a rekey reply, ctx.Err() on caller cancellation, or any
// transport-layer error surfaced by the emit path (no such error is
// returned today — seal failures are logged and dropped per
// emitRekeyRequest's documented posture).
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

// Push seals env under the addressed session's send CipherState, wraps it
// as a noise_msg transport frame, and forwards it to the phone. Safe to
// call from any goroutine other than the dispatch goroutine: the request
// is funneled onto Run via m.push so s.send is never touched concurrently
// with an in-flight dispatchAppFrame reply or a re-key swap. It is the
// missing primitive behind server-initiated delivery to a phone (the
// "broadcast layer" deferred at the V2Session / State() comments).
//
// connID names a specific connected phone. The caller owns env entirely
// (Type, ID, TS, Payload); Push performs no envelope validation — it is a
// transport primitive.
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound) when no session
// with connID exists or it has been torn down; ErrSessionNotOpen when the
// session exists but is not in V2StateOpen (still handshaking, or
// handshake-complete-but-token-unvalidated — refusing the push keeps
// server output away from an un-authenticated peer); ctx.Err() on caller
// cancellation; or a wrapped marshal/seal error (realistically
// unreachable under correct flynn/noise). Returns nil once the sealed
// frame is forwarded to Outbound — a transport-level drop (relay
// disconnected) is logged at debug inside m.send and NOT surfaced,
// matching v1 reconnect semantics and the rest of the package.
//
// Production wire-up of *V2SessionManager into the cmd/pyry daemon for
// server-initiated pushes lands in a separate ticket (#572); until then
// this method is reachable only from internal/relay tests.
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	req := pushReq{connID: connID, env: env, reply: make(chan error, 1)}
	select {
	case m.push <- req:
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

// handlePush runs on Run's dispatch goroutine. It looks up the session,
// requires V2StateOpen, then seals env under s.send and forwards a
// noise_msg — reusing emitRekeyRequest's marshal→Encrypt→wrap→send
// sequence (minus the rekey bookkeeping).
//
// Reads s.send at execution time on the dispatch goroutine, so it always
// uses the current CipherState and composes with re-key swaps: a push
// either seals fully under the old key or fully under the new key, never
// a torn read.
//
// The seal/marshal error paths return wrapped errors (the caller decides
// log level); they MUST NOT echo env, plaintext, ciphertext, or key
// bytes, matching the package's no-AEAD-bytes-in-logs discipline.
func (m *V2SessionManager) handlePush(_ context.Context, connID string, env protocol.Envelope) error {
	s, ok := m.sessions[connID]
	if !ok {
		// A torn-down session was already deleted from the map by
		// closeWith, so "closed" collapses into this same branch.
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		// Defensive: a well-typed envelope (e.g. a message envelope, a
		// closed struct of strings) does not fail to marshal in practice.
		return fmt.Errorf("marshal push envelope: %w", err)
	}
	ciphertext, err := s.send.Encrypt(envJSON)
	if err != nil {
		// Realistically unreachable under correct flynn/noise.
		return fmt.Errorf("seal push envelope: %w", err)
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		return fmt.Errorf("marshal push frame: %w", err)
	}
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
	return nil
}


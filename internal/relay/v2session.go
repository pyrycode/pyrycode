package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Mobile Protocol v2 close codes (docs/protocol-mobile.md § Error codes).
// 4401 (StatusUnauthorized) lives in auth.go and is reused unchanged.
const (
	// StatusIdleTimeout is the WS close code the binary asks the relay to
	// apply when a v2 session receives no inbound frame within idleTimeout
	// and the manager tears it down through the in-repo idle sweep (#774).
	// Echoes HTTP 408 (Request Timeout), consistent with the 44xx←HTTP
	// close-code convention (4401←401, 4404←404, 4409←409, 4429←429). Wire
	// spec: docs/protocol-mobile.md § Error codes, close-code row 4408.
	// Sending it to an already-dropped conn is a harmless relay no-op.
	StatusIdleTimeout websocket.StatusCode = 4408

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

// idleTimeout is the bounded window a v2 session may go without any
// inbound frame before the manager tears it down through the in-repo
// idle sweep (#774). Well short of the 1-hour rekeyInterval so a dropped
// or backgrounded phone's two Noise CipherStates and armed rekey timer
// never linger up to an hour (the relay↔binary leg is a single
// multiplexed WebSocket with no per-connection disconnect frame, so a
// silently-gone phone is only detectable by inbound-frame silence). Long
// enough not to tear down a foregrounded-but-momentarily-quiet phone
// mid-read (which would force a disruptive re-handshake on the next tap).
// Exposed as a package var (lowercase) so tests can substitute a
// sub-second value via a t.Cleanup save-and-restore idiom; not part of
// the public API and not yet config-driven (a deferred concern, same
// posture as modalDenyTimeout's #708 note).
var idleTimeout = 15 * time.Minute

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

// ErrTransportDown is returned by (*V2SessionManager).Rekey when the named
// session is open and eligible but the relay transport is currently down, so
// a manual rekey would seal a rekey_request that cannot reach the phone
// (burning a Noise send-nonce and arming a doomed reply window). Distinct
// from ErrSessionNotOpen so the operator sees "transport down, retry" rather
// than "not open". Surfaced verbatim by the control dispatcher through
// Response.Error with no ErrorCode, the same posture as ErrSessionNotOpen
// (#912).
var ErrTransportDown = errors.New("relay: transport down, rekey deferred - retry")

// wakeKind enumerates the per-session timer events the manager's Run
// goroutine handles on its wake channel. The values are internal and
// MUST NOT be exposed across the package boundary.
type wakeKind int

const (
	wakeRekeyEmit wakeKind = iota
	wakeRekeyReplyTimeout
	wakeIdleTimeout
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

// snapshotReq is enqueued by (*V2SessionManager).ActiveConns and dequeued
// by Run on the snapshot channel arm. reply is per-request (cap=1) so Run's
// reply send is non-blocking even if the caller's ctx fires between enqueue
// and reply. Mirrors manualRekeyReq minus the per-conn inputs — a snapshot
// takes no addressed-conn argument. The reply carries the capability-aware
// enumeration ([]ActiveConn); ActiveConnIDs is a thin projection over the
// same reply.
type snapshotReq struct {
	reply chan []ActiveConn
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

	// interactive is the negotiated interactive-capability decision (the
	// phone's advertised set ∩ supportedV2Capabilities contained
	// CapabilityInteractive). Set exactly once in handleNoiseInit's token-OK
	// path BEFORE s.state advances to V2StateOpen; the zero value (false) is
	// the fail-closed default for every other path. Re-key (handleRekeyInit)
	// preserves it by never touching it, like device/peerStatic. Read by
	// handleActiveConns on the same dispatch goroutine — no lock/atomic.
	interactive bool

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

	// idleTimer fires idleTimeout after this session's last inbound frame
	// and drives the in-repo idle sweep (#774). On fire the AfterFunc
	// callback delivers a wakeIdleTimeout signal onto m.wake; the manager's
	// Run goroutine then either reschedules the timer (a frame arrived after
	// the timer was armed, so time.Since(lastActivityAt) < idleTimeout) or
	// tears the session down via closeWith(StatusIdleTimeout /* 4408 */,
	// nil), bounding the lifetime of the two Noise CipherStates under
	// connect/disconnect churn. Armed at V2StateOpen alongside rekeyTimer;
	// stopped (and replaced with nil) by closeWith on any close path. Nil
	// before initial open; nil after closeWith.
	idleTimer *time.Timer

	// lastActivityAt is the instant of the most recent inbound frame,
	// stamped in handleFrame for every frame (handshake or app). It is the
	// idle deadline the idleTimer chases: a fire whose
	// time.Since(lastActivityAt) is still < idleTimeout reschedules for the
	// remaining window instead of tearing down, so the timer ends up firing
	// at exactly lastActivityAt + idleTimeout. Run-owned (written in
	// handleFrame, read in handleWake) — same single-writer regime as
	// state/rekeyTimer, no lock or atomic. Never crosses the wire, so the
	// monotonic reading is retained (PROJECT-MEMORY's time.Time round-trip
	// discipline does not apply).
	lastActivityAt time.Time

	// replayThrough is the highest durable event id already delivered to this
	// conn by the mid-turn-reconnect replay (#647). Run-owned: written in
	// replayMissed (the #663 clamp) and drainReplayOnce (the per-event trailing
	// advance), read in forwardEnvelope, all on the single Run goroutine — same
	// single-writer regime as state/interactive, so no lock or atomic.
	// forwardEnvelope drops a live structured envelope whose EventID
	// <= replayThrough, so the transient replay/live overlap never
	// double-delivers an event (the deterministic dedup proven in spec
	// § Concurrency model). Zero for any conn that never advertised
	// last_event_id, and live ids are always >= 1, so the guard is inert for
	// them.
	replayThrough uint64

	// replayQueue is the not-yet-forwarded tail of a mid-turn-reconnect replay
	// (#777), populated by replayMissed and drained one event per Run pass by
	// drainReplayOnce so a large replay can no longer monopolise the Run loop.
	// Run-owned: written and read only on the dispatch goroutine (replayMissed,
	// drainReplayOnce, drainOnce's gate, closeWith) — no lock or atomic, same
	// regime as state/replayThrough. nil/empty ⇒ no replay in flight. Bounded by
	// ring retention (≤ MaxEventsPerConversation), never the push cap: the
	// events come straight from ring.After's materialised copy, so there is no
	// drop/gap risk a second cap would reintroduce (spec § Open questions).
	replayQueue []eventring.Event
}

// State returns the externally-observable state. Called from the same
// goroutine that mutates today; a cross-goroutine reader (e.g. a
// broadcast layer added in a later slice) would need atomic.Int32 or a
// small mutex — tracked in spec § Open questions.
func (s *V2Session) State() V2SessionState { return s.state }

// ScreenSnapshotter renders the daemon's live claude screen to plain text:
// text is the rendered screen, live is false (and text "") when no claude
// child is attached. *supervisor.Supervisor satisfies it. Declared here, in
// the consumer, so internal/relay depends on neither internal/supervisor nor
// tui-driver (CODING-STYLE: define interfaces where they are consumed).
type ScreenSnapshotter interface {
	ScreenSnapshot() (text string, live bool)
}

// Interrupter delivers a single Esc to the supervised claude — the remote
// equivalent of a local Esc, claude's own interrupt. *supervisor.Supervisor
// satisfies it via SendEsc (#726), so the supervisor needs no new method.
// Declared here (consumer side), beside ScreenSnapshotter, so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role (matching ScreenSnapshotter.ScreenSnapshot / ModalResolver.Resolve*),
// even though the method keeps the sealed surface's name. SendEsc is safe to call
// from any goroutine — it is the same seam ResolveCancel / ResolveTimeout use.
type Interrupter interface{ SendEsc() error }

// SessionStarter drives the supervised claude's /clear — the remote equivalent
// of a local `/clear`, starting a fresh session (#831). *supervisor.Supervisor
// satisfies it via StartNewSession (#830), so the supervisor needs no new
// method. Declared here (consumer side), beside Interrupter, so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role (matching Interrupter / ScreenSnapshotter), even though the method keeps
// the sealed surface's name; the doc comment fixes the /clear semantics so the
// name is unambiguous against any pool/session lifecycle "start". StartNewSession
// is safe to call from any goroutine — it is the same sealed sendModalKey seam
// SendEsc / ResolveCancel use.
type SessionStarter interface{ StartNewSession() error }

// QueueRemover drops a not-yet-drained queued message from a conversation's
// inbound backlog by id (#723). *msgqueue.Queue satisfies it. Declared here, in
// the consumer, beside Interrupter / ModalResolver, so internal/relay imports
// neither internal/msgqueue nor cmd/pyry (CODING-STYLE: define interfaces where
// they are consumed). Returns true iff a message was removed; an unknown or
// foreign conversationID, an unknown or already-delivered id, or the in-flight
// (draining) head is a safe no-op (false). The conversationID arg IS the
// mutation scope: Remove(A,…) provably never touches conversation B's backlog.
type QueueRemover interface {
	Remove(conversationID string, queuedMsgID uint64) bool
}

// SettingsUpdate is the presence contract for an inbound set_session_settings
// change (#845): a nil field leaves the stored value untouched; a non-nil field
// sets it — including *"" for Model/Effort and *false for YOLO, which are thereby
// distinguishable from omitted. It mirrors sessions.SettingsUpdate 1:1 so the
// cmd/pyry adapter passes the three pointers straight through, without
// internal/relay importing internal/sessions. A nil YOLO can never read as a sent
// false, so an absent field can never enable --dangerously-skip-permissions.
type SettingsUpdate struct {
	Model  *string
	Effort *string
	YOLO   *bool
}

// SettingsUpdater persists a per-session settings change named by an inbound
// set_session_settings control frame (#845); the cmd/pyry adapter wraps
// *sessions.Pool.UpdateSettings. Declared here (consumer side), beside
// Interrupter / SessionStarter / QueueRemover, so internal/relay imports neither
// internal/sessions nor cmd/pyry (CODING-STYLE: define interfaces where they are
// consumed). UpdateSettings returns ErrSessionUnknown for an id the daemon does
// not host (the adapter maps sessions.ErrSessionNotFound onto it) — which the
// handler turns into a session.not_found reply; any other error is a persist
// failure the handler reports as server-unavailable.
type SettingsUpdater interface {
	UpdateSettings(sessionID string, update SettingsUpdate) error
}

// ErrSessionUnknown is the relay-local sentinel the SettingsUpdater adapter
// returns when set_session_settings names a session the daemon does not host. The
// cmd/pyry adapter maps sessions.ErrSessionNotFound onto it so internal/relay
// stays free of an internal/sessions import; handleSetSessionSettings maps it to a
// deterministic session.not_found reply.
var ErrSessionUnknown = errors.New("relay: session unknown")

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state. Declared here (consumer side), beside
// ScreenSnapshotter, so internal/relay imports neither internal/supervisor nor
// cmd/pyry; the cmd/pyry resolver satisfies it. *devices.Device crosses the
// seam (the per-conn s.device); internal/relay already imports internal/devices,
// so no new import. Both methods run on the manager's single Run dispatch
// goroutine.
type ModalResolver interface {
	// ResolveCancel consumes modalID (registry Resolve), routes a cancel/ESC
	// keystroke, audits outcome=cancelled, and returns the dismissal to
	// broadcast with ok=true. An unknown/already-resolved id ⇒ (zero, false):
	// no keystroke, no audit, no dismissal.
	ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool)

	// ResolveAnswer resolves an inbound modal_answer. In this slice it is a
	// deferred no-op — always (zero, false): no keystroke, no mutation, no
	// audit. #717 fills the gated answer arm; the manager code is already
	// general (broadcasts on ok=true) so #717 changes only the impl.
	ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool)

	// ResolveTimeout safe-denies an unanswered modal whose deny-on-timeout
	// window elapsed (#725): it consumes modalID (registry Resolve), routes the
	// fail-closed deny keystroke (ESC), audits outcome=denied_timeout /
	// source=timeout with an empty device (a timeout has no answering device),
	// and returns the dismissal to broadcast with ok=true. An unknown or
	// already-resolved id (an answer/cancel won the race) ⇒ (zero, false): no
	// keystroke, no audit, no broadcast — the AC-2 loser path. Takes no device
	// (unlike ResolveCancel/ResolveAnswer): the safe-deny is unconditional, so
	// there is nothing to gate.
	ResolveTimeout(modalID string) (ModalDismissal, bool)
}

// V2SessionConfig parameterises V2SessionManager. The handshake/transport
// fields are required; NewV2SessionManager validates and panics or errors on
// missing required values per the documentation below. Handlers, Snapshotter,
// and KnownConversation are optional — their per-field docs describe the
// nil behaviour.
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

	// Connected reports whether the relay transport leg is currently up. The
	// push drain (drainOnce) consults it BEFORE sealing a queued envelope: a
	// false result leaves the head un-popped and unsealed, so no Noise
	// send-nonce is burned for a frame that cannot reach the phone (a burned
	// nonce gaps the phone's recv nonce → 4421 close of a still-live session,
	// #874). This extends queuedEnv's "held unsealed" invariant from the
	// enqueue side to the drain side.
	//
	// Optional: nil ⇒ always-connected — the drain never holds, preserving the
	// pre-#874 drop-on-send posture for foreground / unwired / existing tests.
	// Production wires (*relay.Connection).Connected, a level poll of the
	// transport leg's live-conn state. A true result is best-effort: the conn
	// may drop between the poll and the send (a single-frame residual race at
	// the up→down transition instant), but a false result reliably holds. NOT
	// a security decision — it gates only the timing of a seal that would
	// otherwise happen anyway; V2StateOpen and per-conn addressing stay in
	// forwardEnvelope, downstream of the probe.
	Connected func() bool

	// Reconnect, when non-nil, is an edge-triggered signal that fires once per
	// fresh relay transport conn. On each fire, Run re-signals the push drain so
	// a control envelope held while the leg was down (see Connected) flushes the
	// instant the leg recovers, without waiting for the next Push (#875).
	// Production wires (*relay.Connection).Reconnected. Cap-1 drop-on-full,
	// single observer.
	//
	// Optional: nil ⇒ no new wake source. Run's select arm reads a nil channel,
	// which is never ready, so the drain flushes only on the pre-#875
	// Push-driven re-signal — byte-identical to the foreground / unwired /
	// existing-test posture. This wakes the drain only; it seals nothing.
	// drainOnce still consults Connected before the pop, so a conn that drops
	// again between this edge and the pop burns no nonce (#874). NOT a security
	// decision.
	Reconnect <-chan struct{}

	// RekeyInterval overrides the scheduled re-key cadence — the timer that
	// fires emitRekeyRequest("scheduled") on an open session. Optional: zero ⇒
	// the rekeyInterval package default (1h). Read only by armRekeyTimer.
	//
	// Test-only seam (#920): the e2e suite lives in package e2e and cannot
	// mutate internal/relay's unexported timing vars the way the in-package
	// tests do, so the scheduled-rekey wire path is otherwise untriggerable
	// from an e2e test. Production (cmd/pyry) leaves it zero; it is never
	// sourced from the wire, a file, or operator input, so it widens no trust
	// boundary. A smaller value only raises rotation frequency (strictly more
	// forward secrecy) — no value weakens the shipped 1h posture or disables
	// rotation, and it touches no key material, peer-static pin, or AEAD.
	RekeyInterval time.Duration

	// RekeyReplyTimeout overrides the bounded window between emitting a
	// rekey_request and tearing the session down when the phone's fresh
	// noise_init never arrives. Optional: zero ⇒ the rekeyReplyTimeout package
	// default (30s). Read only by armRekeyReplyTimer. Same test-only population
	// and security posture as RekeyInterval.
	RekeyReplyTimeout time.Duration

	// RekeyRetryInterval overrides the short re-arm cadence used when a
	// scheduled re-key wake fires while the relay leg is down and the emit is
	// deferred (#912). Optional: zero ⇒ the rekeyRetryInterval package default
	// (1m). Read only by armRekeyRetryTimer. Same test-only population and
	// security posture as RekeyInterval.
	RekeyRetryInterval time.Duration

	// StaticPriv is the binary's 32-byte X25519 static private key.
	StaticPriv []byte

	// Devices is the token-validation predicate for hello.Token.
	Devices *devices.Registry

	// DevicesPath is the on-disk devices.json path reloaded into Devices
	// immediately before each handshake's token Validate (#782), so a device
	// paired via `pyry pair` after daemon startup is accepted on its next
	// connection without a restart. Optional: "" disables the reload — the
	// handshake validates against the startup-loaded in-memory set only
	// (keeps existing tests byte-stable; mirrors the claudeSessionsDir=""
	// opt-out idiom). On a reload read error the handshake proceeds against
	// the retained in-memory set (fail closed — accept set not widened,
	// loaded devices not lost).
	DevicesPath string

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

	// Snapshotter renders the live claude screen for an inbound
	// request_snapshot (ADR 025 § Safe degradation). Optional: when nil,
	// request_snapshot yields a server.binary_offline error reply — the
	// snapshot feature is simply unavailable, not a crash.
	Snapshotter ScreenSnapshotter

	// KnownConversation reports whether conversationID names a conversation
	// this daemon hosts. request_snapshot rejects an unknown/foreign id with
	// conversation.not_found before any render (AC #4). Optional: when nil,
	// every request_snapshot is rejected as not-found. Production wires it to
	// a conversations.Registry membership check.
	KnownConversation func(conversationID string) bool

	// SnapshotSettings reports the current model / effort / YOLO for the session
	// whose screen the Snapshotter renders (the bootstrap), so
	// handleRequestSnapshot can populate the screen_snapshot reply's settings
	// fields (#848). Optional: nil ⇒ the handler reports the effective defaults
	// (empty model/effort, yolo:false) — preserving the pre-#848 zero-value
	// behaviour. Primitive-typed (three scalars) so internal/relay imports
	// neither internal/sessions nor its SessionSettings type; production wires a
	// closure over *sessions.Pool.DefaultSettings. Empty model/effort mean
	// "inherited daemon default, no per-session override"; yolo:false means
	// permissions enforced.
	//
	// Read-only reflection of an existing, non-secret control — no authz
	// decision, no mutation, no input parsing (contrast SettingsUpdater below,
	// the write path, which is security-sensitive because it mutates the YOLO
	// control from untrusted input).
	SnapshotSettings func() (model, effort string, yolo bool)

	// SnapshotUsage reports the bootstrap session's current context-window
	// occupancy — used tokens and window size — so handleRequestSnapshot can
	// populate the screen_snapshot reply's used_tokens / window_tokens fields
	// (#857). Optional: nil ⇒ the handler reports both at their zero values
	// (used_tokens:0, window_tokens:0), preserving the pre-#857 wire shape (the
	// foreground / unwired case). Primitive-typed (two ints) so internal/relay
	// imports neither internal/contextwindow nor internal/sessions; the cmd/pyry
	// closure resolves the bootstrap transcript path and calls
	// contextwindow.Read, collapsing any open failure to the same zero /
	// window-default report as a fresh session.
	//
	// Read-only reflection of two non-secret aggregate integers — no authz
	// decision, no mutation, no input parsing (same posture as SnapshotSettings
	// above; the transcript content itself never crosses the wire).
	SnapshotUsage func() (usedTokens, windowTokens int)

	// ModalResolver resolves inbound modal_answer / modal_cancel control
	// frames. Optional: when nil, both are inert no-ops (the modal bridge is
	// simply unwired — foreground, or pre-#708 before the producer is live).
	// Production wires the cmd/pyry resolver.
	ModalResolver ModalResolver

	// Interrupter routes an inbound interactive `interrupt` control frame to
	// the supervised claude as one Esc (#707). Optional: nil ⇒ interrupt is
	// inert (no Esc) — the foreground / unwired case. Production wires
	// *supervisor.Supervisor.
	Interrupter Interrupter

	// SessionStarter routes an inbound interactive `new_session` control frame
	// to the supervised claude as a /clear, starting a fresh session (#831).
	// Optional: nil ⇒ new_session is inert (no /clear) — the foreground /
	// unwired case. Production wires *supervisor.Supervisor.
	SessionStarter SessionStarter

	// QueueRemover drops a queued message named by an inbound dequeue_message
	// control frame (#723). Optional: nil ⇒ dequeue_message is inert (foreground
	// / unwired). Production wires *msgqueue.Queue.
	QueueRemover QueueRemover

	// DebugBundler assembles the current session's debug bundle (recent daemon
	// logs plus the newest recording when present) as one in-memory archive, for
	// an inbound request_debug_bundle control frame (#813). Optional: nil ⇒
	// request_debug_bundle replies with a deterministic unavailable error, never
	// a silent drop (foreground / unwired). Production wires a closure over
	// debugbundle.Assemble(recordingsDir, logRing.Snapshot) — the closure returns
	// only (archive, err) so internal/relay never imports internal/debugbundle
	// (the Manifest travels inside the archive as manifest.json, not out-of-band).
	//
	// SECURITY: the returned bytes are the plaintext bundle (recording + logs) —
	// the highest-value secret surface in the system. They MUST NOT be logged.
	// handleDebugBundleRequest streams them ONLY over the AEAD-sealed push path
	// and logs a byte count on success / the failure event on error, never any
	// content byte.
	DebugBundler func() (archive []byte, err error)

	// SettingsUpdater persists an inbound set_session_settings change — a paired
	// interactive phone's per-session model / effort / YOLO (#845). Optional: nil
	// ⇒ set_session_settings replies "unavailable" deterministically, never a
	// silent drop (foreground / unwired). Production wires a *sessions.Pool
	// adapter. The three presence pointers carry no secret; the fail-safe is the
	// pointer-nil semantics (an absent YOLO never enables bypass).
	SettingsUpdater SettingsUpdater

	// OutstandingModals enumerates the daemon's currently-outstanding modals as
	// marshal-ready modal_shown payloads (each already stamped with its original
	// modal_id) for connect-time reconcile (#877). Called on the Run goroutine
	// from handleNoiseInit's interactive-open tail; the returned payloads are
	// unicast to the just-opened conn only. A pure read: it mints no nonce and
	// retires nothing, so it neither re-arms the deny-on-timeout nor changes
	// answerability — a re-sent modal_id stays answerable exactly once, governed
	// by the registry's one-shot Resolve, which this path never calls.
	//
	// A closure returning []protocol.ModalShownPayload, not a *modalbridge.Registry:
	// internal/relay does not import internal/modalbridge, and protocol is already
	// imported, so the payload crosses the boundary with no new import and no cycle
	// (matching SnapshotSettings / SnapshotUsage — define the dependency where it is
	// consumed).
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#877 / foreground /
	// existing-test posture. Production wires modalbridge.Registry.Snapshot.
	OutstandingModals func() []protocol.ModalShownPayload

	// OutstandingQueues enumerates the daemon's current per-conversation queued
	// backlogs as marshal-ready queue_state payloads (one per non-empty
	// conversation) for connect-time reconcile (#878), the queue twin of
	// OutstandingModals. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened
	// conn only. queue_state is snapshot-shaped full state, so the re-send is
	// idempotent by construction. A pure read: it mints no id and dequeues nothing.
	//
	// A closure returning []protocol.QueueStatePayload, not a *msgqueue.Queue:
	// internal/relay does not import internal/msgqueue, and protocol is already
	// imported, so the payload crosses the boundary with no new import and no cycle
	// (matching OutstandingModals — define the dependency where it is consumed).
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#878 / foreground /
	// existing-test posture. Production wires the cmd/pyry outstandingQueues adapter.
	OutstandingQueues func() []protocol.QueueStatePayload
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

	// pushMu is a leaf lock guarding queues (the map) and every pushQueue's
	// contents. Held only around map lookup + enqueue/pop (both O(cap), cap
	// small); NEVER held across an Encrypt, m.send, or any channel op, and
	// never nested with any other lock. It orders below nothing — it is always
	// taken alone. This is what lets an off-Run Push reach the addressed
	// session's queue WITHOUT reading Run-owned m.sessions or touching s.send.
	pushMu sync.Mutex

	// queues maps connID → the per-session bounded push buffer. The KEY SET is
	// Run-managed: a queue is created when handleNoiseInit advances a session
	// to V2StateOpen and deleted by closeWith, both on the Run goroutine. Push
	// only mutates a queue's CONTENTS (under pushMu); it never adds or removes
	// keys. So queue-exists ⟺ session is V2StateOpen for any Run-side observer.
	queues map[string]*pushQueue

	// drainCh signals "some queue has work" to Run's drain arm. Capacity 1 with
	// non-blocking sends (from Push and from the drain's re-signal) collapses
	// concurrent wakes into at most one pending; a Push that enqueues while Run
	// is mid-pass lands its signal into the now-drained channel and triggers the
	// next pass — a self-perpetuating pump with no lost wakeups.
	drainCh chan struct{}

	// replayCh signals "some session has a pending reconnect-replay tail" to
	// Run's replay-drain arm (#777). Mirrors drainCh exactly: capacity 1 with
	// non-blocking sends (from replayMissed and from drainReplayOnce's
	// re-signal) collapses concurrent wakes into at most one pending, so a
	// self-perpetuating pump forwards one replay frame per Run pass with no lost
	// wakeup. Carries no data — the queued events live on each V2Session
	// (replayQueue); this channel references no session and spawns no goroutine,
	// so concurrent multi-conn replays are covered by the single cap-1 channel.
	replayCh chan struct{}

	// snapshot funnels (*V2SessionManager).ActiveConns (and ActiveConnIDs,
	// which projects over it) calls onto Run's dispatch goroutine so the read
	// of m.sessions runs under the single-owner-goroutine invariant, serialised
	// against every map write (lazy-create, delete, state transitions).
	// Unbuffered: backpressure is correct — if Run is busy, the caller waits;
	// the caller's ctx is the escape arm. Not closed by the manager on Run
	// exit; in-flight callers unblock via ctx.Done.
	snapshot chan snapshotReq

	// modalTimeout carries a surfaced modal's id from its time.AfterFunc
	// callback goroutine (armed off-Run by ArmModalTimeout) to the Run goroutine
	// for the deny-on-timeout safe-deny (#725). Daemon-global (a modal is not
	// bound to one conn), keyed by modal_id — unlike wake, which is keyed by
	// *V2Session. Buffered (wakeBufferSize) so a timer callback almost never
	// blocks; callbacks also honour the Run-derived ctx so they unblock cleanly
	// on Run exit and leak no goroutine.
	modalTimeout chan string

	// replayRing + replayCursor are the mid-turn-reconnect replay source
	// (#647), published once after the interactive emitter (which owns the
	// ring) is built — see SetReplaySource for why this is a late-bound setter
	// and not a V2SessionConfig field. Guarded by the pushMu leaf lock: the
	// publish writes them off the Run goroutine during wiring, and replayMissed
	// reads them on the Run goroutine at reconnect (much later, after a full
	// network handshake). nil ring or cursor ⇒ replay disabled (no setter
	// called, or the structured stream is off) — a phone advertising
	// last_event_id then simply gets the live stream, no replay, no resync.
	replayRing   *eventring.Ring
	replayCursor func() string
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
		cfg:          cfg,
		sessions:     make(map[string]*V2Session),
		wake:         make(chan wakeSignal, wakeBufferSize),
		manualRekey:  make(chan manualRekeyReq),
		queues:       make(map[string]*pushQueue),
		drainCh:      make(chan struct{}, 1),
		replayCh:     make(chan struct{}, 1),
		snapshot:     make(chan snapshotReq),
		modalTimeout: make(chan string, wakeBufferSize),
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
		case modalID := <-m.modalTimeout:
			m.handleModalTimeout(runCtx, modalID)
		case req := <-m.manualRekey:
			req.reply <- m.handleManualRekey(runCtx, req.connID)
		case <-m.drainCh:
			m.drainOnce(runCtx)
		case <-m.cfg.Reconnect:
			// Fresh transport conn: wake the drain so any #874-held head flushes
			// now (not on the next Push). Re-signal drainCh — do not call
			// drainOnce here — so the existing drain arm owns the single pop path
			// and its FIFO self-re-signal. A nil m.cfg.Reconnect makes this a
			// nil-channel read, permanently not-ready, so the select behaves
			// exactly as pre-#875.
			select {
			case m.drainCh <- struct{}{}:
			default:
			}
		case <-m.replayCh:
			m.drainReplayOnce(runCtx)
		case req := <-m.snapshot:
			req.reply <- m.handleActiveConns()
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
		if m.transportDown() {
			// #912: the relay leg is down, so a rekey_request sealed now
			// would burn a Noise send-nonce on a frame m.send silently
			// drops — gapping the phone's recv nonce and, one reply window
			// later, tearing this healthy session down as a mislabelled
			// noise.rekey_failed. Defer instead: seal nothing, arm no reply
			// window, and re-arm a short retry so the emit lands once the
			// transport recovers. The fired 1-hour timer that delivered
			// this wake is one-shot and inert, so overwriting rekeyTimer
			// needs no Stop(). A permanently-down phone is reaped by the
			// idle sweep, so this retry loop cannot spin forever.
			w.s.rekeyTimer = m.armRekeyRetryTimer(ctx, w.s)
			m.cfg.Logger.Info("relay: v2 rekey emit deferred (transport down)",
				"event", "v2.rekey.emit.deferred_transport_down",
				"conn_id", w.s.connID,
				"reason", "scheduled",
				"retry_in", rekeyRetryInterval)
			return
		}
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
	case wakeIdleTimeout:
		if idle := time.Since(w.s.lastActivityAt); idle < idleTimeout {
			// A frame arrived after this timer was armed but before the
			// wake was serviced; handleFrame already re-stamped
			// lastActivityAt. Reschedule for the remaining window so the
			// timer fires at exactly lastActivityAt + idleTimeout — no
			// active session is ever torn down by a stale wake.
			w.s.idleTimer = m.armIdleTimer(ctx, w.s, idleTimeout-idle)
			return
		}
		// Genuinely idle: no inbound frame for idleTimeout. Tear down
		// through the existing close path, which drops both CipherStates,
		// stops every per-session timer, and removes the session + push
		// queue.
		m.cfg.Logger.Info("relay: v2 idle teardown",
			"event", "v2.idle.teardown",
			"conn_id", w.s.connID,
			"close_code", int(StatusIdleTimeout))
		m.closeWith(ctx, w.s, StatusIdleTimeout, nil)
	}
}

// armIdleTimer arms the idle-sweep timer (#774) for d. The callback runs
// on a fresh runtime goroutine (time.AfterFunc semantics); it pushes a
// wakeIdleTimeout signal onto m.wake under blocking-send + ctx.Done
// semantics, exactly like armRekeyTimer. The initial arm (in
// handleNoiseInit's success tail) passes idleTimeout; the reschedule arm
// (in handleWake) passes the remaining window so the timer fires at
// exactly lastActivityAt + idleTimeout. The callback touches only m.wake
// — never s.send / s.recv / s.state / m.sessions — so it cannot race the
// cipher states. ctx is the manager's Run-derived runCtx; cancelled on
// Run exit, which unblocks any pending callback goroutine.
func (m *V2SessionManager) armIdleTimer(ctx context.Context, s *V2Session, d time.Duration) *time.Timer {
	return time.AfterFunc(d, func() {
		select {
		case m.wake <- wakeSignal{s: s, kind: wakeIdleTimeout}:
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
	// Every inbound frame — handshake or app — counts as activity for the
	// idle sweep (#774). Stamp before dispatch so the idle timer chases the
	// freshest deadline; a torn-down conn's late frame was dropped by the
	// check above first. A single field write on the hot path — no timer op
	// here; the armed idleTimer reads this on its next fire (handleWake).
	s.lastActivityAt = time.Now()

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

// dispatchAppFrame runs dispatch.Route on a per-frame *dispatch.Conn and
// forwards each reply envelope the handler emits: AEAD-sealed under
// s.send, wrapped as noise_msg, and sent via m.send. Route runs on a
// short-lived goroutine while this (the Run) goroutine drains the reply
// channel concurrently, so a handler emitting more replies than the
// per-frame outbound buffer (handlerOutboundBuf) holds cannot fill the
// channel and deadlock Route inside c.Send (#909). Returns only after
// Route has returned and every buffered reply has been drained.
//
// The seal (s.send.Encrypt, via forwardAppReply) runs only on the Run
// goroutine — the Noise send CipherState is single-owner; only Route →
// handler → c.Send (a marshal + channel push, no AEAD) runs off-Run.
//
// The outbound channel is deliberately NOT closed — a misbehaving handler
// that forks a sender after Route returns writes into a leaked but
// capacity-bounded channel that the GC reclaims once the goroutine exits;
// closing here would panic such a sender (#446).
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
	if err := json.Unmarshal(plaintext, &probeEnv); err == nil {
		switch probeEnv.Type {
		case protocol.TypeRekeyRequest:
			m.handleRekeyRequest(ctx, s, probeEnv)
			return
		case protocol.TypeRequestSnapshot:
			m.handleRequestSnapshot(ctx, s, probeEnv)
			return
		case protocol.TypeModalCancel:
			m.handleModalCancel(ctx, s, probeEnv)
			return
		case protocol.TypeModalAnswer:
			m.handleModalAnswer(ctx, s, probeEnv)
			return
		case protocol.TypeInterrupt:
			m.handleInterrupt(s)
			return
		case protocol.TypeNewSession:
			m.handleNewSession(s)
			return
		case protocol.TypeDequeueMessage:
			m.handleDequeueMessage(s, probeEnv)
			return
		case protocol.TypeRequestDebugBundle:
			m.handleDebugBundleRequest(ctx, s, probeEnv)
			return
		case protocol.TypeSetSessionSettings:
			m.handleSetSessionSettings(ctx, s, probeEnv)
			return
		}
	}

	outbound := make(chan protocol.RoutingEnvelope, handlerOutboundBuf)
	conn := dispatch.NewConn(s.connID, outbound, s.device)

	// Run dispatch.Route on a short-lived goroutine and drain its reply
	// envelopes here on the Run goroutine, so a handler emitting more than
	// handlerOutboundBuf replies cannot fill outbound and deadlock Route
	// inside c.Send — this loop empties outbound continuously while Route
	// runs. Only Route (→ handler → c.Send: a marshal + channel push, no
	// AEAD) runs off-Run; every seal (forwardAppReply → s.send.Encrypt)
	// stays on the Run goroutine, keeping the Noise send CipherState
	// single-owner. A single sender (the handler) plus this single
	// receiver preserves FIFO emission order end to end.
	//
	// ctx.Done() is deliberately NOT a third select arm: the handler
	// already observes cancellation through c.Send's ctx arm (ctx is
	// runCtx), so a shutdown drives Route to return and fires routeDone
	// naturally. Adding a ctx arm would risk orphaning the still-running
	// Route goroutine and dropping ordered replies mid-stream.
	routeDone := make(chan struct{})
	go func() {
		defer close(routeDone)
		dispatch.Route(ctx, m.cfg.Logger, conn, m.cfg.Handlers, plaintext)
	}()
	for {
		select {
		case reply := <-outbound:
			m.forwardAppReply(s, reply)
		case <-routeDone:
			// Route returned (the handler has returned, so no further
			// c.Send is in flight); drain any residual buffered replies in
			// FIFO order, then return.
			for {
				select {
				case reply := <-outbound:
					m.forwardAppReply(s, reply)
				default:
					return
				}
			}
		}
	}
}

// forwardAppReply seals one handler reply under s.send and forwards it as
// a noise_msg via m.send. MUST run only on the manager's Run goroutine —
// s.send is the single-owner Noise send CipherState, and a concurrent
// Encrypt would reuse a nonce / corrupt the send counter on the encrypted
// wire. Drops the reply (WARN, no wire emission) on the
// realistically-unreachable seal/marshal error, exactly as #446: never
// emit an unsealed frame.
func (m *V2SessionManager) forwardAppReply(s *V2Session, reply protocol.RoutingEnvelope) {
	ciphertext, err := s.send.Encrypt(reply.Frame)
	if err != nil {
		// Realistically unreachable under correct flynn/noise. Drop the
		// reply rather than emit the unencrypted frame.
		m.cfg.Logger.Warn("relay: v2 seal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	frame, err := marshalInnerFrameV2(protocol.TypeNoiseMsg, ciphertext)
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 marshal app reply failed; reply dropped",
			"conn_id", s.connID)
		return
	}
	// The reply's CloseCode is ignored: handlers do not signal closes
	// through c.Send/c.Reply; the close-code field on the routing envelope
	// is reserved for the manager's own close-intent emissions (closeWith).
	m.send(protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame})
}

// Static error messages for request_snapshot replies. Deliberately generic:
// the wire reply NEVER echoes the JSON decode error or the (attacker-
// controlled) raw conversation_id, only one of these constants.
const (
	msgSnapshotConvNotFound = "unknown or foreign conversation_id"
	msgSnapshotOffline      = "no live claude session"
)

// handleRequestSnapshot renders the current claude screen and pushes a
// screen_snapshot addressed to s, or a deterministic error reply. It is the
// inbound-control handler for TypeRequestSnapshot — intercepted in
// dispatchAppFrame before dispatch.Route, exactly like handleRekeyRequest —
// and runs on the manager's single Run dispatch goroutine. Every branch pushes
// exactly one reply and returns: it never panics, hangs, or silently drops the
// request (AC #3).
//
// Both the success and error replies are delivered via m.forwardEnvelope — the
// single existing seal-and-forward path. The public Push is deliberately NOT
// used here: it would enqueue the reply onto the buffered push stream (subject
// to the drop policy and a deferred drain pass), whereas a snapshot reply is
// InReplyTo-correlated and must seal immediately and in-line on this same Run
// goroutine.
//
// SECURITY: the rendered screen text is NEVER logged; error replies carry only
// a static message constant. The conversation_id is validated before any render
// (AC #4): an unknown/foreign id renders nothing.
func (m *V2SessionManager) handleRequestSnapshot(ctx context.Context, s *V2Session, env protocol.Envelope) {
	var payload protocol.RequestSnapshotPayload
	// A decode failure is tolerated: it leaves ConversationID == "", which the
	// KnownConversation check below rejects as not-found. The decode error is
	// never echoed back to the phone.
	_ = json.Unmarshal(env.Payload, &payload)

	// AC #4: reject an unknown/foreign conversation_id before any render. A nil
	// KnownConversation (optional seam) rejects everything as not-found.
	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(payload.ConversationID) {
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeConversationNotFound, msgSnapshotConvNotFound, false)
		return
	}

	// AC #3: a nil Snapshotter (optional seam) means the feature is
	// unavailable; report it deterministically rather than dropping.
	if m.cfg.Snapshotter == nil {
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}
	text, live := m.cfg.Snapshotter.ScreenSnapshot()
	if !live {
		// AC #3: no claude child attached (between restarts / idle-evicted).
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}

	// Reflect the bootstrap session's persisted model / effort / YOLO (#848). A
	// nil seam (optional, foreground / unwired) leaves the three at their
	// defaults — empty model/effort ("inherited daemon default"), yolo:false
	// (permissions enforced) — byte-identical to the pre-#848 zero-value reply.
	var model, effort string
	var yolo bool
	if m.cfg.SnapshotSettings != nil {
		model, effort, yolo = m.cfg.SnapshotSettings()
	}

	// Reflect the bootstrap session's current context-window occupancy (#857). A
	// nil seam (optional, foreground / unwired) leaves both at zero — byte-
	// identical to the pre-#857 reply apart from the two always-present fields.
	// The cmd/pyry closure collapses any transcript-open failure to the same
	// zero/window-default report, so this read never errors.
	var usedTokens, windowTokens int
	if m.cfg.SnapshotUsage != nil {
		usedTokens, windowTokens = m.cfg.SnapshotUsage()
	}

	snapPayload, err := json.Marshal(protocol.ScreenSnapshotPayload{
		ConversationID: payload.ConversationID,
		Text:           text,
		TS:             time.Now().UTC(),
		Model:          model,
		Effort:         effort,
		YOLO:           yolo,
		UsedTokens:     usedTokens,
		WindowTokens:   windowTokens,
	})
	if err != nil {
		// ScreenSnapshotPayload is a closed struct of scalars + a time; marshal
		// cannot fail in practice. Defensive — NEVER echo err (it could quote the
		// rendered text). Fall back to a deterministic error reply so the request
		// is still answered, never silently dropped (AC #3).
		m.cfg.Logger.Warn("relay: v2 screen_snapshot marshal failed",
			"event", "v2.snapshot.marshal_err",
			"conn_id", s.connID,
			"conversation_id", payload.ConversationID)
		m.snapshotReplyError(ctx, s, env.ID, protocol.CodeServerBinaryOffline, msgSnapshotOffline, true)
		return
	}
	inReplyTo := env.ID
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeScreenSnapshot,
		TS:        time.Now().UTC(),
		Payload:   snapPayload,
		InReplyTo: &inReplyTo,
	}
	m.cfg.Logger.Info("relay: v2 screen snapshot served",
		"event", "v2.snapshot.served",
		"conn_id", s.connID,
		"conversation_id", payload.ConversationID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture;
		// NEVER echo the rendered text.
		m.cfg.Logger.Debug("relay: v2 screen_snapshot push dropped",
			"event", "v2.snapshot.push_err",
			"conn_id", s.connID,
			"err", err)
	}
}

// snapshotReplyError pushes a single TypeError reply to s, correlated to
// inReplyTo, via the same m.forwardEnvelope seal-and-forward path the success
// reply uses (no parallel send path). message MUST be a static constant —
// never attacker-controlled bytes.
func (m *V2SessionManager) snapshotReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, code, message string, retryable bool) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 snapshot error reply marshal failed",
			"event", "v2.snapshot.err_marshal",
			"conn_id", s.connID,
			"code", code)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 snapshot error reply push dropped",
			"event", "v2.snapshot.err_push",
			"conn_id", s.connID,
			"code", code,
			"err", err)
	}
}

// msgDebugBundleUnavailable is the static message on every request_debug_bundle
// error reply. Deliberately generic: the wire reply NEVER echoes the assembly
// error text — which could quote a recording path or filename — nor any content
// byte, only this constant. The only failure this verb reports is "unavailable"
// (nil DebugBundler seam, or an assembly failure), so no per-cause message is
// needed.
const msgDebugBundleUnavailable = "debug bundle unavailable"

// handleDebugBundleRequest assembles the current session's debug bundle via the
// injected DebugBundler and streams it back to s as debug_bundle_chunk* +
// debug_bundle_done (#812), or sends a single deterministic error reply.
// Intercepted in dispatchAppFrame before dispatch.Route — like handleInterrupt /
// handleRequestSnapshot — and runs on the manager's single Run dispatch
// goroutine. Every branch either enqueues one bundle stream or sends exactly one
// error reply, then returns: it never panics, hangs, or silently drops the
// request.
//
// The request frame is bare (no payload): the bundle is daemon-global (the whole
// log ring plus the newest recording across all sessions, per #811), so there is
// no attacker-controlled field — no conversation_id, no path, no id — that flows
// into assembly or the wire, and no argument that could select another session's
// data.
//
// SECURITY (AC #4): the assembled archive bytes are the plaintext bundle
// (recording + logs) — the highest-value secret surface in the system. They are
// streamed ONLY over the AEAD-sealed push path (StreamBundle → Push → drainOnce
// seals every chunk before m.send); this method logs a content-free byte count
// on success and the failure EVENT on error — never the archive, a member, a log
// line, or a recording byte. Every error reply carries only the static
// msgDebugBundleUnavailable constant, never the assembly error text.
func (m *V2SessionManager) handleDebugBundleRequest(ctx context.Context, s *V2Session, env protocol.Envelope) {
	// A nil DebugBundler (optional seam / foreground / unwired) means the feature
	// is unavailable; report it deterministically rather than dropping.
	if m.cfg.DebugBundler == nil {
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	// #911: bound this conn to one in-flight bundle. If a prior bundle's chunks
	// are still queued (a slow/stalled transport has not drained them), a retry
	// must not stack a second bundle's never-droppable control frames onto the
	// queue — that is unbounded per-retry memory growth (~4/3 × archive per
	// stacked bundle). Reply with the same deterministic retryable "unavailable"
	// error as the branches below, so the phone retries later; once the prior
	// bundle drains the gate clears and the retry is served (AC #4). Placed BEFORE
	// DebugBundler() so the on-disk assembly is skipped, not merely the enqueue
	// (AC #1: "no second bundle is assembled or enqueued").
	if m.bundleInFlight(s.connID) {
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	archive, err := m.cfg.DebugBundler()
	if err != nil {
		// #811 read-failure honesty: a recording that exists but fails to read
		// surfaces as an error, not a false-absent. Log the failure EVENT only —
		// NEVER the wrapped err (it could quote a recording path/filename) — and
		// send a deterministic error reply.
		m.cfg.Logger.Warn("relay: v2 debug bundle assemble failed",
			"event", "v2.bundle.assemble_err",
			"conn_id", s.connID)
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	if err := m.StreamBundle(ctx, s.connID, archive); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine, so
		// its push queue exists. Logged at debug and dropped — the package's
		// outbound-drop posture; NEVER echo the archive.
		m.cfg.Logger.Debug("relay: v2 debug bundle stream dropped",
			"event", "v2.bundle.stream_err",
			"conn_id", s.connID,
			"err", err)
		return
	}

	// One content-free info log: conn_id + byte count only, never the archive or
	// any member (AC #4).
	m.cfg.Logger.Info("relay: v2 debug bundle served",
		"event", "v2.bundle.served",
		"conn_id", s.connID,
		"bytes", len(archive))
}

// debugBundleReplyError sends a single deterministic TypeError reply to s,
// correlated to inReplyTo, via the same m.forwardEnvelope seal-and-forward path
// handleRequestSnapshot uses. The code/message/retryable are FIXED
// (server.binary_offline + msgDebugBundleUnavailable + retryable), because the
// only failure this verb reports is "unavailable" — so no attacker-influenced or
// assembly-error text ever reaches the wire.
func (m *V2SessionManager) debugBundleReplyError(ctx context.Context, s *V2Session, inReplyTo uint64) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      protocol.CodeServerBinaryOffline,
		Message:   msgDebugBundleUnavailable,
		Retryable: true,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 debug bundle error reply marshal failed",
			"event", "v2.bundle.err_marshal",
			"conn_id", s.connID)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 debug bundle error reply push dropped",
			"event", "v2.bundle.err_push",
			"conn_id", s.connID,
			"err", err)
	}
}

// bundleInFlight reports whether connID's push queue still holds any
// debug_bundle_chunk or debug_bundle_done envelope from a prior bundle — i.e.
// a StreamBundle's frames enqueued by an earlier handleDebugBundleRequest have
// not all drained yet. It is the per-conn in-flight gate for #911: while it is
// true, a repeated request_debug_bundle on the same conn is rejected before any
// assembly or enqueue, so a client retry loop against a slow/stalled transport
// cannot stack a second bundle's never-droppable control frames (unbounded
// per-retry memory).
//
// Both types are scanned because StreamBundle enqueues all N chunks PLUS the
// trailing debug_bundle_done in one handler invocation and drainOnce pops one
// per Run pass: during the drain the queue holds a shrinking suffix that always
// includes the done marker until the very last pop, so scanning for either type
// covers the whole in-flight window and clears exactly when the done marker has
// also drained (AC #4).
//
// An unknown conn (!ok) has no queue and cannot be bundle-busy → false, the safe
// direction (a non-open conn's later StreamBundle/Push fails closed with
// ErrConnNotFound anyway).
//
// pushMu is taken ALONE for a pure O(len(items)) read of q.items and released
// before the caller's reply, preserving the leaf lock's "never held across an
// Encrypt, m.send, or any channel op … always taken alone" invariant. The scan
// runs on the Run dispatch goroutine, serialized against this conn's own
// drainOnce pops; the only concurrent mutator is an off-Run Push, which can only
// append frames — it can turn a false into a true (more conservative), never
// clear a true — so there is no TOCTOU that admits a second bundle.
func (m *V2SessionManager) bundleInFlight(connID string) bool {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	q, ok := m.queues[connID]
	if !ok {
		return false
	}
	for i := range q.items {
		switch q.items[i].env.Type {
		case protocol.TypeDebugBundleChunk, protocol.TypeDebugBundleDone:
			return true
		}
	}
	return false
}

// SetReplaySource publishes the mid-turn-reconnect replay source to the manager
// (#647). It is called once during relay wiring, AFTER the interactive emitter
// (which owns the eventring) is constructed: the emitter and manager have a
// circular dependency (the emitter takes the manager as its broadcaster; the
// replay path needs the emitter-owned ring), so the ring does not exist when
// NewV2SessionManager runs — a construction-time V2SessionConfig field is not
// buildable without the constructor cascade #646 avoided. A late-bound setter
// is the seam that breaks the cycle. ring is the emitter's per-conversation
// event ring; currentConv resolves the conversation a reconnecting conn replays
// for (the supervisor's #312 cursor).
//
// Stored under pushMu (the existing leaf lock, taken alone): this write happens
// off the Run goroutine during wiring, and replayMissed reads them on the Run
// goroutine at reconnect — much later, after a full network handshake. A nil
// ring or cursor leaves replay disabled. Idempotent by construction (the wiring
// calls it once).
func (m *V2SessionManager) SetReplaySource(ring *eventring.Ring, currentConv func() string) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.replayRing = ring
	m.replayCursor = currentConv
}

// replayMissed classifies a mid-turn-reconnect for s after its hello advertised
// last_event_id=afterID (#647) and, when there is a tail to replay, hands it to
// the paced replay drain (#777). It runs on the manager's single Run goroutine
// at the tail of handleNoiseInit's success path. The classification work — the
// ring read, the gap→resync branch, and the #663 watermark clamp — completes
// inline; the tail itself is stored in s.replayQueue and forwarded one event per
// Run pass by drainReplayOnce, so a large replay never monopolises Run. Because
// replayMissed populates replayQueue on this pass (before Run returns to its
// select) and the drainOnce gate holds the conn's live push queue until the tail
// empties, every replay frame still reaches the wire before any live frame for
// this conn — AC-2's "before the live stream resumes", now preserved by the gate
// rather than by inline completion. Replay frames seal via forwardEnvelope (the
// established handleRequestSnapshot inline-reply pattern), not the buffered push
// stream.
//
// afterID is untrusted remote input (AC-5): it is only ever an index into the
// self-synchronised ring (ring.After's own mutex makes the read safe off the
// emitter goroutine), scoped to the daemon-resolved conversation (cursor()),
// never to a conversation the phone names. Work is bounded by what the ring
// retains (MaxEventsPerConversation); a hostile-large id classifies as
// caught-up (zero work). SECURITY: replayed payloads are the same structured
// envelopes #649 already streams to this authenticated conn; the bytes are
// never logged.
func (m *V2SessionManager) replayMissed(ctx context.Context, s *V2Session, afterID uint64) {
	m.pushMu.Lock()
	ring, cursor := m.replayRing, m.replayCursor
	m.pushMu.Unlock()
	if ring == nil || cursor == nil {
		return // replay disabled: no SetReplaySource, or the stream is off.
	}
	convID := cursor()
	if convID == "" {
		return // no active conversation; nothing to catch up on.
	}

	// Read the newest retained id BEFORE classifying with After: any event the
	// emitter appends concurrently then carries an id > newest and reaches the
	// live stream instead of the clamp below (staleness can only lower the
	// watermark — deliver more — never raise it, the safe direction for the
	// never-a-silent-gap guarantee).
	newest := ring.NewestID(convID)
	events, gap := ring.After(convID, afterID)
	if gap {
		// The requested position aged out of the bounded ring (AC-4): emit one
		// honest resync marker telling the phone to full-reload, never a
		// partial gap-ful replay. Leave replayThrough untouched — the phone
		// discards its cursor and must accept all live events afterward.
		m.emitResync(ctx, s, convID)
		return
	}
	// Clamp the watermark to server-known reality (#663). afterID is untrusted:
	// a stale cross-/clear id or a hostile 2^64-1 would otherwise set the
	// watermark above this conversation's id space and silently mute every live
	// frame at or below it. min preserves legitimate same-conversation dedup
	// (afterID == newest in the caught-up case there); during the loop the
	// watermark trails one event behind the frame being forwarded, so
	// forwardEnvelope's guard never self-drops a replay envelope.
	s.replayThrough = min(afterID, newest)
	if len(events) == 0 {
		return // caught up: After returned no tail; the clamp above is all the work.
	}
	// Hand the tail to the paced drain instead of forwarding it inline (#777):
	// sealing and forwarding up to MaxEventsPerConversation frames in this single
	// Run pass would stall every other conn's delivery (and inbound frames,
	// wakes, snapshots) until the whole batch finished. drainReplayOnce forwards
	// one event per Run pass, advancing replayThrough as it goes, so Run returns
	// to its select between frames. events is the fresh copy ring.After already
	// materialised — bounded by ring retention, never the push cap, so no
	// drop/gap on this path.
	s.replayQueue = events
	select {
	case m.replayCh <- struct{}{}:
	default:
	}
}

// emitResync forwards a single resync marker to s, signalling that its
// advertised last_event_id aged out of the ring and it must do a full reload of
// convID (#647, AC-4). The marker is a TypeResync control envelope carrying
// only convID in an inline anonymous payload — no named protocol payload type,
// mirroring emitRekeyRequest's payload-less inline-struct control precedent. It
// carries NO EventID (it is not a structured event), so forwardEnvelope's
// replay-watermark guard never touches it.
//
// SECURITY: convID is the daemon's own resolved conversation id, never
// attacker-derived; the marker exposes no buffered conversation content.
func (m *V2SessionManager) emitResync(ctx context.Context, s *V2Session, convID string) {
	payload, err := json.Marshal(struct {
		ConversationID string `json:"conversation_id"`
	}{ConversationID: convID})
	if err != nil {
		// A closed struct of one string; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 resync marshal failed",
			"event", "v2.replay.resync_marshal_failed",
			"conn_id", s.connID)
		return
	}
	marker := protocol.Envelope{
		ID:      1, // non-load-bearing; the phone keys resync on Type, not ID.
		Type:    protocol.TypeResync,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
	m.cfg.Logger.Info("relay: v2 reconnect resync",
		"event", "v2.replay.resync",
		"conn_id", s.connID,
		"conversation_id", convID)
	if err := m.forwardEnvelope(ctx, s.connID, marker); err != nil {
		m.cfg.Logger.Debug("relay: v2 resync marker dropped",
			"event", "v2.replay.resync_dropped",
			"conn_id", s.connID,
			"err", err)
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
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	// Drop any in-flight reconnect-replay tail (#777). The session is deleted
	// from m.sessions just below, so drainReplayOnce's scan can no longer find
	// it; niling matches the timer-cleanup pattern and releases the slice for GC.
	s.replayQueue = nil
	delete(m.sessions, s.connID)
	// Symmetric with the create in handleNoiseInit: drop the per-session push
	// buffer. Any buffered-but-undrained envelopes are discarded — the conn is
	// terminal (#611 reconnect replay, not this ticket, reconciles a returning
	// phone). The close envelope itself is sent synchronously below, bypassing
	// the buffer (it is terminal, not part of the ordered push stream).
	m.pushMu.Lock()
	delete(m.queues, s.connID)
	m.pushMu.Unlock()
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

// Push enqueues env onto the addressed session's bounded push buffer and
// returns immediately — it NEVER blocks on the relay/send path. The Run
// goroutine drains the buffer on its own schedule (drainOnce), sealing each
// envelope under s.send in order; so a slow or stalled relay can never wedge
// the calling producer/dispatch goroutine (the ADR-025 open-risk guard,
// decisions/025 line 220). Safe to call from any goroutine: Push touches only
// m.queues (under pushMu) — never s.send, m.sessions, or Outbound.
//
// connID names a specific connected phone. The caller owns env entirely
// (Type, ID, TS, Payload); Push performs no envelope validation — it is a
// transport primitive.
//
// Under pressure (queue at capacity) the event-class-aware drop policy runs
// pre-seal (pushQueue.enqueue): an assistant_delta evicts the oldest queued
// delta (drop-oldest); a control event is admitted by evicting a droppable
// delta and is never itself dropped. A drop is not an error — Push returns nil
// and debug-logs the running count + the dropped class (env.Type only; never
// payload bytes).
//
// Returns ErrConnNotFound (wraps control.ErrConnNotFound) when no queue exists
// for connID — i.e. the session never reached V2StateOpen, was never seen, or
// has been torn down (closeWith deletes the queue). This collapses the former
// "session not open" case into ErrConnNotFound: a not-open conn has no queue.
// The V2StateOpen security gate is preserved on the drain side — forwardEnvelope
// re-checks s.state before sealing, so a buffered push to a conn that closed or
// de-authed before drain is dropped there, never delivered to an
// un-authenticated peer. Returns ctx.Err() only when ctx is already cancelled
// at entry (preserves the emitter's ctx-teardown branch). Both production
// callers (#632 emitter, #589 coarse bridge) only debug-log the error, so the
// collapse is invisible to them.
func (m *V2SessionManager) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.pushMu.Lock()
	q, ok := m.queues[connID]
	if !ok {
		m.pushMu.Unlock()
		return ErrConnNotFound
	}
	dropped := q.enqueue(env)
	droppedCount := q.dropped
	m.pushMu.Unlock()

	// Non-blocking wake: a cap-1 channel + this default coalesces concurrent
	// signals; the drain re-signals if more work remains, so no wake is lost.
	select {
	case m.drainCh <- struct{}{}:
	default:
	}

	if dropped {
		m.cfg.Logger.Debug("relay: v2 push drop under pressure",
			"event", "v2.push.drop",
			"conn_id", connID,
			"dropped", droppedCount,
			"type", env.Type)
	}
	return nil
}

// transportDown reports whether the push drain should hold the head rather than
// seal-and-forward it (#874). A nil Connected seam ⇒ never down ⇒ the drain path
// is byte-identical to pre-#874 (the foreground / unwired / existing-test case).
// Called off-lock on the Run goroutine, BEFORE pushMu, so pushMu's "taken alone,
// never held across an external call" invariant is preserved.
func (m *V2SessionManager) transportDown() bool {
	return m.cfg.Connected != nil && !m.cfg.Connected()
}

// drainOnce pops at most ONE buffered envelope across all sessions and forwards
// it on the Run goroutine. Popping one-per-pass (rather than draining a whole
// buffer) is the "a slow m.send must not re-block the producer" guard: Run
// returns to its select between sends, so ActiveConns / inbound frames / wakes
// are serviced with at most one in-flight Outbound (≤ one WriteTimeout) of
// delay. If any queue still has items after the pop, it re-signals drainCh.
func (m *V2SessionManager) drainOnce(ctx context.Context) {
	// Transport-down HOLD (#874): if the relay leg is down, pop nothing, seal
	// nothing, re-signal nothing — leave the head un-popped and unsealed so no
	// Noise send-nonce is burned for a frame that cannot reach the phone (a
	// burned nonce gaps the phone's recv nonce → 4421 close of a still-live
	// session). The probe runs off-lock, before pushMu, and BEFORE the seal in
	// forwardEnvelope — the post-send Outbound error is too late, the nonce is
	// already gone. The next Push re-signals drainCh (the existing lazy flush),
	// which re-enters here; once the transport recovers the held head drains in
	// FIFO order (immediate flush-on-reconnect is #875). Session-level failures
	// (ErrConnNotFound / ErrSessionNotOpen / seal failure) still drop, downstream
	// in forwardEnvelope, reached only on this transport-up path (AC2).
	if m.transportDown() {
		return
	}
	// A conn with an in-flight reconnect-replay tail must keep its live push
	// queue buffered until the replay finishes, so replay ids (<= replayThrough)
	// reach the wire before live ids (#777, AC #2). Snapshot the gated conn-ids
	// from m.sessions BEFORE taking pushMu: m.sessions and replayQueue are
	// Run-owned and drainOnce runs on Run, so this read needs no lock, and
	// keeping it outside pushMu preserves pushMu's "guards only m.queues, taken
	// alone" invariant. drainReplayOnce re-signals drainCh when a tail empties,
	// so a gated conn's buffered live events are never stranded.
	var replayPending map[string]struct{}
	for id, s := range m.sessions {
		if len(s.replayQueue) > 0 {
			if replayPending == nil {
				replayPending = make(map[string]struct{})
			}
			replayPending[id] = struct{}{}
		}
	}

	m.pushMu.Lock()
	var (
		connID string
		env    protocol.Envelope
		found  bool
	)
	// Go randomises map-range order, giving rough fairness across the
	// realistically-tiny open-conn count.
	for id, q := range m.queues {
		if len(q.items) == 0 {
			continue
		}
		if _, gated := replayPending[id]; gated {
			continue // replay in flight for this conn; hold its live events (#777).
		}
		connID = id
		env = q.items[0].env
		q.items[0] = queuedEnv{} // release the envelope for GC; slot slides out below
		q.items = q.items[1:]    // pop head (FIFO)
		found = true
		break
	}
	// After the pop, note whether any UNGATED queue still has work to re-signal.
	// Gated queues are excluded: their re-signal comes from drainReplayOnce when
	// the replay tail empties, not from the push pump.
	more := false
	if found {
		for id, q := range m.queues {
			if len(q.items) == 0 {
				continue
			}
			if _, gated := replayPending[id]; gated {
				continue
			}
			more = true
			break
		}
	}
	m.pushMu.Unlock()

	if !found {
		return
	}
	if err := m.forwardEnvelope(ctx, connID, env); err != nil {
		// Session vanished / not open / seal failure: drop with no app content
		// in the log (the package's outbound-drop posture). The V2StateOpen
		// security gate lives in forwardEnvelope.
		m.cfg.Logger.Debug("relay: v2 push drain drop",
			"event", "v2.push.drain_drop",
			"conn_id", connID,
			"err", err)
	}
	if more {
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
	}
}

// drainReplayOnce forwards at most ONE buffered reconnect-replay event across
// all sessions on the Run goroutine, then returns to the select — the same
// one-per-pass yielding drainOnce provides for the push stream, so a large
// mid-turn-reconnect replay (up to MaxEventsPerConversation events) can no
// longer monopolise Run and stall other conns' delivery, inbound frames, wakes,
// or snapshots (#777, AC #1). replayQueue and replayThrough are Run-owned (no
// pushMu): the scan, pop, and watermark advance all run on this goroutine — the
// same single-writer regime forwardEnvelope's s.send.Encrypt relies on, so the
// Noise send-nonce sequence stays monotonic (AC #3).
//
// Re-signal discipline mirrors drainOnce: when the popped conn's tail empties it
// signals drainCh to release the live events the drainOnce gate held back (they
// carry ids > replayThrough, so they drain in order after the replay, AC #2);
// while any session still has a tail it re-signals replayCh to keep the pump
// running (covers concurrent multi-conn replays through the single cap-1
// channel). On a forward error the conn's remaining tail is abandoned (matching
// the old inline loop's early return), but the re-signal scan still runs so a
// different session's replay is never stranded.
func (m *V2SessionManager) drainReplayOnce(ctx context.Context) {
	var s *V2Session
	// Go randomises map-range order, giving rough cross-conn fairness across the
	// realistically-tiny open-conn count (same as drainOnce).
	for _, cand := range m.sessions {
		if len(cand.replayQueue) > 0 {
			s = cand
			break
		}
	}
	if s == nil {
		return
	}

	ev := s.replayQueue[0]
	s.replayQueue[0] = eventring.Event{} // release the event for GC; slot slides out below
	s.replayQueue = s.replayQueue[1:]    // pop head (ascending id order)

	id := ev.ID // per-frame local; never &ev.ID of the loop-scoped copy.
	replay := protocol.Envelope{
		ID:      ev.ID, // per-conn id ascending + self-consistent within the replay.
		Type:    ev.Type,
		TS:      ev.TS,
		Payload: ev.Payload,
		EventID: &id, // required: the phone advances its cursor from this.
	}
	if err := m.forwardEnvelope(ctx, s.connID, replay); err != nil {
		// Session vanished / seal failure: log at debug and abandon this conn's
		// remaining tail — the package's outbound-drop posture (mirrors the old
		// inline loop's early return). NEVER echo payload/ciphertext/key bytes.
		m.cfg.Logger.Debug("relay: v2 reconnect replay frame dropped",
			"event", "v2.replay.frame_dropped",
			"conn_id", s.connID,
			"err", err)
		s.replayQueue = nil
	} else {
		s.replayThrough = ev.ID // trailing-watermark advance (identical to the old inline loop).
	}

	// This conn's replay is done (drained to empty or abandoned on error):
	// release the live events the drainOnce gate held back for it.
	if len(s.replayQueue) == 0 {
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
	}
	// Keep the pump running while ANY session still has a replay tail.
	for _, cand := range m.sessions {
		if len(cand.replayQueue) > 0 {
			select {
			case m.replayCh <- struct{}{}:
			default:
			}
			break
		}
	}
}

// forwardEnvelope runs on Run's dispatch goroutine. It looks up the session,
// requires V2StateOpen (the security gate that keeps server output away from an
// un-authenticated or torn-down peer), then seals env under s.send and forwards
// a noise_msg — reusing emitRekeyRequest's marshal→Encrypt→wrap→send sequence
// (minus the rekey bookkeeping). It is the single existing seal-and-forward
// path, shared by the push-buffer drain (drainOnce) and the snapshot reply
// handlers (handleRequestSnapshot / snapshotReplyError, which call it directly
// because their replies are InReplyTo-correlated and not part of the ordered
// push stream).
//
// Reads s.send at execution time on the dispatch goroutine, so it always
// uses the current CipherState and composes with re-key swaps: a forward
// either seals fully under the old key or fully under the new key, never
// a torn read.
//
// The seal/marshal error paths return wrapped errors (the caller decides
// log level); they MUST NOT echo env, plaintext, ciphertext, or key
// bytes, matching the package's no-AEAD-bytes-in-logs discipline.
func (m *V2SessionManager) forwardEnvelope(_ context.Context, connID string, env protocol.Envelope) error {
	s, ok := m.sessions[connID]
	if !ok {
		// A torn-down session was already deleted from the map by
		// closeWith, so "closed" collapses into this same branch.
		return ErrConnNotFound
	}
	if s.state != V2StateOpen {
		return ErrSessionNotOpen
	}
	// Reconnect-replay dedup (#647): drop a live structured envelope this conn
	// already received via replay. Envelopes with EventID == nil (snapshot,
	// error, rekey, resync) are never structured events and are never dropped;
	// conns that never advertised last_event_id keep replayThrough == 0 and
	// live ids are always >= 1, so the guard is inert for them.
	if env.EventID != nil && *env.EventID <= s.replayThrough {
		return nil
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

// ActiveConn is one open v2 session in the capability-aware enumeration: its
// routing conn-id and the negotiated interactive-capability decision recorded
// at handshake. It holds only non-secret routing/decision data — never a
// *V2Session, CipherState, key, or plaintext — so the snapshot is safe to hand
// to a consumer goroutine. The downstream structured-stream fan-out selects
// interactive vs non-interactive conns on the Interactive flag.
type ActiveConn struct {
	ConnID      string
	Interactive bool
}

// ActiveConns returns a snapshot of every session currently in V2StateOpen —
// the authenticated, token-validated sessions to which Push may deliver — each
// paired with its negotiated interactive flag. The result is an unordered set
// (Go's randomized map-iteration order); a caller that needs a stable order
// must sort it.
//
// Safe to call from any goroutine other than the dispatch goroutine: the
// request is funneled onto Run via m.snapshot so m.sessions is never read
// concurrently with an in-flight handshake transition, dispatchAppFrame
// reply, re-key swap, or closeWith teardown. It is the enumeration half of
// the server-initiated fan-out primitive — a consumer calls this, then Push on
// each conn-id, fanning interactive events only to conns with Interactive set.
// It is the capability-aware v2 analog of v1's dispatch.Dispatcher.ActiveConns().
//
// Sessions still handshaking (V2StateAwaitingInit) or handshake-complete-but-
// token-unvalidated (V2StateHandshakeComplete) are excluded — the same
// V2StateOpen security gate forwardEnvelope enforces, so the negotiated flag of an
// un-authenticated peer is never observable. A torn-down session (deleted from
// the map by closeWith) cannot appear.
//
// Returns nil on caller ctx cancellation, or when Run has already exited
// (Frames closed, no receiver on m.snapshot) and the caller's ctx then fires
// — both equivalent to "no open sessions" for the broadcast consumer, which
// fans out to nobody this round and re-enumerates on the next turn. nil and an
// empty non-nil slice are interchangeable (both len 0); a snapshot has no
// failure the caller can act on, so no error is returned.
func (m *V2SessionManager) ActiveConns(ctx context.Context) []ActiveConn {
	req := snapshotReq{reply: make(chan []ActiveConn, 1)}
	select {
	case m.snapshot <- req:
	case <-ctx.Done():
		return nil
	}
	select {
	case conns := <-req.reply:
		return conns
	case <-ctx.Done():
		return nil
	}
}

// ActiveConnIDs returns a snapshot of the conn IDs of every session currently
// in V2StateOpen — the authenticated, token-validated sessions to which Push
// may deliver. It is a thin projection over ActiveConns (dropping the
// interactive flag) preserved for the capability-agnostic #589 fan-out
// consumer; its signature and observable contract (unordered set, nil on ctx
// cancellation, non-nil-empty on an empty manager) are unchanged.
//
// Production wire-up of *V2SessionManager into the cmd/pyry daemon for
// server-initiated fan-out lands in a separate ticket (#572); until then this
// method is reachable only from internal/relay tests.
func (m *V2SessionManager) ActiveConnIDs(ctx context.Context) []string {
	conns := m.ActiveConns(ctx)
	if conns == nil {
		// Preserve the nil-on-cancel contract: a cancelled snapshot is nil,
		// distinct from a non-nil-empty snapshot of an open-session-less
		// manager.
		return nil
	}
	ids := make([]string, len(conns))
	for i, c := range conns {
		ids[i] = c.ConnID
	}
	return ids
}

// handleActiveConns runs on Run's dispatch goroutine — the only site that
// reads m.sessions for the snapshot, serialised by Run's select against every
// map write (lazy-create in handleFrame, delete in closeWith, state
// transitions in the handshake handlers). No read can observe a half-updated
// map, a torn s.state, or a torn s.interactive (set before V2StateOpen on the
// same goroutine).
//
// The returned slice is freshly allocated and owned by the caller; it holds
// only conn-id strings + the negotiated interactive bool (non-secret routing /
// decision data), never a *V2Session or any key/plaintext bytes. Order is Go's
// randomized map-iteration order — an unordered set by design: the AC requires
// no ordering and the broadcast consumer fans out order-independently, so no
// O(n log n) sort is paid on the single dispatch goroutine.
func (m *V2SessionManager) handleActiveConns() []ActiveConn {
	out := make([]ActiveConn, 0, len(m.sessions))
	for connID, s := range m.sessions {
		if s.state == V2StateOpen {
			out = append(out, ActiveConn{ConnID: connID, Interactive: s.interactive})
		}
	}
	return out
}

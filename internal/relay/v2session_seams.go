package relay

import (
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the V2SessionManager's dependency contracts and configuration,
// carved out of v2session.go so they read in isolation from the manager core.
// It collects the six seam interfaces the manager depends on — ScreenSnapshotter,
// Interrupter, SessionStarter, QueueRemover, SettingsUpdater, and ModalResolver —
// declared consumer-side per CODING-STYLE ("define interfaces where they are
// consumed"), plus the SettingsUpdate value type, the ErrSessionUnknown sentinel,
// and the ~260-line V2SessionConfig struct. Pure move: same package, no behaviour
// change, no call-site change. This is the final #964 slice, after #1026
// (snapshot/replay). The manager core — the V2Session / V2SessionManager structs,
// NewV2SessionManager, Run, handleWake, handleFrame, the app-frame router, the
// outbound send/drain path, and the connection registry — stays in v2session.go.

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
	// SECURITY: handlers run on the addressed conn's app-frame worker
	// goroutine (#965), NOT on the manager's Run goroutine — so a slow
	// handler no longer stalls Run, but the worker processes one frame at a
	// time, so a handler MUST still return in bounded time or it stalls that
	// conn's subsequent frames (bound long waits with a ctx timeout, as
	// create_conversation does). Handlers MUST NOT touch s.send / s.recv or
	// any Noise/session state — the worker never holds them; every reply is
	// sealed back on the Run goroutine (forwardAppReply), keeping the send
	// CipherState single-owner. A handler MUST NOT spawn a long-lived
	// background goroutine that retains the *dispatch.Conn passed in — the
	// conn's outbound channel is per-frame and is drained only while
	// routeAppFrame runs; sends from a forked goroutine after the handler
	// returns are silently lost (the channel is leaked but capacity-bounded,
	// and reclaimed by GC).
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

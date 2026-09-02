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
// It collects the eight seam interfaces the manager depends on —
// ScreenSnapshotter, Interrupter, SessionStarter, QueueRemover, SettingsUpdater,
// ModalResolver, QuestionResolver, and AttachmentIntake — declared consumer-side per CODING-STYLE
// ("define interfaces where they are consumed"), plus the SettingsUpdate and RunConfig value types, the
// ErrSessionUnknown sentinel, and the ~260-line V2SessionConfig struct. Pure
// move: same package, no behaviour
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

// RunConfig is one conversation's complete run configuration as the daemon
// reports it (#1609): the session that conversation is bound to, that session's
// model / effort / YOLO, and that session's context-window occupancy. It is the
// value half of V2SessionConfig.RunConfigFor below and carries primitives only,
// so internal/relay imports neither internal/sessions nor internal/contextwindow
// — the same discipline SettingsUpdate keeps for the write path.
//
// SessionID names the session the other five fields describe. That agreement is a
// property of this type rather than a warning in a comment: the cmd/pyry producer
// resolves the id and reads the settings under ONE pool acquisition, so no field
// can describe a session another field does not, even against a concurrent idle
// eviction. Contrast the bootstrap-scoped SnapshotSettings / SnapshotUsage pair
// on V2SessionConfig, which name no session at all, so their agreement with any
// separately-reported id could only be asserted in prose.
//
// An empty Model or Effort is a REAL reported value — "inherited daemon default,
// no per-session override" — and YOLO false means permissions are enforced, the
// same meaning SnapshotSettings already carries. So the zero RunConfig is
// indistinguishable from a genuine all-defaults session, which is exactly why
// "this conversation is not addressable" is RunConfigFor's comma-ok and never a
// field of this struct.
type RunConfig struct {
	SessionID    string
	Model        string
	Effort       string
	YOLO         bool
	UsedTokens   int
	WindowTokens int
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

// QuestionResolver resolves an inbound question-control frame — a question_answer
// or a question_refused — against the daemon's outstanding clarifying-question
// batches (#1984). Declared here (consumer side), beside ModalResolver, so
// internal/relay imports neither internal/questionbridge nor cmd/pyry
// (CODING-STYLE: define interfaces where they are consumed); #1985 implements it.
// *devices.Device crosses the seam (the per-conn s.device); internal/relay already
// imports internal/devices, so no new import. Both methods run on the manager's
// single Run dispatch goroutine, so an implementation MUST return in bounded time
// or it stalls the manager — ModalResolver's obligation, unchanged.
//
// The whole typed payload crosses rather than exploded fields, which is where this
// seam departs from ModalResolver: that one explodes because its payloads are flat
// strings, where QuestionAnswerPayload carries a nested array, so exploding it into
// (batchID, token, entries) is the same thing spelled longer and invites a caller
// to reassemble it wrongly.
//
// THE BOOL IS A DIAGNOSTIC, NEVER A BROADCAST TRIGGER. It reports whether the
// implementation consumed the batch, and the relay handler's only use for it is
// choosing between two content-free log records — QueueRemover.Remove's exact role
// in handleDequeueMessage. The relay MUST NOT emit question_dismissed on it:
// cmd/pyry's streamApprovalBridge.retireQuestion is that frame's sole broadcaster,
// and a second one would be a second arbiter of whether a batch was consumed. This
// is the other place ModalResolver is deliberately not followed — its
// (ModalDismissal, bool) pair exists BECAUSE the manager broadcasts, and here it
// must not.
//
// SECURITY — what crosses this seam is remote-authored and validated by nothing.
// The handler decodes the frame and rejects malformed bytes; it judges nothing
// else, so an implementation is the sole arbiter and inherits every obligation
// QuestionAnswerEntry's doc block names. Restated here because this is where the
// implementer meets them, and Go's type system cannot say "untrusted":
//
//   - QuestionIndex is CARRIED, NEVER RANGE-CHECKED. Subscripting the parked batch
//     with a negative or over-large index PANICS, and any paired client can pick
//     the index — so an explicit range check is owed before indexing.
//   - Indices may DUPLICATE or be MISSING across entries; neither a silently
//     partial answer nor a last-write-wins one is acceptable.
//   - Answers may be ARBITRARILY LONG. The only limit today is the transport's AEAD
//     frame cap, which bounds bytes and not entries, so per-frame work must not
//     scale unbounded with entry count.
//   - The payload's fields MUST NOT be logged beyond question_batch_id and
//     answer_token, the two internal/protocol marks safe; the values are free text
//     a client authored.
//
// ORDERING OBLIGATION: nothing may be wired to V2SessionConfig.QuestionResolver
// until the per-device answer gate (#1986) exists. The relay handler applies no
// authorization at all — no interactive check, no per-device check — exactly as
// handleModalAnswer applies none, so what makes the #1984 slice fail-safe is
// structural: the seam is nil at every construction site, so no answer reaches an
// actuator. Wiring a resolver ahead of the gate opens a window in which any paired
// device can answer.
type QuestionResolver interface {
	// ResolveAnswer resolves an inbound question_answer against the daemon's
	// parked batch. Returns true iff it consumed an outstanding batch; an unknown
	// or already-resolved question_batch_id is a safe no-op (false), the modal
	// seam's unknown-id posture.
	ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool

	// ResolveRefusal resolves an inbound question_refused — the operator declined
	// to choose, so the batch resolves with no selection. Same comma-ok-shaped
	// report and same no-op posture as ResolveAnswer.
	ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool
}

// AttachmentIntake drives one decoded attachment_chunk from admission to stored
// bytes, and releases a departing conn's uploads (#1897). *attachments.Intake
// satisfies it. Declared here, consumer-side, so the seam's own signatures name
// only internal/protocol — but unlike its neighbours this seam does NOT keep
// internal/relay free of the implementing package: handleAttachmentChunk imports
// internal/attachments for the sentinels, which is the shape #1751's spec settled
// (internal/* packages return Go sentinels; the dispatch site maps them to dotted
// wire codes at the call site via errors.Is). The seams file itself gains no
// import.
//
// Receive ANSWERS ONE OF THREE THINGS and the middle one is the trap. Restated
// here because this is where an implementer and the handler meet it, and because
// reading it the usual way is the single most likely way this path ships broken:
//
//   - ("", false, nil) — accepted, the transfer wants more. What MOST chunks of a
//     healthy upload get, and NOT a refusal. attachments.ErrIncomplete never
//     crosses this seam; Receive is the one place that interprets it.
//   - (attachment_id, true, nil) — the completing chunk's verified bytes are
//     stored.
//   - ("", false, err) — refused, with some layer's own sentinel VERBATIM, so
//     errors.Is reaches it unwrapped.
//
// So err != nil is the handler's whole refusal test and stored separates the
// other two.
//
// SECURITY — the errors coming back are NOT loggable and NOT repliable. EnsureDir
// wraps host paths and the daemon's own conversation id into its refusals, and
// Store's rename leg wraps an *os.LinkError whose Error() prints the sanitised
// filename. An implementation may add no annotation that would make that worse,
// and the handler interpolates the error nowhere — every reply carries a static
// per-code message and every log record carries the mapped code instead.
//
// RECEIVE CARRIES A SINGLE-FEEDER PRECONDITION: exactly one goroutine at a time
// per conn id. The manager discharges it structurally — one appFrameWorker per
// session, strictly FIFO — and it is why the upload runs on that worker rather
// than inline on Run. ReleaseConn deliberately carries NO such precondition: it
// is remove-only, so closeWith calls it on the Run goroutine while that conn's
// worker may still be inside Receive.
//
// Optional: when nil the attachment_chunk case still INTERCEPTS and consumes the
// frame — it no longer reaches dispatch.Route and so no longer draws its
// unknown-type error reply — but nothing is decoded, nothing is uploaded and
// nothing is replied.
type AttachmentIntake interface {
	// Receive routes one decoded chunk of one conn's upload. See the type doc
	// for the three-way answer; connID is the registry key that keeps distinct
	// conns' transfers separate.
	Receive(connID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)

	// ReleaseConn drops every upload still in flight for one conn, returning the
	// daemon-wide capacity they held without waiting for the idle window. A
	// no-op for a conn holding nothing, so closeWith calls it unconditionally.
	ReleaseConn(connID string)
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
	// this daemon hosts. handleRequestSnapshot is its sole reader: it rejects an
	// unknown/foreign id with conversation.not_found before any render (AC #4).
	// request_session_settings consulted it between #1586 and #1610 and no longer
	// does — a pure membership check reads a known but UNBOUND conversation as
	// addressable, so that verb resolves through RunConfigFor instead, which
	// refuses the unknown and the unbound identically. Optional: when nil, every
	// request_snapshot is rejected as not-found. Production wires it to a
	// conversations.Registry membership check, which takes that registry's mutex
	// and linear-scans its slice; it is not a map lookup.
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

	// RunConfigFor reports the NAMED conversation's own run configuration — its
	// bound session id, that session's model / effort / YOLO, and that session's
	// context-window used / window figures — as one RunConfig describing one
	// session (#1609). handleRequestSessionSettings is its reader, and since #1610
	// its ONLY run-configuration source: a client is told about the conversation
	// it is actually in rather than about the shared bootstrap session. It
	// replaced a bootstrap-scoped session-id seam that reported which session the
	// two seams above describe; the agreement a client depends on — that the
	// reported id names the session the reported values came from — is a property
	// of RunConfig now rather than a rule spanning separate fields.
	//
	// Comma-ok rather than a flag inside RunConfig: false means the conversation is
	// not addressable — unknown to this daemon, bound to nothing, or bound to a
	// session the pool no longer holds — and every field of the returned RunConfig
	// is at its zero, which a caller MUST NOT read. true with an empty Model or
	// Effort is a real answer, not a degraded one (see RunConfig), which is why the
	// refusal cannot be expressed in the values.
	//
	// Optional: nil ⇒ no consumer can resolve any conversation (foreground / v1 /
	// unwired), matching the seams above. Primitive-typed in both directions (a
	// string in, a RunConfig of scalars out) so internal/relay imports neither
	// internal/sessions nor internal/contextwindow; production composes it at the
	// cmd/pyry wiring point from a conversations-registry + pool resolver and the
	// by-id context-window reader.
	//
	// SECURITY: the refusal IS the control. conversationID is untrusted network
	// input and stays a lookup key into the daemon's own registry — it is never
	// returned, joined into a path, logged, or wrapped into an error. The reported
	// session id comes out of the daemon's own registry record and the producer
	// confirms the pool holds it before reporting anything, so an unresolvable
	// conversation addresses NOTHING: never the bootstrap session, and never a
	// session a caller named. The reported id is a routing key, not a secret — it
	// already crosses the wire outbound on session_transition and inbound on
	// set_session_settings. Read-only reflection — it reports the YOLO control
	// that only SettingsUpdater, the write path, can change.
	RunConfigFor func(conversationID string) (RunConfig, bool)

	// ModalResolver resolves inbound modal_answer / modal_cancel control
	// frames. Optional: when nil, both are inert no-ops (the modal bridge is
	// simply unwired — foreground, or pre-#708 before the producer is live).
	// Production wires the cmd/pyry resolver.
	ModalResolver ModalResolver

	// QuestionResolver resolves inbound question_answer / question_refused control
	// frames against the daemon's outstanding clarifying-question batches (#1984).
	// Optional: when nil, BOTH FRAMES ARE STILL CONSUMED by the interception —
	// they no longer reach dispatch.Route and so no longer draw its unknown-type
	// error reply — but nothing is decoded, nothing is handed off and nothing is
	// broadcast. That is the whole of the nil behaviour, and it is why the #1984
	// slice lands with no wiring site changed: every existing construction leaves
	// this field nil.
	//
	// Its own field rather than a method grown onto ModalResolver, following
	// OutstandingQuestions' reasoning (#1979): a question batch is its own frame
	// family (#1962), and growing the neighbour would force every ModalResolver
	// implementer to grow with it.
	//
	// SECURITY: the seam's own doc block carries the obligations — the payload is
	// remote-authored and validated by nothing beyond the decode, the index is
	// never range-checked, and NOTHING MAY BE WIRED HERE before the per-device
	// answer gate (#1986) exists, since the relay handler applies no authorization
	// and the nil seam is what makes the interception fail-safe today.
	QuestionResolver QuestionResolver

	// AttachmentIntake receives inbound attachment_chunk frames and releases a
	// departing conn's uploads (#1897). Optional: when nil the frame is STILL
	// CONSUMED by the interception — it no longer reaches dispatch.Route and so no
	// longer draws its unknown-type error reply — but nothing is decoded, nothing
	// is stored and nothing is replied, and closeWith releases nothing. That is
	// the whole of the nil behaviour, and it is what leaves every non-production
	// construction site (unit tests, the fake-daemon e2e harness, foreground/v1)
	// compiling and behaving unchanged.
	//
	// ONE PER DAEMON. attachments.Intake owns a registry whose in-flight ceiling
	// is daemon-wide, so a second instance would be a second budget of the same
	// size and the bound would stop meaning what it says. Production constructs
	// exactly one, in cmd/pyry's startRelayV2.
	//
	// SECURITY: the seam's own doc block carries the obligations — the payload is
	// remote-authored, the errors coming back wrap host paths and a sanitised
	// filename and are therefore neither loggable nor repliable, and Receive's
	// single-feeder precondition is discharged by the per-conn appFrameWorker
	// rather than by any lock.
	AttachmentIntake AttachmentIntake

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

	// RetainedModelLists enumerates the daemon's currently-retained model lists as
	// marshal-ready model_list payloads (one per session holding a list) for
	// connect-time reconcile (#1863) — the third Mode B instance after
	// OutstandingModals and OutstandingQueues. Called on the Run goroutine from
	// handleNoiseInit's interactive-open tail; the returned payloads are unicast to
	// the just-opened conn only. model_list is snapshot-shaped full state ("a
	// SNAPSHOT of what claude will accept, not a delta", ModelListPayload's own
	// doc), so the re-send is idempotent by construction — re-connecting re-sends
	// the same snapshot. A pure read: it mints nothing, retires nothing, and
	// changes no daemon state.
	//
	// A closure returning []protocol.ModelListPayload, not a *sessions.Pool or a
	// turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent (sessions appears only transitively via internal/control,
	// so a go list -deps reading looks like a contradiction and is not one), and
	// protocol is already imported, so the payload crosses the boundary with no new
	// import and no cycle (matching OutstandingModals / OutstandingQueues — define
	// the dependency where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and
	// peerStatic — so there is no "this conn's conversation" to key on at connect
	// time. OutstandingQueues' one-per-conversation enumerate-all shape is the
	// precedent; RunConfigFor is the conversation-keyed variant and is the wrong
	// shape here.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any entry's text — because the bound is decided at construction upstream
	// (ModelListPayload.DroppedModels on the aggregate, ModelOption.TruncatedFields
	// per entry, frozen by #1704/#1705). A second cap here would be a second place
	// the limit is decided and the two could disagree silently, so the obligation
	// stays the producer's. The payload text is claude-authored and untrusted
	// (ModelOption's own doc) and is NEVER logged on the reconcile path.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#1863 / foreground /
	// existing-test posture. #1864 wires the daemon-side producer.
	RetainedModelLists func() []protocol.ModelListPayload

	// OutstandingQuestions enumerates the daemon's currently-outstanding clarifying-
	// question batches as marshal-ready question_shown payloads (each already stamped
	// with its own question_batch_id and conversation_id) for connect-time reconcile
	// (#1979) — the fourth Mode B instance after OutstandingModals, OutstandingQueues
	// and RetainedModelLists. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened conn
	// only. A pure read: it mints no nonce and retires no batch, so it neither re-arms
	// the approval window nor changes answerability — a re-sent question_batch_id stays
	// answerable exactly once, governed by the registry's one-shot Resolve, which this
	// path never calls.
	//
	// The reconcile exists because question_shown has no other path to a late client:
	// the raise-time broadcast reaches only whoever is connected at the instant claude
	// asks, and the frame carries no event id, so it is not in the #647 turn-event
	// replay ring either. reconcileModals' doc block records the twist that makes the
	// miss cost more than a plain miss — the daemon counts an approval answerable while
	// ANY interactive conn is open, so a reconnected client that was never sent the
	// batch re-arms the window at every expiry while being structurally unable to
	// answer it.
	//
	// A closure returning []protocol.QuestionShownPayload, not a
	// *questionbridge.Registry: internal/relay imports neither internal/questionbridge
	// nor internal/modalbridge, and protocol is already imported, so the payload
	// crosses the boundary with no new import and no cycle (matching the three seams
	// above — define the dependency where it is consumed). modalbridge carries nothing
	// for this batch: it is its own frame family (#1962), because denyByClass makes
	// DefaultOptionID the deny option and a clarifying question has no deny option and
	// no safe default.
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id —
	// it holds connID, state, resp, send, recv, device, interactive and peerStatic — so
	// there is no "this conn's conversation" to key on at connect time.
	// RetainedModelLists' doc block states the same reasoning.
	//
	// Order is not part of the contract, and a caller MUST correlate a batch by its
	// question_batch_id rather than by its position in the returned slice: the
	// production producer walks a map, whose order Snapshot's own doc leaves
	// unspecified.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many batches are returned, not on any
	// entry's text — because the bound is decided upstream at parse time
	// (questionbridge.Parse: 1-4 questions, 2-4 options per question, over a
	// maxInputBytes-capped tool input). A second cap here would be a second place the
	// limit is decided and the two could disagree silently, so the obligation stays the
	// producer's. Note that the per-batch bounds above do NOT bound how many batches
	// can be outstanding at once: the registry holds no cardinality cap, so the
	// aggregate is bounded only by claude's own ask concurrency and by every terminal
	// path retiring its batch (#1973). On this path pushQueue's byte ceiling is the
	// backstop; a cardinality cap, if one is ever wanted, belongs to questionbridge and
	// not to either half of this reconcile. The four strings a batch carries — a
	// Question's Text and Header, a QuestionOption's Label and Description — are
	// claude-authored, untrusted text (Question's own doc) and are NEVER logged on the
	// reconcile path.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#1979 / foreground /
	// existing-test posture. #1980 wires the daemon-side producer to
	// questionbridge.Registry.Snapshot.
	OutstandingQuestions func() []protocol.QuestionShownPayload

	// RetainedSlashCommandLists enumerates the daemon's currently-retained
	// slash-command lists as marshal-ready slash_command_list payloads (one per
	// session holding a list, each already stamped with its own conversation_id)
	// for connect-time reconcile (#2006) — the fifth Mode B instance after
	// OutstandingModals, OutstandingQueues, RetainedModelLists and
	// OutstandingQuestions. Called on the Run goroutine from handleNoiseInit's
	// interactive-open tail; the returned payloads are unicast to the just-opened
	// conn only. slash_command_list is snapshot-shaped full state — it is decoded
	// from one initialize reply, it is session configuration rather than a turn
	// event, and receiving one neither opens nor closes a turn — so the re-send is
	// idempotent by construction: re-connecting re-sends the same snapshot. A pure
	// read: it mints nothing, retires nothing, and changes no daemon state.
	//
	// The reconcile exists because the live turn lane is the only path carrying this
	// frame today and three independent loss points sit in front of it: the
	// emitter's empty-conversation early return, unconditional for the bootstrap
	// child because the conversation cursor is only ever set by a successful route
	// while the initialize ask fires at child spawn; the droppable classification
	// under droppableCap; and forwardEnvelope's last_event_id dedup, a reconnect
	// mechanism with no fresh-connect backfill. A client attaching later has no path
	// to the list at all, so its command menu stays empty until a turn that may
	// never come.
	//
	// A closure returning []protocol.SlashCommandListPayload, not a *sessions.Pool
	// or a turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent (sessions appears only transitively via internal/control,
	// so a go list -deps reading looks like a contradiction and is not one), and
	// protocol is already imported, so the payload crosses the boundary with no new
	// import and no cycle (matching the four seams above — define the dependency
	// where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and peerStatic
	// — so there is nothing to key on at connect time; each payload self-identifies
	// by its own conversation_id. RetainedModelLists and OutstandingQuestions state
	// the same reasoning. cmd/pyry's resolveBoundSlashCommandList (#2005) is the
	// conversation-keyed variant and is deliberately the WRONG shape here; bridging
	// the two is #2007's job.
	//
	// Order is not part of the contract, and a caller MUST correlate a list by its
	// conversation_id rather than by its position in the returned slice — the
	// envelope id this path stamps is fixed and non-load-bearing for the same
	// reason.
	//
	// BOUNDED TIME, like every seam the manager calls on its Run goroutine: an
	// implementation that blocks stalls Run and with it every conn the manager
	// services. ModalResolver's doc block states the same obligation and this one is
	// not hypothetical — the #2007 producer walks a conversation registry under that
	// registry's mutex, which is exactly the shape that can block.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any entry's text — because the bound is decided upstream at construction
	// (SlashCommandListPayload.DroppedCommands on the aggregate,
	// SlashCommand.TruncatedFields per entry, over a producer cut measured against
	// marshalled bytes so the envelope stays under the v2 application-envelope cap).
	// A second cap here would be a second place the limit is decided and the two
	// could disagree silently, so the obligation stays the producer's. Note that
	// those per-payload bounds do NOT bound how many payloads can be returned at
	// once; on this path pushQueue's byte ceiling is the backstop, and a cardinality
	// cap, if one is ever wanted, belongs to the producer and not to either half of
	// this reconcile. The four strings a row carries — Name, ArgumentHint,
	// Description and each entry of Aliases — are WORKSPACE-authored, untrusted text
	// that crossed the subprocess trust boundary (SlashCommand's own doc, which
	// grades that origin below claude-authored), and they are NEVER logged on the
	// reconcile path. They are also forwarded UNSANITISED — no control-character or
	// terminal-escape stripping happens here, and Description is measured to carry
	// newlines — which is SlashCommand's own documented decision and not an
	// omission: the render boundary owing the sanitisation is the client's.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#2006 / foreground /
	// existing-test posture, the nil-resolver posture the other optional control
	// seams share. #2007 wires the daemon-side producer.
	RetainedSlashCommandLists func() []protocol.SlashCommandListPayload
}

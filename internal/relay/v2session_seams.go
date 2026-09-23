package relay

import (
	"context"
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

// Interrupter stops the running turn in ONE conversation (#707, widened by #2103)
// — the remote equivalent of a local Esc, claude's own interrupt. Declared here
// (consumer side), beside ScreenSnapshotter, so internal/relay imports neither
// internal/supervisor nor tui-driver. Named for its relay-domain role (matching
// ScreenSnapshotter.ScreenSnapshot / ModalResolver.Resolve*), even though the
// method keeps the sealed surface's name — renaming it to match the actuation
// would churn the whole package for no behavioural gain, and this doc is where
// SendEsc is abstracted as "claude's own interrupt" (#1121). SendEsc is safe to
// call from any goroutine.
//
// The sole implementation is cmd/pyry's activeInterrupter. *supervisor.Supervisor
// used to satisfy this seam and has not since #1121 replaced the Interrupter:
// w.sup wiring that mis-delivered every interrupt to the bootstrap supervisor
// regardless of which conversation's turn was running; cmd/pyry/relay.go's comment
// at the wiring site already says so, and this block said otherwise until #2103.
//
// conversationID names the conversation whose turn to stop, and the implementation
// MUST treat it as UNTRUSTED: it arrives from a paired client, and this package
// deliberately validates nothing about it. internal/relay imports neither
// internal/conversations nor internal/sessions (see the note above
// ScreenSnapshotter on that boundary), so it can neither shape-check the id nor
// resolve it — the handler is a courier and every check lives in the composition
// root. An implementation MUST shape-check before any use and MUST NOT let the
// string become a path component. The obligation is stated here because Go's type
// system cannot: the parameter is a bare string, exactly as SessionStarter's is.
//
// The EMPTY STRING is not an error: it means "the conversation the daemon's own
// cursor points at", which is the pre-#2103 behaviour and the path an un-upgraded
// client still takes, since interrupt carried no payload at all until then.
// handleInterrupt maps every "nothing named" wire shape — absent payload, absent
// field, explicit "", undecodable body — onto it, so an implementation has exactly
// one such state to handle.
//
// An error is BEST-EFFORT diagnostic, not a failure the caller acts on: a
// conversation with no live child yields streamsup.ErrNoLiveChild, which the
// production implementation neither suppresses nor pre-empts — WriteInterrupt
// refuses a nil writer having written nothing, so such a conversation is inert by
// construction on the named and cursor paths alike (#2103; contrast
// SessionStarter, whose actuator mutates before it can discover the same state).
type Interrupter interface {
	SendEsc(conversationID string) error
}

// SessionStarter starts a fresh session in ONE conversation (#831, widened by
// #2099). Declared here (consumer side), beside Interrupter, so internal/relay
// imports neither internal/supervisor nor tui-driver. Named for its relay-domain
// role (matching Interrupter / ScreenSnapshotter), even though the method keeps
// the sealed surface's name, so the name is unambiguous against any pool/session
// lifecycle "start". StartNewSession is safe to call from any goroutine.
//
// conversationID names the conversation to restart, and the implementation MUST
// treat it as UNTRUSTED: it arrives from a paired client, and this package
// deliberately validates nothing about it. internal/relay imports neither
// internal/conversations nor internal/sessions (see the note above ScreenSnapshotter
// on that boundary), so it can neither shape-check the id nor resolve it — the
// handler is a courier and every check lives in the composition root. An
// implementation MUST shape-check before any use and MUST NOT let the string
// become a path component; cmd/pyry's activeSessionStarter is the production one.
//
// The EMPTY STRING is not an error: it means "the conversation the daemon's own
// cursor points at", which is the pre-#2099 behaviour and the path an un-upgraded
// client still takes, since new_session carried no payload at all until then.
// handleNewSession maps every "nothing named" wire shape — absent payload, absent
// field, explicit "", undecodable body — onto it, so an implementation has exactly
// one such state to handle.
// An ERROR IS NOT ALWAYS A FAILED ROTATION, since #2443: a returned
// *RotatedWithoutWorkspaceError reports a rotation that COMPLETED with one
// caveat, and handleNewSession discriminates on it before it reaches the
// best-effort log arm. Every other non-nil error keeps the pre-#2443 meaning —
// the rotation did not happen, log it, owe nothing.
type SessionStarter interface {
	StartNewSession(conversationID string) error
}

// LateSessionStarter is an OPTIONAL widening of SessionStarter for an
// implementation whose rotation does not finish before the call returns (#2477:
// the outgoing child is asked to write a handoff note first, bounded at ninety
// seconds). handleNewSession asserts it on the configured SessionStarter and
// falls back to the plain method when it is absent, so an implementation that
// rotates inline — the PTY posture — needs no change and sees no new behaviour.
//
// IT EXISTS BECAUSE THE VERB'S ONE REPLY HAS A TENSE. StartNewSession's only
// non-best-effort answer is *RotatedWithoutWorkspaceError, whose own doc fixes the
// pool as re-keyed and the session_transition as already broadcast by the time the
// value exists, and forbids it outright for a rotation that has not happened. An
// implementation that defers its rotation therefore cannot answer that method
// truthfully at all: it must either lie about a rotation ninety seconds away or
// stay silent and drop a shipped reply. This widening removes the dilemma rather
// than choosing a side of it — the answer is given when it becomes true.
//
// outcome CARRIES EXACTLY WHAT StartNewSession WOULD HAVE RETURNED, with the same
// meanings: nil for an inert arm or a clean rotation, *RotatedWithoutWorkspaceError
// for a rotation that COMPLETED without the recorded workspace, any other error for
// a rotation that did not happen. An implementation MUST call it exactly once, and
// MAY call it after StartNewSessionLate has returned — that is the whole point —
// but MUST NOT call it from inside a lock a caller could be holding.
//
// THE CALLBACK RUNS ON THE IMPLEMENTATION'S GOROUTINE, so handleNewSession passes
// one that does nothing but hand the value to Run, where the reply is sealed. A
// caller supplying a closure that touches its own single-owner state directly
// would be the bug this shape exists to make hard to write.
type LateSessionStarter interface {
	SessionStarter

	StartNewSessionLate(conversationID string, outcome func(error))
}

// RotatedWithoutWorkspaceError is what a SessionStarter returns when a rotation
// COMPLETED and the conversation's recorded workspace was refused, so the
// successor child stayed in the directory the runner already had (#2443, over
// #1475's move). handleNewSession answers it with a single coded error reply to
// the conn that asked, correlated on in_reply_to; nothing else reads it.
//
// IT IS NOT A FAILURE, and the name is the warning. The pool is re-keyed, the
// conversation rebound, the successor respawning and the session_transition
// already broadcast by the time this value exists — a reader that treats every
// non-nil error from the seam as "nothing happened" is wrong here, which is
// precisely why the refusal travels as a distinct TYPE rather than as a second
// meaning layered onto the existing error. An implementation MUST NOT return it
// when the rotation itself failed: that outcome is the plain error it always was,
// and reporting "rotated without the workspace" for a rotation that never
// happened would be a lie the client cannot check.
//
// A TYPE RATHER THAN A SENTINEL because the reply must NAME a conversation and a
// sentinel carries no fields. A bare new_session names none — it rotates the
// daemon's cursor conversation — so in_reply_to alone cannot tell the client which
// conversation stayed put, and only the implementation, which resolved the cursor,
// knows. Callers match with errors.As.
//
// SECURITY — the two halves of the no-leak bound this error is subject to:
//
//   - ConversationID is DAEMON-RESOLVED, never the raw client string. On the named
//     path it has passed conversations.ValidID; on the bare path it is the daemon's
//     own cursor id. It is never a path and never a path component.
//   - Error() returns a CONSTANT. It names neither the workspace path nor the
//     conversation id, because handleNewSession's other arm logs a seam error
//     verbatim — so an interpolated id or path would reach the daemon log the
//     moment a future edit reordered the discrimination, which is the one channel
//     #2443's AC-4 exists to close. The id is readable from the field and from
//     nowhere else. The confinement error that caused the refusal is discarded at
//     its own site (cmd/pyry's activeSessionStarter.resolveSpawnDir) and never
//     reaches this value at all.
type RotatedWithoutWorkspaceError struct {
	// ConversationID is the conversation that rotated: the one the frame named, or
	// the daemon's cursor conversation when it named none.
	ConversationID string
}

func (e *RotatedWithoutWorkspaceError) Error() string {
	// Constant by contract — see the type's SECURITY block.
	return "relay: new_session rotated without the conversation's recorded workspace"
}

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

	// PermissionMode names the posture to switch to (#1687), mirroring
	// sessions.SettingsUpdate's field of the same name. It reaches this struct
	// only after handleSetSessionSettings has checked it against
	// validPermissionMode, so a non-nil value here is always one of claude's five
	// NON-ESCALATING modes — never "", never bypassPermissions, never an
	// unrecognised string. The escalation therefore has no expression on this field
	// at all: it travels as YOLO and nothing else, which is what keeps the bypass
	// fail-safe to a single bit. That is a wire-vocabulary bound and not a claim
	// about delivery — the daemon has routed the escalation in band, spelled as the
	// bit, since #2066.
	PermissionMode *string
}

// SettingsUpdater persists a per-session settings change named by an inbound
// set_session_settings control frame (#845); the cmd/pyry adapter wraps
// *sessions.Pool.UpdateSettings. Declared here (consumer side), beside
// Interrupter / SessionStarter / QueueRemover, so internal/relay imports neither
// internal/sessions nor cmd/pyry (CODING-STYLE: define interfaces where they are
// consumed). UpdateSettings returns ErrSessionUnknown for an id the daemon does
// not host (the adapter maps sessions.ErrSessionNotFound onto it) — which the
// handler turns into a session.not_found reply. ErrModelNotOffered and
// ErrModelVocabularyUnavailable are pre-mutation validation outcomes; remaining
// errors are persistence failures reported as server-unavailable.
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
//
// PermissionMode is the posture in force (#1687), and unlike Model and Effort its
// empty string is NOT a real reported value: the producer reads it from a
// pool-held session, whose stored mode is normalised at construction, so a
// resolved RunConfig always names one of claude's six modes. "" occurs only in
// the zero RunConfig a refusal returns. It always agrees with YOLO — the daemon
// stores the two so they cannot disagree — so a bypass session reports
// "bypassPermissions" here AND YOLO true, a posture the write path deliberately
// refuses to accept on its own mode field.
type RunConfig struct {
	SessionID      string
	Model          string
	Effort         string
	YOLO           bool
	PermissionMode string
	UsedTokens     int
	WindowTokens   int
}

// ErrSessionUnknown is the relay-local sentinel the SettingsUpdater adapter
// returns when set_session_settings names a session the daemon does not host. The
// cmd/pyry adapter maps sessions.ErrSessionNotFound onto it so internal/relay
// stays free of an internal/sessions import; handleSetSessionSettings maps it to a
// deterministic session.not_found reply.
var ErrSessionUnknown = errors.New("relay: session unknown")

// ErrModelNotOffered means a complete retained model vocabulary proves that a
// requested non-empty model is absent. The settings handler maps it to a fixed,
// non-retryable protocol.malformed response. It deliberately carries no model
// value because callers may log the outcome.
var ErrModelNotOffered = errors.New("relay: model not offered")

// ErrModelVocabularyUnavailable means retained state cannot prove whether a
// requested non-empty model is offered. Missing state, dropped rows, and a row
// whose Value was truncated all map here. The settings handler returns the same
// retryable model_list.unavailable used by the model-list request path.
var ErrModelVocabularyUnavailable = errors.New("relay: model vocabulary unavailable")

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

	// ResolveAnswerWithAlwaysAllow resolves an inbound modal_answer. The Boolean
	// is only a request to use daemon-retained rules; implementations must still
	// authorize the device, classify an allow option, and consume the modal once.
	ResolveAnswerWithAlwaysAllow(modalID, optionID, answerToken string, alwaysAllow bool, dev *devices.Device) (ModalDismissal, bool)
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

// MCPActuator performs one inbound MCP actuation — an mcp_reconnect or an
// mcp_toggle — against one conversation's live claude child, and reports whether it
// was accepted (#2419). Declared here (consumer side), beside QuestionResolver, so
// internal/relay imports neither internal/streamsup nor cmd/pyry (CODING-STYLE:
// define interfaces where they are consumed); #2420 implements it. *devices.Device
// crosses the seam (the per-conn s.device); internal/relay already imports
// internal/devices, so no new import.
//
// The whole typed payload crosses rather than exploded fields — QuestionResolver's
// shape, for its reason: exploding a three-field struct into three arguments is the
// same thing spelled longer and invites a caller to reassemble it wrongly.
//
// THE ACCEPTED ANSWER'S PAYLOAD COMES BACK ACROSS THIS SEAM, and that is the one
// place this contract departs from every read seam on V2SessionConfig. An accepted
// actuation is answered with a fresh mcp_status frame, and the status it carries MUST
// be a read taken AFTER the child acknowledged the actuation — so the implementation,
// which is the only thing that knows when the ack landed, is what reads it. The relay
// performs no second read and deliberately does NOT reach MCPStatusFor on this path:
// a read the relay issued for itself could not be the post-ack one.
//
// COMMA-OK IS THE WHOLE REFUSAL VOCABULARY. false means refused, for ANY reason; a
// caller MUST NOT read the payload on false. true with an empty Servers is a real
// answer, not a degraded one. The relay answers every false with one merged
// protocol.CodeMCPActuationRefused, whose own doc block carries why the reasons are
// not distinguishable on the wire — and streamsup's actuateMCP already collapses
// refusal and unavailability into one bool for the same published decision, so a
// richer return here would have no reader on either side.
//
// SECURITY — what crosses this seam is remote-authored, and its two strings are NOT
// equally checked. Restated here because this is where the implementer meets them and
// Go's type system cannot say "untrusted":
//
//   - ConversationID has ALREADY passed KnownConversation, so it names a conversation
//     this daemon hosts. It remains a lookup key: never returned as the answer's
//     ConversationID, never logged, never joined into a path.
//   - ServerName HAS PASSED NOTHING, anywhere. internal/protocol does not validate it
//     and neither does the handler; this implementation is its sole validator. It MUST
//     be shape-checked before any use, MUST NOT become a path component, and MUST NOT
//     reach a shell. Its length is bounded only transitively, by the transport's AEAD
//     frame cap — one name per frame, so the cap does bind, but sizing a buffer from
//     it relies on a bound stated somewhere else. (Below this seam, streamsup's
//     marshalMCPReconnectEnvelope / marshalMCPToggleEnvelope json.Marshal the name
//     into the child's control request rather than concatenating it, so the child's
//     control stream is not injectable through this field. That is a property of those
//     functions, not of this contract.)
//   - Every string in the returned payload is CLAUDE-AUTHORED and is never logged.
//
// THE AUTHORIZATION DECISION AND ITS AUDIT RECORD ARE THE IMPLEMENTATION'S, ENTIRELY.
// The handler applies no privilege check and must not grow one: the refusal has to be
// audited through internal/audit, which lives in cmd/pyry, and a second arbiter here
// would be a second thing to keep in agreement with it — handleMintPairing's posture,
// unchanged. Note that the merged wire reject also merges TIMING poorly: a gate denial
// returns at once where a real actuation waits for a child round trip, so an
// implementation that wants the reasons genuinely indistinguishable owes that
// consideration; the relay adds no timing of its own and cannot close it from here.
//
// ORDERING OBLIGATION: nothing may be wired to V2SessionConfig.MCPActuator until the
// per-device actuation gate (#2420) exists. The relay handler applies no authorization
// at all — no per-device check, exactly as handleModalAnswer and the QuestionResolver
// path apply none — so what makes the #2419 interception fail-safe is structural: the
// seam is nil at every construction site, so no actuation reaches a live child. Wiring
// an actuator ahead of the gate opens a window in which ANY paired device can
// reconfigure a running child.
//
// Both methods run on the addressed connection's appFrameWorker, NOT on Run, because a
// live implementation waits for a child round trip. An implementation MUST honor ctx so
// manager shutdown terminates that wait, and MUST NOT touch V2Session or any Noise
// state — replies return through forwardToRun for Run-owned sealing.
type MCPActuator interface {
	// Reconnect reconnects the named MCP server in the named conversation. Returns
	// the post-acknowledgement status to answer with and true, or the zero payload
	// and false for every refusal.
	Reconnect(ctx context.Context, p protocol.MCPReconnectPayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)

	// SetEnabled moves the named MCP server to p.Enabled in the named conversation.
	// Named for the flag rather than for the wire's `mcp_toggle`, matching
	// streamsup.Runner.SetMCPServerEnabled: the request carries the value, so a
	// Toggle spelling would invite an implementation to flip whatever the current
	// state is. Same two-way answer as Reconnect.
	SetEnabled(ctx context.Context, p protocol.MCPTogglePayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)
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
	//
	// PRECONDITION, AND IT IS THE CALLER'S: conversationID MUST already have
	// passed KnownConversation. attachments.EnsureDir's own block states it —
	// the id becomes a path component below this seam — and
	// handleAttachmentChunk is the caller that discharges it, on EVERY chunk
	// rather than only the completing one, so a transfer naming an unusable
	// conversation is refused on its FIRST frame. Identical in wording and in
	// reason to HistoryPage's precondition below.
	//
	// It is a STRING and not a conversations.ConversationID on purpose: this
	// package does not import internal/conversations — only
	// internal/relay/handlers does — and KnownConversation is primitive-typed
	// to keep it that way. Declaring the seam with the typed id would add the
	// very import the seam's own docs argue against, so the conversion belongs
	// on the implementing package's side.
	Receive(connID, conversationID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)

	// ReleaseConn drops every upload still in flight for one conn, returning the
	// daemon-wide capacity they held without waiting for the idle window. A
	// no-op for a conn holding nothing, so closeWith calls it unconditionally.
	ReleaseConn(connID string)
}

// V2SessionConfig parameterises V2SessionManager. The handshake/transport
// fields are required; NewV2SessionManager validates and panics or errors on
// missing required values per the documentation below. Optional seams document
// their nil behaviour on each field.
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
	// this daemon hosts. handleRequestSnapshot and handleMCPStatusRequest use it
	// to reject an unknown/foreign id with conversation.not_found before any
	// render or resolver call.
	// request_session_settings consulted it between #1586 and #1610 and no longer
	// does — a pure membership check reads a known but UNBOUND conversation as
	// addressable, so that verb resolves through RunConfigFor instead, which
	// refuses the unknown and the unbound identically. Optional: when nil, every
	// request_snapshot is rejected as not-found. Production wires it to a
	// conversations.Registry membership check, which takes that registry's mutex
	// and linear-scans its slice; it is not a map lookup.
	KnownConversation func(conversationID string) bool

	// HistoryPage serves one backward step of a conversation-history walk for an
	// inbound request_history (#2116), over the daemon's durable on-disk log
	// (#2112). handleRequestHistory is its sole reader. Optional: when nil the
	// frame is CONSUMED BUT INERT — no reply, and not one byte of its payload
	// parsed — mirroring the nil AttachmentIntake / AttachmentResolve guards and
	// buying the same property, that an unwired daemon performs zero parsing of
	// remote-authored bytes.
	//
	// PRECONDITION, AND IT IS THE CALLER'S: conversationID MUST already have
	// passed KnownConversation. history.Store.Page's own block states it — the id
	// becomes a path component below this seam — and handleRequestHistory is the
	// caller that discharges it, before it ever reaches here.
	//
	// cursor IS PASSED THROUGH UNPARSED, always. The wire declares it opaque and
	// history.parseCursor is the only validator anywhere; nothing in
	// internal/relay may decode one, log one, or branch on its contents.
	//
	// limit ARRIVES ALREADY POSITIVE AND ALREADY NARROWED: the handler substitutes
	// its own page size for the client's 0, refuses a negative, and caps the rest
	// at maxHistoryPageEntries. An implementation MUST NOT size any buffer from it
	// — see HistoryPageResult.Entries.
	//
	// BOUNDED TIME, but NOT on the Run goroutine: this seam is called from the
	// addressed conn's appFrameWorker, which is why it is allowed to read files at
	// all. It still stalls that conn's later frames while it runs, so an
	// implementation that blocks indefinitely is a bug — production wires it to a
	// store whose work is bounded by the page rather than by the log.
	HistoryPage HistoryPager

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

	// EffectiveEffortFor reports Claude's applied effort for the current child of
	// the NAMED conversation. handleRequestSessionSettings calls it only after
	// RunConfigFor accepts that same non-empty conversation, and never uses it as
	// a source for the saved Effort or any other RunConfig field (#2516).
	//
	// The pointer is the nullable result: non-nil is a confirmed string, while nil
	// with true is confirmed JSON null (Claude reported no effort parameter). The
	// comma-ok is availability: false means no current reading, and the pointer
	// MUST be ignored even when non-nil. Optional: nil has the same unavailable
	// posture, omitting effective_effort while preserving every saved field.
	//
	// BOUNDED TIME, but NOT on Run: this seam runs on the addressed connection's
	// appFrameWorker because a production implementation may wait on a child round
	// trip. It MUST honor ctx so manager shutdown releases the worker. The relay
	// adds no cache; every accepted request calls the provider once. The contract
	// carries only one nullable scalar, never a full child settings response.
	EffectiveEffortFor func(ctx context.Context, conversationID string) (*string, bool)

	// ModelListFor reports the NAMED conversation's model menu, already shaped as a
	// marshal-ready model_list payload, for an inbound request_model_list (#2125).
	// handleRequestModelList is its sole reader.
	//
	// CONVERSATION-KEYED, NOT ENUMERATE-ALL, and that is the whole difference from
	// RetainedModelLists below. That seam enumerates because a V2Session carries no
	// conversation id, so there is nothing to key a connect-time reconcile on; this
	// path has an id in the request, so RunConfigFor above is the shape it copies —
	// a string in, a payload and a comma-ok out. Do not reach for the enumerator
	// here: it would resolve every conversation the registry carries to answer about
	// one.
	//
	// IT DECIDES NOTHING ABOUT WHICH VOCABULARY ANSWERS. That decision — a bound
	// session's own retained list, else the daemon-wide copy (#2124) — lives inside
	// the cmd/pyry resolver, whose block forbids a caller forking it. This seam
	// answers "what is that conversation's menu" and the handler's separate
	// KnownConversation call answers "is this conversation ours"; neither is a second
	// opinion on the other's question.
	//
	// Comma-ok rather than an empty payload, and this is the ONE PLACE the model-list
	// family cannot follow RunConfigFor's neighbour request_session_settings, whose
	// all-zero reply is a real answer. turnevent.ModelList.Models is documented
	// never-empty, so AN EMPTY Models MUST NEVER STAND IN FOR "UNKNOWN": false means
	// no menu exists to send and the handler turns it into a coded error frame. A
	// caller MUST NOT read the payload on false — the production producer happens to
	// zero its refusal return, but that is a property of cmd/pyry rather than of this
	// contract.
	//
	// Optional: nil ⇒ the verb refuses every request it has already accepted as
	// hosted, with the same retryable model_list.unavailable a resolver refusal
	// earns. The merge is deliberate: distinguishing them would publish whether the
	// host's model-list source is wired, which is a fact about the machine rather
	// than about the request. Foreground / v1 wirings leave it nil, as they leave
	// RetainedModelLists nil, and no existing construction site changes.
	//
	// A closure returning protocol.ModelListPayload rather than a *sessions.Pool or a
	// turnevent value: internal/relay imports neither internal/sessions nor
	// internal/turnevent, and protocol is already imported both sides, so the payload
	// crosses with no new import and no cycle — RetainedModelLists' reason, unchanged.
	//
	// SECURITY: conversationID is untrusted network input and reaches this seam only
	// AFTER KnownConversation has passed on it. It stays a lookup key into the
	// daemon's own registry — never returned, never joined into a path, never wrapped
	// into an error — and the reported conversation_id in the payload comes out of
	// the daemon's own registry record rather than being echoed back, RunConfigFor's
	// posture. This seam accepts ALREADY-BOUNDED payloads only and applies no bound
	// of its own: the entry count and each row's fields are capped at construction
	// (ModelListPayload.DroppedModels, ModelOption.TruncatedFields, frozen by
	// #1704/#1705), so a second cap here would be a second place the limit is decided.
	// The payload text is claude-authored and untrusted (ModelOption's own doc) and
	// is NEVER logged on this path.
	//
	// BOUNDED TIME, on the Run goroutine. The handler answers inline rather than
	// handing off to the conn's appFrameWorker, so an implementation MUST stay a
	// bounded in-memory read — production wires a registry lookup plus a copy of at
	// most ten model rows. An implementation that reads a file or enumerates the
	// registry belongs off Run, and moving it there means switching the handler's
	// emit from forwardEnvelope to forwardToRun in the same change.
	ModelListFor func(conversationID string) (protocol.ModelListPayload, bool)

	// MCPStatusFor reports the current MCP server status for one hosted
	// conversation, already shaped as the existing mcp_status payload. The
	// mcp_status_request handler is its sole reader (#2381).
	//
	// The handler calls this seam only after the request payload decodes, the
	// connection has negotiated the interactive capability, and KnownConversation
	// accepts the id. The id remains an untrusted lookup key and MUST NOT be logged,
	// joined into a path, or returned as the answer's ConversationID. Every string
	// in the returned payload is Claude-authored and MUST NOT be logged either.
	//
	// Comma-ok distinguishes a current empty-server snapshot (true) from no current
	// status (false). A caller MUST NOT inspect the payload when false; the relay
	// translates that outcome to retryable mcp_status.unavailable and never falls
	// back to a retained or empty frame.
	//
	// Optional: nil makes the inbound type consumed but inert before payload decode,
	// membership, or reply. The production daemon leaves it nil until #2382 wires
	// live-child request correlation.
	//
	// This call runs on the addressed connection's appFrameWorker, not Run, because
	// a live implementation may wait for a child round trip. It MUST honor ctx so
	// manager shutdown terminates the wait. Replies return through forwardToRun;
	// implementations must never touch V2Session or Noise state.
	MCPStatusFor func(ctx context.Context, conversationID string) (protocol.MCPStatusPayload, bool)

	// ContextUsageFor reports one hosted conversation's context-window breakdown —
	// current where one can be taken, last known otherwise — already shaped as the
	// existing context_usage payload, for an inbound request_context_usage (#2431).
	// handleRequestContextUsage is its sole reader.
	//
	// MCPStatusFor's shape above, deliberately and in full: a context and a
	// conversation id in, a payload and a comma-ok out, so internal/relay imports
	// neither internal/sessions nor internal/streamsup and protocol is already
	// imported on both sides.
	//
	// THE READING IS FRESH AND EXPENSIVE WHEN ONE CAN BE TAKEN, which is the whole
	// reason the verb exists; see the AsOf paragraph below for when one cannot.
	// The implementation asks claude at detail:"full" — a token-count API call per
	// category — where the automatic post-turn frame (#2371) carries the cheap
	// detail:"summary" estimate. The DETAIL IS THE IMPLEMENTATION'S CHOICE and is
	// deliberately absent from the request payload; see RequestContextUsagePayload.
	//
	// IT MAY WAIT TWICE AND THE RELAY BOUNDS NEITHER. An implementation defers a
	// request that arrives mid-turn until that turn ends, then waits on a child round
	// trip. Both waits belong to the implementation, which is why this seam takes a
	// context and MUST honor it so manager shutdown terminates them.
	//
	// CLOSELY-SPACED ASKS COLLAPSE BELOW THIS SEAM, NEVER ABOVE IT. Each ask costs
	// real tokens, so an implementation answers asks arriving close together from one
	// round trip. That belongs below here because the collapse is PER CONVERSATION,
	// not per connection — two clients watching one conversation is the case it exists
	// for — and this package has no conversation-keyed state to do it in. A caller
	// must therefore not assume its own ask caused the round trip it is answered from.
	//
	// Comma-ok distinguishes a reading to show (true) from none at all (false). A
	// caller MUST NOT inspect the payload when false; the relay translates that
	// outcome to retryable context_usage.unavailable and substitutes NOTHING of its
	// own — no zero payload, no previous answer, no empty frame. That matters more
	// here than on most seams, because a ContextUsagePayload of all zeros is
	// indistinguishable from a genuine empty context, so the refusal cannot be
	// expressed in the values.
	//
	// A TRUE IS NOT A PROMISE THAT CLAUDE WAS JUST ASKED (#2461). When no fresh
	// reading can be taken — no bound session, no live child, a child that never
	// answers — an implementation MAY answer from a reading it stored earlier, and
	// the production one does, so a dormant conversation shows a figure instead of an
	// error. That is a licence for THIS seam only, and it is why the paragraph above
	// says "a reading to show" rather than "a current reading".
	//
	// AN IMPLEMENTATION THAT DOES SO MUST STAMP ContextUsagePayload.AsOf, and the
	// requirement is not stylistic. A stored reading carries the five headline values
	// and empty inventories, while that payload's own contract makes an empty
	// inventory a POSITIVE reading — claude reporting no MCP tools. The two are
	// byte-identical without the key, so an unstamped remembered answer would tell a
	// client that a conversation has no memory files rather than that nobody looked.
	// A fresh reading omits it. The relay neither sets nor inspects the key; it
	// forwards the payload it is handed.
	//
	// Optional: nil makes the inbound type consumed but INERT before payload decode,
	// membership, or reply — the nil HistoryPage / MCPActuator posture, buying the
	// same property, that an unwired daemon parses zero remote-authored bytes.
	// Foreground and v1 wirings leave it nil.
	//
	// SECURITY: conversationID is untrusted network input and reaches this seam only
	// AFTER KnownConversation has passed on it. It stays a lookup key into the
	// daemon's own registry — never returned, never joined into a path, never logged,
	// never wrapped into an error — and the reported conversation_id in the payload
	// comes out of the daemon's own registry record rather than being echoed back,
	// RunConfigFor's and ModelListFor's posture. The returned payload is
	// MIXED-PROVENANCE (ContextUsagePayload's own doc): its ConversationID is
	// daemon-authored and EVERY OTHER STRING is claude- or workspace-authored,
	// including memory-file paths off the operator's own filesystem. None of it is
	// logged on this path at any level, and it reaches the wire only over the unicast,
	// AEAD-sealed reply to the conn that asked.
	//
	// This call runs on the addressed connection's appFrameWorker, NOT on Run, for the
	// waits above. It MUST NOT touch V2Session or any Noise state — replies return
	// through forwardToRun for Run-owned sealing, and emitting from the worker would
	// be a concurrent Encrypt under the single-owner send CipherState.
	ContextUsageFor func(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool)

	// MCPActuator performs an inbound mcp_reconnect or mcp_toggle against one
	// conversation's live claude child (#2419). handleMCPReconnect and
	// handleMCPToggle are its sole readers, and the seam's own doc block carries the
	// contract — the authorization decision and its audit record are the
	// implementation's, the accepted answer's status payload crosses back rather than
	// being re-read here, and ServerName reaches it validated by nothing.
	//
	// The WRITE half of the MCP pair whose read half is MCPStatusFor above, which is
	// why it gets a per-device gate where that one needs none: these two verbs change
	// a running child's configuration.
	//
	// Optional: when nil the frame is CONSUMED BUT INERT — no reply, and not one byte
	// of its payload parsed — mirroring the nil HistoryPage / AttachmentIntake /
	// PairingMint guards and buying the same property, that an unwired daemon performs
	// zero parsing of remote-authored bytes. That is what leaves every non-production
	// construction site (unit tests, the fake-daemon e2e harness, foreground/v1)
	// compiling and behaving unchanged, and it is the whole of the #2419 slice's
	// fail-safety.
	//
	// SECURITY — NOTHING MAY BE WIRED HERE before the per-device actuation gate
	// (#2420) exists; the seam's block states the obligation and why. And note the
	// interface-nil trap, called out on THIS field because here the nil check is a
	// security gate rather than a convenience: assigning a nil-valued CONCRETE type
	// (`cfg.MCPActuator = (*impl)(nil)`) leaves this field non-nil, so dispatchAppFrame
	// admits the frame and the handler calls a method on a nil pointer. internal/relay
	// contains no recover(), so that is a crash rather than a refusal — fail-closed for
	// authorization, since no actuation reaches a child, but a remote-triggerable one
	// once the wiring bug exists. Wire a concrete non-nil implementation or leave the
	// field unset.
	MCPActuator MCPActuator

	// SystemPromptFor reports the NAMED conversation's stored system prompt and how
	// the running session's spawned-with value compares to it, already shaped as a
	// marshal-ready system_prompt payload, for an inbound request_system_prompt
	// (#2152). handleRequestSystemPrompt is its sole reader.
	//
	// CONVERSATION-KEYED, ModelListFor's shape above: a string in, a payload and a
	// comma-ok out, so this package imports neither internal/sessions nor
	// internal/conversations and protocol is already imported both sides.
	//
	// IT ANSWERS TWO QUESTIONS AT ONCE AND THAT IS THE POINT. The stored value comes
	// from the conversations registry; the spawned-with value comes from the live
	// session (#2150's session-keyed Pool.SystemPromptFor). A client needs both,
	// because a stored prompt takes effect only at the conversation's NEXT session
	// start — so the stored value alone would tell an operator who edits it and keeps
	// typing that their change is live when it is not. The producer resolves both and
	// reports the COMPARISON rather than the second value; see the collapse rule
	// below.
	//
	// THE COMPARISON IS COMPUTED ON THE COLLAPSED STORED VALUE, and getting this
	// wrong is the single most likely way this path ships broken. The registry stores
	// a tri-state (nil = no prompt, non-nil "" = explicitly empty, otherwise text)
	// and Pool.SystemPromptFor returns "" for BOTH no-bytes states by design, because
	// composing is the sessions package's business. So a conversation storing an
	// explicitly empty prompt whose session spawned with no operator text MATCHES,
	// and must not be reported as differing. The rule lives in the cmd/pyry producer,
	// which is the only place holding both halves; this package neither re-derives it
	// nor second-guesses it.
	//
	// TWO DIFFERENT RESOLUTION FAILURES, NOT ONE, and the seam keeps them apart even
	// though the wire merges them. false means this daemon does not host the named
	// conversation. A hosted conversation with no running session is TRUE, with the
	// payload's SessionPromptStatus at protocol.SystemPromptStatusNoSession — the
	// stored value is still reported, because "there is nothing running" is exactly
	// when an operator most needs to see what is stored. Collapsing the two at the
	// producer would make that impossible to express.
	//
	// Comma-ok, and a caller MUST NOT read the payload on false — the rule
	// RunConfigFor and ModelListFor both state, and it is pinned here against a
	// POISONED refusal double rather than borrowed from the producer's habit of
	// zeroing its refusal return. Unlike ModelListFor, though, false is not turned
	// into an error frame: this verb answers the constant no-session reply, which is
	// a real answer, so an unhosted conversation is indistinguishable from a hosted
	// one holding nothing and the verb is not a membership oracle.
	//
	// Optional: nil ⇒ every request is answered with that same constant reply
	// (foreground / v1 / unwired), never an error and never a silent drop. This is
	// deliberately NOT SettingsUpdater's nil posture: that seam is a write path that
	// owes a distinguishable "unavailable", where a read documented as always
	// answering one shape has nothing to gain from a second one.
	//
	// BOUNDED TIME, on the Run goroutine. The handler answers inline rather than
	// handing off to the conn's appFrameWorker, so an implementation MUST stay a
	// bounded in-memory read — production wires a registry lookup plus one pool map
	// read. An implementation that reads a file belongs off Run, and moving it there
	// means switching the handler's emit from forwardEnvelope to forwardToRun in the
	// same change (see the handler's file header).
	//
	// SECURITY: conversationID is untrusted network input and stays a lookup key into
	// the daemon's own registry — never returned, never joined into a path, never
	// logged, never wrapped into an error. The SESSION id the producer reads the
	// spawned-with value under is daemon-authored, taken from the resolved registry
	// record, so a caller can never reach another conversation's session through this
	// seam (the #678 hazard, closed by construction). The returned SystemPrompt is
	// OPERATOR-AUTHORED TEXT that becomes standing instructions to a claude child: it
	// is never logged at any level, and it reaches the wire only over the unicast,
	// AEAD-sealed reply to the conn that asked — never the broadcast push path, which
	// is why the write half's conversation_updated ack carries no prompt either.
	// Read-only reflection: this seam holds nothing that can start, restart, rotate or
	// interrupt a session, which is the structural half of "reading the prompt leaves
	// a running session alone". Do not widen it.
	SystemPromptFor func(conversationID string) (protocol.SystemPromptPayload, bool)

	// PairingMint mints a pairing for another device on behalf of the conn's
	// authenticated one, for an inbound mint_pairing (#2127). handleMintPairing is
	// its sole reader, and the seam's own doc block carries the contract — the
	// authorization decision and its audit record are the implementation's, the
	// minted device is always unprivileged, and the label arrives shape-validated.
	//
	// Optional: when nil the frame is CONSUMED BUT INERT — no reply, and not one
	// byte of its payload parsed — mirroring the nil AttachmentIntake /
	// AttachmentResolve / HistoryPage guards and buying the same property, that an
	// unwired daemon performs zero parsing of remote-authored bytes. That is what
	// leaves every non-production construction site (unit tests, the fake-daemon
	// e2e harness, foreground/v1) compiling and behaving unchanged.
	//
	// SECURITY: this is the only seam on the manager that MINTS A CREDENTIAL, and
	// the returned string is a plaintext bearer token. It reaches exactly one place
	// — the pairing field of the AEAD-sealed pairing_minted reply, unicast to the
	// conn that asked — and is never logged, never broadcast, and never wrapped
	// into an error. Wiring it widens docs/protocol-mobile.md § Security model
	// threat 4 on purpose (#2126's published decision): minting authority moves
	// from "a shell on the host" to "any privileged paired device", bounded by the
	// minted device being always unprivileged and by an unredeemed token expiring
	// at devices.RedemptionWindow.
	PairingMint PairingMinter

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

	// AttachmentResolve answers the on-host path of one stored attachment inside
	// one conversation (#2054) — the READ half of the seam above, and the retrieval
	// leg's only filesystem reach. handleRequestAttachment is its sole reader.
	// Optional: when nil the request_attachment frame is STILL CONSUMED by the
	// interception — it no longer reaches dispatch.Route and so no longer draws its
	// unknown-type error reply — but nothing is decoded, nothing is resolved and
	// nothing is replied. That is the whole of the nil behaviour, matching
	// AttachmentIntake's, and it is what leaves every non-production construction
	// site compiling and behaving unchanged.
	//
	// COMMA-OK, NOT AN ERROR, and the collapse is the point rather than a
	// simplification. attachments.ResolvePath answers ONE sentinel for an unknown
	// id, a non-canonically-shaped id and an id resolving outside the named
	// conversation's directory alike, precisely so a dispatch site cannot branch on
	// what CodeAttachmentNotFound's deliberate merge forbids distinguishing; and its
	// shape-invalid refusal formats the RAW client-supplied id into its message,
	// which docs/protocol-mobile.md § Attachments forbids logging. So the error dies
	// at the one adapter that ever holds it — cmd/pyry's attachmentResolve closure,
	// already built there for handlers.SendMessage — and this seam receives only the
	// bool. Primitive-typed in both directions, like KnownConversation, so
	// internal/relay imports neither internal/attachments nor internal/conversations
	// for it.
	//
	// IT DISCHARGES NO REGISTRY CHECK WHATSOEVER. attachments.ResolvePath's stated
	// precondition is that conversationID is one the CALLER has already validated
	// against the daemon's registry — "this function cannot check that, and a caller
	// that gets it wrong defeats every check below" — and the production closure
	// validates nothing. handleRequestAttachment is that caller: it consults
	// KnownConversation BEFORE either id reaches this seam, which is what keeps an
	// identifier § Attachments repeatedly calls not a capability from becoming one.
	//
	// SECURITY: the only sanctioned implementation wraps attachments.ResolvePath.
	// StreamAttachment re-validates nothing it is handed, so a path from anywhere
	// else carries NO containment guarantee — the traversal defence is that
	// function's full-path equality check — and a path naming a NON-REGULAR FILE
	// misbehaves rather than erroring: os.ReadFile on a FIFO blocks indefinitely,
	// which here would wedge the conn's appFrameWorker and silently stall every
	// later frame on that conn. ResolvePath answers only regular files at exactly
	// the path its two ids build, so both are unreachable through it. The returned
	// path is NOT fully daemon-authored — its leaf is a sanitised client filename —
	// so it must never be logged, and no reply derived from it ever reaches the wire.
	AttachmentResolve func(conversationID, attachmentID string) (path string, ok bool)

	// Interrupter stops the running turn in the conversation an inbound
	// interactive `interrupt` control frame names (#707, #2103) — or, when the
	// frame names none, in the one the daemon's cursor points at. Optional: nil ⇒
	// interrupt is inert (no actuation) — the foreground / unwired case.
	// Production wires cmd/pyry's activeInterrupter, which owns the shape check
	// and the registry resolution this package cannot perform.
	Interrupter Interrupter

	// SessionStarter starts a fresh session in the conversation an inbound
	// interactive `new_session` control frame names (#831, #2099) — or, when the
	// frame names none, in the one the daemon's cursor points at. Optional: nil ⇒
	// new_session is inert — the foreground / unwired case. Production wires
	// cmd/pyry's activeSessionStarter, which owns the shape check and the registry
	// resolution this package cannot perform.
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

	// RetainedBackgroundTaskRosters enumerates the background-task rosters the
	// daemon currently holds as marshal-ready background_task_roster payloads (one
	// per session holding a roster, each already stamped with its own
	// conversation_id) for connect-time reconcile (#2078) — the sixth Mode B
	// instance after OutstandingModals, OutstandingQueues, RetainedModelLists,
	// OutstandingQuestions and RetainedSlashCommandLists. Called on the Run
	// goroutine from handleNoiseInit's interactive-open tail; the returned payloads
	// are unicast to the just-opened conn only. background_task_roster is
	// snapshot-shaped full state ("A SNAPSHOT, not a delta",
	// BackgroundTaskRosterPayload's own doc) — it reports what is alive at one
	// moment rather than what changed, it is conversation-scoped rather than
	// turn-scoped, and receiving one neither opens nor closes a turn — so the
	// re-send is idempotent by construction: re-connecting re-sends the same
	// snapshot. A pure read: it mints nothing, retires nothing, and changes no
	// daemon state.
	//
	// The reconcile exists because neither recovery mode in
	// docs/protocol-mobile.md § Reconnect / Backfill semantics serves this family
	// today. Mode A (cursor replay) needs the client to advertise
	// hello.last_event_id and pyrycode-desktop advertises none, whose stated
	// consequence is no replay at all; Mode B did not cover this frame until this
	// seam. So a client opening an interactive session sees an empty background-task
	// panel until claude next CHANGES the roster, which on a quiet session may never
	// happen — the roster is only ever emitted on the live turn lane.
	//
	// AN EMPTY ROSTER IS A POSITIVE STATEMENT that nothing is alive, and this is the
	// one place the five seams above give the wrong answer by analogy. Their
	// producers filter an empty aggregate away; a producer for this seam MUST NOT,
	// because "nothing is running" is exactly the signal a consumer of #1240's
	// symptom needs, and BackgroundTaskRosterPayload.MarshalJSON exists to guarantee
	// such a payload serialises as "tasks":[] rather than null. The consumer
	// likewise sends it rather than skipping it.
	//
	// A closure returning []protocol.BackgroundTaskRosterPayload, not a
	// *sessions.Pool or a turnevent value: internal/relay imports neither
	// internal/sessions nor internal/turnevent (sessions appears only transitively
	// via internal/control, so a go list -deps reading looks like a contradiction
	// and is not one), and protocol is already imported, so the payload crosses the
	// boundary with no new import and no cycle (matching the five seams above —
	// define the dependency where it is consumed).
	//
	// Enumerate-all, not conversation-keyed. A V2Session carries no conversation id
	// — it holds connID, state, resp, send, recv, device, interactive and peerStatic
	// — so there is nothing to key on at connect time; each payload self-identifies
	// by its own conversation_id. RetainedModelLists, OutstandingQuestions and
	// RetainedSlashCommandLists state the same reasoning.
	//
	// Order is not part of the contract, and a caller MUST correlate a roster by its
	// conversation_id rather than by its position in the returned slice — the
	// envelope id this path stamps is fixed and non-load-bearing for the same
	// reason.
	//
	// BOUNDED TIME, like every seam the manager calls on its Run goroutine: an
	// implementation that blocks stalls Run and with it every conn the manager
	// services. ModalResolver's doc block states the same obligation, and it is not
	// hypothetical here — the #2079 producer walks a conversation registry under
	// that registry's mutex, which is exactly the shape that can block.
	//
	// SECURITY: this seam accepts ALREADY-BOUNDED payloads only. The reconcile path
	// applies no bound of its own — not on how many payloads are returned, not on
	// any row's text — because the bound is decided upstream at construction
	// (BackgroundTaskRosterPayload.DroppedTasks on the aggregate,
	// BackgroundTask.TruncatedFields per row, over internal/streamsup's
	// maxTaskRosterEntries and maxTaskRosterDescription caps). A second cap here
	// would be a second place the limit is decided and the two could disagree
	// silently, so the obligation stays the producer's. Note that those per-payload
	// bounds do NOT bound how many payloads can be returned at once; on this path
	// pushQueue's byte ceiling is the backstop, and a cardinality cap, if one is ever
	// wanted, belongs to the producer and not to either half of this reconcile. All
	// four strings a task row carries — TaskID, TaskType, Description and each entry
	// of TruncatedFields — are claude-authored, untrusted text (BackgroundTask's own
	// doc) and are NEVER logged on the reconcile path. Description is a literal
	// command line for the local_bash task type, which BackgroundTask's doc grades
	// as the more tempting shape of this family precisely because a LIST of command
	// lines invites being fed somewhere structured; it is safe to RENDER as inert
	// text and never to execute, re-shell, or feed to an HTML sink, an attribute or
	// a URL.
	//
	// Optional: nil ⇒ no reconcile — byte-identical to the pre-#2078 / foreground /
	// existing-test posture, the nil-resolver posture the other optional control
	// seams share. #2079 wires the daemon-side producer.
	RetainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload
}

// HistoryPager is the shape of the V2SessionConfig.HistoryPage seam (#2116),
// which carries that field's full contract; this block carries only what is
// about the SHAPE.
//
// PRIMITIVES IN, protocol TYPES OUT, and the asymmetry is the decision. Inbound,
// the three values are the client's own claims and share no type worth carrying.
// Outbound, the entries ARE the wire shape: protocol.HistoryEntry mirrors
// history.Entry key for key, so returning four parallel primitive slices instead
// would put that correspondence in a SECOND place, free to drift from a key set
// TestHistoryPagePayload_WireKeys has frozen. internal/relay already imports
// internal/protocol in every file; it imports internal/history nowhere, and this
// seam is what keeps that true.
//
// NOT COMMA-OK, unlike AttachmentResolve, and that is why it has a result struct
// at all. A bool collapses every failure into one answer, which is right there —
// six causes deliberately share attachment.not_found — and wrong here: the merged
// cursor refusal has to stay SEPARABLE from the read failures for the handler to
// pick between history.invalid_cursor and history.unavailable, which differ in
// the retryable flag a client branches on.
type HistoryPager func(conversationID, cursor string, limit int) HistoryPageResult

// HistoryPageResult is one answer from the HistoryPage seam: history.Page's three
// fields, restated in wire types, plus the outcome that says whether they mean
// anything.
//
// Entries, Cursor and AtStart are meaningful ONLY when Outcome is HistoryPageOK.
// On any other outcome they are the zero values and the handler answers a reject
// instead of reading them.
type HistoryPageResult struct {
	// Entries is this page's entries, NEWEST-FIRST, forwarded to the client
	// verbatim.
	//
	// NEVER SIZED FROM limit. An implementation allocates from len() of what the
	// log actually returned; make([]protocol.HistoryEntry, 0, limit) reads as
	// ordinary Go and would pin capacity chosen by a remote caller, which is the
	// rule protocol.RequestHistoryPayload.Limit states in as many words.
	//
	// The payload bytes inside are REPLAYED CONTENT and carry the trust class of
	// the live frame they mirror — claude-authored for a stored assistant frame,
	// so § Security model's threat 1 lands on them. They are forwarded UNCHANGED:
	// not re-decoded, not re-encoded, not sanitised. That is what makes a page
	// reducible through the client's existing live-lane reducer, and the
	// sanitisation is the client's, exactly as it is on the live lane.
	Entries []protocol.HistoryEntry

	// Cursor is the opaque position to ask with next. Empty whenever AtStart.
	Cursor string

	// AtStart reports that the start of the log was reached while filling this
	// page — the ONLY termination signal for a walk. It comes from the log and is
	// never synthesised: a page the handler shortened to fit the envelope cap
	// carries whatever the log said for the smaller ask, so shortening cannot
	// forge an end-of-log.
	AtStart bool

	// Outcome says whether the three fields above mean anything.
	Outcome HistoryPageOutcome
}

// HistoryPageOutcome discriminates the seam's answers finely enough for the
// handler to choose a wire code, and no more finely than that.
//
// THREE MEMBERS, NOT ONE PER SENTINEL. internal/history raises six sentinels a
// Page call can reach, and the published reject vocabulary has one code for the
// cursor and one for everything else that fails, so a per-sentinel enum would be
// two bodies duplicated three ways — and would tempt a future caller into
// answering the distinctions the merge exists to hide.
type HistoryPageOutcome uint8

const (
	// HistoryPageOK means the page is the log's answer, including the empty
	// terminal page a conversation with no log reads as.
	//
	// THE ZERO VALUE DELIBERATELY: an implementation that forgets to set an
	// outcome reports success with no entries, which is that same terminal page —
	// inert, and never a refusal a client did not earn.
	HistoryPageOK HistoryPageOutcome = iota

	// HistoryPageBadCursor is the ONE merged client fault: a cursor that does not
	// decode, one minted for another conversation, and one naming a position not
	// in this log arrive here identically, because internal/history raises the
	// single ErrInvalidCursor sentinel for all three. The handler therefore
	// CANNOT branch on what it must not distinguish.
	HistoryPageBadCursor

	// HistoryPageUnavailable is every daemon-side failure: a corrupt or
	// unknown-version segment, a containment refusal, an I/O error, and a daemon
	// whose log is not wired at all. It is the only RETRYABLE outcome, because
	// every cause can clear without the client changing its request.
	//
	// It also absorbs the two refusals that are unreachable by construction —
	// a non-canonical conversation id and a page size below one — since the
	// membership gate fires first and the handler never passes a limit below one.
	// Answering a daemon bug as a daemon problem rather than as the client's
	// malformed request is the fail-safe direction.
	HistoryPageUnavailable
)

// PairingMinter mints a pairing for ANOTHER device on behalf of an already-paired
// one (#2127), serving the wire contract #2126 declared. Declared here
// (consumer side), beside ModalResolver and QuestionResolver, so internal/relay
// imports none of what minting actually needs — crypto/rand, internal/pair,
// internal/keys, internal/identity, internal/audit — and learns neither the relay
// URL nor the server id it would otherwise have to be told. *devices.Device crosses
// the seam (the per-conn s.device); this package already imports internal/devices,
// so no new import. cmd/pyry's resolver is the sole production implementation.
//
// THE AUTHORIZATION DECISION IS THE IMPLEMENTATION'S, NOT THE HANDLER'S, and that
// is the seam's defining property rather than a division of labour. A refusal must
// be recorded through internal/audit, which lives in cmd/pyry; separating "deny"
// from "record the denial" across two packages is how a later edit ends up with one
// and not the other — questionResolverV2.admit's stated reason, transferred. The
// handler applies no privilege check of its own and MUST NOT grow one: a second
// arbiter of the same question is a second thing to keep in agreement.
//
// IT RUNS ON THE CONN'S appFrameWorker, NOT on the manager's Run goroutine, which
// is what lets it take a file lock and write devices.json at all. It still stalls
// that conn's later frames while it runs, so an implementation MUST return in
// bounded time — production bounds its lock acquisition explicitly rather than
// waiting the devices package default.
//
// SECURITY — three obligations Go's type system cannot state, restated here because
// this is where an implementer meets them:
//
//   - deviceName IS REMOTE-AUTHORED, and it arrives ALREADY SHAPE-VALIDATED. The
//     handler bounds it (protocol.MaxDeviceNameBytes, enforced at decode) and
//     refuses any C0 control, DEL or C1 control before this seam is reached, which
//     is what makes the value safe to store, to log and to render in a
//     `pyry pair list` column. UTF-8 validity is NOT among those checks and does
//     not need to be: encoding/json replaces every invalid byte and unpaired
//     surrogate with U+FFFD, so a decoded string is valid by construction — the
//     decoder's guarantee, spelled out at mintLabelIsDisplaySafe. An
//     implementation MUST NOT relax the rest of the assumption by re-deriving the
//     name from anywhere else, and MUST NOT let it become a path component —
//     resolveDevicesPath sanitises the INSTANCE name, never a device name.
//   - THE MINTED DEVICE IS ALWAYS UNPRIVILEGED. MintPairingPayload has no field for
//     devices.Device.AllowRemotePermissions by declaration, and an implementation
//     MUST pass false as a literal rather than threading a value from anywhere. A
//     stolen privileged pairing can mint devices that watch and send; it must never
//     be able to mint one that approves.
//   - requester IS THE AUTHENTICATED DEVICE AND THE ONLY IDENTITY THERE IS. It is
//     the per-conn s.device, bound at handshake after the presented token validated,
//     and no field of the request names a device. A nil requester is a conn with no
//     authenticated device and MUST deny — devices.Device.MayAnswerRemotePermission
//     is nil-receiver-safe precisely so that reads as ordinary code.
type PairingMinter interface {
	// MintPairing mints one pairing for a new device and returns it encoded, or
	// says why it did not. deviceName is the label to file the record under; the
	// EMPTY STRING is not an error and not a refusal — it is "the client named no
	// device", which an implementation answers with the same device-<hash8>
	// fallback `pyry pair` generates, so the two entry points produce
	// indistinguishable records.
	MintPairing(requester *devices.Device, deviceName string) PairingMintResult
}

// PairingMintResult is one answer from the PairingMinter seam.
//
// AN OUTCOME DISCRIMINANT, NEVER AN ERROR, and the reason is specific rather than
// stylistic: every error a mint can fail with wraps the ABSOLUTE devices.json path
// — devices.WithLock's open/flock wraps, devices.Load's and Registry.Save's own —
// so an error crossing this boundary would put a host path in reach of a handler
// whose entire discipline is that it holds none, one interpolation away from a log
// line or a reply. The error dies at the single scope that ever holds it.
//
// TWO REFUSALS RATHER THAN ONE, HistoryPageResult's reason: they differ in the
// retryable flag a client actually branches on, so a comma-ok would collapse a
// permanent refusal and a transient one into the same answer.
type PairingMintResult struct {
	// Pairing is the pair.Encode string — the {server, relay, token,
	// server_static_pubkey} tuple as base64url, byte-for-byte what `pyry pair`
	// prints on this host.
	//
	// A PLAINTEXT BEARER CREDENTIAL. It is never logged, no field decoded out of it
	// is ever logged, and its ONLY egress is the pairing field of the AEAD-sealed
	// pairing_minted reply unicast to the conn that asked. EMPTY unless Outcome is
	// PairingMintOK, so a handler that ignored the outcome would emit nothing rather
	// than a stale credential.
	Pairing string

	// Outcome says whether Pairing means anything.
	Outcome PairingMintOutcome
}

// PairingMintOutcome discriminates the seam's answers finely enough for the handler
// to choose a wire code, and no more finely than that.
//
// THREE MEMBERS, NOT ONE PER CAUSE. A busy lock, a registry read failure, a write
// failure and an RNG failure all mean the same thing to a client and share one
// published code; a per-cause enum would tempt a future handler into answering
// distinctions protocol.CodePairingUnavailable's merge exists to hide.
type PairingMintOutcome uint8

const (
	// PairingMintOK means a record was written and Pairing carries the credential.
	//
	// NOT the zero value, and that is the one place this enum departs from
	// HistoryPageOutcome. There, a forgotten outcome reports an empty terminal page
	// — inert. Here it would report a successful mint with an empty credential,
	// which is a refusal spelled as a success. The zero value therefore denies.
	PairingMintOK PairingMintOutcome = iota + 1

	// PairingMintUnauthorized is the privilege refusal: the requesting device is
	// authenticated but does not carry devices.Device.AllowRemotePermissions, or no
	// longer does. NOTHING WAS CREATED — an implementation MUST reach this outcome
	// without having written a record, and the handler's contract with a client is
	// that a refusal leaves the registry untouched.
	PairingMintUnauthorized

	// PairingMintFailed is every host-side failure, merged: a devices.json lock the
	// implementation could not acquire, a registry that could not be read or
	// written, and a CSPRNG that refused. The only RETRYABLE outcome, because every
	// cause can clear without the client changing its request.
	PairingMintFailed
)

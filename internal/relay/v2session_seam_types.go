package relay

import (
	"context"
	"errors"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the V2SessionManager's dependency contracts and configuration,
// carved out of v2session.go so they read in isolation from the manager core.
// It collects the seven seam interfaces the manager depends on —
// Interrupter, SessionStarter, QueueRemover, SettingsUpdater,
// ModalResolver, QuestionResolver, and AttachmentIntake — declared consumer-side per CODING-STYLE
// ("define interfaces where they are consumed"), plus the SettingsUpdate and RunConfig value types, the
// ErrSessionUnknown sentinel, and the ~260-line V2SessionConfig struct. Pure
// move: same package, no behaviour
// change, no call-site change. This is the final #964 slice, after #1026
// (snapshot/replay). The manager core — the V2Session / V2SessionManager structs,
// NewV2SessionManager, Run, handleWake, handleFrame, the app-frame router, the
// outbound send/drain path, and the connection registry — stays in v2session.go.

// BackgroundTaskStopOutcome is the result of one task stop attempt. Refused is
// the zero value so an implementation must explicitly accept an attempt.
type BackgroundTaskStopOutcome uint8

const (
	BackgroundTaskStopRefused BackgroundTaskStopOutcome = iota
	BackgroundTaskStopAccepted
	BackgroundTaskStopCannotActOnConversation
)

// BackgroundTaskStopper stops one named task. The implementation owns conversation
// validation and resolution: ids are untrusted lookup keys, never paths, and must
// never select the cursor conversation. CannotActOnConversation silently means the
// requested conversation cannot be acted on; Refused produces the fixed wire error.
// The call runs on the connection's appFrameWorker and must honor ctx cancellation.
// It receives no device permission decision: stopping is a paired-device action.
type BackgroundTaskStopper interface {
	StopBackgroundTask(ctx context.Context, conversationID, taskID string) BackgroundTaskStopOutcome
}

// Interrupter stops the running turn in ONE conversation (#707, widened by #2103)
// — the remote equivalent of a local Esc, claude's own interrupt. Declared here
// (consumer side), so internal/relay imports neither internal/supervisor nor
// tui-driver (CODING-STYLE: define interfaces where they are consumed). Named for
// its relay-domain role (matching ModalResolver.Resolve*), even though the
// method keeps the sealed surface's name — renaming it to match the actuation
// would churn the whole package for no behavioural gain, and this doc is where
// SendEsc is abstracted as "claude's own interrupt" (#1121). SendEsc is safe to
// call from any goroutine.
//
// The sole implementation is cmd/pyry's activeInterrupter. The bootstrap
// supervisor used to satisfy this seam, until #1121 replaced that wiring because
// it mis-delivered every interrupt to the bootstrap supervisor regardless of which
// conversation's turn was running; #1348 then deleted the supervisor. The
// activeInterrupter doc records the same history, and this block said otherwise
// until #2103.
//
// conversationID names the conversation whose turn to stop, and the implementation
// MUST treat it as UNTRUSTED: it arrives from a paired client, and this package
// deliberately validates nothing about it. internal/relay imports neither
// internal/conversations nor internal/sessions, so it can neither shape-check the id nor
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
// role (matching Interrupter), even though the method keeps
// the sealed surface's name, so the name is unambiguous against any pool/session
// lifecycle "start". StartNewSession is safe to call from any goroutine.
//
// conversationID names the conversation to restart, and the implementation MUST
// treat it as UNTRUSTED: it arrives from a paired client, and this package
// deliberately validates nothing about it. internal/relay imports neither
// internal/conversations nor internal/sessions, so it can neither shape-check the
// id nor resolve it — the
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
// rotates inline needs no change and sees no new behaviour.
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

// AgentSwitcher switches a named conversation on a relay worker, never on Run.
// SwitchAgent may block during wrap-up. It must honor manager cancellation, be
// safe for concurrent calls and classify admission/busy failures itself. Client
// disconnect does not cancel ctx. The conversation ID is untrusted: implementations
// must validate and resolve it without a cursor fallback before any path use.
// Production adaptation and committed event publication belong to cmd/pyry.
type AgentSwitcher interface {
	SwitchAgent(ctx context.Context, request protocol.SwitchAgentPayload) AgentSwitchOutcome
}

// AgentSwitchState distinguishes an inert preflight refusal from an uncommitted
// failure (which may follow wrap-up) and commitment even with cleanup failure.
type AgentSwitchState uint8

const (
	AgentSwitchFailed AgentSwitchState = iota
	AgentSwitchRefused
	AgentSwitchCommitted
)

// AgentSwitchFailure contains only safe classifications, never downstream text.
type AgentSwitchFailure uint8

const (
	AgentSwitchOtherFailure AgentSwitchFailure = iota
	AgentSwitchConversationNotFound
	AgentSwitchInvalidRequest
	AgentSwitchModelNotOffered
	AgentSwitchEffortNotOffered
	AgentSwitchVocabularyUnavailable
	AgentSwitchBusy
	AgentSwitchWorkspaceRejected
)

// AgentSwitchOutcome is the switch result. A nonempty daemon session ID means
// State MUST be AgentSwitchCommitted, even if cleanup failed. Committed outcomes
// owe no error reply; existing state events are the adapter's responsibility.
// The zero value is an uncommitted offline failure, never silent success.
type AgentSwitchOutcome struct {
	State   AgentSwitchState
	Failure AgentSwitchFailure
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

// QueueSender writes a queued message into the conversation's running turn
// instead of waiting for idle (#2729). *msgqueue.Queue satisfies it. Declared
// here beside QueueRemover for the same reason. Returns true iff the message was
// written; every refusal (idle turn, unknown id, committing head, a session that
// cannot take input mid-turn, a failed write) is a safe no-op (false) that leaves
// the backlog as it was. The conversationID arg is the mutation scope, as for
// QueueRemover.
type QueueSender interface {
	SendNow(conversationID string, queuedMsgID uint64) bool
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
// eviction.
//
// An empty Model or Effort is a REAL reported value — "inherited daemon default,
// no per-session override" — and YOLO false means permissions are enforced. So
// the zero RunConfig is
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

// AgentCapabilities is the half of a session's capability list that depends on
// its agent and model (#2646), the value half of V2SessionConfig.CapabilitiesFor.
// Primitives only, like RunConfig. The relay composes it with the permission
// modes and attachment types it owns into protocol.SessionCapabilities.
type AgentCapabilities struct {
	Interrupt          bool
	MidTurnInput       bool
	SlashCommands      bool
	MCPServers         bool
	ContextUsageDetail bool
	EffortLevels       []string
	Models             []string
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

// ErrEffortNotOffered means a well-formed non-empty effort is not offered by the
// model the session will run: absent from that model's advertised levels, or,
// when the model has no entry for its agent, outside the fallback set (#2629).
// The settings handler replies exactly as it does to an effort validEffort
// refuses — the fixed, non-retryable protocol.malformed — and carries no value.
var ErrEffortNotOffered = errors.New("relay: effort not offered")

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state. Declared here (consumer side), so internal/relay
// imports neither internal/supervisor nor cmd/pyry; the cmd/pyry resolver
// satisfies it. *devices.Device crosses the
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
// choosing a content-free terminal log reason — QueueRemover.Remove's exact role
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

// DiagnosticQuestionResolver optionally extends QuestionResolver with the outcome
// of the same resolution attempt. The handler calls a diagnostic method INSTEAD
// OF its bool-only counterpart, never both. Existing configuration wiring and
// bool-only implementations remain valid.
//
// Each method performs resolution under QuestionResolver's validation,
// eligibility-before-consume and bounded-time obligations. consumed reports
// whether it consumed the batch. On non-consumption, reason MUST be a stable,
// content-free outcome code: never client text, an answer token, a payload value
// or an error message. On consumption the handler logs "resolved" regardless of
// reason. Neither result triggers a reply, dismissal or security audit decision.
type DiagnosticQuestionResolver interface {
	QuestionResolver
	ResolveAnswerDiagnostic(p protocol.QuestionAnswerPayload, dev *devices.Device) (consumed bool, reason string)
	ResolveRefusalDiagnostic(p protocol.QuestionRefusedPayload, dev *devices.Device) (consumed bool, reason string)
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
//     stolen privileged pairing can mint devices that watch, send and answer
//     prompts (every device may, since #2605); it must never be able to mint one
//     that can itself mint or actuate MCP servers.
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

// WorkspaceFile is one live read answered by the WorkspaceFileRead seam (#2598).
//
// AttachmentID is daemon-minted per transfer — a lowercase UUIDv4 that keys this
// one chunk stream and addresses nothing afterwards: nothing is stored under it.
// Filename is the base name of the RESOLVED file, which after symlink
// resolution may differ from the leaf the client named. Neither Filename nor
// Data may be logged.
type WorkspaceFile struct {
	AttachmentID string
	Filename     string
	Data         []byte
}

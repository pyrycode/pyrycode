package relay

import (
	"context"
	"errors"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the seam interfaces, outcome types and sentinel errors that
// V2SessionConfig (v2session_seams.go) refers to. Each seam is declared here, on
// the consumer side (CODING-STYLE: define interfaces where they are consumed), so
// internal/relay depends on the behaviour it needs rather than on the packages
// that implement it. cmd/pyry supplies every production implementation.

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

// Interrupter stops the running turn in one conversation, the remote equivalent
// of a local Esc (claude's own interrupt). SendEsc is safe to call from any
// goroutine. cmd/pyry's activeInterrupter is the sole implementation.
//
// conversationID is untrusted: it comes from a paired client and this package
// validates nothing about it, because every check lives in the composition root.
// An implementation MUST shape-check it before any use and MUST NOT let it become
// a path component.
//
// The empty string means the conversation the daemon's cursor points at, which
// is also what an older client that sends no payload gets. handleInterrupt maps
// every "nothing named" shape (absent payload or field, explicit "", undecodable
// body) onto it, so an implementation has one such state to handle.
//
// A returned error is a best-effort diagnostic that the caller only logs. A
// conversation with no live child returns streamsup.ErrNoLiveChild having written
// nothing, so the call is inert there.
type Interrupter interface {
	SendEsc(conversationID string) error
}

// SessionStarter starts a fresh session in one conversation. StartNewSession is
// safe to call from any goroutine. cmd/pyry's activeSessionStarter is the
// production implementation.
//
// conversationID is untrusted, with Interrupter's obligations: an implementation
// MUST shape-check it before any use and MUST NOT let it become a path component.
// The empty string means the daemon's cursor conversation, and handleNewSession
// maps every "nothing named" wire shape onto it, as handleInterrupt does.
//
// A *RotatedWithoutWorkspaceError reports a rotation that completed with one
// caveat, and handleNewSession checks for it before its logging arm. Any other
// non-nil error means the rotation did not happen; the caller logs it and owes no
// reply.
type SessionStarter interface {
	StartNewSession(conversationID string) error
}

// LateSessionStarter is an optional widening of SessionStarter for an
// implementation whose rotation finishes after the call returns (production first
// asks the outgoing child for a handoff note, bounded at ninety seconds).
// handleNewSession uses it when the configured SessionStarter implements it and
// falls back to StartNewSession otherwise.
//
// It exists because StartNewSession's one meaningful answer,
// *RotatedWithoutWorkspaceError, may only describe a rotation that has already
// happened. A deferred rotation reports through outcome once the answer is true.
//
// outcome receives exactly what StartNewSession would have returned, with the
// same meanings. An implementation MUST call it exactly once, MAY call it after
// StartNewSessionLate has returned, and MUST NOT call it while holding a lock a
// caller could hold. outcome runs on the implementation's goroutine, so
// handleNewSession passes one that only hands the value to Run, where the reply is
// sealed.
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
// completed but the conversation's recorded workspace was refused, so the
// successor child runs in the directory the runner already had. handleNewSession
// answers it with one coded error reply to the conn that asked, correlated by
// in_reply_to. Callers match it with errors.As.
//
// It is not a failure: by the time it exists the pool is re-keyed, the
// conversation rebound and the session_transition broadcast. An implementation
// MUST NOT return it when the rotation itself failed. It is a type rather than a
// sentinel because the reply must name the conversation, and a bare new_session
// names none.
//
// Security: ConversationID is daemon-resolved (it passed conversations.ValidID, or
// it is the daemon's cursor id), never the raw client string and never a path.
// Error returns a constant that names neither the id nor the workspace path,
// because handleNewSession's other arm logs seam errors verbatim. The confinement
// error behind the refusal is discarded in cmd/pyry and never reaches this value.
type RotatedWithoutWorkspaceError struct {
	// ConversationID is the conversation that rotated: the one the frame named, or
	// the daemon's cursor conversation when it named none.
	ConversationID string
}

// Error returns a constant message; see the type doc's security note.
func (e *RotatedWithoutWorkspaceError) Error() string {
	return "relay: new_session rotated without the conversation's recorded workspace"
}

// QueueRemover drops a not-yet-drained queued message from a conversation's
// inbound backlog by id; *msgqueue.Queue satisfies it. Remove returns true iff a
// message was removed. An unknown or foreign conversation, an unknown or
// already-delivered id, or the draining head is a safe no-op (false).
// conversationID is the mutation scope: Remove never touches another
// conversation's backlog.
type QueueRemover interface {
	Remove(conversationID string, queuedMsgID uint64) bool
}

// QueueSender writes a queued message into the conversation's running turn
// instead of waiting for idle; *msgqueue.Queue satisfies it. SendNow returns true
// iff the message was written. Every refusal (idle turn, unknown id, committing
// head, a session that cannot take input mid-turn, a failed write) is a safe no-op
// (false) that leaves the backlog as it was. conversationID is the mutation scope,
// as for QueueRemover.
type QueueSender interface {
	SendNow(conversationID string, queuedMsgID uint64) bool
}

// SettingsUpdate is the presence contract for an inbound set_session_settings
// change: a nil field leaves the stored value untouched and a non-nil field sets
// it, including *"" for Model or Effort and *false for YOLO. It mirrors
// sessions.SettingsUpdate field for field, so the cmd/pyry adapter passes the
// pointers straight through. An absent YOLO leaves the stored value alone, so it
// can never enable --dangerously-skip-permissions.
type SettingsUpdate struct {
	Model  *string
	Effort *string
	YOLO   *bool

	// PermissionMode names the posture to switch to, mirroring
	// sessions.SettingsUpdate.PermissionMode. handleSetSessionSettings has already
	// checked it against validPermissionMode, so a non-nil value is one of claude's
	// five non-escalating modes, never "" or bypassPermissions. The escalation
	// travels only as YOLO, which keeps the bypass fail-safe to a single bit.
	PermissionMode *string
}

// SettingsUpdater persists a per-session settings change named by an inbound
// set_session_settings frame; the cmd/pyry adapter wraps
// *sessions.Pool.UpdateSettings. UpdateSettings returns ErrSessionUnknown for a
// session the daemon does not host, which the handler answers with
// session.not_found. ErrModelNotOffered, ErrModelVocabularyUnavailable and
// ErrEffortNotOffered are validation outcomes returned before any mutation. Any
// other error is a persistence failure, reported as server-unavailable.
type SettingsUpdater interface {
	UpdateSettings(sessionID string, update SettingsUpdate) error
}

// RunConfig is one conversation's run configuration as the daemon reports it: the
// session the conversation is bound to, that session's model, effort, YOLO and
// permission mode, and its context-window occupancy. It is the value half of
// V2SessionConfig.RunConfigFor and carries primitives only.
//
// Every field describes SessionID: the cmd/pyry producer resolves the id and reads
// the settings under one pool acquisition, so no field can describe another
// session, even across a concurrent idle eviction.
//
// An empty Model or Effort is a real value (the inherited daemon default) and YOLO
// false means permissions are enforced, so the zero RunConfig is indistinguishable
// from an all-defaults session. "Not addressable" is therefore RunConfigFor's
// comma-ok, never a field.
//
// PermissionMode is different: a resolved RunConfig always names one of claude's
// six modes, so "" appears only in a refusal's zero value. It always agrees with
// YOLO, so a bypass session reports "bypassPermissions" here and YOLO true, a mode
// the write path refuses on its own mode field.
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
// its agent and model, the value half of V2SessionConfig.CapabilitiesFor.
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
// cmd/pyry adapter maps sessions.ErrSessionNotFound onto it;
// handleSetSessionSettings maps it to a deterministic session.not_found reply.
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
// when the model has no entry for its agent, outside the fallback set. The
// settings handler replies as it does to an effort validEffort refuses, with the
// fixed, non-retryable protocol.malformed, and the error carries no value.
var ErrEffortNotOffered = errors.New("relay: effort not offered")

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state; the cmd/pyry resolver satisfies it. dev is the conn's
// authenticated device. Both methods run on the manager's Run goroutine, so they
// MUST return in bounded time.
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

// QuestionResolver resolves an inbound question_answer or question_refused frame
// against the daemon's outstanding clarifying-question batches. dev is the conn's
// authenticated device. Both methods run on the manager's Run goroutine and MUST
// return in bounded time. The typed payload crosses whole because
// QuestionAnswerPayload nests an array of entries.
//
// The bool reports whether the batch was consumed, and the handler uses it only
// to pick a content-free log reason. The relay MUST NOT broadcast
// question_dismissed on it: cmd/pyry's streamApprovalBridge.retireQuestion is that
// frame's sole broadcaster.
//
// The relay applies no authorization, so the implementation is the answer gate:
// it MUST deny an ineligible device before consuming anything. The payload is
// remote-authored and only decoded, so the implementation also owes the checks
// QuestionAnswerEntry's doc names:
//
//   - QuestionIndex is not range-checked. Indexing the parked batch with it
//     unchecked can panic, and any paired client picks the index.
//   - Indices may repeat or be missing; neither a partial nor a last-write-wins
//     answer is acceptable.
//   - Answers may be arbitrarily long, bounded only by the transport's AEAD frame
//     cap, so per-frame work must not grow unbounded with entry count.
//   - Nothing from the payload is logged beyond question_batch_id and
//     answer_token; the values are client-authored free text.
type QuestionResolver interface {
	// ResolveAnswer resolves an inbound question_answer against the daemon's
	// parked batch. Returns true iff it consumed an outstanding batch; an unknown
	// or already-resolved question_batch_id is a safe no-op (false), the modal
	// seam's unknown-id posture.
	ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool

	// ResolveRefusal resolves an inbound question_refused: the operator declined
	// to choose, so the batch resolves with no selection. Same report and same
	// no-op posture as ResolveAnswer.
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

// MCPActuator performs one inbound mcp_reconnect or mcp_toggle against one
// conversation's live claude child. dev is the conn's authenticated device.
//
// Both methods run on the addressed conn's appFrameWorker, not Run, because a live
// implementation waits for a child round trip. An implementation MUST honor ctx so
// manager shutdown ends that wait, and MUST NOT touch V2Session or Noise state;
// replies return through forwardToRun for Run-owned sealing.
//
// On true, the returned status MUST be one read after the child acknowledged the
// actuation, which only the implementation can do; the relay does not call
// MCPStatusFor on this path. true with an empty Servers is a real answer. false
// means refused for any reason, a caller MUST NOT read the payload, and the relay
// answers every false with protocol.CodeMCPActuationRefused.
//
// The relay applies no authorization and must not grow any. The implementation is
// the per-device actuation gate and records its decision through internal/audit,
// so the denial and its record stay in one place. A gate denial returns at once
// while a real actuation waits for the child, so the merged refusal does not hide
// timing; the relay cannot close that.
//
// Security: what crosses this seam is remote-authored.
//
//   - ConversationID has passed KnownConversation. It stays a lookup key: never
//     returned as the answer's ConversationID, logged, or joined into a path.
//   - ServerName has passed no validation anywhere; the implementation is its sole
//     validator. It MUST be shape-checked before use and MUST NOT become a path
//     component or reach a shell. Only the transport's AEAD frame cap bounds it.
//   - Every string in the returned payload is claude-authored and is never logged.
type MCPActuator interface {
	// Reconnect reconnects the named MCP server in the named conversation. Returns
	// the post-acknowledgement status to answer with and true, or the zero payload
	// and false for every refusal.
	Reconnect(ctx context.Context, p protocol.MCPReconnectPayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)

	// SetEnabled moves the named MCP server to p.Enabled in the named conversation.
	// It sets the requested value and never flips the current one, which is why it
	// is not named for the wire's mcp_toggle. Same two-way answer as Reconnect.
	SetEnabled(ctx context.Context, p protocol.MCPTogglePayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)
}

// AttachmentIntake drives one decoded attachment_chunk from admission to stored
// bytes and releases a departing conn's uploads; *attachments.Intake satisfies
// it. handleAttachmentChunk maps the returned internal/attachments sentinels to
// wire codes with errors.Is.
//
// Receive has three answers, and the first is not a refusal:
//
//   - ("", false, nil): accepted, the transfer wants more. Most chunks of a
//     healthy upload get this; attachments.ErrIncomplete never crosses the seam.
//   - (attachment_id, true, nil): the completing chunk's verified bytes are
//     stored.
//   - ("", false, err): refused, with the failing layer's own sentinel unwrapped
//     so errors.Is reaches it.
//
// Security: a returned error may wrap host paths, the daemon's conversation id or
// the sanitised filename, so it is neither logged nor replied. The handler sends a
// static per-code message and logs the mapped code. An implementation must not
// add annotation that leaks more.
//
// Receive requires a single feeder, one goroutine at a time per conn id; the
// manager guarantees it by running uploads on the conn's FIFO appFrameWorker.
// ReleaseConn has no such precondition: it only removes, so closeWith calls it on
// Run while that conn's worker may still be inside Receive.
type AttachmentIntake interface {
	// Receive routes one decoded chunk of one conn's upload; see the type doc for
	// its three answers. connID keeps distinct conns' transfers apart.
	//
	// conversationID MUST already have passed KnownConversation, because it
	// becomes a path component below this seam (attachments.EnsureDir).
	// handleAttachmentChunk checks it on every chunk, so a transfer naming an
	// unusable conversation is refused on its first frame. It is a string rather
	// than a conversations.ConversationID so this package need not import
	// internal/conversations.
	Receive(connID, conversationID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)

	// ReleaseConn drops every upload still in flight for one conn, returning the
	// daemon-wide capacity they held without waiting for the idle window. A
	// no-op for a conn holding nothing, so closeWith calls it unconditionally.
	ReleaseConn(connID string)
}

// HistoryPager is the type of the V2SessionConfig.HistoryPage seam, whose field
// doc carries the contract.
//
// Primitives in, protocol types out: protocol.HistoryEntry mirrors history.Entry
// key for key (frozen by TestHistoryPagePayload_WireKeys), so returning it keeps
// that correspondence in one place and internal/relay never imports
// internal/history. The result is a struct rather than comma-ok because the
// handler must tell a bad cursor (history.invalid_cursor) from a read failure
// (history.unavailable); they differ in the retryable flag a client branches on.
type HistoryPager func(conversationID, cursor string, limit int) HistoryPageResult

// HistoryPageResult is one answer from the HistoryPage seam: history.Page's three
// fields in wire types, plus the outcome. Entries, Cursor and AtStart mean
// something only when Outcome is HistoryPageOK; otherwise they are zero and the
// handler answers a reject.
type HistoryPageResult struct {
	// Entries is the page, newest first, forwarded to the client verbatim. An
	// implementation sizes it from what the log returned, never from limit, which
	// a remote caller chose (protocol.RequestHistoryPayload.Limit states the rule).
	//
	// The payloads are replayed content with the trust class of the live frame
	// they mirror: claude-authored for an assistant frame, so docs/protocol-mobile.md
	// § Security model threat 1 applies. They are forwarded unchanged, not
	// re-encoded or sanitised, so the client's live-lane reducer and sanitisation
	// handle them.
	Entries []protocol.HistoryEntry

	// Cursor is the opaque position to ask with next. Empty whenever AtStart.
	Cursor string

	// AtStart reports that the start of the log was reached while filling this
	// page; it is the only signal that ends a walk. It comes from the log and is
	// never synthesised, so a page the handler shortened to fit the envelope cap
	// cannot forge an end of log.
	AtStart bool

	// Outcome says whether the three fields above mean anything.
	Outcome HistoryPageOutcome
}

// HistoryPageOutcome discriminates the seam's answers finely enough for the
// handler to choose a wire code, and no more. internal/history has six sentinels a
// Page call can reach, but the wire has one code for a bad cursor and one for
// every other failure.
type HistoryPageOutcome uint8

const (
	// HistoryPageOK means the page is the log's answer, including the empty
	// terminal page of a conversation with no log. It is the zero value, so an
	// implementation that forgets to set an outcome reports that inert empty page,
	// never a refusal the client did not earn.
	HistoryPageOK HistoryPageOutcome = iota

	// HistoryPageBadCursor is the one client fault: a cursor that does not decode,
	// was minted for another conversation, or names a position not in this log.
	// internal/history raises the single ErrInvalidCursor for all three, so the
	// handler cannot tell them apart.
	HistoryPageBadCursor

	// HistoryPageUnavailable is every daemon-side failure: a corrupt or
	// unknown-version segment, a containment refusal, an I/O error, or no log
	// wired. It is the only retryable outcome. It also covers two refusals that
	// are unreachable by construction (a non-canonical conversation id, a page
	// size below one), so a daemon bug reads as a daemon problem rather than as
	// the client's malformed request.
	HistoryPageUnavailable
)

// PairingMinter mints a pairing for another device on behalf of an already-paired
// one, so internal/relay holds none of the minting machinery (randomness, pairing
// encoding, keys, audit). requester is the conn's authenticated device. cmd/pyry's
// resolver is the sole production implementation.
//
// The implementation makes the authorization decision and records it through
// internal/audit, so the denial and its record cannot drift apart. The handler
// applies no privilege check and MUST NOT grow one.
//
// It runs on the conn's appFrameWorker, not Run, so it may take a file lock and
// write devices.json. It still stalls that conn's later frames, so it MUST return
// in bounded time; production bounds its lock wait explicitly.
//
// Security:
//
//   - deviceName is remote-authored and already shape-checked: bounded by
//     protocol.MaxDeviceNameBytes at decode, with no C0, DEL or C1 control
//     (mintLabelIsDisplaySafe), and valid UTF-8 because encoding/json guarantees
//     it. That makes it safe to store, log and show in `pyry pair list`. An
//     implementation MUST NOT re-derive the name from anywhere else and MUST NOT
//     let it become a path component.
//   - The minted device is always unprivileged. MintPairingPayload has no field
//     for devices.Device.AllowRemotePermissions, and an implementation MUST pass a
//     literal false. A stolen privileged pairing must never mint a device that can
//     itself mint or actuate MCP servers.
//   - requester is the only identity: the per-conn device bound at handshake. No
//     request field names a device. A nil requester MUST be denied
//     (devices.Device.MayAnswerRemotePermission is nil-receiver-safe).
type PairingMinter interface {
	// MintPairing mints one pairing for a new device and returns it encoded, or
	// says why it did not. deviceName is the label for the record. An empty
	// deviceName means the client named none, and the implementation uses the
	// same device-<hash8> fallback `pyry pair` does, so both entry points produce
	// indistinguishable records.
	MintPairing(requester *devices.Device, deviceName string) PairingMintResult
}

// PairingMintResult is one answer from the PairingMinter seam. It carries an
// outcome rather than an error because every mint error wraps the absolute
// devices.json path, which must not reach the handler; the error ends in the
// implementation. There are two refusal outcomes because they differ in the
// retryable flag a client branches on.
type PairingMintResult struct {
	// Pairing is the pair.Encode string ({server, relay, token,
	// server_static_pubkey} as base64url), byte for byte what `pyry pair` prints.
	// It is a plaintext bearer credential: never logged, nothing decoded from it
	// logged, and its only egress is the AEAD-sealed pairing_minted reply unicast
	// to the conn that asked. Empty unless Outcome is PairingMintOK.
	Pairing string

	// Outcome says whether Pairing means anything.
	Outcome PairingMintOutcome
}

// PairingMintOutcome discriminates the seam's answers finely enough for the
// handler to choose a wire code, and no more. A busy lock, a registry read or
// write failure and an RNG failure share protocol.CodePairingUnavailable, so they
// share one outcome.
type PairingMintOutcome uint8

const (
	// PairingMintOK means a record was written and Pairing carries the
	// credential. It is deliberately not the zero value: a forgotten outcome must
	// deny, not report a mint with an empty credential.
	PairingMintOK PairingMintOutcome = iota + 1

	// PairingMintUnauthorized is the privilege refusal: the requesting device does
	// not, or no longer does, carry devices.Device.AllowRemotePermissions. An
	// implementation MUST reach it without writing a record, so a refusal leaves
	// the registry untouched.
	PairingMintUnauthorized

	// PairingMintFailed is every host-side failure: a devices.json lock not
	// acquired, a registry not read or written, or a CSPRNG that refused. It is
	// the only retryable outcome.
	PairingMintFailed
)

// WorkspaceFile is one live read answered by the WorkspaceFileRead seam.
//
// AttachmentID is daemon-minted per transfer, a lowercase UUIDv4 that keys this
// one chunk stream and addresses nothing afterwards: nothing is stored under it.
// Filename is the base name of the resolved file, which after symlink resolution
// may differ from the leaf the client named. Neither Filename nor Data may be
// logged.
type WorkspaceFile struct {
	AttachmentID string
	Filename     string
	Data         []byte
}

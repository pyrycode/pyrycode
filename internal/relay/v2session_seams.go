package relay

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// V2SessionConfig parameterises V2SessionManager. Frames, Outbound, StaticPriv,
// Devices, ServerID and Logger are required: NewV2SessionManager panics on a nil
// Frames or Logger and returns an error for the others. Every other field is
// optional and its doc states what the zero value does. Per-seam design notes
// live in docs/knowledge/features/v2-session-manager*.md.
//
// Connect-time reconcile seams (OutstandingModals, OutstandingQueues,
// RetainedModelLists, OutstandingQuestions, RetainedSlashCommandLists,
// RetainedBackgroundTaskRosters, RunningTurnPhases, ReplySuggestions) share one
// contract, the Mode B rule of docs/protocol-mobile.md § Reconnect / Backfill
// semantics. handleNoiseInit's interactive-open tail calls each on the Run
// goroutine and unicasts the payloads to the just-opened conn only. An
// implementation MUST be a pure read in bounded time and MUST NOT round-trip
// through Run (ActiveConns, for example), or the handshake deadlocks. Each seam
// enumerates rather than taking a key, because a V2Session carries no
// conversation id; every payload names its own conversation, and slice order is
// not part of the contract. Where a producer bounds its payloads, the reconcile
// adds no second cap, and pushQueue's byte ceiling backstops the count. The
// reconcile path logs no payload field but the conversation id. nil means no
// reconcile.
//
// Security: StaticPriv is the binary's 32-byte X25519 static private key. It
// MUST NOT be logged, wrapped into an error or emitted on any wire surface, the
// same contract internal/keys and internal/noise state for those bytes.
type V2SessionConfig struct {
	// ThreadReady must attest installation of live publication, authoritative
	// watermark lookup and session-state reconciliation. Nil/false disables thread;
	// production must leave it unset until all three providers are installed.
	ThreadReady func() bool
	// ThreadLastShownVersion returns only an authoritative usable reading. False
	// covers missing, rebuilding and unavailable state; zero with true is usable.
	ThreadLastShownVersion func(conversationID string) (uint64, bool)

	// WorkspaceBase is a caller-resolved base advertised only to admitted peers
	// in the encrypted hello_ack. Nil uses WorkspaceRoot(); a supplied absolute
	// value is reported verbatim, while empty or relative values omit the key
	// without fallback. Advertisement performs no filesystem access. The caller
	// must not mutate the pointed-to value while the manager runs.
	WorkspaceBase *string

	// Frames is the inbound RoutingEnvelope stream from a relay.Connection (or an
	// in-memory channel in tests). Required. Run consumes it until it closes or
	// ctx is done.
	Frames <-chan protocol.RoutingEnvelope

	// Outbound sends one RoutingEnvelope to the relay; production passes
	// (*relay.Connection).Send. Required. An error is logged at debug and
	// dropped, because the relay leg's reconnect handles recovery.
	Outbound func(protocol.RoutingEnvelope) error

	// Connected reports whether the relay transport leg is up. drainOnce consults
	// it before sealing a queued envelope: false leaves the head unpopped and
	// unsealed, so no Noise send nonce is burned on a frame that cannot arrive (a
	// burned nonce gaps the phone's receive nonce and closes a live session with
	// 4421). true is best effort, since the conn may drop between the poll and the
	// send. It decides only when a seal happens, so it is not a security decision.
	// Optional: nil means always connected and the drain never holds. Production
	// wires (*relay.Connection).Connected.
	Connected func() bool

	// Reconnect fires once per fresh relay transport conn (edge-triggered, cap 1,
	// drop on full, single observer). Run then re-signals the push drain so an
	// envelope held while the leg was down flushes at once. It only wakes the
	// drain; drainOnce still consults Connected before popping. Optional: nil is a
	// channel that is never ready, so the drain wakes only on Push. Production
	// wires (*relay.Connection).Reconnected.
	Reconnect <-chan struct{}

	// RekeyInterval overrides the scheduled re-key cadence read by armRekeyTimer.
	// Optional: zero uses rekeyInterval (1h). It exists for e2e tests, which
	// cannot reach this package's unexported timing vars; production leaves it
	// zero, and it is never sourced from the wire, a file or operator input. A
	// smaller value only rotates keys more often; it touches no key material, pin
	// or AEAD.
	RekeyInterval time.Duration

	// RekeyReplyTimeout overrides how long armRekeyReplyTimer waits for the
	// phone's fresh noise_init after a rekey_request before tearing the session
	// down. Optional: zero uses rekeyReplyTimeout (30s). Test-only, like
	// RekeyInterval.
	RekeyReplyTimeout time.Duration

	// MinClientVersions holds the minimum app version, as MAJOR.MINOR.PATCH,
	// keyed by the app name a hello's client_version carries. Once any entry is
	// non-empty, a hello whose token is accepted but whose client_version is
	// unparsable, or below its app's minimum, is refused with
	// client.update_required and close 4412. Optional: nil or an empty value means
	// no minimum for that app. NewV2SessionManager errors on a value that does not
	// parse. Production passes ShippedMinClientVersions(); tests pass their own
	// map. Never sourced from the wire or operator input.
	MinClientVersions map[string]string

	// RekeyRetryInterval overrides the re-arm cadence armRekeyRetryTimer uses
	// when a scheduled re-key fires while the relay leg is down. Optional: zero
	// uses rekeyRetryInterval (1m). Test-only, like RekeyInterval.
	RekeyRetryInterval time.Duration

	// StaticPriv is the binary's 32-byte X25519 static private key. Required; see
	// the type doc's security note.
	StaticPriv []byte

	// Devices validates hello.Token. Required.
	Devices *devices.Registry

	// DevicesPath is the on-disk devices.json. Each handshake reloads Devices from
	// it before validating the token, so a device paired with `pyry pair` after
	// startup is accepted without a restart (ADR 029). The handshake also records
	// first redemption and the reported client version there. Optional: ""
	// disables the reload and both writes. A reload read error keeps the
	// in-memory set, which neither widens the accepted set nor loses devices.
	DevicesPath string

	// ServerID is surfaced in the hello_ack early-data payload. Required.
	ServerID string

	// Logger receives lifecycle and reject events. Required. Tokens, key bytes,
	// payload bytes, AEAD ciphertext and their base64 forms MUST NOT appear in any
	// logged field.
	Logger *slog.Logger

	// Handlers maps an application envelope type to its handler for open-state
	// dispatch through dispatch.Route. Optional: nil or empty means every such
	// envelope gets a sealed protocol.unsupported reply.
	//
	// Security: handlers run on the addressed conn's appFrameWorker, one frame at
	// a time, so a handler MUST return in bounded time (bound long waits with a
	// ctx timeout) or it stalls that conn's later frames. A handler MUST NOT touch
	// Noise or session state; every reply is sealed on Run (forwardAppReply),
	// keeping the send CipherState single-owner. A handler MUST NOT keep the
	// *dispatch.Conn in a goroutine that outlives it: the conn's outbound channel
	// is drained only while routeAppFrame runs, so later sends are silently lost.
	Handlers map[string]dispatch.Handler

	// KnownConversation reports whether conversationID names a conversation this
	// daemon hosts. The handlers for request_snapshot, request_history,
	// request_attachment, attachment_chunk, read_workspace_file,
	// request_model_list, request_context_usage, mcp_status_request and the MCP
	// actuation verbs consult it before calling their seam and refuse an unknown
	// id. It checks membership only, so a known but unbound conversation
	// passes; request_session_settings resolves through RunConfigFor instead.
	// Optional: nil reads every id as unknown. Production scans the conversations
	// registry under its mutex.
	KnownConversation func(conversationID string) bool

	// CodexConversation reports whether conversationID's bound session runs
	// Codex. forwardEnvelope consults it (withheldFromConn) on Run, per frame to a
	// conn that did not negotiate protocol.CapabilityMultiAgent, and withholds
	// every pushed frame about such a conversation from that conn. Production
	// takes a conversations-registry lookup plus sessions.Pool.HarnessFor. An
	// unknown conversation, one with no bound session and a harness miss all
	// report false. Optional: nil means no conversation is Codex.
	CodexConversation func(conversationID string) bool

	// ConversationAgent returns conversationID's agent (protocol.AgentClaude or
	// protocol.AgentCodex) by the rule list_conversations tags rows with; ok is
	// false for a conversation it does not know. forwardEnvelope and
	// forwardAppReply consult it (agentTaggedForConn) on Run for each
	// conversation_updated sealed for a multi_agent conn. Optional: nil delivers
	// every conversation_updated without an agent.
	ConversationAgent func(conversationID string) (agent string, ok bool)

	// MergedModelOptions returns, for a pushed model_list's Claude entries, the
	// list a multi_agent conn receives instead: those entries tagged as Claude's,
	// then the daemon's Codex entries, tagged as ModelListFor's replies tag them.
	// forwardEnvelope consults it (mergedForConn) on Run for each pushed
	// model_list, live or replayed, to such a conn; request replies and the
	// connect-time reconcile are already merged and never reach it. It must
	// return a slice it owns and never write through its argument. Optional: nil
	// delivers every pushed model_list unchanged.
	MergedModelOptions func(claude []protocol.ModelOption) []protocol.ModelOption

	// HistoryPage serves one backward step of a conversation-history walk over
	// the daemon's durable log, for an inbound request_history.
	// handleRequestHistory is its sole caller, on the conn's appFrameWorker,
	// which is why it may read files; it still stalls that conn's later frames,
	// so it MUST return in bounded time. Optional: nil consumes the frame inertly,
	// with no reply and no byte of the payload parsed.
	//
	// conversationID has already passed KnownConversation; the caller owes that,
	// because the id becomes a path component below this seam. cursor is passed
	// through unparsed: the wire declares it opaque, history.parseCursor is its
	// only validator, and nothing in internal/relay may decode, log or branch on
	// it. limit arrives positive and capped at maxHistoryPageEntries; never size a
	// buffer from it (see HistoryPageResult.Entries).
	HistoryPage HistoryPager

	// RunConfigFor reports the named conversation's run configuration as one
	// RunConfig describing one session. It is handleRequestSessionSettings' only
	// run-configuration source, so a client learns about the conversation it is
	// in, never the shared bootstrap session.
	//
	// false means the conversation is not addressable (unknown, bound to nothing,
	// or bound to a session the pool no longer holds), and the RunConfig MUST NOT
	// be read. true with an empty Model or Effort is a real answer (see
	// RunConfig). Optional: nil resolves no conversation.
	//
	// Security: conversationID is untrusted and stays a lookup key: never
	// returned, joined into a path, logged or wrapped into an error. The reported
	// session id comes from the daemon's registry record and the producer confirms
	// the pool holds it, so an unresolvable conversation addresses nothing. That
	// id is a routing key, not a secret. The seam is read-only; only
	// SettingsUpdater writes.
	RunConfigFor func(conversationID string) (RunConfig, bool)

	// EffectiveEffortFor reports claude's applied effort for the named
	// conversation's current child. handleRequestSessionSettings calls it once per
	// request, only after RunConfigFor accepted the same conversation, and never
	// uses it for the saved Effort or any other RunConfig field.
	//
	// A non-nil pointer is a confirmed string; nil with true is a confirmed JSON
	// null (claude reported no effort). false means no current reading, and the
	// pointer MUST then be ignored. Optional: nil omits effective_effort and keeps
	// every saved field. It runs on the conn's appFrameWorker because it may wait
	// on a child round trip, and it MUST honor ctx. The relay caches nothing.
	EffectiveEffortFor func(ctx context.Context, conversationID string) (*string, bool)

	// MemorySearchFor supplies a wire-ready search-access report for the named
	// conversation and the session ID accepted by RunConfigFor. The handler calls
	// it once per fully decoded, resolved settings request; it retains no result.
	// An error produces an unknown report with no providers. Optional: nil omits
	// memory_search. This runs on the conn's app-frame worker and must honor ctx.
	MemorySearchFor func(ctx context.Context, conversationID, sessionID string) (protocol.MemorySearchReport, error)

	// CapabilitiesFor reports the agent-and-model half of a session's capability
	// list for a multi_agent conn's session_settings reply.
	// handleRequestSessionSettings calls it once, after RunConfigFor accepted,
	// with that RunConfig's SessionID and Model, never the client's conversation
	// id. false means no session with a known agent, and the reply carries no
	// capability object. The relay adds the permission modes and attachment types
	// and drops any value its own shape checks refuse. The lists must come from
	// the code the set_session_settings checks run, so every listed option is
	// accepted. Runs on the conn's app-frame worker. Optional: nil omits the
	// object.
	CapabilitiesFor func(sessionID, model string) (AgentCapabilities, bool)

	// ModelListFor reports the named conversation's model menu as a marshal-ready
	// model_list payload, for an inbound request_model_list.
	// handleRequestModelList is its sole caller, after KnownConversation accepted
	// the id. It is keyed by conversation, unlike the RetainedModelLists
	// enumerator, which must not be used here. Which vocabulary answers (a bound
	// session's retained list, else the daemon-wide copy) is the cmd/pyry
	// resolver's decision.
	//
	// false means no menu exists, and the handler sends a coded error frame; a
	// caller MUST NOT read the payload then. An empty Models never stands for
	// unknown (turnevent.ModelList.Models is never empty). Optional: nil answers
	// every hosted request with the same retryable model_list.unavailable a
	// refusal gets, so the reply does not reveal whether the source is wired.
	//
	// multiAgent is the asking conn's negotiated multi_agent decision: true asks
	// for the merged list of both agents' entries, tagged with agent and family;
	// false asks for the Claude-only list.
	//
	// Security: conversationID stays a lookup key: never returned, joined into a
	// path or wrapped into an error, and the payload's conversation_id comes from
	// the registry record. Payloads arrive bounded at construction
	// (ModelListPayload.DroppedModels, ModelOption.TruncatedFields); this seam
	// adds no second cap. The text is claude-authored and never logged here.
	//
	// It runs inline on Run, so it MUST stay a bounded in-memory read (production:
	// a registry lookup and at most ten model rows). Moving a slower
	// implementation off Run means switching the handler's emit from
	// forwardEnvelope to forwardToRun in the same change.
	ModelListFor func(conversationID string, multiAgent bool) (protocol.ModelListPayload, bool)

	// MCPStatusFor reports one hosted conversation's current MCP server status as
	// an mcp_status payload. handleMCPStatusRequest is its sole caller, after the
	// payload decodes, the conn has negotiated interactive and KnownConversation
	// accepts the id. The id stays an untrusted lookup key: never logged, joined
	// into a path or returned as the answer's ConversationID. Every string in the
	// payload is claude-authored and never logged.
	//
	// true with no servers is a current empty snapshot. false means no current
	// status: a caller MUST NOT read the payload, and the relay replies retryable
	// mcp_status.unavailable without falling back to a retained or empty frame.
	// Optional: nil consumes the frame inertly, before decode, membership or
	// reply.
	//
	// It runs on the conn's appFrameWorker because a live implementation waits
	// for a child round trip, and it MUST honor ctx. Replies return through
	// forwardToRun; an implementation never touches V2Session or Noise state.
	MCPStatusFor func(ctx context.Context, conversationID string) (protocol.MCPStatusPayload, bool)

	// ContextUsageFor reports one hosted conversation's context-window breakdown
	// as a context_usage payload, for an inbound request_context_usage.
	// handleRequestContextUsage is its sole caller, after KnownConversation
	// accepted the id. Same shape as MCPStatusFor.
	//
	// A fresh reading is expensive: the implementation asks claude at detail
	// "full", a token-count call per category, where the automatic post-turn frame
	// carries the cheap "summary". The detail is the implementation's choice and
	// is not in the request. The implementation may defer a mid-turn request
	// until the turn ends and then wait on a child round trip; the relay bounds
	// neither wait, so it MUST honor ctx. Asks close together are collapsed per
	// conversation below this seam, so a caller must not assume its own ask caused
	// the round trip it is answered from.
	//
	// true means a reading to show, not necessarily a fresh one. When none can be
	// taken (no bound session, no live child, no answer) an implementation MAY
	// return a stored reading, and then MUST set ContextUsagePayload.AsOf: a
	// stored reading has empty inventories, which without AsOf would read as
	// claude reporting none. A fresh reading omits AsOf; the relay neither sets
	// nor inspects it. false means no reading: a caller MUST NOT read the payload,
	// and the relay replies retryable context_usage.unavailable with nothing of
	// its own, since an all-zero payload is indistinguishable from an empty
	// context. Optional: nil consumes the frame inertly, before decode, membership
	// or reply.
	//
	// Security: conversationID stays a lookup key: never returned, joined into a
	// path, logged or wrapped into an error. The payload's ConversationID is
	// daemon-authored; every other string is claude- or workspace-authored,
	// including memory-file paths from the operator's filesystem. None of it is
	// logged, and it reaches the wire only in the unicast, AEAD-sealed reply.
	//
	// It runs on the conn's appFrameWorker, not Run, for the waits above, and
	// MUST NOT touch V2Session or Noise state; replies return through
	// forwardToRun.
	ContextUsageFor func(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool)

	// MCPActuator performs inbound mcp_reconnect and mcp_toggle against a
	// conversation's live child; handleMCPReconnect and handleMCPToggle are its
	// callers. The MCPActuator type doc carries the contract, including that the
	// implementation is the per-device authorization gate. Optional: nil consumes
	// both frames inertly, with no reply and no byte of the payload parsed.
	//
	// Leave it unset or assign a non-nil implementation. A typed nil pointer makes
	// the field non-nil, so the frame is admitted and the handler calls a method
	// on a nil pointer; internal/relay has no recover(), so that is a
	// remote-triggerable crash.
	MCPActuator MCPActuator

	// SystemPromptFor reports the named conversation's stored system prompt and
	// how the running session's spawned-with prompt compares to it, as a
	// marshal-ready system_prompt payload, for an inbound request_system_prompt.
	// handleRequestSystemPrompt is its sole caller.
	//
	// The comparison matters because a stored prompt takes effect only at the
	// next session start. It is computed on the collapsed stored value: the
	// registry stores nil, "" or text, and Pool.SystemPromptFor returns "" for
	// both no-text states, so an explicitly empty stored prompt matches a session
	// spawned with no operator text. The cmd/pyry producer owns that rule; this
	// package does not re-derive it.
	//
	// false means the daemon does not host the conversation, and a caller MUST NOT
	// read the payload. A hosted conversation with no running session is true,
	// with SessionPromptStatus protocol.SystemPromptStatusNoSession and the stored
	// value still reported. On false the handler answers the constant no-session
	// reply, so the verb is not a membership oracle. Optional: nil answers every
	// request with that same reply, never an error or a drop.
	//
	// It runs inline on Run, so it MUST stay a bounded in-memory read (production:
	// a registry lookup and one pool map read). Moving a slower implementation off
	// Run means switching the handler's emit from forwardEnvelope to forwardToRun.
	//
	// Security: conversationID stays a lookup key: never returned, joined into a
	// path, logged or wrapped into an error. The session id the producer reads
	// under comes from the resolved registry record, so a caller cannot reach
	// another conversation's session. The prompt is operator-authored text that
	// becomes standing instructions to claude: never logged, and sent only in the
	// unicast, AEAD-sealed reply, never on the broadcast push path. The seam is
	// read-only and can start, restart, rotate or interrupt nothing; do not widen
	// it.
	SystemPromptFor func(conversationID string) (protocol.SystemPromptPayload, bool)

	// PairingMint mints a pairing for another device on behalf of the conn's
	// authenticated one, for an inbound mint_pairing; handleMintPairing is its
	// sole caller. The PairingMinter type doc carries the contract. Optional: nil
	// consumes the frame inertly, with no reply and no byte of the payload parsed.
	//
	// Security: this is the only seam that mints a credential. The returned
	// bearer token reaches only the AEAD-sealed pairing_minted reply unicast to
	// the conn that asked; it is never logged, broadcast or wrapped into an error.
	// Wiring it deliberately widens docs/protocol-mobile.md § Security model
	// threat 4: any privileged paired device can mint, bounded by the minted
	// device being unprivileged and an unredeemed token expiring at
	// devices.RedemptionWindow.
	PairingMint PairingMinter

	// ModalResolver resolves inbound modal_answer and modal_cancel frames.
	// Optional: nil makes both inert. Production wires the cmd/pyry resolver.
	ModalResolver ModalResolver

	// QuestionResolver resolves inbound question_answer and question_refused
	// frames against the outstanding clarifying-question batches. The
	// QuestionResolver type doc carries the contract, including that the
	// implementation is the answer gate. Optional: nil still consumes both frames
	// (they never reach dispatch.Route's unknown-type reply) but decodes, hands
	// off and broadcasts nothing. It is a seam of its own rather than a
	// ModalResolver method because a question batch is its own frame family.
	QuestionResolver QuestionResolver

	// AttachmentIntake receives inbound attachment_chunk frames and releases a
	// departing conn's uploads; the AttachmentIntake type doc carries the
	// contract. Optional: nil still consumes the frame (it never reaches
	// dispatch.Route's unknown-type reply) but decodes, stores and replies
	// nothing, and closeWith releases nothing. Wire exactly one per daemon: its
	// in-flight ceiling is daemon-wide, so a second instance would be a second
	// budget. Production builds it in cmd/pyry's startRelayV2.
	AttachmentIntake AttachmentIntake

	// AttachmentResolve returns the on-host path of one stored attachment in one
	// conversation: the read half of AttachmentIntake and the retrieval leg's only
	// filesystem reach. handleRequestAttachment is its sole caller. Optional: nil
	// still consumes request_attachment but decodes, resolves and replies nothing.
	//
	// Comma-ok, not an error. attachments.ResolvePath answers one sentinel for an
	// unknown id, a malformed id and an id outside the conversation's directory,
	// so the handler cannot distinguish what CodeAttachmentNotFound merges, and its
	// shape error embeds the raw client id, which docs/protocol-mobile.md
	// § Attachments forbids logging. The error ends in cmd/pyry's
	// attachmentResolve closure.
	//
	// It performs no registry check. ResolvePath requires a conversation id the
	// caller already validated, and handleRequestAttachment consults
	// KnownConversation before either id reaches this seam.
	//
	// Security: the only sanctioned implementation wraps attachments.ResolvePath.
	// StreamAttachment re-validates nothing, so a path from elsewhere has no
	// containment guarantee, and a non-regular file such as a FIFO would block
	// os.ReadFile and wedge the conn's appFrameWorker. ResolvePath answers only
	// regular files at exactly the path the two ids build. The path's leaf is a
	// sanitised client filename, so the path is never logged and never reaches
	// the wire.
	AttachmentResolve func(conversationID, attachmentID string) (path string, ok bool)

	// WorkspaceFileRead reads one regular file live from the workspace or the
	// admitted read folders: the path a client names, not a stored copy.
	// handleReadWorkspaceFile is its sole caller. Optional: nil consumes
	// read_workspace_file inertly, as a nil AttachmentResolve does for
	// request_attachment.
	//
	// Comma-ok for AttachmentResolve's reason: every refusal (denied name, no
	// admitted root, missing file, out-of-tree path, non-regular file, over the
	// size bound) is false inside cmd/pyry's workspaceFileReader, so the handler
	// cannot tell them apart and never holds an error that prints a host path.
	//
	// It performs no registry check; the handler consults KnownConversation
	// first. Confinement to the admitted roots, the two-leaf secret-name rule and
	// the checked read belong to the implementation. The only sanctioned one is
	// workspaceFileReader, which reuses the attach_file verb's confineFile and
	// readChecked.
	WorkspaceFileRead func(conversationID, path string) (WorkspaceFile, bool)

	// Interrupter stops the running turn in the conversation an inbound
	// interactive interrupt frame names, or in the daemon's cursor conversation
	// when it names none. Optional: nil makes interrupt inert. Production wires
	// cmd/pyry's activeInterrupter, which owns the shape check and the registry
	// resolution.
	Interrupter Interrupter

	// BackgroundTaskStopper stops a named background task for an inbound
	// stop_background_task. Optional; production installs the bound-child adapter
	// in cmd/pyry. When unwired, leave a true nil interface, never a typed nil
	// pointer: dispatchAppFrame's nil check must consume the verb before payload
	// decode or enqueue. The negotiated interactive capability is the only other
	// gate; this paired-device action bypasses the tool-permission gate.
	BackgroundTaskStopper BackgroundTaskStopper

	// SessionStarter starts a fresh session in the conversation an inbound
	// interactive new_session frame names, or in the daemon's cursor conversation
	// when it names none. Optional: nil makes new_session inert. Production wires
	// cmd/pyry's activeSessionStarter, which owns the shape check and the registry
	// resolution.
	SessionStarter SessionStarter

	// AgentSwitcher handles switch_agent asynchronously. Optional: nil replies
	// binary_offline.
	AgentSwitcher AgentSwitcher

	// QueueRemover drops a queued message named by an inbound dequeue_message
	// frame. Optional: nil makes dequeue_message inert. Production wires
	// *msgqueue.Queue.
	QueueRemover QueueRemover

	// QueueSender writes a queued message named by an inbound send_queued_now
	// frame into the running claude turn. Optional: nil makes send_queued_now
	// inert. Production wires *msgqueue.Queue.
	QueueSender QueueSender

	// DebugBundler assembles the daemon's debug bundle (recent logs plus the
	// newest recording, if any) as one in-memory archive, for an inbound
	// request_debug_bundle. Optional: nil replies a deterministic unavailable
	// error, never a silent drop. Production wires a closure over
	// debugbundle.Assemble; the manifest travels inside the archive as
	// manifest.json.
	//
	// Security: the bytes are the plaintext bundle, the most sensitive data in the
	// system, and MUST NOT be logged. handleDebugBundleRequest streams them only
	// over the AEAD-sealed push path and logs a byte count or the failure, never
	// content.
	DebugBundler func() (archive []byte, err error)

	// SettingsUpdater persists an inbound set_session_settings change from an
	// interactive paired client. Optional: nil replies "unavailable"
	// deterministically, never a silent drop. Production wires a *sessions.Pool
	// adapter. The fail-safe is SettingsUpdate's nil-pointer semantics: an absent
	// YOLO never enables bypass.
	SettingsUpdater SettingsUpdater

	// OutstandingModals returns the daemon's outstanding modals as modal_shown
	// payloads, each with its original modal_id, for connect-time reconcile (see
	// the type doc). It mints no nonce and retires nothing, so it neither re-arms
	// deny-on-timeout nor changes answerability: a re-sent modal_id stays
	// answerable exactly once, through the registry's one-shot Resolve, which this
	// path never calls. Production wires modalbridge.Registry.Snapshot.
	OutstandingModals func() []protocol.ModalShownPayload

	// OutstandingQueues returns one queue_state payload per conversation with a
	// non-empty backlog, for connect-time reconcile (see the type doc).
	// queue_state is full state, so re-sending it is idempotent. It mints no id
	// and dequeues nothing. Production wires cmd/pyry's outstandingQueues adapter.
	OutstandingQueues func() []protocol.QueueStatePayload

	// RetainedModelLists returns one model_list payload per session holding a
	// retained list, for connect-time reconcile (see the type doc). model_list is
	// a full snapshot, so re-sending it is idempotent. Payloads arrive bounded at
	// construction (ModelListPayload.DroppedModels, ModelOption.TruncatedFields),
	// and their text is claude-authored. multiAgent is the opening conn's
	// negotiated multi_agent decision, with ModelListFor's meaning.
	RetainedModelLists func(multiAgent bool) []protocol.ModelListPayload

	// OutstandingQuestions returns the outstanding clarifying-question batches as
	// question_shown payloads, each with its question_batch_id and
	// conversation_id, for connect-time reconcile (see the type doc). The frame is
	// broadcast once when claude asks and carries no event id, so nothing else
	// reaches a late client, and the daemon would keep re-arming a batch's window
	// for a connected client that was never sent it. It mints no nonce and
	// retires no batch; a re-sent batch stays answerable exactly once through the
	// registry's Resolve. Correlate by question_batch_id.
	//
	// Each batch is bounded at parse time (questionbridge.Parse: 1 to 4
	// questions, 2 to 4 options each, over a maxInputBytes-capped tool input), but
	// the registry does not cap how many are outstanding. Question text and
	// header, and option label and description, are claude-authored. Production
	// wires questionbridge.Registry.Snapshot.
	OutstandingQuestions func() []protocol.QuestionShownPayload

	// RetainedSlashCommandLists returns one slash_command_list payload per session
	// holding a list, each with its conversation_id, for connect-time reconcile
	// (see the type doc). The list is session configuration decoded from one
	// initialize reply, so re-sending it is idempotent. Without the reconcile a
	// client that attaches later has no path to the list, because the live turn
	// lane is the only other carrier and can drop it.
	//
	// Payloads arrive bounded at construction
	// (SlashCommandListPayload.DroppedCommands, SlashCommand.TruncatedFields, cut
	// against marshalled bytes so the envelope fits the v2 cap). Name,
	// ArgumentHint, Description and Aliases are workspace-authored, untrusted
	// text, forwarded unsanitised (Description can carry newlines); the client
	// owes render sanitisation.
	RetainedSlashCommandLists func() []protocol.SlashCommandListPayload

	// RetainedBackgroundTaskRosters returns one background_task_roster payload per
	// session holding a roster, each with its conversation_id, for connect-time
	// reconcile (see the type doc). A roster is a full snapshot, so re-sending it
	// is idempotent. Without it a client sees an empty task panel until claude
	// next changes the roster.
	//
	// An empty roster is a positive statement that nothing is running, so a
	// producer MUST NOT filter it out as an empty aggregate, and the consumer
	// sends it (BackgroundTaskRosterPayload.MarshalJSON writes "tasks":[]).
	//
	// Payloads arrive bounded at construction
	// (BackgroundTaskRosterPayload.DroppedTasks, BackgroundTask.TruncatedFields,
	// over streamsup's maxTaskRosterEntries and maxTaskRosterDescription). Every
	// string in a row is claude-authored. Description is a literal command line
	// for local_bash tasks: render it as inert text, and never execute it or feed
	// it to an HTML sink, attribute or URL.
	RetainedBackgroundTaskRosters func() []protocol.BackgroundTaskRosterPayload

	// RunningTurnPhases returns the current phase of every running turn as
	// turn_state payloads, each with its conversation_id, for connect-time
	// reconcile (see the type doc). turn_state otherwise travels only on the event
	// stream, which a reconnect replays only after hello.last_event_id, and the
	// emitter sends no repeated phase, so a long single-phase turn would show
	// nothing.
	//
	// The current phase is the thinking or responding the daemon last sent for a
	// turn still open. An implementation MUST NOT derive a phase that was never
	// sent and MUST NOT return an idle conversation: absence is the idle answer,
	// which the client reads under the reset-on-reconnect rule.
	//
	// This reconcile runs after replayMissed, and the producer must record a
	// transition before the emitter appends its event to the replay ring and asks
	// ActiveConns for the fan-out. A turn ending while a conn opens then either
	// reads as ended here, or its idle event lands above the conn's replayThrough
	// and arrives live after the reconciled frame. Payloads carry only a
	// daemon-minted conversation_id and a phase from turnbridge's closed
	// vocabulary.
	RunningTurnPhases func() []protocol.TurnStatePayload

	// ReplySuggestions returns the producer's suggested-reply state as
	// reply_suggestion payloads, one per conversation that has state, for
	// connect-time reconcile (see the type doc). A cleared suggestion is returned
	// with SuggestedReply nil, because the clear is state a reconnecting client
	// must apply. A leaf mutex over the producer's map is the expected shape. A
	// live publish racing this read needs only the producer's per-conversation
	// revision: forwardEnvelope drops a reply_suggestion at or below the highest
	// revision already delivered for that conversation on that conn
	// (replySuggestionStale). SuggestedReply is claude-derived text.
	ReplySuggestions func() []protocol.ReplySuggestionPayload
}

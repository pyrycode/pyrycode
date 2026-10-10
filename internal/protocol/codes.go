package protocol

// Thread live updates are declared outgoing vocabulary, not yet emitted.
// They are not v1 application requests and must stay out of inboundAppTypeSet.
const (
	TypeThreadItemAdded   = "thread_item_added"
	TypeThreadItemChanged = "thread_item_changed"
	TypeThreadTextAppend  = "thread_text_append"
)

// Error codes: wire values for the "code" field of an ErrorPayload
// (docs/protocol-mobile.md § Error codes, which publishes each code's
// retryability). Names follow Code<Category><Reason> for the dotted
// category.reason string, grouped by category in spec-table order. The reasoning
// behind each group's merges and splits is in
// docs/knowledge/features/protocol-package-constants-codes-go-error-codes-21.md.
// Several codes merge causes on purpose, mostly so a reply discloses nothing
// about the host or about other ids; do not split one without reading its
// group's doc.
const (
	// Protocol errors.
	CodeProtocolUnknownType = "protocol.unknown_type"
	CodeProtocolMalformed   = "protocol.malformed"
	CodeProtocolUnsupported = "protocol.unsupported"

	// Auth errors.
	CodeAuthInvalidToken = "auth.invalid_token"
	CodeAuthTokenRevoked = "auth.token_revoked"

	// Server errors.
	CodeServerBinaryOffline = "server.binary_offline"
	CodeServerBinaryBusy    = "server.binary_busy"

	// Conversation errors.
	CodeConversationNotFound        = "conversation.not_found"
	CodeConversationAlreadyPromoted = "conversation.already_promoted"

	// Host system prompt storage failures are retryable. Malformed requests use
	// CodeProtocolMalformed (non-retryable).
	CodeHostSystemPromptUnavailable = "host_system_prompt.unavailable"

	// Message errors.
	CodeMessageTooLong = "message.too_long"

	// Relay errors.
	CodeRelayNoServer         = "relay.no_server"
	CodeRelayServerIDConflict = "relay.server_id_conflict"

	// Session errors.
	CodeSessionNotFound      = "session.not_found"
	CodeSessionBlocked       = "session.blocked"        // terminal give-up; not a retry hint (contrast server.binary_busy)
	CodeSessionChildCrashing = "session.child_crashing" // not terminal: claude keeps exiting at startup, the daemon is still restarting it and queued messages are kept

	// Attachment errors (docs/protocol-mobile.md § Attachments): the reject
	// vocabulary of the attachment frames and of send_message's attachment ids.
	//
	// attachment.not_found answers every request that yields no bytes: an unknown
	// id, a non-canonical id and an id resolving outside the named conversation's
	// directory alike, on retrieval and on a send_message whose attachment id does
	// not resolve under its own conversation. Distinct codes would make the verbs a
	// path-existence oracle, so the message stays static and, where a request names
	// several ids, MUST NOT say which one failed.
	//
	// The three retryable codes (too_many_uploads, storage_failed, stream_aborted)
	// mean retry after a backoff; an immediate resend is a hot loop.
	CodeAttachmentInvalidChunk    = "attachment.invalid_chunk"
	CodeAttachmentIntegrityFailed = "attachment.integrity_failed"
	CodeAttachmentTooManyUploads  = "attachment.too_many_uploads" // transient; clears when other uploads finish (contrast attachment.too_large)
	CodeAttachmentTooLarge        = "attachment.too_large"        // the whole transfer exceeds the receiver's per-upload bound; one oversize envelope is message.too_long
	CodeAttachmentStorageFailed   = "attachment.storage_failed"
	CodeAttachmentNotFound        = "attachment.not_found"
	CodeAttachmentStreamAborted   = "attachment.stream_aborted" // a TypeError correlated via in_reply_to, never a second attachment frame

	// Conversation-history errors (docs/protocol-mobile.md § Conversation
	// history). An unknown or non-canonical conversation_id is
	// CodeConversationNotFound: there is no second id to probe for, and
	// request_snapshot already answers an unknown conversation distinguishably.
	// history.invalid_cursor is one answer for a cursor that does not decode, was
	// minted for another conversation, or names a position not in this log; the
	// handler sees only history.ErrInvalidCursor and never echoes the cursor. Only
	// history.unavailable is retryable; the other three need a different request.
	CodeHistoryInvalidRequest  = "history.invalid_request"   // the payload did not decode; never an empty-but-successful request
	CodeHistoryInvalidPageSize = "history.invalid_page_size" // a negative limit; 0 is not a reject, it asks the daemon to choose
	CodeHistoryInvalidCursor   = "history.invalid_cursor"    // undecodable, foreign, or naming a position not in this log; one merged answer
	CodeHistoryUnavailable     = "history.unavailable"       // the log could not be read; the only retryable member of this group

	// Read-mark save error (docs/protocol-mobile.md § Marking a conversation
	// read). The advance could not be persisted, so the daemon reverted it and
	// pushed nothing; the same request can succeed once the registry saves again.
	CodeReadMarkUnavailable = "read_mark.unavailable" // retryable

	// On-demand model-list error (docs/protocol-mobile.md § Asking for a model
	// list on demand). The daemon hosts the conversation but has no menu: nothing
	// retained yet, or no model-list source wired. The causes are merged so the
	// reply reveals nothing about the host's configuration; an unhosted
	// conversation is CodeConversationNotFound (KnownConversation separates the
	// two). Retryable, since a child that has not answered initialize yet will.
	// This code, never an empty models array, is how the daemon says "no list":
	// turnevent.ModelList.Models is never empty.
	CodeModelListUnavailable = "model_list.unavailable" // the daemon hosts the conversation but has no vocabulary to answer with; retryable

	// MCP status error (docs/protocol-mobile.md § Asking for MCP status on
	// demand). Sent only after the membership check passes, when the hosted
	// conversation's resolver has no live result; the daemon never substitutes an
	// empty or retained status.
	CodeMCPStatusUnavailable = "mcp_status.unavailable" // hosted conversation has no current MCP status; retryable

	// MCP actuation error (docs/protocol-mobile.md § Actuating MCP servers on
	// demand). The one answer to every refused mcp_reconnect or mcp_toggle: an
	// unauthorized device, an unknown server, no live child and a declined
	// actuation alike. Splitting them would tell a device which MCP servers the
	// host runs and whether it is privileged; the daemon-side audit record keeps
	// the reasons apart. Never retryable, so the retry flag cannot re-split what
	// the code merges. It carries no server name and no claude-authored text; the
	// message is a constant in internal/relay.
	CodeMCPActuationRefused = "mcp_actuation.refused" // reconnect or toggle refused, reason deliberately merged; never retryable

	// Context-usage read error (docs/protocol-mobile.md § Asking for a context
	// usage reading on demand). The daemon hosts the conversation but has no
	// reading. streamsup's QueryContextUsage already collapses every such cause
	// (no session, no live child, a rotation, a failed write, a deadline, an
	// unusable payload) into one bool, and an unwired source merges in so the
	// host's configuration stays private. An unhosted conversation is
	// CodeConversationNotFound. Retryable; the resolver caches a refusal for its
	// short collapsing window, so a retry inside it gets the same answer. A
	// zero-valued ContextUsagePayload MUST NOT stand in for this code.
	CodeContextUsageUnavailable = "context_usage.unavailable" // the daemon hosts the conversation but has no reading to give; retryable

	// Workspace error (docs/protocol-mobile.md § Renaming a workspace).
	// rename_workspace names a workspace path, not a conversation, so this is not
	// CodeConversationNotFound. The refusal is also the workspace-label map's only
	// bound: a label can be stored only at a path that equals a stored
	// conversation's cwd. Not retryable; the message is static and never echoes
	// the path.
	CodeWorkspaceNotFound = "workspace.not_found"

	// Pairing-mint errors (docs/protocol-mobile.md § Minting a pairing from a
	// paired client). Neither is CodeAuthInvalidToken: the device has already
	// authenticated, and an auth code would wrongly invite it to re-pair.
	// pairing.not_permitted reports only that the asking device lacks the
	// remote-permissions flag and does not depend on device_name.
	// pairing.unavailable merges every host-side failure (a busy devices.json
	// lock, a registry read or write error, an RNG failure); the errors behind it
	// carry host paths and never reach the wire. A malformed request, including a
	// device_name over MaxDeviceNameBytes, is CodeProtocolMalformed.
	CodePairingNotPermitted = "pairing.not_permitted" // the device is authenticated but is not privileged to mint; permanent
	CodePairingUnavailable  = "pairing.unavailable"   // the mint could not be completed on the host; one merged, retryable answer

	// New-session workspace refusal (docs/protocol-mobile.md § New session). It
	// reports a rotation that succeeded: new_session rotated the conversation and
	// the successor came up, but re-confining the conversation's recorded
	// workspace at spawn time refused it (the folder was deleted or now points
	// outside $HOME). An error frame carries it so the refusal is correlated by
	// in_reply_to. It reveals nothing a client could not already derive, and every
	// inert arm of new_session stays silent, so the verb still cannot answer
	// "does this conversation exist?". Not retryable: the operator must repair or
	// re-point the folder. The conversation id travels in
	// ErrorPayload.ConversationID (a bare new_session names none); no part of the
	// path reaches the message, which is a constant in internal/relay, or a log.
	CodeNewSessionWorkspaceRefused = "new_session.workspace_refused" // the rotation completed; the recorded workspace was refused, so the successor stayed put

	// Client errors (docs/protocol-mobile.md § Compatibility). The hello's
	// client_version names an app build older than this host's minimum for that
	// app, or cannot be parsed once a minimum is set. Its own category: the fault
	// is the client build, not the protocol version or the token. Sent sealed,
	// after the Noise handshake and after the token check, so an unauthenticated
	// peer learns nothing of the version policy; the relay's
	// StatusClientUpdateRequired close follows it. Not retryable, and terminal for
	// this host only. The message is static; the minimum travels in
	// ErrorPayload.MinClientVersion.
	CodeClientUpdateRequired = "client.update_required" // the app build is older than this host's minimum; never retryable
)

// Envelope types: wire values for Envelope.Type (docs/protocol-mobile.md
// § Application message types). Every type in this block is in
// inboundAppTypeSet (envelope.go) except TypeHostSystemPrompt and
// TypeClaudeAccount, which are outbound only; its phone → binary verbs are
// dispatched through dispatch.Route's Handlers map. The v2-only types follow in
// their own blocks.
//
// Every Type* constant MUST be declared in this file. cmd/pyry/relay_guard_test.go
// parses it to collect the constant universe and classifies each type
// (map-dispatched, switch-intercepted, reply, push and so on); because
// internal/protocol/compat_test.go's lists are maintained by hand, that guard is
// the one that fails on a new constant left unclassified. Rationale per family:
// docs/knowledge/features/protocol-package-constants-codes-go-envelope-types.md.
const (
	// Handshake and control.
	TypeHello    = "hello"
	TypeHelloAck = "hello_ack"
	TypeError    = "error"
	TypeAck      = "ack"

	// Messaging.
	TypeSendMessage = "send_message"
	TypeMessage     = "message"

	// Conversations.
	TypeListConversations   = "list_conversations"
	TypeConversations       = "conversations"
	TypeCreateConversation  = "create_conversation"
	TypeConversationCreated = "conversation_created"
	TypePromoteConversation = "promote_conversation"
	TypeConversationUpdated = "conversation_updated"
	// TypeRenameConversation is a phone → binary write verb that renames a
	// conversation and replies with conversation_updated.
	TypeRenameConversation = "rename_conversation"
	// TypeDeleteConversation is a phone → binary write verb that permanently
	// removes a conversation from the registry (archive_conversation is the
	// reversible path). It replies with conversation_deleted.
	TypeDeleteConversation = "delete_conversation"
	// TypeConversationDeleted is the binary → phone reply to
	// delete_conversation, correlated via in_reply_to. It carries only the
	// deleted conversation's id, since the record no longer exists.
	TypeConversationDeleted = "conversation_deleted"
	// TypeArchiveConversation is a phone → binary write verb that sets a
	// conversation's durable archived flag and replies with
	// conversation_updated. unarchive_conversation reverses it; both carry
	// ArchiveConversationPayload.
	TypeArchiveConversation = "archive_conversation"
	// TypeUnarchiveConversation is a phone → binary write verb that clears a
	// conversation's archived flag and replies with conversation_updated.
	TypeUnarchiveConversation = "unarchive_conversation"
	// TypeSetConversationMuted is a phone → binary write verb that sets or
	// clears a conversation's durable muted flag from the payload's required
	// muted bool. It replies with conversation_updated and pushes the same
	// record to every interactive conn, so other clients stop alerting without
	// re-listing.
	TypeSetConversationMuted = "set_conversation_muted"
	// TypeMarkConversationRead is a phone → binary write verb that raises a
	// conversation's host-local read mark (ReadUpTo) toward the payload's
	// required up_to, clamped to the newest durable history entry and never
	// lowered. Every success replies with conversation_updated; only an actual
	// advance also pushes it to every interactive conn.
	TypeMarkConversationRead = "mark_conversation_read"
	// TypeChangeWorkspace is a phone → binary write verb that moves a
	// conversation to another workspace by setting its Cwd (a workspace is a
	// conversation's Cwd; there is no separate workspace id). The target path
	// is confined to $HOME before it is stored. It replies with
	// conversation_updated.
	TypeChangeWorkspace = "change_workspace"
	// TypeSetSystemPrompt is a phone → binary write verb that sets or clears a
	// conversation's durable system prompt, appended to every session that
	// conversation spawns. It is keyed by conversation, not session, so it can
	// be set with no session live; a new value applies from the next session
	// start. system_prompt is nullable so clearing differs from setting "". The
	// byte bound and UTF-8 check live at the registry
	// (conversations.MaxSystemPromptBytes). The reply, conversation_updated,
	// deliberately carries no prompt, because that record goes to every phone
	// on the server-id.
	TypeSetSystemPrompt = "set_system_prompt"

	// Daemon-wide host prompt family (docs/protocol-mobile.md § Daemon-wide host
	// system prompt). Both client → daemon verbs are map-dispatched; the daemon →
	// client reply is outbound only. See the consumer contract in
	// host_system_prompt.go.
	TypeRequestHostSystemPrompt = "request_host_system_prompt"
	TypeSetHostSystemPrompt     = "set_host_system_prompt"
	TypeHostSystemPrompt        = "host_system_prompt"

	// Claude account source status pair (docs/protocol-mobile.md § Claude account
	// source). The client → daemon read verb is map-dispatched; the daemon →
	// client reply is outbound only. See the consumer contract in
	// claude_account.go.
	TypeRequestClaudeAccount = "request_claude_account"
	TypeClaudeAccount        = "claude_account"

	// Workspace.
	// TypeCreateWorkspaceFolder is a phone → binary write verb that creates a
	// folder on the daemon host under a client-supplied parent path, confined
	// to $HOME. It touches no conversation and replies with
	// workspace_folder_created.
	TypeCreateWorkspaceFolder = "create_workspace_folder"
	// TypeWorkspaceFolderCreated is the binary → phone reply to
	// create_workspace_folder, correlated via in_reply_to. It carries the
	// created folder's canonical (symlink-resolved) absolute path.
	TypeWorkspaceFolderCreated = "workspace_folder_created"
	// TypeRecentWorkspaces is a phone → binary read verb, with an empty
	// payload, for the distinct recently used workspace folders. The list comes
	// only from Cwd values already in the conversations registry, so no
	// untrusted input drives a filesystem operation. It replies with
	// recent_workspaces_list.
	TypeRecentWorkspaces = "recent_workspaces"
	// TypeRecentWorkspacesList is the binary → phone reply to
	// recent_workspaces, correlated via in_reply_to: the distinct workspace
	// paths, most recent first, each with its latest last_used_at.
	TypeRecentWorkspacesList = "recent_workspaces_list"
	// TypeRenameWorkspace is a phone → binary write verb that sets or clears a
	// workspace's display label. label is nullable so clearing differs from
	// setting "". The handler applies the non-blank check and the
	// MaxWorkspaceLabelBytes bound, because
	// conversations.Registry.SetWorkspaceLabel validates nothing. It is keyed by
	// workspace path, which many conversations may share, so it replies with
	// workspace_updated rather than a conversation record.
	TypeRenameWorkspace = "rename_workspace"
	// TypeWorkspaceUpdated carries a workspace's path and stored label (null
	// when cleared). It is the reply to rename_workspace, correlated via
	// in_reply_to, and is also pushed to every other interactive conn. It is a
	// new type rather than a new arm on an existing one because an old client
	// drops an unknown type but would misrender a new meaning on a known one.
	TypeWorkspaceUpdated = "workspace_updated"

	// Push.
	TypeRegisterPushToken = "register_push_token"
)

// Mobile Protocol v2 control envelope (docs/protocol-mobile.md § Re-key). This
// and every later Type* block holds v2-only types, which MUST NOT be added to
// inboundAppTypeSet (envelope.go). Inbound v2 control frames are intercepted in
// internal/relay/v2session.go's dispatchAppFrame before internal/dispatch.Route,
// and outbound v2 events go only to v2 clients; IsKnownAppType rejecting them
// with ErrUnknownType keeps a control frame out of the v1 handler chain and stops
// a v1 client sending an outbound-only type. internal/protocol/compat_test.go
// files each in v2OnlyTypes, and cmd/pyry/relay_guard_test.go classifies each
// from the moment the constant exists.
const (
	// TypeRekeyRequest lets either side nudge the peer to start a re-key
	// handshake. The binary is the IK responder (ADR 024), so an inbound
	// rekey_request takes no transport action. relay_guard_test.go files it as
	// switch-intercepted.
	TypeRekeyRequest = "rekey_request"
)

// Mobile Protocol v2 interactive events (docs/protocol-mobile.md § Interactive
// events): binary → phone, pushed only to a phone that negotiated the
// "interactive" capability, and never dispatched inbound.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives these.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeTurnState      = "turn_state"
	TypeAssistantDelta = "assistant_delta"
	TypeToolUse        = "tool_use"
	TypeToolResult     = "tool_result"
	TypeTurnEnd        = "turn_end"
	TypeStall          = "stall"
)

// TypeReplySuggestion carries outbound v2 suggestion state, gated by the
// negotiated "interactive" capability (docs/protocol-mobile.md
// § reply_suggestion). ReplySuggestionPayload defines its set and explicit-null
// clear contract. Never dispatched inbound or added to the v1
// inboundAppTypeSet; push in relay_guard_test.go.
const TypeReplySuggestion = "reply_suggestion"

// Mobile Protocol v2 status frames read from claude's stream-json output: the
// API-error retry and auto-compaction sub-states (docs/protocol-mobile.md
// § api_retry, § compacting). Like stall, each has a show/clear shape, goes only
// to an interactive phone, and never opens or closes a turn.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives them.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeApiRetry   = "api_retry"  // binary → phone, outbound v2 api-retry status
	TypeCompacting = "compacting" // binary → phone, outbound v2 compaction status
)

// TypeResetting reports a conversation reset in progress (docs/protocol-mobile.md
// § resetting). A reset runs a wrap-up turn that writes a handoff note, then kills
// and respawns claude under a new session id; without this frame a client sees
// only a pause. One reset emits two active:true frames before a single
// active:false; ResettingPayload states that and its closed value sets.
//
// It is grouped alone because its source is the daemon's own reset routine, not
// claude's output: every value on it is daemon-authored, so nothing claude wrote
// crosses on it. It is not a turnevent variant and never opens or closes a turn.
//
// MUST NOT be added to inboundAppTypeSet: no phone may send it, and IsKnownAppType
// rejects an inbound "resetting". v2OnlyTypes in compat_test.go, push in
// relay_guard_test.go.
const (
	TypeResetting = "resetting" // binary → phone, outbound v2 conversation-reset status
)

// TypeUnrecognizedMessage carries to the client any stream-json message or content
// block the parser neither maps nor lists as measured known-ignored
// (docs/protocol-mobile.md § unrecognized_message), so a claude release that moves
// something meaningful into a new type is visible rather than lost. It reports a
// gap in the daemon's mapping, not a claude sub-state, and its payload is the only
// interactive one that carries raw model-adjacent JSON.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeUnrecognizedMessage = "unrecognized_message" // binary → phone, outbound v2 parser-gap diagnostic
)

// Mobile Protocol v2 background-task frames (docs/protocol-mobile.md
// § background_task_started and the three sections after it). Their subject is
// work that outlives the turn that started it, such as a shell command claude
// backgrounded, so a client can tell a finished turn from work still running.
// background_task_progress is a periodic reading; the others are lifecycle frames
// and a snapshot. The grouping is by subject, not shape.
//
// The names are the daemon's and follow internal/turnevent's variants, so a claude
// rename lands in one place (streamsup.emitSystemSubtype's case arms are the
// authority on claude's subtypes). background_task_roster is named for what it is,
// a snapshot, not for claude's trigger (background_tasks_changed), so no client
// reads it as a delta and infers a finish the daemon never observed.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives these.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeBackgroundTaskStarted  = "background_task_started"  // binary → phone, outbound v2 background-task open
	TypeBackgroundTaskUpdated  = "background_task_updated"  // binary → phone, outbound v2 background-task change
	TypeBackgroundTaskRoster   = "background_task_roster"   // binary → phone, outbound v2 background-task snapshot
	TypeBackgroundTaskProgress = "background_task_progress" // binary → phone, outbound v2 background-task activity reading
)

// TypeThinkingProgress is claude's mid-turn proof of life, the daemon's
// translation of the system/thinking_tokens line (docs/protocol-mobile.md
// § thinking_progress), so a client can tell a slow answer from a wedged one. A
// periodic reading with no edges. It carries no reasoning content (ThoughtChunk
// carries that and is never forwarded, ADR 025), so there is nothing to render as
// text. The name follows turnevent.ThinkingProgress: "progress" is what the daemon
// reports, "tokens" is claude's word.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeThinkingProgress = "thinking_progress" // binary → phone, outbound v2 thinking-progress reading
)

// TypeRateLimited reports that claude's usage-limit window is in a state other
// than the one measured as benign, so a turn that stops on a limit has a reason on
// the wire (docs/protocol-mobile.md § rate_limited). A condition report about a
// window no turn owns. The name follows turnevent.RateLimited and matches what
// cmd/pyry's eventKind logs for the variant; that agreement is not tested because
// internal/protocol cannot import cmd/pyry.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeRateLimited = "rate_limited" // binary → phone, outbound v2 usage-limit report
)

// TypeModelAnnounced reports the model claude says it resolved for the turn, from
// its system/init line (docs/protocol-mobile.md § model_announced): an identity
// report. Its model field is what claude announced, while the model field on
// ScreenSnapshotPayload, SessionSettingsPayload and SetSessionSettingsPayload is
// the per-session override ("" for the inherited default); the two can disagree.
//
// The name is the daemon's and follows turnevent.ModelAnnounced. claude's word on
// this path is its subtype, init. "model" is both the subject noun and claude's
// key, so a vocabulary check MUST NOT test for the substring "model", which the
// correct name contains.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeModelAnnounced = "model_announced" // binary → phone, outbound v2 announced-model report
)

// Mobile Protocol v2 refusal-fallback report (docs/protocol-mobile.md
// § model_refusal_fallback). claude emits this fact when it refuses a turn on one
// model and retries on another. The frame explains a later model change;
// TypeModelAnnounced remains the authority on which model claude is running.
//
// MUST NOT enter inboundAppTypeSet. This is an outbound interactive push, and
// the protocol and relay totality guards classify it as v2-only and push-only.
const (
	TypeModelRefusalFallback = "model_refusal_fallback"
)

// Mobile Protocol v2 refusal-without-fallback report (docs/protocol-mobile.md
// § model_refusal_no_fallback). claude emits this fact when it refuses a turn and
// does not retry it on another model. The frame is explanatory only;
// TypeModelAnnounced remains the current-model authority.
//
// MUST NOT enter inboundAppTypeSet. This is an outbound interactive push, and
// the protocol and relay totality guards classify it as v2-only and push-only.
const (
	TypeModelRefusalNoFallback = "model_refusal_no_fallback"
)

// TypeModelList carries the models array of claude's initialize control reply, so
// a client can build its model, reasoning-effort and permission-mode menus
// (docs/protocol-mobile.md § model_list). A capability report, not turn-scoped. It
// is pushed on the interactive lane and at connect, and is also the unchanged
// answer to request_model_list.
//
// The name is the daemon's. claude's words here are initialize and models, and
// model_list contains neither; "model" is the subject noun, so a vocabulary check
// MUST NOT test for it (see TypeModelAnnounced).
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeModelList = "model_list" // binary → phone, outbound v2 model-list report
)

// TypeSlashCommandList carries the slash commands the running child accepts for a
// conversation (docs/protocol-mobile.md § slash_command_list), from the commands
// array of the same initialize reply model_list uses. That array is the source
// because it carries each command's aliases, which the names-only slash_commands
// key on system/init does not; reset, the desktop Actions menu's entry, is an
// alias of clear. A capability inventory of verbs, kept apart from model_list
// because the subjects differ. There is no inbound request verb: the list is
// pushed on the interactive lane and at connect.
//
// The name is the daemon's. claude's four words here are initialize, commands,
// slash_commands and terminal_slash_commands; slash_command_list contains none of
// them and no "init". The trap is three words wide: command, slash_command and
// slash are substrings of the correct name, so a vocabulary check tests the plural
// commands, which both longer keys contain
// (TestSlashCommandListType_IsNotClaudesVocabulary).
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeSlashCommandList = "slash_command_list" // binary → phone, outbound v2 slash-command-list report
)

// TypeSessionFacts reports claude's build version and the permission posture it
// says the child runs under, from the same system/init line as model_announced
// (docs/protocol-mobile.md § session_facts). The daemon knows what it asked for;
// only claude knows what it got. An identity report about the child run, kept
// apart from model_announced, which is about one turn. "Session" names that child
// run: the frame carries no session identity, and claude's session_id is not the
// daemon's conversation.
//
// The discriminating words are claude's subtype (init) and keys
// (claude_code_version, permissionMode). "session" is a substring of the correct
// name and claude's own word (session_id), so a check MUST NOT test for it, and the
// session_-prefixed siblings are told apart by equality
// (TestSessionFactsType_IsNotClaudesVocabulary).
//
// There is no effort field: no init line carries an effort key, even with an
// effort set, and effortInitPins in internal/streamsup fails if that changes.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeSessionFacts = "session_facts" // binary → phone, outbound v2 session-facts report
)

// Mobile Protocol v2 MCP server status and actuation (docs/protocol-mobile.md
// § mcp_status and the two sections after it). TypeMCPStatus carries a
// conversation's MCP inventory, pushed live and sent as the correlated reply to
// each inbound type here. TypeMCPStatusRequest reads it. TypeMCPReconnect and
// TypeMCPToggle actuate; an accepted actuation is answered with a fresh mcp_status
// correlated by the request envelope's id, so there is no ack type. The actuators
// use the claude child's own control-protocol verbs (WriteMCPReconnect and
// WriteMCPToggle in internal/streamsup); the read verb has a suffix only to avoid
// colliding with mcp_status.
//
// MUST NOT be added to inboundAppTypeSet: all four are in v2OnlyTypes.
// relay_guard_test.go files the three inbound types as switch-intercepted and
// TypeMCPStatus as push+reply.
const (
	TypeMCPStatus        = "mcp_status"         // binary → phone, outbound v2 MCP server-status report
	TypeMCPStatusRequest = "mcp_status_request" // phone → binary, inbound v2 control (switch-intercepted)
	TypeMCPReconnect     = "mcp_reconnect"      // phone → binary, inbound v2 control (switch-intercepted)
	TypeMCPToggle        = "mcp_toggle"         // phone → binary, inbound v2 control (switch-intercepted)
)

// Mobile Protocol v2 screen snapshot (docs/protocol-mobile.md § Screen snapshot).
// The daemon renders no screen: handleRequestSnapshot refuses every
// request_snapshot (conversation.not_found, else server.binary_offline), and
// screen_snapshot is never emitted and stays declared for reference. Clients read
// the run configuration from request_session_settings.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes.
// relay_guard_test.go files request_snapshot as switch-intercepted and
// screen_snapshot as push.
const (
	TypeRequestSnapshot = "request_snapshot" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeScreenSnapshot  = "screen_snapshot"  // binary → phone, outbound v2 event (plain text only)
)

// TypeResync tells a reconnecting phone to reload a conversation in full: its
// hello.last_event_id has aged out of the bounded per-conversation event ring, so
// the daemon sends this instead of a partial replay (ADR 025 § Backpressure /
// replay; docs/protocol-mobile.md § resync). Its payload is an inline
// conversation_id with no named struct.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeResync = "resync" // binary → phone, outbound v2 mid-turn-reconnect resync marker
)

// TypeSessionTransition marks a session boundary: a conversation's session rotated
// (a /clear, an idle eviction, a workspace change), so the phone can draw the
// boundary rather than infer it (docs/protocol-mobile.md § session_transition).
// The payload is SessionTransitionPayload.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeSessionTransition = "session_transition" // binary → phone, outbound v2 session-boundary marker
)

// Mobile Protocol v2 modal frames (docs/protocol-mobile.md § Modal). The daemon
// describes a modal claude raised (a permission prompt, a plan approval, a tool
// confirmation), the phone answers, and the daemon drives the answer back into
// claude. modal_shown and modal_dismissed are outbound; modal_answer and
// modal_cancel are inbound control frames intercepted by dispatchAppFrame, with
// no dispatch.Route handler. Viewing rides the "interactive" capability;
// answering is gated separately, per device and default off, by the security
// model rather than a wire capability.
//
// MUST NOT be added to inboundAppTypeSet: all four are in v2OnlyTypes.
// relay_guard_test.go files the inbound two as switch-intercepted and the
// outbound two as push.
const (
	TypeModalShown     = "modal_shown"     // binary → phone, outbound v2 modal-surfaced event
	TypeModalAnswer    = "modal_answer"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalCancel    = "modal_cancel"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalDismissed = "modal_dismissed" // binary → phone, outbound v2 modal-resolution event
)

// Mobile Protocol v2 queued-backlog frames (docs/protocol-mobile.md § Queue). A
// message sent while claude is busy waits in internal/msgqueue. queue_state is the
// wire form of msgqueue.Snapshot; dequeue_message drives msgqueue.Remove, and
// send_queued_now drives msgqueue.SendNow, writing the entry into the running
// turn instead of dropping it. The two inbound verbs are intercepted by
// dispatchAppFrame. Unlike modal_answer they are ungated for any paired phone
// (ADR 025 § Security model); queued_msg_id is a per-conversation counter, not a
// security primitive.
//
// MUST NOT be added to inboundAppTypeSet: all three are in v2OnlyTypes.
// relay_guard_test.go files the inbound two as switch-intercepted and queue_state
// as push.
const (
	TypeQueueState     = "queue_state"     // binary → phone, outbound v2 queued-backlog snapshot
	TypeDequeueMessage = "dequeue_message" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeSendQueuedNow  = "send_queued_now" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// TypeInterrupt stops the running turn, the remote equivalent of Esc
// (docs/protocol-mobile.md § Interrupt). The daemon maps it to turnevent.Cancel.
// Its optional InterruptPayload names the conversation; an absent payload, an
// absent or empty conversation_id and a body that does not decode all mean the
// conversation the daemon's cursor points at, which keeps old clients working. A
// replay just stops the turn again, so there is no nonce or dedup. It requires
// the negotiated "interactive" capability and is exempt from the per-device
// permission gate (ADR 025 § Security model); naming a conversation is a
// validated lookup key, not a widening.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeInterrupt = "interrupt" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 debug-bundle stream (docs/protocol-mobile.md § Debug
// bundle). A bundle routinely exceeds one AEAD frame, so the daemon streams it as
// debug_bundle_chunk frames (one base64 slice each, 0-based contiguous seq) ended
// by debug_bundle_done, which carries the exact chunk count so the phone can
// detect truncation. Both ride the asynchronous push path, never the synchronous
// handler-reply channel.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives them.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeDebugBundleChunk = "debug_bundle_chunk" // binary → phone, outbound v2 bundle chunk
	TypeDebugBundleDone  = "debug_bundle_done"  // binary → phone, outbound v2 bundle completion marker
)

// TypeRequestDebugBundle asks for the daemon's debug bundle, built around its
// recent log ring (docs/protocol-mobile.md § Debug bundle lists the contents).
// The bundle is daemon-global, so the frame is bare: no payload, and no field
// that could select another session's data. The answer is the debug_bundle_chunk
// stream, never the synchronous handler-reply path. Authorization is pairing,
// enforced at the Noise IK handshake (an unpaired device is refused with 4401),
// so the verb has no gate of its own.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeRequestDebugBundle = "request_debug_bundle" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// TypeNewSession starts a fresh session in one conversation
// (docs/protocol-mobile.md § New session): a kill and respawn under a new session
// id through the SessionStarter seam, not a /clear keystroke. There is no ack;
// the client sees the break as session_transition (reason "clear"), and apart
// from CodeNewSessionWorkspaceRefused the verb is silent. Its optional
// NewSessionPayload names the conversation; an absent payload, an absent
// conversation_id and an undecodable body all mean the conversation the daemon's
// cursor points at. It requires the negotiated "interactive" capability and is
// exempt from the per-device permission gate (ADR 025 § Security model).
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeNewSession = "new_session" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 session-settings write (docs/protocol-mobile.md § Session
// settings). set_session_settings changes one session's model, reasoning effort
// and YOLO (bypass-permissions) mode; session_settings_updated confirms it.
// SetSessionSettingsPayload uses per-field pointers, so nil means "leave
// unchanged", distinct from an explicit zero value.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes.
// relay_guard_test.go files the request as switch-intercepted and the
// confirmation as reply.
const (
	TypeSetSessionSettings     = "set_session_settings"     // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeSessionSettingsUpdated = "session_settings_updated" // binary → phone, outbound v2 reply confirming the change
)

// TypeSwitchAgent names a phone → binary v2 agent-switch request
// (docs/protocol-mobile.md § switch_agent), intercepted by dispatchAppFrame. It
// remains outside the v1 inboundAppTypeSet.
const TypeSwitchAgent = "switch_agent"

// Mobile Protocol v2 session-settings read (docs/protocol-mobile.md § Session
// settings): a client asks for the current values and for the session id that
// set_session_settings must address.
//
// RequestSessionSettingsPayload names the conversation. conversation_id is
// untrusted and used only for an in-memory lookup through
// handleRequestSessionSettings' conversation-keyed seam; it reaches no log, error
// string, path or reply. It selects which session the reply describes, and the
// reported id and values resolve together. Every unresolvable case (an unhosted
// conversation, one with no live session, an absent or empty id) gets a
// zero-valued session_settings, never an error and never the shared bootstrap
// session's id or values.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes.
// relay_guard_test.go files the request as switch-intercepted and the answer as
// reply.
const (
	TypeRequestSessionSettings = "request_session_settings" // phone → binary, inbound v2 control carrying RequestSessionSettingsPayload (intercepted pre-dispatch.Route)
	TypeSessionSettings        = "session_settings"         // binary → phone, outbound v2 reply carrying the current run configuration
)

// TypeSessionError reports a conversation-scoped session problem as an unsolicited
// frame (docs/protocol-mobile.md § Error codes). Its code says whether the problem
// is terminal:
//
//   - CodeSessionBlocked is terminal. internal/msgqueue bounded a
//     persistent-failure drain and gave up (the OnGiveUp seam); a client must
//     never read it as transient.
//   - CodeSessionChildCrashing is not terminal. claude has exited at startup
//     several times in a row and the daemon is still restarting it. Queued
//     messages are kept, and a CodeSessionBlocked may follow if delivery never
//     recovers.
//
// It answers no request, so SessionErrorPayload carries the conversation identity
// itself, plus the code and a human-readable message.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeSessionError = "session_error" // binary → phone, outbound v2 unsolicited conversation-scoped session-error frame; the code says whether it is terminal
)

// TypeAttachmentChunk carries one slice of one attachment's bytes plus the whole
// transfer's metadata (AttachmentChunkPayload; docs/protocol-mobile.md
// § Attachments). A file routinely exceeds one AEAD frame, so the sender splits it
// and the receiver reassembles; the relay stays transport-only.
//
// It is the one bidirectional type: upload (client → daemon, handled by
// handleAttachmentChunk in internal/relay) and retrieval (daemon → client, sent by
// StreamAttachment) share the frame, so the two legs cannot drift apart. The trust
// asymmetry (inbound every field is a client claim, outbound daemon-authored)
// lives in AttachmentChunkPayload's SECURITY block. There is no completion frame:
// TotalChunks rides every chunk, so a receiver knows the count from the first one
// it sees. A retrieval abandoned mid-stream ends in a TypeError correlated via
// in_reply_to.
//
// MUST NOT be added to inboundAppTypeSet: since the upload leg is inbound, that
// rejection is what keeps a v1 client from sending one into dispatch.Route.
// v2OnlyTypes in compat_test.go, switch-intercepted in relay_guard_test.go.
const (
	TypeAttachmentChunk = "attachment_chunk" // phone ↔ binary, one chunk of an attachment's bytes (both directions)
)

// TypeAttachmentStored is the upload leg's one success reply: the transfer
// completed, its claims checked out, and the bytes are on the host under the id
// the client chose (AttachmentStoredPayload; docs/protocol-mobile.md
// § Attachments). It is the positive of CodeAttachmentStorageFailed and covers the
// whole transfer. The name says what the frame is, the daemon's assertion, not the
// client's action.
//
// It is a reply correlated via InReplyTo, as TypeSessionSettingsUpdated is and as
// the CodeAttachmentStreamAborted failure on the same leg is, so the payload has
// no request-id key. InReplyTo names the chunk whose arrival completed the
// transfer, not the one with the highest index: chunks may arrive in any order and
// the client cannot predict which envelope closes the set, so the payload also
// carries the attachment id. This block, the § Application message types row and
// relay_guard_test.go's reply entry state one decision and must agree.
//
// MUST NOT be added to inboundAppTypeSet: that rejection is what keeps a v1 client
// from sending one into dispatch.Route. v2OnlyTypes in compat_test.go, reply in
// relay_guard_test.go.
const (
	TypeAttachmentStored = "attachment_stored" // binary → phone, the upload leg's success reply, correlated via in_reply_to
)

// TypeRequestAttachment asks the daemon for a stored attachment
// (RequestAttachmentPayload; docs/protocol-mobile.md § Attachments). The answer is
// an attachment_chunk stream correlated by in_reply_to, or one
// CodeAttachmentNotFound or CodeAttachmentStreamAborted TypeError. The name
// follows the request_* family of read verbs.
//
// The payload names a conversation and an attachment and nothing else. Safety is
// confinement: the id is a lookup key validated against the daemon's registry
// before any path join, and naming a conversation is not authorization;
// RequestAttachmentPayload's block states the rule in full. Correlation rides
// InReplyTo with no request-id key, as for TypeAttachmentStored, and the committed
// testdata/request_attachment.json and testdata/attachment_chunk_retrieval.json
// pin it.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeRequestAttachment = "request_attachment" // phone → binary, inbound v2 control (switch-intercepted)
)

// TypeReadWorkspaceFile reads one regular file as it is on the host now, from the
// recorded workspace or an admitted read folder of the conversation it names
// (ReadWorkspaceFilePayload; docs/protocol-mobile.md § read_workspace_file). It is
// answered exactly as TypeRequestAttachment is: an attachment_chunk stream
// correlated by in_reply_to, or one CodeAttachmentNotFound or
// CodeAttachmentStreamAborted TypeError.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeReadWorkspaceFile = "read_workspace_file" // phone → binary, inbound v2 control (switch-intercepted)
)

// TypeAttachmentOffered tells a client that a file exists on the host for a
// conversation (AttachmentOfferedPayload; docs/protocol-mobile.md § Attachments),
// which is how a client learns an attachment id it did not mint. No bytes ride it
// ("offered", not "sent"); a client that wants them sends request_attachment.
// TypeMessage could not carry this: it is pushed only for the operator's own
// message, and widening it would change a record shape history-page decoders
// already read.
//
// It is an unsolicited push, so nothing correlates it; this block, the
// § Application message types row and relay_guard_test.go's push entry must agree.
// Like modal_shown it originates from a claude MCP tool call on the control socket
// and carries a conversation id but no turn id.
//
// MUST NOT be added to inboundAppTypeSet: that rejection also stops a v1 client
// sending one to claim that a file exists. v2OnlyTypes in compat_test.go, push in
// relay_guard_test.go; it has no inbound leg.
const (
	TypeAttachmentOffered = "attachment_offered" // binary → phone, unsolicited push: a file exists on the host for this conversation
)

// TypeRequestHistory asks for conversation-history entries older than the ones a
// client holds (RequestHistoryPayload; docs/protocol-mobile.md § Conversation
// history). It reads internal/history's durable log; catch-up after a dropped
// connection (replayMissed over internal/eventring) is a separate, bounded
// mechanism. Pages walk backwards, newest first, addressed by an opaque cursor
// rather than an offset or page number, which a concurrent append would
// invalidate. RequestHistoryPayload's block carries the handler's rules: the
// cursor is not a secret or a capability, a Limit of 0 means the daemon chooses,
// and the conversation id is validated against the registry before any path join.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeRequestHistory = "request_history" // phone → binary, inbound v2 control (switch-intercepted)
)

// TypeHistoryPage is one backward step of a history walk: the entries, the cursor
// to ask again with, and whether the start of the log was reached
// (HistoryPagePayload, mirroring history.Page; docs/protocol-mobile.md
// § Conversation history). It is a reply correlated via InReplyTo, like
// TypeSessionSettings, and carries no conversation id because the client knows
// what it asked.
//
// Each entry is one wire envelope's worth (type, payload, timestamp, durable entry
// id), so a client re-reduces a page oldest first through its live-stream
// reducer. The id is the log's durable per-conversation id, not Envelope.EventID,
// which does not survive a restart. A walk ends on at_start, never on an empty
// entries list (HistoryPagePayload says why). Unlike the request, this frame
// carries replayed content, claude-authored for assistant entries, so § Security
// model's threat 1 applies (see HistoryEntry).
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go, reply in
// relay_guard_test.go.
const (
	TypeHistoryPage = "history_page" // binary → phone, one backward step of a history walk, correlated via in_reply_to
)

// TypeRequestModelList asks for a conversation's model menu at any time
// (RequestModelListPayload; docs/protocol-mobile.md § Asking for a model list on
// demand). It covers a conversation created after the client connected, which
// neither the live lane nor the connect-time reconcile reaches. The answer is
// model_list with ModelListPayload unchanged and from the same source, correlated
// by InReplyTo and carrying no EventID, so it never enters the replay ring and
// model_list stays a push in relay_guard_test.go. A refusal is a TypeError with
// CodeConversationNotFound or CodeModelListUnavailable, since an empty models
// array must never stand in for "unknown".
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeRequestModelList = "request_model_list" // phone → binary, inbound v2 control (switch-intercepted)
)

// Mobile Protocol v2 conversation system-prompt read (RequestSystemPromptPayload,
// SystemPromptPayload; docs/protocol-mobile.md § Reading a conversation's system
// prompt). set_system_prompt's ack does not carry the value, so this pair is how
// a client learns what is stored. A stored prompt takes effect at the next session
// start, so instead of a second copy of the text the reply carries a three-value
// verdict comparing it with what the live session was spawned with.
//
// It is a route of its own because the prompt (up to
// conversations.MaxSystemPromptBytes) would push the all-rows conversations reply
// past the 65519-byte envelope cap, and SessionSettingsPayload is all zeros
// exactly when no session resolves. Every unresolvable case, an unhosted
// conversation included, gets the truthful no_session reply, so there is no error
// code and the verb is no membership oracle.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes.
// relay_guard_test.go files the request as switch-intercepted and the answer as
// reply.
const (
	TypeRequestSystemPrompt = "request_system_prompt" // phone → binary, inbound v2 control (switch-intercepted)
	TypeSystemPrompt        = "system_prompt"         // binary → phone, one conversation's stored prompt plus a live-session verdict, correlated via in_reply_to
)

// Mobile Protocol v2 clarifying-question batch (docs/protocol-mobile.md
// § Question). claude's AskUserQuestion call rides the same approval bridge as a
// permission prompt (handleApprove parks it in internal/permbridge keyed by
// tool_use_id, and streamApprovalBridge.Surface raises it), but reaches a client
// as one question_shown frame per batch rather than as a modal.
//
// It is its own frame family, not a grown modal_shown, for security.
// ModalShownPayload.DefaultOptionID is the deny option (denyByClass in
// internal/modalbridge), and default_option_id MUST equal one of options[].id on
// every modal. A question has no deny option and no safe default, so riding
// modal_shown would make that invariant class-conditional. ModalAnswerPayload also
// carries a single OptionID, and claude nests options, each with a description,
// under each question, so the modal shape fits neither leg. Full argument:
// docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-question-batch.md
//
// The names are the daemon's: a wire type names what the frame is to a client.
// claude's words are AskUserQuestion and the keys questions, question, header,
// options and multiSelect. The singular "question" is a substring of the correct
// name, so a vocabulary check tests the plural questions and the "ask" every
// derivation of the tool name carries; that is why the name is not
// questions_shown (TestQuestionShownType_IsNotClaudesVocabulary).
//
// question_dismissed is its own type because modal_dismissed identifies its
// target by modal_id and a client routes it to the modal panel. The source values
// do not carry over: of modal_dismissed's {remote, local, timeout} only timeout
// applies here, and the caller-disconnect and shutdown paths have no member, so
// source is a plain string the producer owns (docs/protocol-mobile.md § Question
// publishes the values).
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives these.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeQuestionShown     = "question_shown"     // binary → phone, outbound v2 clarifying-question batch
	TypeQuestionDismissed = "question_dismissed" // binary → phone, outbound v2 question-batch resolution event
)

// Mobile Protocol v2 clarifying-question answers (QuestionAnswerPayload,
// QuestionRefusedPayload; docs/protocol-mobile.md § Question): question_answer
// carries the operator's selections and question_refused says the operator
// declined. Two types rather than a nullable flag, mirroring modal_answer and
// modal_cancel.
//
// An answer names each question by its index into the batch, and the daemon reads
// the text from its own parked copy, so no claude-authored string comes back
// across the trust boundary. The values are client-authored free text, opaque and
// checked against nothing. claude's optional top-level freeform response is
// deliberately not carried; adding it later needs no wire change. The singular
// "question" is a substring of AskUserQuestion, so neither name may be probed with
// a strings.Contains on it.
//
// Both are intercepted by dispatchAppFrame, so relay_guard_test.go files them as
// switch-intercepted, beside modal_answer and modal_cancel. The handler applies no
// authorization itself: the QuestionResolver seam (questionResolverV2 in cmd/pyry)
// is the per-device gate.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes, and
// IsKnownAppType rejects them with ErrUnknownType.
const (
	TypeQuestionAnswer  = "question_answer"  // phone → binary, inbound v2 control (switch-intercepted)
	TypeQuestionRefused = "question_refused" // phone → binary, inbound v2 control (switch-intercepted)
)

// Mobile Protocol v2 pairing mint (MintPairingPayload, PairingMintedPayload;
// docs/protocol-mobile.md § Minting a pairing from a paired client). An
// already-paired client asks the daemon to mint a pairing for another device, so
// pairing a second device needs no shell on the host (pyry pair).
//
// The names follow the write-verb family (create_conversation →
// conversation_created), not request_*: the request_* verbs read existing state,
// and this one creates a credential and a devices.json record, so the type string
// shows a side effect on the host.
//
// The reply is a bearer credential. Keeping mint_pairing out of inboundAppTypeSet
// is what stops a v1 client reaching a credential mint at all. The handler MUST
// answer only an authenticated paired device; the interactive capability is a
// feature advertisement, not an authorization, and is no substitute. Minting
// authority moves from "a shell on the host" to "any paired device", widening
// § Security model threat 4 on purpose; per-device revocation (pyry pair rm)
// still applies. device_name reaches no claude prompt.
//
// MUST NOT be added to inboundAppTypeSet: both are in v2OnlyTypes.
// relay_guard_test.go files the request as switch-intercepted and the answer as
// reply.
const (
	TypeMintPairing   = "mint_pairing"   // phone → binary, inbound v2 control (switch-intercepted)
	TypePairingMinted = "pairing_minted" // binary → phone, one minted pairing as the pair.Encode string, correlated via in_reply_to
)

// TypeCompactionBoundary marks a finished compaction: what triggered it and how
// far the context shrank, so a thread can say why claude no longer remembers what
// came before (docs/protocol-mobile.md § compaction_boundary). It cannot be a wider
// compacting payload: claude states the counts on a separate
// system/compact_boundary line that arrives after the system/status line
// compacting's falling edge is read from (see turnevent.CompactionBoundary). The
// name uses the daemon's word, compaction; "boundary" is what the frame marks.
//
// MUST NOT be added to inboundAppTypeSet: an old phone never receives it.
// v2OnlyTypes in compat_test.go, push in relay_guard_test.go.
const (
	TypeCompactionBoundary = "compaction_boundary" // binary → phone, outbound v2 compaction boundary
)

// TypeToolDenied says which announced tool call claude refused to run, so a
// blocked tool row looks blocked rather than broken (docs/protocol-mobile.md
// § tool_denied). It is a frame of its own rather than fields on tool_result:
// claude's system/permission_denied line arrives before the tool result, and a
// denial recovered from the result line can arrive after tool_result has shipped
// (see turnevent.ToolCallDenied). The name says what the frame is about, a tool
// call; "permission" would suggest the unrelated MCP approval flow.
//
// MUST NOT be added to inboundAppTypeSet: that is also the guarantee that nothing
// accepts a tool_denied from a phone. v2OnlyTypes in compat_test.go, push in
// relay_guard_test.go.
const (
	TypeToolDenied = "tool_denied" // binary → phone, outbound v2 tool-denial marker
)

// Mobile Protocol v2 tool-progress reading (docs/protocol-mobile.md
// § tool_progress). This push-only frame updates the open tool row named by
// tool_use_id with claude's signed elapsed-seconds reading. It is a report, not a
// lifecycle edge or an inbound control surface.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go, push in
// relay_guard_test.go.
const (
	TypeToolProgress = "tool_progress" // binary → phone, outbound v2 tool-progress reading
)

// TypeBanner carries text claude printed about the session rather than as part of
// an answer, such as a hook's block reason (docs/protocol-mobile.md § banner). No
// other frame could carry it: unrecognized_message is a parser-gap diagnostic,
// compacting and rate_limited each report one machine state, and the assistant
// stream carries only what the model said. A single report with no edges. The
// name derives from none of claude's words (the subtypes informational and
// notification, the keys content, level and prevent_continuation);
// TestBannerType_IsNotClaudesVocabulary pins that.
//
// MUST NOT be added to inboundAppTypeSet: that is also the guarantee that nothing
// accepts a banner from a phone. v2OnlyTypes in compat_test.go, push in
// relay_guard_test.go.
const (
	TypeBanner = "banner" // binary → phone, outbound v2 operator-facing text
)

// TypeContextUsage carries one conversation's context breakdown: the model, the
// totals and percentage, and three independently bounded inventories (categories,
// MCP tools, memory files), each with its own dropped count
// (docs/protocol-mobile.md § context_usage). One shape serves both producers: a
// push after each turn and the correlated reply to request_context_usage. The
// reading is claude's own arithmetic and informational; control decisions key on
// contextwindow.Read, and nothing in the daemon gates on what a client does with
// it.
//
// MUST NOT be added to inboundAppTypeSet: that is also the guarantee that nothing
// accepts a context reading from a phone. v2OnlyTypes in compat_test.go,
// push+reply in relay_guard_test.go.
const (
	TypeContextUsage = "context_usage" // binary → phone, outbound v2 context-window reading
)

// TypeRequestContextUsage asks for a conversation's context breakdown now
// (RequestContextUsagePayload; docs/protocol-mobile.md § Asking for a context usage
// reading on demand). The post-turn push is a detail:"summary" reading; this verb
// gets the costlier detail:"full" one. The answer is context_usage unchanged,
// correlated by InReplyTo and carrying no EventID, so it never enters the replay
// ring.
//
// The payload has no detail key; the daemon always asks at "full". Closely spaced
// asks collapse into one round trip and share its result, so a client-chosen
// detail would let one client downgrade another's reading.
//
// A refusal is CodeConversationNotFound or CodeContextUsageUnavailable. A
// malformed payload is tolerated rather than refused: the id only reaches a
// registry membership check, never a path, so the empty id a failed decode leaves
// is refused there. Do not copy that tolerance to a verb that joins the id into a
// path.
//
// MUST NOT be added to inboundAppTypeSet. v2OnlyTypes in compat_test.go,
// switch-intercepted in relay_guard_test.go.
const (
	TypeRequestContextUsage = "request_context_usage" // phone → binary, inbound v2 control (switch-intercepted)
)

// TypeStopBackgroundTask is a phone → binary v2 control verb, intercepted before
// v1 dispatch (docs/protocol-mobile.md § Stop background task). It requires
// negotiated interactive and is inert without a configured stopper.
// CapabilityStopBackgroundTask only lets a client detect support; it is not
// required to use the verb.
const TypeStopBackgroundTask = "stop_background_task"

// CodeStopBackgroundTaskRefused merges invalid task ids and seam refusals into
// one nonretryable answer without exposing child diagnostics or task existence.
const CodeStopBackgroundTaskRefused = "stop_background_task.refused"

// Hosted-app discovery and lifecycle vocabulary is v2-only and declared ahead
// of its producers. These constants enable neither routing nor advertisement.
const (
	TypeListApps     = "list_apps"     // client → daemon, discovery request
	TypeApps         = "apps"          // daemon → client, correlated discovery reply
	TypeAppUpdated   = "app_updated"   // daemon → client, whole-record notification
	TypeAppRemoved   = "app_removed"   // daemon → client, tombstone notification
	TypeAppCancel    = "app_cancel"    // client → daemon, list or resource cancellation
	TypeAppCancelled = "app_cancelled" // daemon → client, correlated cancellation reply
)

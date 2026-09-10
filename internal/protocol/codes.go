package protocol

// Error-code constants — wire values for the "code" field of error
// payloads (docs/protocol-mobile.md § Error codes). The naming convention
// is Code<Category><Reason>, matching the dotted-string structure
// category.reason. Grouped by category in spec-table order.
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

	// Message errors.
	CodeMessageTooLong = "message.too_long"

	// Relay errors.
	CodeRelayNoServer         = "relay.no_server"
	CodeRelayServerIDConflict = "relay.server_id_conflict"

	// Session errors.
	CodeSessionNotFound = "session.not_found"
	CodeSessionBlocked  = "session.blocked" // terminal give-up; NOT a retry hint (contrast server.binary_busy)

	// Attachment errors (#1751; docs/protocol-mobile.md § Attachments). The
	// reject vocabulary both attachment_chunk legs answer with, declared in one
	// place so #1741, #1743, #1897 and #2054 do not each invent a name (#1744 and
	// #1746, the two this list named when it was written, were each split and no
	// longer exist as work). The reasoning is published in that section rather
	// than duplicated here.
	//
	// attachment.not_found is DELIBERATELY MERGED: it answers every request that
	// yields no bytes — an unknown id, a non-canonical id, and an id resolving
	// outside the named conversation's directory alike. Two distinguishable codes
	// would make the asking verb a path-existence oracle, so the merge is a
	// disclosure decision, not an imprecision.
	//
	// IT SPANS BOTH VERBS as of #2036, no longer retrieval alone: a send_message
	// naming an attachment id that does not resolve under the message's own
	// conversation is answered with this same code. The predicate is identical —
	// an id did not resolve to a file inside that conversation's directory — and
	// so are the retryability, the static message and the client's repair, so a
	// second code would carry nothing a client could act on differently. The
	// merge argument applies with MORE force there, since send_message is the
	// cheaper probe of the two.
	//
	// The message stays STATIC and, where a request names several ids, MUST NOT
	// name WHICH of them failed — a per-id answer turns one message into a batch
	// existence-probe for up to MaxAttachmentIDsPerMessage ids and rebuilds the
	// oracle this merge exists to prevent.
	//
	// The three retryable members (too_many_uploads, storage_failed,
	// stream_aborted) are published as retry-AFTER-A-BACKOFF: an immediate
	// resend turns each into a hot loop, and too_many_uploads' bound clears
	// only when OTHER uploads finish.
	CodeAttachmentInvalidChunk    = "attachment.invalid_chunk"
	CodeAttachmentIntegrityFailed = "attachment.integrity_failed"
	CodeAttachmentTooManyUploads  = "attachment.too_many_uploads" // transient; the bound clears when other uploads finish (contrast attachment.too_large)
	CodeAttachmentTooLarge        = "attachment.too_large"        // the WHOLE transfer exceeds the receiver's per-upload bound; ONE oversize envelope is message.too_long
	CodeAttachmentStorageFailed   = "attachment.storage_failed"
	CodeAttachmentNotFound        = "attachment.not_found"
	CodeAttachmentStreamAborted   = "attachment.stream_aborted" // a TypeError correlated via in_reply_to, never a second attachment frame

	// Conversation-history errors (#2116; docs/protocol-mobile.md
	// § Conversation history). MINTED WITH THE HANDLER THAT SENDS THEM, which is
	// the rule that section states in as many words — it published the reject
	// CONDITIONS ahead of any code and named this ticket the owner of the codes,
	// the sequencing attachment_chunk established. None of these existed while
	// request_history had no answer.
	//
	// A CONDITION THE SECTION LISTS IS ANSWERED BY AN EXISTING CODE, not by a
	// fifth new one: a conversation_id that is not of canonical shape or names no
	// conversation the daemon hosts is CodeConversationNotFound. That is the
	// OPPOSITE of the attachment retrieval verb's choice above, deliberately —
	// there, merging an unknown conversation into attachment.not_found prevents a
	// path-existence oracle over a SECOND id. There is no second id here, and
	// request_snapshot already answers an unknown or foreign conversation
	// distinguishably, so a merge would buy nothing and cost a client the ability
	// to tell "wrong conversation" from "wrong cursor".
	//
	// history.invalid_cursor IS DELIBERATELY MERGED, and it is the one merge on
	// this path: a cursor that does not decode, one minted for another
	// conversation, and one naming a position not in this log are ONE answer.
	// Those distinctions are exactly what a probe would want, and internally they
	// arrive as the single history.ErrInvalidCursor sentinel, so the handler
	// CANNOT branch on what it must not distinguish. The refusal never echoes the
	// cursor back.
	//
	// RETRYABILITY SPLITS THE SET THREE-TO-ONE, and it is the field a client
	// actually branches on. The three client faults are permanent for the request
	// as sent — a different request repairs each — while history.unavailable is
	// the only one whose cause can clear without the client changing anything.
	CodeHistoryInvalidRequest  = "history.invalid_request"   // the payload did not decode; never an empty-but-successful request
	CodeHistoryInvalidPageSize = "history.invalid_page_size" // a NEGATIVE limit; 0 is not a reject, it asks the daemon to choose
	CodeHistoryInvalidCursor   = "history.invalid_cursor"    // undecodable, foreign, or naming a position not in this log — one merged answer
	CodeHistoryUnavailable     = "history.unavailable"       // the log could not be read; the only retryable member of this group

	// On-demand model-list error (#2125; docs/protocol-mobile.md § Error codes).
	// MINTED WITH THE HANDLER THAT SENDS IT, the sequencing #2052 established and
	// the history group above followed: no reject vocabulary exists ahead of the
	// code that can emit it.
	//
	// ONE NEW CODE, NOT TWO. The other condition request_model_list can refuse —
	// the daemon does not host the named conversation — is answered by the EXISTING
	// CodeConversationNotFound, for the history group's stated reason: there is no
	// second id here to build a path-existence oracle over, and request_snapshot and
	// request_history already answer an unknown conversation distinguishably, so a
	// merge would buy nothing and cost a client the ability to tell "wrong
	// conversation" from "no menu yet". KnownConversation is what separates the two
	// arms.
	//
	// IT MERGES TWO CAUSES DELIBERATELY: nothing retained anywhere (no bootstrap in
	// the pool, a bootstrap constructed evicted, or its initialize reply not yet
	// arrived) and no daemon-side source wired at all. Both mean the same thing to a
	// client — the daemon hosts this conversation and has no menu to give you — and
	// the repair is identical. Distinguishing them would publish whether the host's
	// model-list source is configured, which is a fact about the machine rather than
	// about the request.
	//
	// RETRYABLE, and the retryability follows the dominant cause rather than the
	// merged one: a child that has not yet answered its initialize ask will, and the
	// caller changes nothing to make that happen.
	//
	// IT IS NOT AN EMPTY MENU. turnevent.ModelList.Models is documented never-empty,
	// so an empty models array must NEVER stand in for "unknown" — this code is the
	// only way to say "no list", alongside the absence of a frame on the
	// unsolicited paths.
	CodeModelListUnavailable = "model_list.unavailable" // the daemon hosts the conversation but has no vocabulary to answer with; retryable

	// Workspace error (#2207; docs/protocol-mobile.md § Error codes). MINTED WITH
	// THE HANDLER THAT SENDS IT, the sequencing #2052, the history group and #2125
	// each followed: no reject vocabulary exists ahead of the code that can emit it.
	//
	// NOT CodeConversationNotFound, and the distinction is the point rather than a
	// naming preference. A rename_workspace names no conversation at all — its key
	// is a workspace path, and N conversations may share one. Answering with the
	// conversation code would send a client looking for a row it never asked about
	// and could not act on.
	//
	// IT IS THE MAP'S ONLY CEILING, which is why it is a containment property and
	// not merely a UX nicety. A workspace_labels key is creatable only at a path
	// that byte-equals a stored conversation's cwd, so the key count is bounded by
	// the number of distinct cwds the daemon actually hosts. Relaxing this refusal
	// would remove that bound.
	//
	// NON-RETRYABLE: the same path fails identically until a conversation exists
	// there, which is not something a retry accomplishes. The message is static and
	// never echoes the requested path.
	CodeWorkspaceNotFound = "workspace.not_found"

	// Pairing-mint errors (#2127; docs/protocol-mobile.md § Error codes). MINTED
	// WITH THE HANDLER THAT SENDS THEM, the sequencing #2052, the history group,
	// #2125 and #2207 each followed: no reject vocabulary exists ahead of the code
	// that can emit it.
	//
	// NEITHER IS CodeAuthInvalidToken, and that is the group's defining decision
	// rather than a naming preference. A mint_pairing reaching a handler at all has
	// already presented a token the handshake validated, so answering an auth code
	// would tell a legitimate client its credential was rejected and invite it to
	// re-pair — the one repair that cannot help. The device is authenticated; what
	// it lacks is privilege.
	//
	// NEITHER IS AN ORACLE, which is the property the wire contract asks this group
	// for. pairing.not_permitted reports only that the asking device lacks the
	// remote-permissions flag — a fact it can already establish by answering any
	// permission modal and being denied — and NOTHING about the host, the registry,
	// or any other device. It is not conditioned on the requested device_name, so it
	// cannot be used to probe which labels exist.
	//
	// pairing.unavailable MERGES EVERY HOST-SIDE FAILURE deliberately: a busy
	// devices.json lock, a registry read or write error, and an RNG failure arrive
	// as one answer. Each can clear without the client changing anything, the repair
	// is identical for all three, and distinguishing them would publish facts about
	// the machine rather than about the request. The errors behind them format an
	// absolute host path and never reach the wire at all.
	//
	// A MALFORMED REQUEST IS NOT IN THIS GROUP. An undecodable payload — including
	// a device_name over MaxDeviceNameBytes, which MintPairingPayload.UnmarshalJSON
	// refuses — is answered with the existing CodeProtocolMalformed, the answer that
	// type's own doc block assigns. A third code for it would be a second spelling
	// of a decision already made.
	//
	// RETRYABILITY SPLITS THE PAIR ONE-TO-ONE: the privilege refusal is permanent
	// for the device as paired (only a shell on the host can change it), and the
	// host-side failure is the transient one.
	CodePairingNotPermitted = "pairing.not_permitted" // the device is authenticated but is not privileged to mint; permanent
	CodePairingUnavailable  = "pairing.unavailable"   // the mint could not be completed on the host; one merged, retryable answer
)

// Envelope-type constants — wire values for Envelope.Type
// (docs/protocol-mobile.md § Message types). The set is closed in v1; new
// types require a v2 envelope per the protocol's versioning policy.
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
	// TypeRenameConversation is a phone → binary dispatch.Route write verb
	// (like create_conversation / promote_conversation): it renames an
	// existing conversation and replies with the reused conversation_updated
	// record. It is a inboundAppTypeSet member, not a v2 control frame — see the
	// v1/v2 partition in envelope.go / compat_test.go.
	TypeRenameConversation = "rename_conversation"
	// TypeDeleteConversation is a phone → binary dispatch.Route write verb
	// (like rename_conversation / create_conversation): it PERMANENTLY removes
	// an existing conversation from the registry (hard delete — the reversible
	// path is archive/unarchive) and replies with a conversation_deleted
	// acknowledgement. It is a inboundAppTypeSet member, not a v2 control frame — see
	// the v1/v2 partition in envelope.go / compat_test.go.
	TypeDeleteConversation = "delete_conversation"
	// TypeConversationDeleted is the binary → phone acknowledgement replied to
	// a delete_conversation, correlated via in_reply_to. Unlike
	// conversation_updated it carries only the deleted conversation's id — the
	// record no longer exists, so no name/cwd/last_used_at can be projected.
	// Also a inboundAppTypeSet member.
	TypeConversationDeleted = "conversation_deleted"
	// TypeArchiveConversation is a phone → binary dispatch.Route write verb
	// (like rename_conversation / delete_conversation): it sets an existing
	// conversation's durable archived flag (IsArchived = true) and replies with
	// the reused conversation_updated record reflecting the new state. It is a
	// inboundAppTypeSet member, not a v2 control frame — see the v1/v2 partition in
	// envelope.go / compat_test.go. Its symmetric restore is
	// unarchive_conversation; both carry the shared ArchiveConversationPayload.
	TypeArchiveConversation = "archive_conversation"
	// TypeUnarchiveConversation is the symmetric restore of
	// archive_conversation: a phone → binary dispatch.Route write verb that
	// clears the durable archived flag (IsArchived = false) and replies with the
	// reused conversation_updated record reflecting the restored (active) state.
	// It is a inboundAppTypeSet member, not a v2 control frame — see the v1/v2 partition
	// in envelope.go / compat_test.go.
	TypeUnarchiveConversation = "unarchive_conversation"
	// TypeChangeWorkspace is a phone → binary dispatch.Route write verb (like
	// rename_conversation / delete_conversation): it moves an existing
	// conversation to a client-chosen workspace folder by updating its recorded
	// workspace (its Cwd) to a target filesystem path — confined to $HOME before
	// it is stored — and replies with the reused conversation_updated record
	// reflecting the new workspace. "Workspace" IS the conversation's Cwd (this
	// codebase has no separate workspace-id concept). It is a inboundAppTypeSet member,
	// not a v2 control frame — see the v1/v2 partition in envelope.go /
	// compat_test.go ("v2 wire message" in the ticket title names the encrypted
	// v2 transport, not the v1/v2 type partition). The reply reuses
	// conversation_updated / ConversationUpdatedPayload (only cwd changed), so
	// there is no new reply type.
	TypeChangeWorkspace = "change_workspace"
	// TypeSetSystemPrompt is a phone → binary dispatch.Route write verb (like
	// rename_conversation / change_workspace): it sets or clears a conversation's
	// durable per-conversation system prompt — the operator-authored text #2150
	// appends to every session that conversation spawns. Keyed by conversation,
	// NOT by session (contrast set_session_settings): the prompt must be settable
	// with no session live and must outlive every session the conversation has.
	// The payload's system_prompt is nullable so "clear" is expressible distinctly
	// from "set it to the empty string"; the byte bound and the UTF-8 check live at
	// the registry door (conversations.MaxSystemPromptBytes), not here. It is a
	// inboundAppTypeSet member, not a v2 control frame — see the v1/v2 partition in
	// envelope.go / compat_test.go. The reply reuses conversation_updated /
	// ConversationUpdatedPayload, so there is no new reply type — and that record
	// deliberately carries NO prompt field, so the ack confirms the write without
	// echoing up to 8192 bytes of operator text to a frame documented as broadcast
	// to every phone on this server-id. The new value takes effect at the
	// conversation's next session start, not on a running child.
	TypeSetSystemPrompt = "set_system_prompt"

	// Workspace.
	// TypeCreateWorkspaceFolder is a phone → binary dispatch.Route write verb
	// (like create_conversation / change_workspace): it creates a new folder on
	// the daemon host under a client-supplied parent path, confined to $HOME, and
	// replies with the new workspace_folder_created record carrying the created
	// folder's canonical absolute path. It touches NO conversations registry — it
	// only creates a directory. It is a inboundAppTypeSet member, not a v2 control frame —
	// see the v1/v2 partition in envelope.go / compat_test.go ("v2 wire message"
	// in the ticket title names the encrypted v2 transport, not the v1/v2 type
	// partition). Unlike change_workspace (which reuses conversation_updated) the
	// reply is a NEW type, TypeWorkspaceFolderCreated.
	TypeCreateWorkspaceFolder = "create_workspace_folder"
	// TypeWorkspaceFolderCreated is the binary → phone reply to a
	// create_workspace_folder, correlated via in_reply_to. It carries the
	// canonical (symlink-resolved) absolute path of the created folder — no
	// conversation is involved, so no conversation record is projected. Also a
	// inboundAppTypeSet member.
	TypeWorkspaceFolderCreated = "workspace_folder_created"
	// TypeRecentWorkspaces is a phone → binary dispatch.Route read verb (like
	// list_conversations): it asks for the distinct set of recently-used
	// workspace folders and replies with a recent_workspaces_list record. The
	// list is derived entirely from Cwd values the daemon already owns in the
	// conversations registry — no untrusted input drives a filesystem operation,
	// so it is NOT security-sensitive (contrast create_workspace_folder, which
	// writes to disk from untrusted input). It is a inboundAppTypeSet member, not a v2
	// control frame — see the v1/v2 partition in envelope.go / compat_test.go
	// ("v2 wire message" in the ticket title names the encrypted v2 transport,
	// not the v1/v2 type partition). Its request payload is empty by spec.
	TypeRecentWorkspaces = "recent_workspaces"
	// TypeRecentWorkspacesList is the binary → phone reply to a
	// recent_workspaces, correlated via in_reply_to. It carries the distinct
	// workspace paths ordered most-recent-first, each with its most-recent
	// last_used_at. Also a inboundAppTypeSet member.
	TypeRecentWorkspacesList = "recent_workspaces_list"
	// TypeRenameWorkspace is a phone → binary dispatch.Route write verb (like
	// rename_conversation / set_system_prompt): it sets or clears the
	// operator-chosen display name of a workspace, so a folder whose name is
	// unhelpful reads the way the operator thinks of it. The payload's label is
	// nullable so "clear" is expressible distinctly from "set it to the empty
	// string"; the non-blank check and the MaxWorkspaceLabelBytes bound live in the
	// handler, because conversations.Registry.SetWorkspaceLabel is documented as
	// validating nothing. It is a inboundAppTypeSet member, not a v2 control frame —
	// see the v1/v2 partition in envelope.go / compat_test.go.
	//
	// KEYED BY WORKSPACE, NOT BY CONVERSATION, and that is why its reply is a new
	// type rather than the reused conversation_updated record change_workspace
	// answers with: N conversations share one cwd, a workspace has no row of its
	// own, and no conversation record could carry the change without naming one
	// arbitrary member of that set.
	TypeRenameWorkspace = "rename_workspace"
	// TypeWorkspaceUpdated is the binary → phone reply to a rename_workspace,
	// correlated via in_reply_to. It carries the workspace's path and its stored
	// label (null when cleared). Also a inboundAppTypeSet member.
	//
	// A NEW TYPE RATHER THAN A NEW ARM ON AN EXISTING ONE, deliberately, for
	// compatibility: an un-upgraded client drops an unknown frame type, whereas a
	// new field or a new meaning on a type it already handles would be rendered
	// wrongly rather than ignored.
	TypeWorkspaceUpdated = "workspace_updated"

	// Push.
	TypeRegisterPushToken = "register_push_token"
)

// Mobile Protocol v2 control-envelope types. These are NOT v1 application
// types; they MUST NOT appear in inboundAppTypeSet (internal/protocol/envelope.go).
// The v2 session manager intercepts them at the dispatch boundary
// (internal/relay/v2session.go's dispatchAppFrame) before
// internal/dispatch.Route is called, so handler-table lookup never sees
// them.
const (
	// TypeRekeyRequest is the Mobile Protocol v2 control envelope either
	// side may emit to nudge the peer toward initiating a re-key
	// handshake (docs/protocol-mobile.md § Re-key). It is informational
	// from the binary's perspective: the binary is the IK responder per
	// ADR 024, so an inbound rekey_request takes no transport action.
	//
	// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a
	// leak into that set would route the envelope to dispatch.Route's
	// handler chain, violating the v2 control / v1 application boundary
	// enforced by internal/relay's v2 session manager. The drift detector
	// in internal/protocol/compat_test.go partitions Type* constants
	// between inboundAppTypeSet and v2OnlyTypes; this constant lives in the
	// latter.
	TypeRekeyRequest = "rekey_request"
)

// Mobile Protocol v2 interactive application-event types. These are
// additive, capability-gated events the binary pushes to a phone that has
// advertised the "interactive" capability (docs/protocol-mobile.md
// § Interactive events). Unlike TypeRekeyRequest (a v2 control envelope
// intercepted before dispatch.Route), these are outbound binary → phone
// application events that are never dispatched inbound — but for the
// v1/v2 partition's purpose they are equally "v2-only".
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an old
// phone receives the coarse v1 "message" fan-out, not these. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these six live in the latter.
const (
	TypeTurnState      = "turn_state"
	TypeAssistantDelta = "assistant_delta"
	TypeToolUse        = "tool_use"
	TypeToolResult     = "tool_result"
	TypeTurnEnd        = "turn_end"
	TypeStall          = "stall"
)

// Mobile Protocol v2 PTY-derived status peers of TypeStall. Like stall, these
// are additive, capability-gated status frames the binary pushes to a phone
// that advertised the "interactive" capability — they surface claude's
// API-error retry and auto-compaction sub-states (docs/protocol-mobile.md
// § api_retry / § compacting). They are outbound binary → phone status events,
// never a turn-lifecycle event and never dispatched inbound.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an old
// phone never receives them. The drift detector in
// internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
const (
	TypeApiRetry   = "api_retry"  // binary → phone, outbound v2 api-retry status
	TypeCompacting = "compacting" // binary → phone, outbound v2 compaction status
)

// Mobile Protocol v2 unrecognized-message diagnostic. The stream-json parser
// recognises three top-level message types from claude and a fixed set of
// content blocks; everything outside the measured known-ignored list used to be
// dropped into a debug log the production daemon does not print, so a claude
// version that moved something meaningful into a new type would show nothing
// anywhere. This frame carries that drop to the client instead
// (docs/protocol-mobile.md § unrecognized_message).
//
// Grouped alone rather than with api_retry/compacting because it is not a claude
// sub-state: it reports a gap in OUR mapping, and its payload is the only
// interactive frame that carries raw model-adjacent JSON.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an old
// phone never receives it. The drift detector in
// internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this lives in the latter.
const (
	TypeUnrecognizedMessage = "unrecognized_message" // binary → phone, outbound v2 parser-gap diagnostic
)

// Mobile Protocol v2 background-task types. These are the frames in the
// vocabulary whose subject is work that OUTLIVES the turn that started it —
// claude backgrounds a shell command and it keeps running after the assistant
// reports end_turn. That gap is #1240's symptom exactly: the daemon reported
// turn_end carrying end_turn and went idle while a command claude started was
// provably still alive, and nothing reaching a phone separated that from a
// genuine finish.
//
// Grouped alone rather than with api_retry/compacting or with the
// turn-lifecycle six: those describe a live turn's sub-states and its
// lifecycle, and neither cluster's subject is turn-independent work.
//
// AMENDED 2026-09-10 (#2246): the block admits a periodic READING beside three
// lifecycle frames, and the two sentences above are edited rather than annotated
// because both were counts of a set that grew. background_task_progress reports
// that a running task is still working and what it is doing, so it has no edges at
// all — but this block's criterion is the frame's SUBJECT, turn-independent work,
// not its shape, which is why it lands here and not beside thinking_progress. That
// constant is grouped alone for the reason its own doc gives: its subject is neither
// the turn's lifecycle nor turn-independent work.
//
// The NAMES are the daemon's, not claude's. internal/streamsup/parser.go
// translates claude's system/task_started, system/task_updated,
// system/background_tasks_changed and system/task_progress subtypes into
// internal/turnevent variants — the authority on that set being
// streamsup.emitSystemSubtype's case arms rather than this list, which is a reading
// aid and fails no build when it goes stale — and
// the wire follows the VARIANTS. The daemon is the single place a claude rename
// lands; if every client read claude's vocabulary directly, one claude release
// could break all of them at once with nothing in between to absorb it. In
// particular background_task_roster is named for what the frame IS (a snapshot)
// rather than for claude's trigger (…_changed), which would invite a consumer to
// read it as a delta and infer a finish the daemon has never observed.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: these
// are outbound binary → phone events an old phone never receives. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these all live in the latter.
//
// The declaring ticket (#1393) was wire vocabulary only; #1394 added
// internal/turnbridge's MapEvent cases for the first three variants and the
// docs/protocol-mobile.md section, so those frames reach an interactive v2
// mobile client. #2246 added the fourth end to end in one change.
const (
	TypeBackgroundTaskStarted  = "background_task_started"  // binary → phone, outbound v2 background-task open
	TypeBackgroundTaskUpdated  = "background_task_updated"  // binary → phone, outbound v2 background-task change
	TypeBackgroundTaskRoster   = "background_task_roster"   // binary → phone, outbound v2 background-task snapshot
	TypeBackgroundTaskProgress = "background_task_progress" // binary → phone, outbound v2 background-task activity reading
)

// Mobile Protocol v2 thinking-progress reading. claude's mid-turn proof of life:
// during a long assistant turn nothing else crosses the stream-json surface, so
// a client showing "thinking" cannot separate a slow answer from a wedged one.
// This frame carries the daemon's translation of claude's system/thinking_tokens
// line (docs/protocol-mobile.md § thinking_progress).
//
// Grouped alone rather than with api_retry/compacting or with the background-task
// three: it is not a show/clear sub-state with two edges, and its subject is
// neither the turn's lifecycle nor turn-independent work — it is a periodic
// READING of work in progress, with no edges at all.
//
// The NAME is the daemon's, not claude's, for the reason the background-task
// block above gives: the wire follows internal/turnevent's VARIANT
// (turnevent.ThinkingProgress), so a claude rename lands in one place instead of
// breaking every client at once. The discriminating word is "progress" — what the
// daemon reports — not "tokens", which is claude's subtype. It also disambiguates
// against ThoughtChunk, which carries the CONTENT of claude's reasoning and is
// never forwarded (ADR 025): this frame carries none, so a client that renders it
// as text has nothing to render.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this
// is an outbound binary → phone event an old phone never receives. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this lives in the latter.
const (
	TypeThinkingProgress = "thinking_progress" // binary → phone, outbound v2 thinking-progress reading
)

// Mobile Protocol v2 usage-limit report. claude's usage-limit window is in a
// state other than the one measured-benign one, so a turn that stops making
// progress because of a limit has something on the wire that says why. Before
// #1404 the daemon dropped claude's rate_limit_event line whole and the
// information existed nowhere in pyrycode, let alone on a phone
// (docs/protocol-mobile.md § rate_limited).
//
// Grouped alone rather than with any block above: it is not a turn sub-state
// with two edges, not turn-independent work, and not a periodic reading. It is a
// CONDITION report about a usage-limit window that is orthogonal to any turn —
// whichever turn happened to observe it neither owns it nor bounds it.
//
// The NAME is the daemon's, not claude's, for the reason the two blocks above
// give: the wire follows internal/turnevent's VARIANT (turnevent.RateLimited),
// so a claude rename lands in one place instead of breaking every client at
// once. claude's line type is rate_limit_event; the discriminating word is
// "event" — claude's, describing its line — where ours names the condition. It
// is also not a third vocabulary word: rate_limited is already the daemon's own
// name for this variant on its logging surface (cmd/pyry/interactive_turn_v2.go's
// eventKind returns exactly this string), so the wire agrees with the daemon.
// That agreement is prose here rather than a test because internal/protocol
// cannot import cmd/pyry.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this
// is an outbound binary → phone event an old phone never receives, and a leak
// into that set would let a phone send one into dispatch.Route. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this lives in the latter.
//
// The declaring ticket (#1405) was wire vocabulary only; #1410 added
// internal/turnbridge's MapEvent case for turnevent.RateLimited and cmd/pyry's
// handler case, so this frame now reaches an interactive v2 mobile client. Unlike
// #1393 the docs/protocol-mobile.md section landed with the shape rather than with
// the producer, because #1410 disclaimed the file.
const (
	TypeRateLimited = "rate_limited" // binary → phone, outbound v2 usage-limit report
)

// Mobile Protocol v2 announced-model report. claude names the model it resolved
// for the turn on its system/init line, and that value used to stop at the daemon
// boundary: internal/streamsup's parser has translated the line into
// turnevent.ModelAnnounced since #1600, but turnbridge.MapEvent's default dropped
// the variant, so no client could see what claude actually ran. #1638 added the
// case, so a client sees it now (docs/protocol-mobile.md § model_announced).
//
// Grouped alone rather than with any block above: it is not a turn sub-state with
// two edges, not turn-independent work, not a periodic reading, and not a
// condition report about a window. It is an IDENTITY report — what claude says it
// is, for the turn it says it about.
//
// The NAME is the daemon's, not claude's, for the reason the three blocks above
// give: the wire follows internal/turnevent's VARIANT (turnevent.ModelAnnounced),
// so a claude rename lands in one place instead of breaking every client at once.
// claude's subtype is init; the discriminating word is "init" — claude's, naming
// its LINE — where ours names what the daemon reports. The sibling blocks' form
// does not transfer to a test on "model": claude's KEY for the value is model, and
// so is turnevent.ModelAnnounced's field name, so that word is the subject noun
// rather than a vocabulary import.
//
// The wire field keeps the name model even though three v2 payloads already carry
// one (ScreenSnapshotPayload, SessionSettingsPayload, SetSessionSettingsPayload).
// Those three mean the per-session OVERRIDE, where "" is "inherited default"; this
// means what claude ANNOUNCED, and in the ordinary case the two disagree loudly.
// The field name is turnevent's in snake_case per the house convention, every
// field on this wire is scoped by its envelope type, and a rename would not reach
// a client author reading only screen_snapshot's row — the doc cross-references do
// (docs/protocol-mobile.md § model_announced, § Screen snapshot, § Session
// settings).
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this
// is an outbound binary → phone event an old phone never receives, and a leak
// into that set would let a phone send a model_announced frame into
// dispatch.Route. The drift detector in internal/protocol/compat_test.go
// partitions Type* constants between inboundAppTypeSet and v2OnlyTypes; this
// lives in the latter.
//
// The declaring ticket (#1616) was wire vocabulary only; #1638 added
// internal/turnbridge's MapEvent case for turnevent.ModelAnnounced and cmd/pyry's
// handler case, so this frame now reaches an interactive v2 mobile client. Same
// declare-then-emit sequencing as #1405→#1410 and #1393→#1394.
const (
	TypeModelAnnounced = "model_announced" // binary → phone, outbound v2 announced-model report
)

// Mobile Protocol v2 refusal-fallback report. claude emits this fact when it
// refuses a turn on one model and retries on another. The frame explains a
// later model change; TypeModelAnnounced remains the authority on which model
// claude is running.
//
// MUST NOT enter inboundAppTypeSet. This is an outbound interactive push, and
// the protocol and relay totality guards classify it as v2-only and push-only.
const (
	TypeModelRefusalFallback = "model_refusal_fallback"
)

// Mobile Protocol v2 model-list report. The daemon can ask claude which models it
// will accept — a control_request with subtype initialize, written on the child's
// held-open stdin, returns a models array — and that inventory used to stop at the
// daemon boundary, so no client could build a model menu, know which
// reasoning-effort levels a model supports, or know which models accept auto
// permission mode. #1848 added the turnbridge arm and #1849 the emitting case, so
// a client can read the inventory now (docs/protocol-mobile.md § model_list); the
// menus built on it are client-side work still outstanding (pyrycode-desktop#561,
// blocked since 2026-08-19; #682 is the same defect for the permission-mode menu).
//
// Grouped alone rather than with any block above: it is not a turn sub-state with
// two edges, not turn-independent work, not a periodic reading, not a condition
// report about a window, and not an identity report about one turn. It is a
// CAPABILITY report — what claude says it CAN be, where model_announced reports
// what it IS for the turn it says it about. An inventory rather than an event: it
// does not open or close a turn and is not turn-scoped.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give:
// the wire type names what the frame IS to a client, so a claude rename lands in
// one place instead of breaking every client at once. claude's words on this path
// are initialize (the control_request subtype) and models (the array key), so the
// discriminating words are "init" and "models" — model_list contains neither. The
// sibling blocks' form does not transfer to a test on "model": that is this
// frame's subject noun and the daemon's own word, so a strings.Contains check on
// it would be RED against the correct name, the same trap TypeModelAnnounced's
// block records for its own name.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this
// is an outbound binary → phone report an old phone never receives, and a leak
// into that set would let a phone send a model_list frame into dispatch.Route.
// Two drift detectors classify it, both mandatory from the moment the constant
// exists rather than from the moment something emits it: the partition in
// internal/protocol/compat_test.go splits Type* constants between
// inboundAppTypeSet and v2OnlyTypes (this lives in the latter), and
// cmd/pyry/relay_guard_test.go's excludedTypes records it as a push.
//
// No inbound request verb is declared here, and that is not an omission.
// TestEveryInboundV2TypeHasHandler's Assertion #1 requires an inbound type to be
// wired into cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame switch, and this ticket ships no handler — so a verb declared
// here would be red by construction, and filing it under excludedTypes to dodge
// that would be a lie to the guard. It shipped as a PUSH rather than a reply:
// #1849 emits it from interactiveTurnEmitterV2.Handle on the interactive turn
// lane, so this constant's excludedTypes classification stays push. A later
// ticket picking request/reply would still have to declare the verb together with
// its handler; a client's decode path is the same frame whatever it picks, which
// is what declaring the shape ahead of the producer bought.
//
// The declaring ticket (#1704) was wire vocabulary only; #1848 added
// internal/turnbridge's MapEvent arm for turnevent.ModelList and #1849 added
// cmd/pyry's emitting Handle case, so this frame now reaches an interactive v2
// mobile client, and #1705 added the encoding fixtures and the
// docs/protocol-mobile.md § model_list section. Same declare-then-emit sequencing
// as #1405→#1410 and #1616→#1638.
const (
	TypeModelList = "model_list" // binary → phone, outbound v2 model-list report
)

// Mobile Protocol v2 slash-command-list report. The set of slash commands the
// running child accepts for this conversation, sourced from the same initialize
// control reply the block above uses — that reply carries a commands array
// alongside its models array, so one round trip answers both. The defect it
// closes: the desktop's Actions menu offers reset, compact and knowledge
// capture, and knowledge capture is workspace-specific — it exists in the
// operator's vault and in almost no repository — so a menu that always offers it
// is wrong in most repositories, and sending it there produces an "Unknown
// command" reply in the thread. Two consumers wait on the frame
// (pyrycode-desktop#681, the Actions-menu grey-out; pyrycode-desktop#694, a
// slash-command type-ahead in the message box).
//
// Grouped alone rather than with any block above: it is not a turn sub-state
// with two edges, not turn-independent work, not a periodic reading, not a
// condition report about a window, and not an identity report about one turn. It
// is a capability inventory of VERBS — what the operator may ask the session to
// do — where model_list is a capability inventory of identities and
// model_announced reports the one identity in force. It is not merged into the
// TypeModelList block despite sharing the initialize round trip: sharing a
// source is not sharing a subject, the two naming paragraphs have to say
// different things (two claude words there, four here), and every block in this
// run groups alone.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give:
// the wire type names what the frame IS to a client, so a claude rename lands in
// one place instead of breaking every client at once.
//
// claude has FOUR words on this path where model_list had two: initialize (the
// control_request subtype), commands (the array key in the control reply, 51
// entries in the committed capture initialize_control_v2.1.239.json),
// slash_commands (a key on the system/init stdout line carrying the identical 51
// names as bare strings) and terminal_slash_commands (a different array on that
// same line, 2 entries: doctor and color). The names-only twin is not the source
// because it carries none of the 11 alias strings the control reply publishes
// across 9 of its 51 entries — a daemon forwarding it would ship the grey-out
// consumer a list in which reset does not appear, and reset is the desktop
// Actions menu's own entry (an alias of clear, not a command name).
//
// The discriminating checks are therefore the PLURALS, and the sibling blocks'
// subject-noun trap cuts three words wide here rather than one: command,
// slash_command and slash are each a substring of the correct name, so a
// strings.Contains check on any of the three would be RED against it — the same
// trap TypeModelAnnounced's block records for model and TypeModelList's for
// models. slash_command_list contains none of claude's four words and no init,
// which is what makes the negative pins satisfiable at all. One plural check
// covers all three: commands is a substring of slash_commands, which is a
// substring of terminal_slash_commands, so a name derived from either longer key
// necessarily contains the shorter one (see
// TestSlashCommandListType_IsNotClaudesVocabulary).
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this
// is an outbound binary → phone report an old phone never receives, and a leak
// into that set would let a phone send a slash_command_list frame into
// dispatch.Route. Two drift detectors classify it, both mandatory from the
// moment the constant exists rather than from the moment something emits it: the
// partition in internal/protocol/compat_test.go splits Type* constants between
// inboundAppTypeSet and v2OnlyTypes (this lives in the latter), and
// cmd/pyry/relay_guard_test.go's excludedTypes records it as a push.
//
// No inbound request verb is declared here, and that is not an omission.
// TestEveryInboundV2TypeHasHandler's Assertion #1 requires an inbound type to be
// wired into cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame switch, and this ticket ships no handler — so a verb declared
// here would be red by construction, and filing it under excludedTypes to dodge
// that would be a lie to the guard. If #1720 picks request/reply it declares the
// verb together with its handler and moves this constant from push to reply; a
// client's decode path is the same frame either way, which is what declaring the
// type now exists to freeze.
//
// The declaring ticket (#1726) is wire vocabulary only: #1727 declares the
// payload and its entry type, #1720 produces and emits the frame, and #1718 adds
// the encoding fixtures and the docs/protocol-mobile.md § slash_command_list
// section. Same declare-then-emit sequencing as #1405→#1410, #1616→#1638 and
// #1704→#1848.
const (
	TypeSlashCommandList = "slash_command_list" // binary → phone, outbound v2 slash-command-list report
)

// Mobile Protocol v2 session-facts report. What claude's own build IS and what
// posture claude says the child is running under, taken from the SAME system/init
// line TypeModelAnnounced's value comes from. internal/streamsup has translated
// those two keys into turnevent.SessionFacts since #2252, and turnbridge.MapEvent has
// mapped the variant onto this type since #2254, so a client sees both facts. The
// defect it closes is the one TypeModelAnnounced's block states, applied to two more
// facts of the same shape: the daemon knows what it ASKED FOR and only claude knows
// what it GOT, so an unexpected posture or an unexpected build was being discarded
// with the rest of the line. The waiting consumer is pyrycode-desktop#1241.
//
// Grouped alone rather than with any block above: it is not a turn sub-state with
// two edges, not turn-independent work, not a periodic reading, not a condition
// report about a window, and not a capability inventory. It is an IDENTITY report
// like TypeModelAnnounced, and it is a separate block from that one because the two
// report identities of DIFFERENT SUBJECTS — model_announced reports what claude will
// run AS for one turn, this reports what the child RUN itself is and how it is
// postured. A client renders the model on every turn; a version and a posture are
// what an operator checks when something looks wrong.
//
// THE NAME CARRIES A TENSION WORTH MEETING HEAD-ON rather than leaving a reader to
// notice. This frame declares no session identity at all: claude's session_id is
// claude's session and NOT the daemon's conversation, and it is not even declared on
// the producer's decode target. "Session" here names the CHILD RUN the two facts
// describe, not an identifier the frame carries.
//
// The NAME is otherwise the daemon's, not claude's, for the reason the blocks above
// give: the wire follows internal/turnevent's VARIANT (turnevent.SessionFacts), so a
// claude rename lands in one place instead of breaking every client at once.
//
// THE DISCRIMINATING WORDS INVERT THE OBVIOUS CHOICE, and the trap the sibling
// blocks record cuts closer here than on any of them. `session` is a substring of
// this very name, so a strings.Contains check on it is RED against the correct name
// — TypeModelAnnounced's block records the same trap for the singular `model` and
// TypeSlashCommandList's records it three words wide for command, slash_command and
// slash. `session` is also claude's own word, via the session_id key on the same
// init line, so the collision is not merely a coincidence of spelling. The checkable
// words are claude's SUBTYPE (init) and claude's two KEYS for the values
// (claude_code_version and permissionMode); this name contains none of the three,
// which is what makes the negative pins satisfiable at all. Separating it from the
// three shipped session_-prefixed constants — TypeSessionTransition,
// TypeSessionSettings and TypeSessionError — has to be an EQUALITY rather than a
// containment for that same reason, the shape TypeQuestionDismissed's pin needed
// against TypeModalDismissed (see TestSessionFactsType_IsNotClaudesVocabulary).
//
// NO effort FIELD ON THE PAYLOAD, and the absence is MEASURED rather than forgotten.
// #2251 captured the init line under the production spawn shape with an effort
// actually set, on the launch argv and acknowledged in band, and no init line
// carries an effort key. That measurement is machine-enforced by effortInitPins in
// internal/streamsup. Declaring the field anyway would put a permanently empty field
// on a shipped wire type, which is far harder to withdraw than to add later; should
// a later claude start publishing one, that pin reddens and that is the ticket to
// write then.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is
// an outbound binary → phone report an old phone never receives, and a leak into
// that set would let a phone send a session_facts frame into dispatch.Route. Two
// drift detectors classify it, both mandatory from the moment the constant exists
// rather than from the moment something emits it: the partition in
// internal/protocol/compat_test.go splits Type* constants between inboundAppTypeSet
// and v2OnlyTypes (this lives in the latter), and cmd/pyry/relay_guard_test.go's
// excludedTypes records it as a push. Only the second of the two actually reddens on
// a BRAND-NEW constant — compat_test.go's lists are hand-maintained, so a constant
// left off both of them moves its size assertion nowhere, while the relay guard
// walks codes.go's AST.
//
// No inbound request verb is declared here, and that is not an omission.
// TestEveryInboundV2TypeHasHandler's Assertion #1 requires an inbound type to be
// wired into cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame switch, and this ticket ships no handler — so a verb declared
// here would be red by construction, and filing it under excludedTypes to dodge that
// would be a lie to the guard.
//
// The declaring ticket (#2253) was wire vocabulary and documentation only; #2252 added
// cmd/pyry's emitting Handle case and #2254 internal/turnbridge's MapEvent arm for
// turnevent.SessionFacts, so this frame now reaches an interactive v2 mobile client.
// Same declare-then-emit sequencing as #1405→#1410, #1616→#1638, #1704→#1848 and
// #1726→#2003, with the two halves landing in the opposite order: the Handle case came
// first here and pushed nothing until the mapping arrived beneath it.
const (
	TypeSessionFacts = "session_facts" // binary → phone, outbound v2 session-facts report
)

// Mobile Protocol v2 screen-snapshot types. The always-available,
// parser-independent screen snapshot is the floor of ADR 025's
// safe-degradation strategy (docs/protocol-mobile.md § Screen snapshot): the
// phone asks for a one-shot text picture of the current screen, the binary
// renders it and pushes it back. The pair groups here so a reader greps
// "snapshot" and finds both adjacent with their rationale.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go. Like
// TypeRekeyRequest, TypeRequestSnapshot is an inbound v2 control envelope the
// v2 session manager intercepts before dispatch.Route; a leak into inboundAppTypeSet
// would route it to the handler chain. TypeScreenSnapshot is an outbound
// binary → phone event an old phone must never receive. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
const (
	TypeRequestSnapshot = "request_snapshot" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeScreenSnapshot  = "screen_snapshot"  // binary → phone, outbound v2 event (plain text only)
)

// Mobile Protocol v2 mid-turn-reconnect resync marker. When a reconnecting
// phone advertises a hello.last_event_id that has aged out of the bounded
// per-conversation event ring, the daemon emits this marker (instead of a
// partial, gap-ful replay) to tell the phone to do a full reload of the named
// conversation (#647; ADR 025 § Backpressure / replay). It carries only a
// conversation_id in an inline anonymous payload — no named payload struct,
// mirroring TypeRekeyRequest's payload-less control precedent.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: it is an
// outbound binary → phone v2 event an old phone must never receive. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
const (
	TypeResync = "resync" // binary → phone, outbound v2 mid-turn-reconnect resync marker
)

// Mobile Protocol v2 session-boundary marker. When the daemon's session
// rotates (a /clear, an idle eviction, or a workspace change), it emits this
// outbound binary → phone event so the phone can construct a
// ThreadItem.SessionBoundary marker (pyrycode-mobile#336) instead of inferring
// boundaries from message fields that do not exist. The multi-field payload
// lives in SessionTransitionPayload (messaging.go): previous/new session id,
// the transition reason, when it occurred, and the workspace cwd.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: it is an
// outbound binary → phone v2 event an old phone must never receive. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
//
// This ticket (#656) is wire vocabulary only — the producer that emits the
// marker on session transitions is sibling #657.
const (
	TypeSessionTransition = "session_transition" // binary → phone, outbound v2 session-boundary marker
)

// Mobile Protocol v2 modal vocabulary (epic #597 Phase 3,
// docs/protocol-mobile.md § Modal). When the supervised claude surfaces a modal
// (a permission prompt, a plan-approval, a tool-confirmation), the daemon
// describes it to the phone, the phone answers, and the daemon drives that
// answer back into claude. modal_shown rides the existing "interactive"
// capability (#607) negotiated in hello/hello_ack — viewing a modal is ungated;
// answering is gated separately, per-device, default OFF, in the security model
// (#702), which is NOT a wire capability.
//
// Two natures in one cluster. modal_shown / modal_dismissed are outbound
// binary → phone events an old phone must never receive. modal_answer /
// modal_cancel are inbound phone → binary *control* envelopes the v2 session
// manager intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeRekeyRequest / TypeRequestSnapshot); there
// is NO dispatch.Route handler for them.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// either route an inbound control envelope to the handler chain or offer an
// outbound modal event to an old phone, violating the v1/v2 boundary. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these four live in the latter.
//
// This ticket (#701) is wire vocabulary only — the producer that mints modal_id
// nonces, dedups answers by answer_token, validates inbound answers, and gates
// the fan-out is sibling #703 (with #706 building two-heads ownership and #702
// the per-device answer gate).
const (
	TypeModalShown     = "modal_shown"     // binary → phone, outbound v2 modal-surfaced event
	TypeModalAnswer    = "modal_answer"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalCancel    = "modal_cancel"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalDismissed = "modal_dismissed" // binary → phone, outbound v2 modal-resolution event
)

// Mobile Protocol v2 queued-backlog vocabulary (epic #597 Phase 3,
// docs/protocol-mobile.md § Queue). A phone that types while claude is busy has
// its turn buffered in internal/msgqueue; queue_state (daemon → phone) is the
// wire form of msgqueue.Snapshot(convID) so the phone can see the backlog, and
// dequeue_message (phone → daemon) drives msgqueue.Remove(convID, id) so the
// phone can cancel an entry it no longer wants.
//
// Two natures in one cluster. queue_state is an outbound binary → phone event
// an old phone must never receive. dequeue_message is an inbound phone → binary
// *control* envelope the v2 session manager intercepts at
// internal/relay/v2session.go's dispatchAppFrame before internal/dispatch.Route
// (like TypeModalAnswer / TypeRequestSnapshot); there is NO dispatch.Route
// handler for it.
//
// Trust contrast with the modal cluster: unlike modal_answer, dequeuing is
// ungated for any paired phone (ADR 025 § Security model) — viewing and
// dequeuing are an ordinary capability, with no nonce and no per-device gate.
// queued_msg_id is a plain per-conversation counter from internal/msgqueue, not
// a security primitive.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// either route the inbound control envelope to the handler chain or offer the
// outbound queue_state event to an old phone, violating the v1/v2 boundary. The
// drift detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
//
// This ticket (#720) is wire vocabulary only — the producer that emits
// queue_state is sibling #722 and the handler that applies dequeue_message is
// sibling #723.
const (
	TypeQueueState     = "queue_state"     // binary → phone, outbound v2 queued-backlog snapshot
	TypeDequeueMessage = "dequeue_message" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 interrupt control (epic #597 Phase 3,
// docs/protocol-mobile.md § Interrupt). A paired phone sends interrupt to stop
// the running turn — the remote equivalent of pressing Esc at the local
// terminal. The daemon maps it to the neutral turnevent.Cancel command and
// routes it to the supervised claude as a single Esc keystroke (claude's own
// interrupt). #600 maps ACP session/cancel onto the same neutral shape.
//
// It is an inbound phone → binary *control* envelope the v2 session manager
// intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeModalCancel / TypeDequeueMessage); there is
// NO dispatch.Route handler for it. Since #2103 it carries an OPTIONAL
// InterruptPayload naming the conversation whose turn to stop — still no
// modal_id nonce, no answer_token and no idempotency key, and a replayed
// interrupt simply stops the turn again (a no-op when none is running), so no
// dedup is needed. An absent payload, an absent conversation_id, an empty one,
// and a body that does not decode at all are ONE meaning: the conversation the
// daemon's own cursor points at, the pre-#2103 behaviour an un-upgraded client
// still gets.
//
// Trust posture: interrupt is gated on the negotiated `interactive` capability
// (a non-interactive conn's interrupt is inert, whether it names a conversation
// or not) and is exempt from the per-device permission gate (#702) —
// interrupting one's own paired session is a normal paired-phone action
// (ADR 025 § Security model), not a privileged tool-permission decision. Naming
// a conversation is a validated lookup key and not a widening: a paired device
// could already reach any conversation by routing a send_message to move the
// shared cursor and then sending the bare frame.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// route this inbound control envelope to the handler chain. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
const (
	TypeInterrupt = "interrupt" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 debug-bundle streaming vocabulary (#812, split from #803;
// docs/protocol-mobile.md § Debug bundle). A content-bearing debug bundle
// (assembled by #811) routinely exceeds one AEAD frame, so the daemon streams it
// to a paired phone as ordered, cap-respecting chunks ending in a completion
// marker. debug_bundle_chunk carries one base64 slice of the bundle with a
// 0-based contiguous seq; debug_bundle_done carries the exact chunk count so the
// phone detects a truncated stream. Both ride the manager's asynchronous push
// path (StreamBundle → Push → drainOnce), never the synchronous handler-reply
// channel.
//
// Both are outbound binary → phone v2 events an old phone must never receive.
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// offer the outbound event to an old phone, violating the v1/v2 boundary. The
// drift detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
//
// This ticket (#812) delivers the streaming primitive unwired; the request verb
// that drives it for the debug bundle is sibling #813.
const (
	TypeDebugBundleChunk = "debug_bundle_chunk" // binary → phone, outbound v2 bundle chunk
	TypeDebugBundleDone  = "debug_bundle_done"  // binary → phone, outbound v2 bundle completion marker
)

// Mobile Protocol v2 debug-bundle request verb (#813, split from #803;
// docs/protocol-mobile.md § Debug bundle). A paired phone sends
// request_debug_bundle to ask for the current session's debug bundle — the
// recent daemon log ring plus the newest terminal recording when debug capture
// was on. The bundle is daemon-global by construction (the log ring has no
// per-session key, per #811), so this is a BARE control frame — no payload, no
// conversation_id, no field an attacker could use to select another session's
// data — mirroring TypeInterrupt.
//
// It is an inbound phone → binary *control* envelope the v2 session manager
// intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeInterrupt / TypeDequeueMessage); there is NO
// dispatch.Route handler for it. The daemon replies by STREAMING the assembled
// bundle back as debug_bundle_chunk* + debug_bundle_done (#812), never through
// the synchronous 8-frame handler-reply path.
//
// Authorization is pairing, enforced structurally at the Noise IK handshake: an
// unpaired device is refused with 4401 and never reaches dispatchAppFrame, so no
// new authorization gate lives on this verb.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// route this inbound control envelope to the handler chain. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
const (
	TypeRequestDebugBundle = "request_debug_bundle" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 start-new-session control (#831, split from #824;
// docs/protocol-mobile.md § New session). A paired phone sends new_session to
// start a fresh session in one conversation. On the stream path that is a kill
// and respawn under a freshly minted session id, NOT a `/clear` keystroke — the
// terminal-era framing this block used to carry described a supervisor that no
// longer runs the interactive path. The client observes the break through the
// EXISTING session_transition marker (reason: "clear", #656/#657) — there is NO
// synchronous ack (fire-and-forget, like interrupt). Unlike interrupt
// (→turnevent.Cancel) it maps to no neutral turnevent command; it drives the
// SessionStarter seam directly.
//
// It is an inbound phone → binary *control* envelope the v2 session manager
// intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeInterrupt / TypeRequestDebugBundle); there
// is NO dispatch.Route handler for it. It carries an OPTIONAL NewSessionPayload
// naming the conversation to restart (#2099) — one field, no modal_id nonce, no
// answer_token, no idempotency key. The payload is optional in full: the frame
// was bare until #2099, and an absent payload, an absent conversation_id and an
// undecodable body all mean "the conversation the daemon's cursor points at",
// which is what keeps an un-upgraded client working.
//
// Trust posture: new_session is gated on the negotiated `interactive` capability
// (a non-interactive conn's new_session is inert) and is exempt from the
// per-device permission gate (#702) — starting a fresh session in one's own
// paired session is a normal paired-phone action (ADR 025 § Security model), not
// a privileged tool-permission decision.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// route this inbound control envelope to the handler chain. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
const (
	TypeNewSession = "new_session" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

// Mobile Protocol v2 set-session-settings vocabulary (#844, split from #841;
// docs/protocol-mobile.md § Session settings). A paired client sends
// set_session_settings to change one session's per-session model / reasoning
// effort / YOLO (bypass-permissions); the daemon confirms with
// session_settings_updated. The request payload (SetSessionSettingsPayload,
// settings.go) uses per-field pointers so an omitted setting decodes distinctly
// from one explicitly set to its zero value — nil means "leave unchanged".
//
// This ticket (#844) is wire vocabulary ONLY — these two Type* constants, the
// two payload structs, and CodeSessionNotFound. The handler that intercepts the
// request, gates on the interactive capability, validates, persists via
// sessions.Pool.UpdateSettings (#840), and emits the reply is sibling #845.
// Splitting vocabulary from handler follows the established v2 precedent
// (#701→#703, #720→#723, #812→#813, #656→#657).
//
// Two natures in one cluster. set_session_settings is an inbound phone → binary
// *control* envelope the v2 session manager intercepts at
// internal/relay/v2session.go's dispatchAppFrame before internal/dispatch.Route
// (like TypeModalAnswer / TypeNewSession); there is NO dispatch.Route handler
// for it. session_settings_updated is an outbound binary → phone reply an old
// phone must never receive.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// either route the inbound control envelope to the handler chain or offer the
// outbound reply to an old phone, violating the v1/v2 boundary. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
const (
	TypeSetSessionSettings     = "set_session_settings"     // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeSessionSettingsUpdated = "session_settings_updated" // binary → phone, outbound v2 reply confirming the change
)

// Mobile Protocol v2 read-session-settings vocabulary (#491/#1214;
// docs/protocol-mobile.md § Session settings). The READ half of the #844
// cluster above, which shipped write-only: set_session_settings changes the
// values and session_settings_updated only echoes the id back, so a client had
// no way to ASK what the current values are, nor to learn the session id it
// must address a change to.
//
// Until now a client got both by reading screen_snapshot, which carries them as
// a side-load (#848, #857). That coupling is the bug: screen_snapshot is a
// photograph of the terminal, and on the stream-json runner there is no terminal
// to photograph, so handleRequestSnapshot answers CodeServerBinaryOffline
// (#1101) and the settings — which have nothing to do with a terminal — are
// refused with it. This pair carries them on their own route, gated on their own
// seams, so it answers on BOTH runners. screen_snapshot is deliberately left
// untouched: its side-loaded copies stay for the shipped mobile client.
//
// The request frame carries a RequestSessionSettingsPayload naming the
// conversation the client is asking about (#1586). It WAS bare until then —
// like TypeRequestDebugBundle, which stays bare because its bundle is
// daemon-global and has no per-session key to name — but this reply is what
// hands a client the session_id every subsequent set_session_settings must
// address, so a client needed a way to say which conversation it meant.
// conversation_id is untrusted network input used for exactly one thing: an
// in-memory resolution through handleRequestSessionSettings' conversation-keyed
// run-configuration seam. It reaches no log line, no error string, no filesystem
// path, and not the reply.
//
// Since #1610 it SELECTS which session the reply describes, and the reported id
// and the reported values move in one step because they are resolved as one
// value. Every unresolvable case — a conversation the daemon does not host, one
// bound to no live session, and an absent or empty id — is answered with a
// zero-valued session_settings rather than an error, and never with the shared
// bootstrap session's id or values.
//
// Two natures in one cluster, mirroring #844. request_session_settings is an
// inbound phone → binary *control* envelope the v2 session manager intercepts at
// internal/relay/v2session.go's dispatchAppFrame before internal/dispatch.Route
// (like TypeSetSessionSettings / TypeRequestSnapshot); there is NO dispatch.Route
// handler for it. session_settings is an outbound binary → phone reply an old
// phone must never receive.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: a leak would
// either route the inbound control envelope to the handler chain or offer the
// outbound reply to an old phone, violating the v1/v2 boundary. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; these two live in the latter.
const (
	TypeRequestSessionSettings = "request_session_settings" // phone → binary, inbound v2 control carrying RequestSessionSettingsPayload (intercepted pre-dispatch.Route)
	TypeSessionSettings        = "session_settings"         // binary → phone, outbound v2 reply carrying the current run configuration
)

// Mobile Protocol v2 session-error marker (#1007, split from #1001;
// docs/protocol-mobile.md § Error codes). When the daemon's interactive
// message queue (internal/msgqueue) bounds a persistent-failure drain and
// gives up (the OnGiveUp seam, #1000), it can surface that terminal give-up to
// a paired phone as this unsolicited, conversation-scoped frame — a typed error
// the client attaches to the right session and never mistakes for a transient
// retry. Unlike TypeError it is NOT in_reply_to-correlated to a client request
// (there is no request to reply to), so the payload (SessionErrorPayload,
// messaging.go) carries the conversation identity itself, plus the terminal
// CodeSessionBlocked and a human-readable message.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: it is an
// outbound binary → phone v2 event an old phone must never receive. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
//
// This ticket (#1007) is wire vocabulary only — the producer that emits the
// frame on msgqueue give-up is sibling #1008.
const (
	TypeSessionError = "session_error" // binary → phone, outbound v2 unsolicited terminal session-error frame
)

// Mobile Protocol v2 attachment chunk (#1752, split from #1750; the
// docs/protocol-mobile.md § Attachments section is #1751). One slice of one
// attachment's bytes, carrying the whole transfer's metadata on every chunk
// (AttachmentChunkPayload, attachments.go). A file crossing the encrypted mobile
// channel routinely exceeds one AEAD frame, so the client splits it and the
// receiver reassembles; the relay stays transport-only, with no blob endpoint —
// that option was considered and rejected upstream.
//
// BOTH DIRECTIONS RIDE THIS ONE FRAME, which is why the trailing comment below
// carries the file's only bidirectional arrow rather than a typo. Upload
// (client → daemon) and retrieval (daemon → client) are built months apart, and
// declaring exactly one type is what makes them impossible to drift apart: there
// is no second shape to update. The trust asymmetry that creates — inbound every
// field is a client claim, outbound the same fields are daemon-authored — cannot
// be expressed in a struct, so it lives in AttachmentChunkPayload's SECURITY
// block.
//
// There is NO completion frame, and that is not an omission. TotalChunks rides
// every chunk, so a receiver learns the expected count from the FIRST frame it
// sees and detects a truncated stream earlier than debug_bundle_done detects one
// for the bundle stream — whose chunks carry only seq and therefore need a
// terminal frame to learn the count at all. A terminal *error* (a retrieval the
// daemon abandons mid-stream) is TypeError correlated via in_reply_to, which is
// #1751's reject vocabulary, not a second attachment frame.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an
// old (v1) phone must never receive this frame, and rejection is also what keeps
// the type off the v1 inbound path — the upload leg really is inbound, so
// IsKnownAppType refusing it is the structural bar against a v1 client sending
// one into dispatch.Route. The drift detector in
// internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this constant lives in the latter.
//
// cmd/pyry/relay_guard_test.go's excludedTypes records it too, and NOT as a
// "push": the reason its seven newest neighbours give — this slice declares no
// inbound request verb — is false for a frame whose upload leg is inbound, so
// copying one would put a lie in the guard. Filing it in inboundTypes instead
// would fail Assertion #1, which requires an inbound type to be wired into
// cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame switch, and this slice ships no dispatch. So the entry is
// excluded under its own label with the reason that is actually true — the
// inbound leg has no handler YET. #1744 adds the dispatchAppFrame case, at which
// point the entry moves to inboundTypes as "switch-intercepted". TypeHello, a
// borderline phone → binary type deliberately not filed inbound, is the
// precedent followed there rather than the pushes.
//
// The declaring ticket (#1752) is wire vocabulary only: #1753 adds the per-chunk
// size cap and the both-direction encoding fixtures, #1751 publishes the
// client-facing contract and the attachment.* reject codes, #1741 reassembles
// and checks the claims, #1743 stores, and #1897 dispatches the inbound leg.
// Retrieval landed as three slices rather than the one #1746 this block first
// named: #2052 declares the request verb (TypeRequestAttachment below), #2053
// builds the outbound stream, and #2054 answers the verb with one or the other.
// Same declare-then-emit sequencing as #1616→#1638 and #1704→#1848.
const (
	TypeAttachmentChunk = "attachment_chunk" // phone ↔ binary, one chunk of an attachment's bytes (both directions)
)

// Mobile Protocol v2 attachment upload success reply (#1895, split from #1744;
// docs/protocol-mobile.md § Attachments publishes it). The one POSITIVE terminal
// signal the upload leg had: the transfer completed, its claims were checked, and
// the bytes are on the host addressable by the id the client chose. Its payload
// is AttachmentStoredPayload (attachments.go). Before this constant a client that
// uploaded was told what went wrong on every failure path and got silence on the
// one that worked, and that section said so — "No success frame is declared here,
// and a client must not invent one."
//
// THE NAME is the positive of CodeAttachmentStorageFailed, so the family's
// terminal outcomes read as one set rather than a positive invented in a
// different idiom from its negatives. It is not read as narrow: six other
// attachment.* codes also terminate an upload, and this is the single positive
// terminal for the WHOLE transfer, not a report that only the storage step
// succeeded. "attachment_uploaded" was rejected because the wire type names what
// the frame IS to a client — TypeQuestionShown's block records that rule — and
// what this frame is, is the daemon's assertion, not the client's action.
// "attachment_ack" was rejected outright: TypeAck already exists and AckPayload
// is a struct{}, so the name would promise the existing empty frame while
// carrying a payload. That emptiness is also why the generic ack cannot serve
// here — it can name no attachment.
//
// IT IS A REPLY, correlated via the envelope's InReplyTo, and that is ONE
// decision expressed in three places that must agree: this block, the
// docs/protocol-mobile.md § Application message types row, and
// cmd/pyry/relay_guard_test.go's excludedTypes entry, which defines "reply" as
// exactly this. TypeSessionSettingsUpdated is the nearest analogue on both counts
// — an outbound v2 confirmation of an inbound control frame, filed "reply",
// published as correlated by in_reply_to, and carrying only the id it confirms
// because the client already knows what it sent. The reject half of this same leg
// already went that way: CodeAttachmentStreamAborted is a TypeError correlated
// via in_reply_to, and a success correlating differently from the failure it is
// the alternative to would split one leg across two mechanisms.
//
// WHAT InReplyTo POINTS AT is the envelope of the chunk WHOSE ARRIVAL COMPLETED
// THE TRANSFER — not the one with the highest Index. Chunks may arrive in any
// order and the transfer is complete when every index in [0, TotalChunks) has
// arrived exactly once, so the completing chunk is whichever one closed the set,
// and a client cannot predict which of its envelope ids that will be. That is why
// the payload ALSO carries the attachment id: the envelope field says which frame
// this answers, the payload says which transfer it concludes, and only the second
// is a value the client chose and can look up. The two are not redundant.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an old
// (v1) phone never receives this frame, and IsKnownAppType rejecting it is the
// structural bar against a v1 client sending one into dispatch.Route. Filing it
// in inboundTypes is impossible anyway — Assertion #1 requires an inbound type to
// be wired into cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame, and this slice ships no dispatch. Two drift detectors classify
// it, both mandatory from the moment the constant exists rather than from the
// moment something emits it: the partition in internal/protocol/compat_test.go
// (this lives in v2OnlyTypes), and excludedTypes as a reply.
//
// The declaring ticket (#1895) is wire vocabulary and publication only — no
// producer, no consumer, no validator. #1897 dispatches the inbound leg and emits
// this frame, and #1898 observes it end to end. Same declare-then-emit sequencing
// as #1752→#1753 and #1616→#1638.
const (
	TypeAttachmentStored = "attachment_stored" // binary → phone, the upload leg's success reply, correlated via in_reply_to
)

// Mobile Protocol v2 attachment RETRIEVAL REQUEST verb (#2052, split from #1746;
// docs/protocol-mobile.md § Attachments publishes it). The frame a client sends to
// ask the daemon for a stored attachment — the one thing that section had
// deliberately left unpublished, in its own words: "the retrieval request verb ...
// (no such type exists in the daemon today)". Its payload is
// RequestAttachmentPayload (attachments.go). It is the FIRST inbound type in the
// attachment family that is inbound and nothing else — TypeAttachmentChunk's
// upload leg is inbound, but that frame rides both directions.
//
// THE NAME follows the three inbound "ask the daemon for X" verbs already on this
// wire — TypeRequestSnapshot, TypeRequestDebugBundle, TypeRequestSessionSettings —
// rather than inventing a fourth idiom for the same act. It is fixed here rather
// than left to the handler because a wire type string IS the contract: the desktop
// client that consumes it writes its sender against the published name, and a name
// chosen twice is a name chosen wrong once.
//
// IT NAMES A CONVERSATION AND AN ATTACHMENT, AND NOTHING ELSE. It was the one place
// this family let a client name a scope until #2142 gave AttachmentChunkPayload a
// conversation_id as well, and the argument that had kept it the only one — that an
// upload could only steer bytes elsewhere by naming a conversation — is reversed
// there rather than qualified here. Publishing that field changed no trust level,
// for the reason the rest of this paragraph gives, and it left the two ids doing
// different work: this one selects which conversation's file to read, the chunk's
// decides which conversation an upload is filed under (#2143). The safety was never
// in the id's shape or its randomness in
// either case — it is CONFINEMENT: the id is
// a lookup key validated against the daemon's own registry before it reaches a path
// join, never a value trusted as sent, and naming a conversation is not
// authorization. RequestAttachmentPayload's block carries that rule in full,
// because the block is what the handler's author reads.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO REQUEST-ID
// KEY — TypeAttachmentStored's block has that decision and the reasoning transfers
// unchanged, since both terminals of this leg already correlate that way: the
// answering chunks and the CodeAttachmentStreamAborted TypeError alike. The
// committed testdata/attachment_chunk_retrieval.json settled it in bytes before
// this constant existed, riding in_reply_to 91, and testdata/request_attachment.json
// is the envelope 91 it answers. Inventing a request-id key here would leave that
// landed fixture describing a different scheme.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is a
// v2 CONTROL envelope intercepted before internal/dispatch.Route, exactly as
// TypeRequestSessionSettings and TypeModalAnswer are. IsKnownAppType rejecting it
// with ErrUnknownType is also the structural bar against a v1 client pushing one
// into the v1 handler chain. The drift detector in internal/protocol/compat_test.go
// partitions Type* constants between inboundAppTypeSet and v2OnlyTypes; this one
// lives in the latter, and inboundAppTypeSet's asserted count does not move.
//
// THE GUARD FILES IT WITH ITS INBOUND NEIGHBOURS, and getting that wrong is a red
// build rather than a style question. cmd/pyry/relay_guard_test.go's inboundTypes
// carries it as "switch-intercepted", beside TypeAttachmentChunk, because
// internal/relay/v2session.go's dispatchAppFrame now has a case for it (#2054).
// Between #2052 and #2054 it sat in excludedTypes as "pending handler (#2054)":
// inboundTypes would have failed Assertion #1, which requires an inbound type to be
// wired into cmd/pyry/relay.go's Handlers map or dispatchAppFrame, and that slice
// shipped no dispatch; "push", the reason the outbound neighbours give, is false for
// a genuinely inbound frame, and filing it that way to dodge Assertion #1 would have
// been a lie to the guard. TypeQuestionAnswer / TypeQuestionRefused sat under exactly
// that label between #1983 and #1984, and TypeAttachmentChunk before #1897; each had
// to move the moment its case landed, which Assertion #2 makes mandatory rather than
// tidy-up — a wired type left in excludedTypes fails. Filing is required from the
// moment the constant exists: Assertion #3 reports an unclassified constant, not an
// unemitted one.
//
// The declaring ticket (#2052) was wire vocabulary and publication only — no
// producer, no consumer, no validator, and NO ADMISSION-TIME CHECK. #2053 built the
// outbound chunk stream and #2054 answers this verb, owning the shape check, the
// registry validation and the CodeAttachmentNotFound reject path. Same
// declare-then-serve sequencing as #1752→#1897, #1895→#1897 and #1983→#1984.
const (
	TypeRequestAttachment = "request_attachment" // phone → binary, inbound v2 control (switch-intercepted — #2054)
)

// Mobile Protocol v2 ATTACHMENT ANNOUNCEMENT (#2082; docs/protocol-mobile.md
// § Attachments publishes it). The frame that tells a client a file exists on the
// host for a conversation. Its payload is AttachmentOfferedPayload
// (attachments.go).
//
// THE DEFECT IT CLOSES: a client could only ever name a file IT MINTED ITSELF.
// Upload lands and is answered (TypeAttachmentStored), a message declares which
// attachments it carries (SendMessagePayload.AttachmentIDs), and retrieval works
// end to end (TypeRequestAttachment) — but every one of those needs an
// attachment_id the client already holds, so nothing on the wire ever handed a
// client an id it did not mint. A file claude produced reached a client as
// nothing at all (pyrycode-desktop#1028).
//
// WIDENING TypeMessage WOULD NOT HAVE WORKED, and it is the obvious move. That
// frame is not pushed to an attached client at all: its one producer builds a
// MessagePayload for the durable conversation-history log (#2115), the live
// assistant reply on the v2 interactive lane is a stream of TypeAssistantDelta
// closed by TypeTurnEnd, and a client learns a stored message only by asking with
// TypeRequestHistory. So widening it would announce nothing at the moment a file
// appears, and would also change a record shape history-page decoders already
// read.
//
// THE NAME follows the past-participle form of TypeAttachmentStored rather than
// inventing an idiom, and is fixed here rather than left to the producer for
// TypeRequestAttachment's reason: a wire type string IS the contract, the desktop
// client writes its decoder against the published name, and a name chosen twice is
// a name chosen wrong once. "OFFERED" RATHER THAN "SENT" IS LOAD-BEARING — no
// bytes ride this frame. A client that wants them asks with TypeRequestAttachment
// and gets TypeAttachmentChunk back.
//
// IT IS A PUSH AND NOT A REPLY, which is ONE decision expressed in three places
// that must agree: this block, the docs/protocol-mobile.md § Application message
// types row, and cmd/pyry/relay_guard_test.go's excludedTypes entry. Nothing
// solicits it, so there is no request envelope for the envelope's InReplyTo to
// name — the whole difference from TypeAttachmentStored, which is outbound-only
// too but correlates to the chunk whose arrival completed the transfer.
// TypeModalShown is the nearest analogue on this count and on correlation: it has
// the same origin (an MCP tool call from claude, arriving over the control socket
// and broadcast to attached clients from the control-server handler goroutine) and
// its payload likewise carries a conversation id and no turn id.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: an old
// (v1) phone never receives this frame, and IsKnownAppType rejecting it with
// ErrUnknownType is also the structural bar against a v1 client SENDING one into
// dispatch.Route and asserting that a file exists on the host that does not.
// Filing it in inboundTypes is impossible anyway — Assertion #1 requires an
// inbound type to be wired into cmd/pyry/relay.go's Handlers map or
// internal/relay/v2session.go's dispatchAppFrame, and this slice ships no
// dispatch. Two drift detectors classify it, both mandatory from the moment the
// constant exists rather than from the moment something emits it: the partition in
// internal/protocol/compat_test.go (this lives in v2OnlyTypes), and excludedTypes
// as a push. Unlike TypeRequestAttachment's entry, which had to move to
// inboundTypes the moment #2054 landed its case, this frame has NO INBOUND LEG AT
// ALL, so its entry is permanent and never moves.
//
// The declaring ticket (#2082) is wire vocabulary and publication only — no
// producer, no consumer, no validator. #2083 emits it, and is also where the file
// itself comes from; whether it records the offer into the conversation-history
// log is that slice's call. Same declare-then-emit sequencing as #1752→#1897,
// #1895→#1897 and #2052→#2054.
const (
	TypeAttachmentOffered = "attachment_offered" // binary → phone, unsolicited push: a file exists on the host for this conversation
)

// Mobile Protocol v2 CONVERSATION-HISTORY REQUEST VERB (#2113, split from #2091;
// docs/protocol-mobile.md § Conversation history publishes it). The frame a client
// sends to ask for entries older than the ones it already has. Its payload is
// RequestHistoryPayload (history.go).
//
// THE DEFECT IT CLOSES: a client opening an existing conversation sees nothing
// that happened before it connected, because there was no verb to ask with.
// internal/relay's replayMissed is catch-up across a dropped connection over the
// bounded in-memory ring in internal/eventring — empty after a daemon restart —
// and § Reconnect / Backfill semantics said in as many words that a client wanting
// more "has no verb to ask for them and receives none". The log it reads landed in
// #2112 (internal/history); this constant puts that log's shapes on the wire.
//
// THE NAME follows the four inbound "ask the daemon for X" verbs already here —
// TypeRequestSnapshot, TypeRequestDebugBundle, TypeRequestSessionSettings,
// TypeRequestAttachment — rather than inventing a fifth idiom for the same act. It
// is fixed by this declaring ticket rather than by the handler because a wire type
// string IS the contract: pyrycode-desktop#1088 and pyrycode-mobile#623 are both
// parked on the published section, not on the handler, and a name chosen twice is
// a name chosen wrong once.
//
// PAGED, NEWEST-FIRST, WALKING BACKWARDS, ADDRESSED BY AN OPAQUE CURSOR rather
// than by an offset or a page number, both of which a concurrent append
// invalidates. RequestHistoryPayload's block carries the rules a handler's author
// needs: the cursor is not a secret and not a capability, Limit's zero means "the
// daemon chooses" and must never be allocated from, and the conversation id is a
// lookup key validated against the registry before any path join rather than a
// value trusted as sent.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is
// a v2 CONTROL envelope intercepted before internal/dispatch.Route, exactly as
// TypeRequestAttachment and TypeRequestSessionSettings are. IsKnownAppType
// rejecting it with ErrUnknownType is also the structural bar against a v1 client
// pushing one into the v1 handler chain, and that rejection's load-bearing half is
// the inbound one because this leg really is inbound. The partition in
// internal/protocol/compat_test.go files it in v2OnlyTypes, and
// inboundAppTypeSet's asserted count does not move.
//
// IT SITS IN excludedTypes AS "pending handler (#2116)" UNTIL THAT SIBLING LANDS,
// and moves to inboundTypes the moment internal/relay/v2session.go's
// dispatchAppFrame gains its case — mandatory rather than tidy-up, since Assertion
// #2 fails a wired type left behind. inboundTypes today would fail Assertion #1,
// which requires a dispatch that this ticket does not ship, and "push" would be a
// lie to the guard about a genuinely inbound frame. TypeRequestAttachment sat under
// exactly that label between #2052 and #2054, and TypeQuestionAnswer /
// TypeQuestionRefused between #1983 and #1984. Filing is required from the moment
// the constant exists: Assertion #3 reports an unclassified constant, not an
// unemitted one.
//
// The declaring ticket (#2113) is wire vocabulary and publication only — no
// producer, no consumer, no validator and no admission-time check. #2116 answers
// the verb, owning the shape check, the registry validation, the page's byte
// budgeting and every reject code; the reject CONDITIONS are published here, their
// codes are not, because minting a parallel reject vocabulary ahead of the handler
// is what #2052's precedent declines to do. Same declare-then-serve sequencing as
// #2052→#2054 and #1983→#1984.
const (
	TypeRequestHistory = "request_history" // phone → binary, inbound v2 control (pending handler — #2116)
)

// Mobile Protocol v2 CONVERSATION-HISTORY PAGE REPLY (#2113, split from #2091;
// docs/protocol-mobile.md § Conversation history publishes it). One backward step
// of a history walk: the entries, the cursor to ask again with, and whether the
// start of the log was reached. Its payload is HistoryPagePayload (history.go),
// mirroring history.Page.
//
// THE NAME names what the frame IS to a client — a page of history — which is
// TypeQuestionShown's rule. "history" alone was rejected as describing the whole
// log rather than the one step this frame carries, and a client that read it that
// way would have no reason to walk.
//
// IT IS A REPLY, correlated via the envelope's InReplyTo, and that is ONE decision
// expressed in three places that must agree: this block, the § Application message
// types row, and cmd/pyry/relay_guard_test.go's excludedTypes entry, which defines
// "reply" as exactly this. TypeSessionSettings is the nearest analogue on every
// count — an outbound v2 answer to an inbound request verb that named a
// conversation, filed "reply", published as correlated by in_reply_to, and
// carrying NO conversation id of its own because the client already knows what it
// asked. That last point is why the payload has no such key.
//
// ONE ENTRY IS ONE WIRE ENVELOPE'S WORTH: a wire type, its payload, a timestamp
// and a durable entry id. That is what makes a page renderable without a second
// mapping — a client re-reduces it oldest-first through the timeline reducer it
// already has for the live stream. The id is the log's DURABLE per-conversation
// one, deliberately not Envelope.EventID, whose ring ids are per-process and do
// not survive a restart.
//
// A WALK TERMINATES ON at_start, NEVER ON AN EMPTY entries list, and the entry
// clamp bounds a count rather than bytes — so a page may be short of what was
// asked for and shortness means nothing. HistoryPagePayload's block carries both
// rules with their reasoning, and all three page shapes are pinned by committed
// fixtures rather than left to inference.
//
// UNLIKE ITS REQUEST HALF THIS FRAME CARRIES CONTENT, and the trust consequence is
// stated on HistoryEntry rather than borrowed from a neighbour: each entry's type
// and payload are REPLAYED content, claude-authored for a stored assistant frame,
// so § Security model's threat 1 lands here. TypeRequestAttachment's family
// publishes the opposite for itself and that sentence must not be carried over.
//
// MUST NOT be added to inboundAppTypeSet: an old (v1) phone never receives this
// frame, and IsKnownAppType rejecting it is the structural bar against a v1 client
// sending one into dispatch.Route. Filing it in inboundTypes is impossible anyway
// — Assertion #1 requires a dispatch case and this frame has no inbound leg at
// all, so its excludedTypes entry never moves. Two drift detectors classify it,
// both mandatory from the moment the constant exists: v2OnlyTypes in
// internal/protocol/compat_test.go, and excludedTypes as a reply.
//
// The declaring ticket (#2113) is wire vocabulary and publication only. #2116
// emits it. Same declare-then-emit sequencing as #1895→#1897 and #1616→#1638.
const (
	TypeHistoryPage = "history_page" // binary → phone, one backward step of a history walk, correlated via in_reply_to
)

// Mobile Protocol v2 ON-DEMAND MODEL-LIST REQUEST VERB (#2125, split from #2084;
// docs/protocol-mobile.md § model_list publishes it). The frame a client sends to
// ask for a conversation's model menu at any time. Its payload is
// RequestModelListPayload (interactive.go).
//
// THE DEFECT IT CLOSES: TypeModelList reaches a client two ways and a conversation
// created AFTER the client connected gets neither. The live interactive turn lane
// drops every event whose producing session is not the active conversation's, and
// the connect-time reconcile runs only inside the handshake, so a conversation
// that did not exist then is not in it. Without a request verb there is no third
// option, and a client's model and effort menus stay blank on a fresh chat
// (pyrycode-desktop#1054). #2085 makes the case universal by deferring the spawn
// to the first message.
//
// THE NAME follows the five inbound "ask the daemon for X" verbs already here —
// TypeRequestSnapshot, TypeRequestDebugBundle, TypeRequestSessionSettings,
// TypeRequestAttachment, TypeRequestHistory — rather than inventing a sixth idiom
// for the same act. #2052's rule applies: a wire type string IS the contract, and a
// name chosen twice is a name chosen wrong once.
//
// IT MINTS NO SECOND OUTBOUND SHAPE. The answer is TypeModelList and
// ModelListPayload UNCHANGED, correlated by the envelope's InReplyTo and carrying
// NO EventID — so the reply never enters the #647 replay ring and advances no
// client cursor, exactly as the connect-time reconcile's frame does not. The
// payload is what the daemon-side resolver already returns, so "the same payload
// the reconcile would send" means the SAME SOURCE rather than the same fields
// copied; the turnbridge mapping is not forked. That is why TypeModelList stays in
// cmd/pyry/relay_guard_test.go's excludedTypes as an outbound push, exactly as
// TypeHistoryPage does for TypeRequestHistory.
//
// UNLIKE ITS NEAREST SHAPE PRECEDENT IT CAN REFUSE. TypeRequestSessionSettings
// answers every unresolvable case with an all-zero payload and never an error
// frame; this verb cannot borrow that, because turnevent.ModelList.Models is
// documented never-empty and an empty models array must never stand in for
// "unknown". The reject shape is TypeRequestHistory's — a TypeError envelope
// correlated by InReplyTo — with two codes: the existing CodeConversationNotFound
// and CodeModelListUnavailable (above), minted by this same ticket because it is
// the one that sends them.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is
// a v2 CONTROL envelope intercepted before internal/dispatch.Route, exactly as
// TypeRequestHistory and TypeRequestSessionSettings are. IsKnownAppType rejecting
// it with ErrUnknownType is also the structural bar against a v1 client pushing one
// into the v1 handler chain. The partition in internal/protocol/compat_test.go
// files it in v2OnlyTypes, and inboundAppTypeSet's asserted count does not move.
//
// IT NEVER SITS IN excludedTypes AS "pending handler". Unlike TypeRequestHistory
// (#2113→#2116) and TypeRequestAttachment (#2052→#2054), the declaration and the
// handler land in ONE ticket — the declaration's only daemon-side consumer is that
// handler, so cutting them apart would produce a slice consumed by exactly one
// sibling. It is therefore filed in inboundTypes as "switch-intercepted" from the
// moment it exists; filing it as pending would fail Assertion #2 the moment
// dispatchAppFrame's case exists in the same commit.
const (
	TypeRequestModelList = "request_model_list" // phone → binary, inbound v2 control (switch-intercepted — #2125)
)

// Mobile Protocol v2 CONVERSATION SYSTEM-PROMPT READ PAIR (#2152, split from
// #2094; docs/protocol-mobile.md § Reading a conversation's system prompt
// publishes both). The frame a client sends to ask what system prompt one
// conversation holds, and the frame it is answered with. Their payloads are
// RequestSystemPromptPayload and SystemPromptPayload (system_prompt.go).
//
// THE DEFECT IT CLOSES is the read half TypeSetSystemPrompt (#2151) shipped
// without. That verb's ack is the reused TypeConversationUpdated record, which
// deliberately does NOT carry the value — it is broadcast to every phone on the
// server-id, so carrying it there would widen the audience for a value only the
// requester asked about — so a client that did not itself perform the write has
// no way to learn what is stored. And because a stored prompt takes effect at the
// conversation's NEXT session start, a client showing the stored value alone
// tells an operator their edit is live when it is not.
//
// SO THE REPLY REPORTS A DIFFERENCE, NOT A SECOND COPY OF THE TEXT. The daemon
// knows what the live session was spawned with (#2150's session-keyed
// Pool.SystemPromptFor); the client's need is to be told the two disagree, so
// SystemPromptPayload carries a three-value verdict rather than echoing up to
// another 8192 bytes of operator text back over the wire. A client that wants to
// show a diff is a later ticket.
//
// A READ ROUTE, NOT A FIELD ON AN EXISTING REPLY, and both alternatives were
// rejected for a stated reason. It does not go on the TypeConversations record:
// a prompt is capped at conversations.MaxSystemPromptBytes (8192) and that reply
// carries EVERY row, so a handful of prompted conversations would blow the
// 65519-byte application-envelope cap. It does not go on SessionSettingsPayload
// either: that payload's contract is that every field sits at its zero when no
// session resolves, which is exactly when an operator most needs to see what is
// stored.
//
// THE NAME follows the six inbound "ask the daemon for X" verbs already here —
// TypeRequestSnapshot, TypeRequestDebugBundle, TypeRequestSessionSettings,
// TypeRequestAttachment, TypeRequestHistory, TypeRequestModelList — rather than
// inventing a seventh idiom for the same act. #2052's rule applies: a wire type
// string IS the contract, and a name chosen twice is a name chosen wrong once.
//
// UNLIKE ITS NEAREST FILE-SPREAD PRECEDENT IT MINTS NO ERROR CODE AND HAS NO
// ERROR FRAME. TypeRequestModelList needs two codes because turnevent.ModelList's
// Models is documented never-empty, so an empty menu cannot stand in for
// "unknown". Here every unresolvable case has a truthful constant answer — the
// reply whose system_prompt key is absent and whose verdict is "no_session" — so
// this verb takes TypeRequestSessionSettings' always-answer posture instead. A
// conversation this daemon does not host is answered with exactly that reply,
// which makes the verb useless as a membership oracle.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: these
// are v2 CONTROL envelopes intercepted before internal/dispatch.Route, exactly as
// TypeRequestSessionSettings and TypeRequestModelList are. IsKnownAppType
// rejecting them with ErrUnknownType is also the structural bar against a v1
// client pushing one into the v1 handler chain. The partition in
// internal/protocol/compat_test.go files both in v2OnlyTypes, and
// inboundAppTypeSet's asserted count does not move.
//
// NEITHER EVER SITS IN excludedTypes AS "pending handler". Like
// TypeRequestModelList and unlike TypeRequestHistory (#2113→#2116) or
// TypeRequestAttachment (#2052→#2054), the declaration and the handler land in
// ONE ticket — the declaration's only daemon-side consumer is that handler, so
// cutting them apart would produce a slice consumed by exactly one sibling. The
// request is therefore filed in cmd/pyry/relay_guard_test.go's inboundTypes as
// "switch-intercepted" from the moment it exists, and the reply in excludedTypes
// as an outbound reply, exactly as TypeHistoryPage is for TypeRequestHistory.
const (
	TypeRequestSystemPrompt = "request_system_prompt" // phone → binary, inbound v2 control (switch-intercepted — #2152)
	TypeSystemPrompt        = "system_prompt"         // binary → phone, one conversation's stored prompt plus a live-session verdict, correlated via in_reply_to
)

// Mobile Protocol v2 clarifying-question batch (#1962, split from #1926). The
// questions claude asks mid-turn when it needs the operator to choose between
// approaches, carried to a client as one frame per batch. The call rides the same
// approval bridge a permission prompt does: it blocks on pyry mcp-approve,
// handleApprove parks it in internal/permbridge keyed by tool_use_id, and
// streamApprovalBridge.Surface raises it to clients. That surfacer hard-codes
// tuidriver.ModalClassPermission and uses the tool name as the prompt body, which
// is the defect this vocabulary exists to close — a clarifying question reaches a
// remote client today as a modal titled "Permission required" whose body reads as
// AskUserQuestion.
//
// Grouped alone rather than merged into the modal block above, and the deciding
// argument is SECURITY rather than taste. denyByClass in
// internal/modalbridge/modal.go makes ModalShownPayload.DefaultOptionID the DENY
// option, so a careless confirm on a remote surface denies rather than allows, and
// docs/protocol-mobile.md § Modal states as a hard invariant that
// default_option_id MUST equal one of options[].id. That invariant is TOTAL on the
// permission surface: every modal_shown frame satisfies it, so any client or test
// may assert it unconditionally. A clarifying question has no deny option and no
// safe default, so a question riding modal_shown would demote a total invariant to
// a class-conditional one — every asserting site would have to learn a class
// exemption, on precisely the field whose whole purpose is fail-safe. Two
// supporting reasons, both re-checked against the tree: ModalAnswerPayload carries
// exactly one OptionID, so a batch with per-question multi-select forks the
// inbound leg either way and the claimed reuse is false at the joint that carries
// the security contract; and ModalOption is flat {id, label} while the committed
// capture internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json nests
// options under each question and gives each a description, so growing the modal
// shape would add a field that is always empty for permission and trust and would
// rewrite every committed modal_shown golden on both sides of the wire. Sharing an
// approval bridge is not sharing a subject.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give:
// the wire type names what the frame IS to a client, so a claude rename lands in
// one place instead of breaking every client at once. question_shown mirrors
// modal_shown because the frame is the same KIND of thing — a prompt surfaced to a
// client, answered or dismissed on a terminal path — while being a different
// family.
//
// claude's words on this path are the tool name AskUserQuestion and the tool_input
// keys questions, question, header, options and multiSelect. The sibling blocks'
// subject-noun trap cuts here too: the SINGULAR question is a substring of the
// correct name, so a strings.Contains check on it would be RED against it — the
// same trap TypeSlashCommandList's block records three words wide for command,
// slash_command and slash, and TypeModelAnnounced's block records for the singular
// model. The discriminating words are therefore the PLURAL array key questions,
// which question_shown does not contain (it carries question_, not questions), and
// the tool name itself, whose snake-cased derivations all carry ask. That
// containment is why the name is not questions_shown, which would make the plural
// check red by construction. header, options and multiSelect are per-entry keys
// and belong to the payload-bytes half that arrives with the shape. See
// TestQuestionShownType_IsNotClaudesVocabulary.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is
// an outbound binary → phone report an old phone never receives, and a leak into
// that set would let a phone send a question_shown frame into dispatch.Route. Two
// drift detectors classify it, both mandatory from the moment the constant exists
// rather than from the moment something emits it: the partition in
// internal/protocol/compat_test.go splits Type* constants between
// inboundAppTypeSet and v2OnlyTypes (this lives in the latter), and
// cmd/pyry/relay_guard_test.go's excludedTypes records it as a push.
//
// No inbound request verb is declared here, and that is not an omission. Both
// constants in this block are outbound-only. The inbound pair — TypeQuestionAnswer
// / TypeQuestionRefused, declared by #1983 in the block below — sits in
// cmd/pyry/relay_guard_test.go's inboundTypes as "switch-intercepted" since #1984
// gave internal/relay/v2session.go's dispatchAppFrame a case for each. Between
// those two slices it was filed in excludedTypes under its own pending-handler
// label, because TestEveryInboundV2TypeHasHandler's Assertion #1 requires an
// inbound type to be wired into cmd/pyry/relay.go's Handlers map or that switch —
// a verb declared without its handler is red by construction — and filing it AS A
// PUSH to dodge that would have been a lie to the guard. TypeAttachmentChunk is
// the same shape one step earlier, still excluded until #1744 dispatches it. See
// that block for the reasoning in full.
//
// THE DISMISSAL IS ITS OWN TYPE (#1974), settling what TypeQuestionShown's own
// slice deferred. ModalDismissedPayload identifies what it clears by modal_id, and
// a client decoding modal_dismissed routes it to the modal panel — so a
// question_batch_id arriving in that field clears the wrong panel, or none.
// Reusing the modal frame would have made the routing depend on a value's shape
// instead of on the frame's name. Its payload is QuestionDismissedPayload
// (questions.go): question_batch_id, outcome and source, field for field with the
// modal frame's, including the absent conversation_id.
//
// The source values do NOT carry over intact from modal_dismissed, and that is
// published in docs/protocol-mobile.md § Question rather than left to be assumed.
// Of the modal frame's closed set {remote, local, timeout}, only timeout is
// emitted by a slice in flight; remote and local are answered outcomes belonging
// to the answer half. Two of the producer's terminal paths — the caller
// disconnecting and the daemon shutting down — have NO member in that set at all,
// so source is declared a plain string whose vocabulary the producer owns, the
// Outcome/Class posture rather than the modal source's.
//
// The declaring tickets are wire vocabulary only: #1963 declares the batch payload
// and its nested per-question and per-option types, #1974 the dismissal payload,
// #1965 the parse that fills the batch from claude's tool input, #1975 the nonce
// mint, and #1973 the producer that emits both frames. pyrycode-desktop#849
// decodes what this family lands. Same declare-then-emit sequencing as
// #1405→#1410, #1616→#1638, #1704→#1848 and #1726.
const (
	TypeQuestionShown     = "question_shown"     // binary → phone, outbound v2 clarifying-question batch
	TypeQuestionDismissed = "question_dismissed" // binary → phone, outbound v2 question-batch resolution event
)

// Mobile Protocol v2 clarifying-question ANSWER vocabulary (#1983, split from
// #1907). The frames a client sends back to resolve a batch the block above
// surfaced: question_answer carries the operator's selections, question_refused
// says the operator declined to choose. These are the question family's FIRST
// INBOUND types — everything above is binary → phone.
//
// TWO TYPES, NOT ONE NULLABLE FLAG. A refusal carries no answers, and the
// family's own precedent is that a distinct meaning gets its own type rather than
// a nullable field on a shared one: TypeQuestionDismissed above is its own type
// rather than a reused modal_dismissed (#1974) for exactly that reason. The pair
// mirrors TypeModalAnswer / TypeModalCancel.
//
// SELECTIONS ARE POSITIONAL; ONLY THE VALUES ARE CLIENT-AUTHORED. An answer entry
// names its question by its INDEX into the batch's questions array — the
// canonical order the client already renders — rather than echoing the question
// text back, and the daemon reads the text from its own parked copy.
// QuestionOption carries no id and claude's protocol selects an option by its
// LABEL, so the alternative would carry a claude-authored string back across the
// trust boundary in both directions for no gain. QuestionShownPayload's warning
// stands unchanged: publishing such a string outbound does not make it trusted
// when it returns. The values an entry carries ARE genuinely client-authored —
// claude's contract permits free text anywhere and requires no value to be one of
// the offered labels — so they are opaque strings, checked against nothing.
//
// THE VENDOR response FIELD IS DELIBERATELY NOT CARRIED. claude's contract
// (https://code.claude.com/docs/en/agent-sdk/user-input) offers an optional
// top-level freeform reply that replaces answers entirely; both this repo and
// pyrycode-desktop decided on 2026-08-31 not to use it. It stays available later
// with no wire change, so nothing here forecloses it.
//
// THE GUARD CLASSIFIES THESE WITH THEIR INBOUND NEIGHBOURS, and getting it wrong
// is a red build rather than a style question. Both sit in
// cmd/pyry/relay_guard_test.go's inboundTypes as "switch-intercepted", beside
// TypeModalAnswer / TypeModalCancel, because internal/relay/v2session.go's
// dispatchAppFrame now has cases for them (#1984). Between #1983 and #1984 they
// were filed in excludedTypes as "pending handler (#1984)" — inboundTypes would
// have failed Assertion #1 with no dispatch, and "push", the reason the eight
// outbound neighbours give, is false for a genuinely inbound frame. That entry had
// to move the moment the cases landed, which Assertion #2 makes mandatory rather
// than tidy-up. TypeAttachmentChunk is the same shape one step earlier, still
// excluded until #1744 dispatches it. Filing is required from the moment the
// constant exists: Assertion #3 reports an unclassified constant, not an
// unemitted one.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: these
// are v2 CONTROL envelopes intercepted before internal/dispatch.Route, exactly as
// TypeModalAnswer and TypeInterrupt are, and there is no dispatch.Route handler
// for either. A leak into that set would route an inbound control envelope into
// the v1 handler chain. The drift detector in internal/protocol/compat_test.go
// partitions Type* constants between inboundAppTypeSet and v2OnlyTypes; these two
// live in the latter, and IsKnownAppType rejects both with ErrUnknownType.
//
// The NAMES are the daemon's, not claude's, for the reason TypeQuestionShown's
// block gives: the wire type names what the frame IS to a client, so a claude
// rename lands in one place. The subject-noun trap cuts here too — the SINGULAR
// question is a substring of the tool name AskUserQuestion, so neither name may be
// probed with a strings.Contains on it.
//
// BOTH FRAMES ARE NOW INTERCEPTED (#1984) — dispatchAppFrame has a case for each,
// so neither reaches dispatch.Route and neither draws the unknown-type reply it
// drew while #1983's declaration stood alone. Declaring the constants had changed
// no runtime path, which is what made that vocabulary-only slice safe on an
// inbound surface; adding the cases is what changed it, since a Go constant is not
// a registry. THE INTERCEPTION STILL GRANTS NO INBOUND CAPABILITY: the handler
// applies no authorization at all, and what makes that fail-safe is that its
// resolver seam is nil at every construction site, so no frame reaches an
// actuator. The interactive capability gate and the per-device answer gate (#702)
// remain the resolver's to apply, default deny, and #1986 installs the latter
// before anything is wired.
//
// The payloads are QuestionAnswerPayload (with its nested QuestionAnswerEntry)
// and QuestionRefusedPayload in questions.go. #1985 resolves an answer against the
// daemon's parked batch; pyrycode-desktop#853 is the client that sends these. Same
// declare-then-serve sequencing as #1752→#1744.
const (
	TypeQuestionAnswer  = "question_answer"  // phone → binary, inbound v2 control (switch-intercepted — #1984)
	TypeQuestionRefused = "question_refused" // phone → binary, inbound v2 control (switch-intercepted — #1984)
)

// Mobile Protocol v2 PAIRING-MINT PAIR (#2126; docs/protocol-mobile.md § Minting a
// pairing from a paired client publishes both). The frame an already-paired client
// sends to ask the daemon to mint a pairing for ANOTHER device, and the frame it is
// answered with. Their payloads are MintPairingPayload and PairingMintedPayload
// (pairing.go).
//
// THE GAP IT CLOSES: pairing has exactly one minter, the `pyry pair` CLI, and it
// needs a shell on the daemon's host. runPairDefault reads 32 random bytes, hashes
// them into a devices.Device and hands the tuple to pair.Encode. Two clients want a
// second device paired without a shell — a browser build of the desktop client, and
// a second person's client — and the shape is the one WhatsApp Web and Signal
// Desktop use: a device that is already paired links the next one.
//
// THE NAMES ARE THE WRITE-VERB FAMILY'S, NOT THE request_* FAMILY'S, and the
// departure is the point rather than an oversight. All seven inbound request_*
// verbs — TypeRequestSnapshot, TypeRequestDebugBundle, TypeRequestSessionSettings,
// TypeRequestAttachment, TypeRequestHistory, TypeRequestModelList,
// TypeRequestSystemPrompt — are READS: each asks for something that already exists
// and is answered with a projection of daemon state. This one CREATES. It mints a
// credential and writes a durable devices.json record, so it takes the shape the
// vocabulary already has for that — TypeCreateConversation→TypeConversationCreated,
// TypeCreateWorkspaceFolder→TypeWorkspaceFolderCreated, imperative verb in and past
// participle out. Filing a state-mutating verb under the read family's prefix would
// hide the one thing a client most needs to read off the type string: this frame
// has a side effect on the host. #2052's rule cuts the same way it does for the
// reads — the wire type string IS the contract, and a name chosen twice is a name
// chosen wrong once.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: these are
// v2 CONTROL envelopes intercepted before internal/dispatch.Route, exactly as
// TypeRequestHistory, TypeRequestModelList and TypeRequestSystemPrompt are. The
// partition in internal/protocol/compat_test.go files both in v2OnlyTypes, and
// inboundAppTypeSet's asserted count does not move.
//
// THAT LANE BINDS HARDER HERE THAN ON ANY OF THOSE THREE, because this verb's reply
// is a bearer credential: IsKnownAppType returning ErrUnknownType is the structural
// bar that stops a v1 client pushing a credential-minting verb into the v1 handler
// chain at all. TypeRecentWorkspaces is the OTHER family and is not the precedent to
// copy — its own block files it as a dispatch.Route read verb and justifies that
// with "no untrusted input drives a filesystem operation, so it is NOT
// security-sensitive". The inverse of that sentence is this pair.
//
// THE HANDLER MUST ANSWER ONLY AN AUTHENTICATED PAIRED DEVICE, and this declaration
// carries that obligation rather than leaving it to the ticket that serves the
// frame. A one-key request yields a reply carrying a live credential, so the whole
// risk of the pair concentrates in the gate, and a declare-ahead slice is
// structurally bad at remembering one. AN INTERACTIVE-CAPABILITY GATE IS NOT A
// SUBSTITUTE: capability negotiation is a feature advertisement, not an
// authorization, and the check that matters is that the conn is itself an
// AEAD-authenticated device the registry holds. Rate-limiting the mint — an
// already-paired hostile client can otherwise grow devices.json without bound and
// hold many live credentials — is docs/protocol-mobile.md § Security model threat
// 7's deferred posture, and #2127 must weigh it rather than inherit silence.
//
// IT WIDENS THREAT 4 (token leak via phone) ON PURPOSE. Minting authority moves from
// "someone with a shell on the host" to "any paired device", so one compromised
// client can mint further devices. The mitigation named there — per-device
// revocation via `pyry pair rm` — still applies and the blast radius is unchanged in
// kind. THREAT 1 DOES NOT LAND: device_name reaches no claude prompt on any path;
// it is a registry label, and the hazards it does carry are named at the field.
//
// THE REPLY IS DECLARED AHEAD OF ITS HANDLER. #2127 serves this pair, blocked on the
// redemption window (#1528, #1529) and the devices.json lock (#1531); until then
// TypeMintPairing sits in cmd/pyry/relay_guard_test.go's excludedTypes as "pending
// handler (#2127)" — TypeRequestHistory's precedent (#2113→#2116), the entry moving
// up to inboundTypes as "switch-intercepted" when dispatchAppFrame gains its case —
// and TypePairingMinted as an outbound "reply", exactly as TypeHistoryPage is.
// Declaring ahead of serving is what lets the two client slices start against a
// published contract instead of waiting on three blockers, and it is this repo's
// established sequencing: #2052→#2054, #1983→#1984, #2113→#2116.
const (
	TypeMintPairing   = "mint_pairing"   // phone → binary, inbound v2 control (pending handler — #2127)
	TypePairingMinted = "pairing_minted" // binary → phone, one minted pairing as the pair.Encode string, correlated via in_reply_to
)

// Mobile Protocol v2 compaction boundary. claude finished compacting a
// conversation's context; this frame says what triggered it and how far the
// context shrank, so the mark a compaction leaves in a thread can say WHY claude
// no longer remembers something from before that point
// (docs/protocol-mobile.md § compaction_boundary).
//
// Grouped alone rather than with api_retry/compacting, and the reason is an
// ORDERING rather than a taxonomy preference. Those two are show/clear sub-states
// with two edges; this is a single mark with none. More to the point, it CANNOT be
// a wider compacting payload: claude states the counts on a separate
// system/compact_boundary line that arrives AFTER the closing system/status line
// the falling edge is read off, so by the time the numbers exist the frame that
// would have carried them has shipped. See internal/turnevent's CompactionBoundary
// for the captured line order.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give:
// the wire follows internal/turnevent's VARIANT, so a claude rename lands in one
// place instead of breaking every client at once. claude's subtype is
// compact_boundary; the daemon's word for the process is "compaction" throughout
// this package and internal/streamsup, and the discriminating word is "boundary" —
// what this frame MARKS — where `compacting` beside it names a state with two
// edges. The two frames stay separate and neither absorbs the other.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is
// an outbound binary → phone event an old phone never receives. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this lives in the latter.
const (
	TypeCompactionBoundary = "compaction_boundary" // binary → phone, outbound v2 compaction boundary
)

// Mobile Protocol v2 tool-denial marker. claude refused to run a tool call it had
// already announced; this frame says WHICH call, so a blocked tool row can look
// blocked rather than broken (docs/protocol-mobile.md § tool_denied, #2233).
//
// A FRAME OF ITS OWN RATHER THAN TWO FIELDS ON tool_result, and the choice is forced
// twice over rather than preferred. claude states the denial on a separate
// system/permission_denied line that arrives BEFORE the tool result
// (assistant/tool_use → system/permission_denied → user/tool_result, measured across
// the three denial-bearing arms of the committed bypass_reescalation capture), so
// folding it in would need the parser to latch cross-line state keyed by tool_use_id.
// And #2234's result-line recovery reports denials whose tool_result frame has already
// shipped, which a field on a sent frame cannot answer at all. A standalone frame needs
// neither. See internal/turnevent's ToolCallDenied for the captured line order.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give: the
// wire follows internal/turnevent's VARIANT, so a claude rename lands in one place
// instead of breaking every client at once. claude's subtype is permission_denied; the
// daemon's word names what the frame is ABOUT — a tool call — where "permission" names
// the mechanism that refused it and would read as a sibling of the MCP approval flow
// this has nothing to do with.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is an
// outbound binary → phone event an old phone never receives. The drift detector in
// internal/protocol/compat_test.go partitions Type* constants between
// inboundAppTypeSet and v2OnlyTypes; this lives in the latter. Keeping it there is also
// the structural guarantee that no inbound re-authorize path exists — nothing in this
// daemon accepts a tool_denied FROM a phone.
const (
	TypeToolDenied = "tool_denied" // binary → phone, outbound v2 tool-denial marker
)

// Mobile Protocol v2 tool-progress reading. This push-only frame updates the open
// tool row named by tool_use_id with claude's signed elapsed-seconds reading. It is
// a report, not a lifecycle edge or an inbound control surface.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go. The
// drift detectors in internal/protocol/compat_test.go and cmd/pyry's relay guard
// classify it as an outbound v2 push.
const (
	TypeToolProgress = "tool_progress" // binary → phone, outbound v2 tool-progress reading
)

// Mobile Protocol v2 operator-facing banner. claude printed text ABOUT the session
// rather than as part of an answer — a hook's block reason, a local command's output,
// a loop notification — and this frame is the one typed place it arrives
// (docs/protocol-mobile.md § banner, #2256).
//
// THE FRAME EXISTS BECAUSE NOTHING ELSE ON THE WIRE COULD CARRY IT, and the gap was
// total rather than awkward. TypeUnrecognizedMessage is a parser-gap diagnostic,
// TypeCompacting and TypeRateLimited each report one specific machine state, and the
// assistant stream carries only what the model said. Text claude prints about the
// session had nowhere to go and was dropped.
//
// Grouped alone rather than with api_retry/compacting. Those are show/clear sub-states
// of a live turn with two edges each; this is a single report with none, and its
// producer set spans two unrelated claude subtypes rather than one line pair.
//
// The NAME is the daemon's, not claude's, for the reason the blocks above give: the
// wire follows internal/turnevent's VARIANT, so a claude rename lands in one place
// instead of breaking every client at once. claude's words on this path are the
// subtypes informational and notification and the payload keys content, level and
// prevent_continuation; `banner` derives from none of them and names what the thing IS
// to a client. TestBannerType_IsNotClaudesVocabulary pins that, and pins which of
// claude's words the check list may NOT contain.
//
// IT HAS ONE PRODUCER SINCE #2319, which maps claude's informational subtype onto this
// frame against a committed capture, and NO SECOND ONE. #2258, which owned
// local_command_output and notification, is CLOSED AS ANSWERED (2026-09-10): the
// capture recorded both unobserved with zero frames, so neither had a field set to map. The frame shipped ahead of that
// first producer deliberately, and declaring a contract ahead of its producers is this
// file's established sequencing —
// #2052→#2054, #1983→#1984 — and it lets the client slices start against a published
// shape. Unlike TypeMintPairing above, nothing is pending on the INBOUND side: this is
// an outbound push and cmd/pyry/relay_guard_test.go's excludedTypes carries it as one.
//
// MUST NOT be added to inboundAppTypeSet in internal/protocol/envelope.go: this is an
// outbound binary → phone event an old phone never receives. The drift detector in
// internal/protocol/compat_test.go partitions Type* constants between inboundAppTypeSet
// and v2OnlyTypes; this lives in the latter, which is also the structural guarantee
// that nothing in this daemon accepts a banner FROM a phone.
const (
	TypeBanner = "banner" // binary → phone, outbound v2 operator-facing text
)

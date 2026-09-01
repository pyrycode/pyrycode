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
	// place so #1741, #1743, #1744 and #1746 do not each invent a name. The
	// reasoning is published in that section rather than duplicated here.
	//
	// attachment.not_found is DELIBERATELY MERGED: it answers every retrieval
	// that yields no bytes — an unknown id, a non-canonical id, and an id
	// resolving outside the named conversation's directory alike. Two
	// distinguishable codes would make the retrieval verb a path-existence
	// oracle, so the merge is a disclosure decision, not an imprecision.
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

// Mobile Protocol v2 background-task types. These three are the first frames in
// the vocabulary whose subject is work that OUTLIVES the turn that started it —
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
// The NAMES are the daemon's, not claude's. internal/streamsup/parser.go
// translates claude's system/task_started, system/task_updated and
// system/background_tasks_changed subtypes into internal/turnevent variants, and
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
// between inboundAppTypeSet and v2OnlyTypes; these three live in the latter.
//
// The declaring ticket (#1393) was wire vocabulary only; #1394 added
// internal/turnbridge's MapEvent cases for all three variants and the
// docs/protocol-mobile.md section, so these frames now reach an interactive v2
// mobile client.
const (
	TypeBackgroundTaskStarted = "background_task_started" // binary → phone, outbound v2 background-task open
	TypeBackgroundTaskUpdated = "background_task_updated" // binary → phone, outbound v2 background-task change
	TypeBackgroundTaskRoster  = "background_task_roster"  // binary → phone, outbound v2 background-task snapshot
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
// NO dispatch.Route handler for it. Unlike the modal frames it carries NO
// payload — no conversation_id, no modal_id nonce, no answer_token, no
// idempotency key: a bare control frame.
//
// Trust posture: interrupt is gated on the negotiated `interactive` capability
// (a non-interactive conn's interrupt is inert) and is exempt from the
// per-device permission gate (#702) — interrupting one's own paired session is
// a normal paired-phone action (ADR 025 § Security model), not a privileged
// tool-permission decision.
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
// start a fresh session — the remote equivalent of typing `/clear` at the local
// terminal. The daemon routes it to the supervised claude as a `/clear` via the
// sealed supervisor StartNewSession seam (#830); the client observes the
// resulting break through the EXISTING session_transition marker
// (reason: "clear", #656/#657) — there is NO synchronous ack (fire-and-forget,
// like interrupt). Unlike interrupt (→turnevent.Cancel) it maps to no neutral
// turnevent command; it drives the supervisor seam directly.
//
// It is an inbound phone → binary *control* envelope the v2 session manager
// intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeInterrupt / TypeRequestDebugBundle); there
// is NO dispatch.Route handler for it. Unlike the modal frames it carries NO
// payload — no conversation_id, no modal_id nonce, no answer_token, no
// idempotency key: a bare control frame.
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
// and checks the claims, #1743 stores, #1744 dispatches the inbound leg, and
// #1746 serves retrieval. Same declare-then-emit sequencing as #1616→#1638 and
// #1704→#1848.
const (
	TypeAttachmentChunk = "attachment_chunk" // phone ↔ binary, one chunk of an attachment's bytes (both directions)
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
// No inbound request verb is declared here, and that is not an omission.
// TestEveryInboundV2TypeHasHandler's Assertion #1 requires an inbound type to be
// wired into cmd/pyry/relay.go's Handlers map or internal/relay/v2session.go's
// dispatchAppFrame switch, and this slice ships no handler — so a verb declared
// here would be red by construction, and filing it under excludedTypes to dodge
// that would be a lie to the guard. That still holds for TypeQuestionDismissed
// below: both constants here are outbound-only, and the inbound answer verb is
// #1907's, to be declared with the handler that serves it.
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

package protocol

import (
	"encoding/json"
	"time"
)

// ModelAnnouncedPayload is the body of an Envelope whose Type ==
// TypeModelAnnounced (docs/protocol-mobile.md § model_announced, #1616). Binary →
// phone direction; the wire form of turnevent.ModelAnnounced, which reports the
// model claude named for the current turn on its system/init line.
//
// Emitted since #1638: the shape was declared here (#1616) so a client could be
// written against it, and #1638 added turnbridge.MapEvent's case for the variant —
// the sequencing #1405 used ahead of #1410.
//
// Like RateLimitedPayload it is conversation-scoped rather than turn-scoped, so
// there is no turn_id, and receiving one neither opens nor closes a turn: a
// per-turn announcement is not a turn boundary (turnevent.ModelAnnounced's own
// doc). The bridge (#1638) supplies ConversationID because the internal event
// carries none. claude's session_id and cwd are deliberately absent for
// BackgroundTaskStartedPayload's reason plus #1380's — one is claude's session
// identity and the other the operator's local filesystem path, neither is the
// daemon's conversation identity, and neither is even declared on the producer's
// decode target (streamsup's systemInitLine), so this payload cannot carry them
// even by accident.
//
// The value's semantics are NOT restated here: turnevent.ModelAnnounced's field
// comments are their single source of truth, in the manner ThinkingProgressPayload
// delegates its two consumer hazards. Named and delegated: Model is claude's
// identifier VERBATIM and never empty; claude echoes an identifier at least as
// specific as the one it was given, so the value is not reliably dated and need
// not appear in any published model list, which makes a lookup miss ORDINARY
// rather than an error; and bounded-and-UTF-8-valid is all it is. A
// consumer-facing statement of each is in docs/protocol-mobile.md
// § model_announced.
//
// Truncated is a bool rather than the siblings' TruncatedFields []string,
// following UnrecognizedMessagePayload: this payload bounds a SINGLE string, so a
// name list would be permanently either nil or ["model"] — a variable-length
// container carrying one bit, plus a name the reader must check against the only
// field there is. The slice exists on the background-task and rate-limit payloads
// because they bound two to four fields and the report has to say which. It is
// load-bearing either way — a payload that dropped it would present claude's cut
// text to a phone as complete.
//
// Three v2 payloads already carry a wire field named model — ScreenSnapshotPayload,
// SessionSettingsPayload and SetSessionSettingsPayload — and all three mean the
// per-session OVERRIDE, where "" means "inherited default, no override". This one
// means what claude ANNOUNCED for the turn, and in the ordinary case the two
// disagree: the override is "" while claude has named a concrete model. The name
// is kept (turnevent's field name in snake_case, per the convention
// RateLimitedPayload states) and the distinction is drawn by cross-reference in
// docs/protocol-mobile.md, which is what reaches a client author reading only one
// of the existing rows.
//
// SECURITY: Model is a claude-authored string that crossed the subprocess trust
// boundary. It is safe to RENDER as inert text and must never be fed to an HTML
// sink, an attribute, or a URL; the daemon bounds it but does not sanitize it — no
// control-character or terminal-escape stripping happens on this path — so it stays
// untrusted, model-influenced text all the way to the client, and the render
// boundary owing the sanitization is the CLIENT's. Its bound is the producer's,
// decided at construction (internal/streamsup/parser.go's maxModelField), so this
// struct re-decides no maximum: a second cap here would be a second place the limit
// is decided, and the two could disagree silently. Nor is there a charset check —
// internal/relay's validModel bounds a PHONE-supplied override and is deliberately
// a different rule; applying it here would reject identifiers claude legitimately
// announces. The constraint on turnevent.ModelAnnounced follows the data onto the
// wire: it is a REPORT, never a control input, so a client MUST NOT branch
// security-relevant behaviour on Model.
type ModelAnnouncedPayload struct {
	ConversationID string `json:"conversation_id"`
	Model          string `json:"model"`
	Truncated      bool   `json:"truncated"`
}

// ModelRefusalFallbackPayload reports that claude refused a turn on one model
// and retried it on another. It is conversation-scoped: the daemon has no
// request or claude message identity that can honestly join the refusal to an
// assistant_delta, so no turn_id, request_id, or message UUID is published.
// ModelAnnouncedPayload remains the authority on which model claude is running.
//
// Every value except ConversationID is claude-authored, bounded by the producer
// and not sanitized. OriginalModel and FallbackModel are opaque identifiers;
// Scope and RefusalCategory are open strings carried verbatim. In particular,
// RefusalCategory is claude's assertion about the request, never a daemon
// finding and never an actuator. Banner may echo refused user text and must be
// rendered as inert text attributed to claude, never fed to an HTML sink, URL,
// command, or shell.
//
// TruncatedFields and DroppedFields use this frame's wire-key vocabulary. Nil
// means no published field was reported and intentionally marshals as null, not
// []; neither field uses omitempty and this type must not gain a MarshalJSON.
type ModelRefusalFallbackPayload struct {
	ConversationID  string   `json:"conversation_id"`
	OriginalModel   string   `json:"original_model"`
	FallbackModel   string   `json:"fallback_model"`
	Scope           string   `json:"scope"`
	RefusalCategory string   `json:"refusal_category"`
	Banner          string   `json:"banner"`
	TruncatedFields []string `json:"truncated_fields"`
	DroppedFields   []string `json:"dropped_fields"`
}

// ModelRefusalNoFallbackPayload reports that claude refused a turn without
// retrying it on another model. It is conversation-scoped because the daemon
// has no request or claude message identity that can join it to an
// assistant_delta. ModelAnnouncedPayload remains the current-model authority.
//
// Every value except ConversationID is bounded but unsanitized claude-authored
// data. OriginalModel is opaque and RefusalCategory is claude's open assertion,
// never a daemon finding or actuator. Banner may echo refused user text and must
// be rendered as inert, claude-attributed text, never passed to an HTML sink,
// URL, command, or shell.
//
// TruncatedFields and DroppedFields use this frame's wire-key vocabulary. Nil
// means no published field was reported and intentionally marshals as null, not
// []; neither field uses omitempty and this type must not gain a MarshalJSON.
type ModelRefusalNoFallbackPayload struct {
	ConversationID  string   `json:"conversation_id"`
	OriginalModel   string   `json:"original_model"`
	RefusalCategory string   `json:"refusal_category"`
	Banner          string   `json:"banner"`
	TruncatedFields []string `json:"truncated_fields"`
	DroppedFields   []string `json:"dropped_fields"`
}

// SessionFactsPayload is the body of an Envelope whose Type == TypeSessionFacts
// (docs/protocol-mobile.md § session_facts, #2253). Binary → phone direction; the
// wire form of turnevent.SessionFacts, which reports what claude's own build IS and
// what posture claude says the child is running under, from the same system/init
// line ModelAnnouncedPayload's value comes from.
//
// Declared here (#2253) ahead of its producer so a client can be written against the
// shape — the sequencing #1405 used ahead of #1410, #1616 ahead of #1638 and #1704
// ahead of #1848. The producer has since landed: turnbridge.MapEvent's
// turnevent.SessionFacts arm constructs it (#2254) and cmd/pyry's interactive turn
// emitter pushes it (#2252), so live traffic carries the frame.
//
// Like ModelAnnouncedPayload it is conversation-scoped rather than turn-scoped, so
// there is no turn_id, and receiving one neither opens nor closes a turn: a per-turn
// report is not a turn boundary (turnevent.SessionFacts' own doc). PER LINE, NOT PER
// SESSION — claude emits init once per TURN, so one session produces several of
// these and the producer does not dedup. The bridge supplies ConversationID because
// the internal event carries none.
//
// The two values' semantics are NOT restated here: turnevent.SessionFacts' field
// comments are their single source of truth, in the manner ModelAnnouncedPayload
// delegates to ModelAnnounced.Model. Named and delegated: both are claude's text
// VERBATIM, neither is parsed, normalised or checked against any published list;
// EITHER may be empty, because the producer emits when either fact is present; and
// PermissionMode is an OPEN SET on purpose, since an allow-list would drop the first
// report of a posture nobody has heard of, which is the case an operator most needs
// to see. A consumer-facing statement of each is in docs/protocol-mobile.md
// § session_facts.
//
// TruncatedFields is the siblings' []string rather than ModelAnnouncedPayload's
// Truncated bool, and that payload's doc states the rule both follow: a bool is
// right when the payload bounds a SINGLE string and a name list would be permanently
// either nil or one known name; a named list is right the moment the report has to
// say WHICH of several was cut. Two fields is where the rule flips. Entries are the
// DAEMON's wire names — "claude_code_version" and "permission_mode", the second NOT
// claude's own permissionMode, exactly as RateLimitedPayload's are "limit_type" and
// not claude's rateLimitType. nil when nothing was cut, never an empty non-nil
// slice, and nil serialises as null: UnrecognizedMessagePayload's form, and what
// every truncated_fields row in docs/protocol-mobile.md already describes. It is
// load-bearing either way — a payload that dropped it would present claude's cut
// text to a phone as complete.
//
// FOUR KEYS OF THE CAPTURED INIT LINE ARE DELIBERATELY ABSENT, and the absence is
// the guarantee rather than a gap: cwd, memory_paths and messaging_socket_path are
// the operator's local filesystem, and session_id is claude's own session identity,
// which is not the daemon's conversation identity (BackgroundTaskStartedPayload's
// reason plus #1380's). None is declared on the producer's decode target
// (internal/streamsup's systemInitLine), so this payload cannot carry them even by
// accident — a field never decoded cannot leak whatever a later sweep forgets to
// check. There is NO effort field because claude publishes none, measured in #2251
// and machine-enforced by effortInitPins; see TypeSessionFacts' block. MCP server
// status belongs to the frame #2373 declares and is deliberately not folded in here.
//
// SECURITY: both strings are claude-authored values that crossed the subprocess
// trust boundary. They are safe to RENDER as inert text and must never be fed to an
// HTML sink, an attribute, or a URL; the daemon bounds them but does not sanitize
// them — no control-character or terminal-escape stripping happens on this path,
// beyond the invalid-UTF-8 scrub a mid-rune cut forces — so they stay untrusted,
// model-influenced text all the way to the client, and the render boundary owing the
// sanitization is the CLIENT's. Their bounds are the producer's, decided at
// construction (internal/streamsup's maxClaudeVersionField and
// maxPermissionModeField), so this struct re-decides no maximum: a second cap here
// would be a second place the limit is decided, and the two could disagree silently.
//
// PermissionMode CARRIES ONE HAZARD THE SIBLING DOES NOT, and it is the field a
// client is likeliest to misuse. It is a CLAIM, NOT A GUARANTEE: a buggy or
// compromised claude can report `default` while running under any posture at all,
// and what the value proves is only what claude SAID. Nothing in the daemon gates on
// it and the value is never fed back into a spawn or a control request — in
// particular it must not become an input to internal/streamsup's
// permissionModeAllowed, which bounds what the DAEMON may ASK FOR and is a
// deliberately different rule. A client MUST NOT read it as an authorization
// decision: suppressing a warning, unlocking an action, or rendering a safety
// posture on the strength of this string is exactly the misuse it cannot support.
// Both fields follow ModelAnnouncedPayload's standing constraint — this is a REPORT,
// never a control input.
type SessionFactsPayload struct {
	ConversationID    string   `json:"conversation_id"`
	ClaudeCodeVersion string   `json:"claude_code_version"`
	PermissionMode    string   `json:"permission_mode"`
	TruncatedFields   []string `json:"truncated_fields"`
}

// MCPStatusPayload is the body of an Envelope whose Type == TypeMCPStatus.
// Binary → phone direction; the conversation-scoped snapshot of MCP servers
// Claude reported. This is the sole outbound wire shape for both later live
// publication and on-demand replies. #2373 declares the shape before the
// request, mapping and publication work in #2374, #2375 and #2276.
//
// Servers preserves Claude's order. Its key is always present and never null —
// see MarshalJSON. DroppedServers is copied verbatim from
// turnevent.MCPStatus.DroppedServers by a later mapper; this layer must not infer
// loss from len(Servers).
//
// Config, tools, serverInfo.name, request ids and the raw Claude response are
// deliberately absent. Every string in Servers is untrusted Claude-authored
// text. This layer neither validates nor sanitizes it, and a client must render
// every field as inert text rather than use it as an actuator.
type MCPStatusPayload struct {
	ConversationID string            `json:"conversation_id"`
	Servers        []MCPServerStatus `json:"servers"`
	DroppedServers int               `json:"dropped_servers"`
}

// MarshalJSON normalises a nil Servers slice to an empty array. An empty server
// list is a positive snapshot, so clients receive "servers":[] rather than null.
// The value receiver keeps normalisation local and does not mutate the caller.
func (p MCPStatusPayload) MarshalJSON() ([]byte, error) {
	if p.Servers == nil {
		p.Servers = []MCPServerStatus{}
	}
	type alias MCPStatusPayload
	return json.Marshal(alias(p))
}

// MCPServerStatus is one retained server row in MCPStatusPayload. All five keys
// remain present even when their strings are empty. Error is free-form prose
// already capped to 256 bytes by the producer; the wire layer does not cap it
// again. Scope, Status and Version remain open strings rather than validation or
// authority.
type MCPServerStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Error   string `json:"error"`
	Scope   string `json:"scope"`
	Version string `json:"version"`
}

// MCPStatusRequestPayload is the body of an Envelope whose Type ==
// TypeMCPStatusRequest. It asks for the current MCP status of one conversation.
// Correlation rides Envelope.InReplyTo on the TypeMCPStatus answer, so this
// payload has no request-id field.
//
// ConversationID is an unverified remote-authored lookup key. The relay decodes
// it before consulting membership and never logs or returns it as trusted input;
// the successful answer's id comes from the resolver's daemon-side record.
// The key is unconditional because absent and empty have the same meaning and a
// committed fixture pins the complete request shape.
type MCPStatusRequestPayload struct {
	ConversationID string `json:"conversation_id"`
}

// MCPReconnectPayload is the body of an Envelope whose Type == TypeMCPReconnect
// (#2419). It asks the daemon to reconnect one named MCP server inside one
// conversation. There is no ack type: an accepted actuation answers with a fresh
// TypeMCPStatus frame correlated by Envelope.InReplyTo, so a client's next render is
// the list it would have asked for anyway and this payload carries no request id.
//
// BOTH STRINGS ARE UNVERIFIED REMOTE-AUTHORED INPUT, and they are not equally
// checked downstream, which is the part a reader is most likely to get wrong.
// ConversationID is a lookup key the relay resolves against the daemon's own
// registry before anything else sees it. ServerName is checked by NOTHING anywhere
// in this package or in internal/relay — it crosses the actuation seam verbatim, and
// the seam's implementation is its sole validator. Neither is logged, returned, or
// joined into a path by any layer that handles this type; the answer's
// ConversationID comes from the daemon's own record rather than being echoed back.
//
// Both keys are unconditional so a committed fixture pins the complete wire shape.
type MCPReconnectPayload struct {
	ConversationID string `json:"conversation_id"`
	ServerName     string `json:"server_name"`
}

// MCPTogglePayload is the body of an Envelope whose Type == TypeMCPToggle (#2419):
// MCPReconnectPayload's two fields plus the state to move one named MCP server to.
// Same answer shape, same absence of a request id, and the same split trust in its
// two strings — see that type's block, which this one does not restate.
//
// Declared flat rather than by embedding MCPReconnectPayload. Embedding would
// promote the fields for JSON and encode identically, but it would route this verb's
// wire-shape assertions through a type whose own doc block describes a different
// verb, and it would couple two wire shapes that are free to diverge later.
//
// ENABLED IS A PLAIN BOOL, NOT A POINTER, and the choice is deliberate in a family
// where SettingsUpdate reaches for pointers. An absent key therefore decodes as
// false — "disable" — which is the NON-ESCALATING direction: an omission, a
// truncation, or a client that forgot the field can only ever turn a server off,
// never on. A pointer would add a third state (nil) that some layer would then have
// to interpret, and interpreting it is exactly the judgement internal/relay is
// forbidden from making on this path — the value crosses the seam as the client sent
// it and the per-device gate below decides. A plain bool also marshals the key
// unconditionally, which is what lets the committed fixture pin a complete key set.
// Contrast marshalMCPToggleEnvelope in internal/streamsup, which does use a pointer:
// that is the OUTBOUND leg to the claude child, where omitting the key would let the
// child pick a default, a hazard that has no counterpart on this inbound decode.
type MCPTogglePayload struct {
	ConversationID string `json:"conversation_id"`
	ServerName     string `json:"server_name"`
	Enabled        bool   `json:"enabled"`
}

// ContextUsagePayload is the body of an Envelope whose Type == TypeContextUsage.
// Binary → phone direction; one conversation's context-window breakdown as claude
// reported it. This is the sole outbound wire shape for both later producers —
// #2371's post-turn publication and #2293's on-demand reply — so it carries no
// request verb and no correlation field: a reply rides Envelope.InReplyTo.
//
// IT IS MIXED-PROVENANCE, and that is the field-level fact a reader is likeliest to
// get wrong. ConversationID is DAEMON-authored: the mapper fills it from the daemon's
// own registry record, never from claude's bytes. EVERY OTHER STRING here and on all
// three row types is claude- or workspace-authored descriptive text. Assuming one
// provenance for the whole struct errs in a harmful direction half the time, because
// it promotes Model and the row strings to values they were never checked to be.
// This layer neither validates nor sanitizes them, and a client MUST render each as
// inert text rather than use it as an actuator, a link, or an authorization input.
//
// THE READING IS INFORMATIONAL, mirroring turnevent.ContextUsage's own constraint:
// consumers may display it, but it does not replace the daemon-owned
// contextwindow.Read value that control decisions use. The integers are claude's own
// and the daemon neither recomputes nor normalizes them, so a client must not assume
// Percentage is derivable from TotalTokens and MaxTokens, nor that the categories sum
// to the total.
//
// Each list preserves the producer's descending-token order, and arrives as a PREFIX
// of it: any cut takes entries off the tail, so a shortened list is never a list with
// holes. All three keys are always present and never null — see MarshalJSON. Bounds
// are not re-decided here: internal/streamsup caps every string and list count at
// construction, and a second cap in this package would be a second place the limit is
// decided, free to disagree silently — SessionFactsPayload's stated reason. The one
// bound decided outside the producer is on a dimension it does not cover, the frame's
// BYTE cost against the v2 application-envelope cap, and it lives in the mapper
// rather than here for that same no-second-place reason; see the dropped counts below.
//
// THE THREE DROPPED COUNTS ARE INDEPENDENT AND NOT INFERABLE. Each is TWO CUTS' worth
// of loss: the count its turnevent.ContextUsage counterpart carries — the producer's,
// recorded when its entry or string caps fired — plus whatever the mapper's own frame
// budget removed from that same list to keep the envelope under the v2 cap
// (internal/turnbridge's maxContextUsageListBytes). The mapper ADDS to the base rather
// than replacing it, so this layer must still not infer loss from a retained list's
// length, and the three pairs are still never cross-read. Each list's original size
// stays recoverable as len(list) + its OWN dropped count, across both cuts.
//
// A NON-ZERO COUNT THEREFORE SAYS NOTHING ABOUT HOW MANY ENTRIES ARRIVED, and the
// sibling's shortcut does not transfer: SlashCommandListPayload.DroppedCommands is the
// same two-cut arithmetic on a frame with one list, where this frame divides one
// envelope across three and can cut all three at once. A client that treats a full
// list as proof nothing was dropped, or an empty one as proof everything was, is wrong
// in both directions.
type ContextUsagePayload struct {
	ConversationID     string                   `json:"conversation_id"`
	Model              string                   `json:"model"`
	TotalTokens        int                      `json:"total_tokens"`
	MaxTokens          int                      `json:"max_tokens"`
	Percentage         int                      `json:"percentage"`
	Categories         []ContextUsageCategory   `json:"categories"`
	DroppedCategories  int                      `json:"dropped_categories"`
	MCPTools           []ContextUsageMCPTool    `json:"mcp_tools"`
	DroppedMCPTools    int                      `json:"dropped_mcp_tools"`
	MemoryFiles        []ContextUsageMemoryFile `json:"memory_files"`
	DroppedMemoryFiles int                      `json:"dropped_memory_files"`

	// AsOf marks this reading as REMEMBERED rather than live (#2461), and is the
	// one key on this frame whose ABSENCE is the ordinary case. Present: the daemon
	// could not take a fresh reading and answered from the summary it stored when
	// claude last reported, and this is when that was. Absent: claude produced this
	// reading in response to this frame's own trigger.
	//
	// A CLIENT MUST READ IT BEFORE TRUSTING THE THREE INVENTORIES, and that is the
	// whole reason the key exists rather than being a nicety. A remembered answer
	// carries empty Categories, MCPTools and MemoryFiles because the daemon never
	// stored them — conversations.ContextUsageReading deliberately holds the five
	// headline values and nothing else — while MarshalJSON's own block promises that
	// an empty inventory is a POSITIVE reading, claude reporting no MCP tools. Those
	// two states are byte-identical without this key. The five headline numbers ARE
	// claude's, on both kinds of answer; it is the breakdown that is missing rather
	// than zeroed, and the three dropped counts are 0 for the same reason.
	//
	// Pointer plus omitempty, this file's ONE departure from its no-omitempty rule
	// (see the header). Both halves are forced. A plain time.Time would encode
	// "0001-01-01T00:00:00Z" on every live frame, because omitempty does not test a
	// struct; and a pointer WITHOUT omitempty — CompactionBoundaryPayload's shape,
	// which is how this file normally bends the rule — would put "as_of": null on
	// every live frame, moving bytes that two shipped producers already emit and
	// that internal/protocol/testdata/context_usage.json pins.
	// HelloClientPayload.LastSeenTS is the precedent for the shape.
	//
	// RFC 3339 WITH A Z OFFSET, and this layer does not enforce that — time.Time
	// marshals with whatever offset it carries. The normalisation lives at the one
	// door that stores a reading, conversations.Registry.SetLastContextUsage, so a
	// producer reaching the wire any other way is the thing to fix rather than a
	// formatter here. Daemon-authored, unlike every other non-id field on this
	// frame: it is the daemon's own clock at the moment it recorded, never a value
	// claude reported.
	AsOf *time.Time `json:"as_of,omitempty"`
}

// MarshalJSON normalises all three nil inventory slices to empty arrays. An empty
// inventory is a positive reading — claude reported no MCP tools, or no memory files
// — so clients receive [] rather than null and never have to distinguish the two.
// The value receiver keeps normalisation local and does not mutate the caller.
//
// One normaliser on the payload rather than three on the row types: the nil-to-[]
// question belongs to whoever owns the keys, and the rows encode correctly on their
// own. MCPStatusPayload is arranged the same way.
func (p ContextUsagePayload) MarshalJSON() ([]byte, error) {
	if p.Categories == nil {
		p.Categories = []ContextUsageCategory{}
	}
	if p.MCPTools == nil {
		p.MCPTools = []ContextUsageMCPTool{}
	}
	if p.MemoryFiles == nil {
		p.MemoryFiles = []ContextUsageMemoryFile{}
	}
	type alias ContextUsagePayload
	return json.Marshal(alias(p))
}

// RequestContextUsagePayload is the body of an Envelope whose Type ==
// TypeRequestContextUsage (docs/protocol-mobile.md § context_usage, published by
// #2431). The frame a client sends to ask for one conversation's context breakdown
// now, rather than waiting for the next turn to end.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. The frame it is answered with —
// ContextUsagePayload above — rides the other way and shares no type with it, so
// that type's mixed-provenance rule says nothing about this one. Here there is no
// mixture to reason about: the single field is remote-authored, full stop.
//
// IT NAMES A CONVERSATION, because a context window is conversation-scoped and this
// wire is multi-conversation. THE ID IS A LOOKUP KEY, NEVER A VALUE TRUSTED AS SENT:
// it is resolved against the daemon's own registry, the reported conversation_id in
// the reply comes out of the RESOLVED RECORD rather than being echoed back, and
// NAMING A CONVERSATION IS NOT AUTHORIZATION — authorization is pairing, enforced
// structurally at the Noise_IK handshake.
//
// LIKE RequestModelListPayload AND UNLIKE RequestHistoryPayload, THE ID NEVER
// BECOMES A PATH COMPONENT. That single difference is why a decode failure of this
// payload is TOLERATED rather than rejected: it leaves ConversationID empty, which
// reaches only a registry membership check and is refused there, where an empty path
// component would have resolved to a directory root.
//
// NO DETAIL KEY, and its absence is a decision rather than an omission. The daemon
// always asks claude at detail:"full"; see TypeRequestContextUsage's block for why a
// client-selected detail would let one client downgrade another's reading once
// closely-spaced asks collapse.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO REQUEST-ID
// KEY — TypeAttachmentStored's decision, transferred unchanged.
// TestRequestContextUsagePayload_WireKeys pins the key set so this is checked rather
// than reviewed.
//
// NO omitempty AND NO MarshalJSON, matching RequestModelListPayload and
// MCPStatusRequestPayload. There is no presence contract: absent and empty are the
// SAME case, "no conversation named", which names nothing and is refused, so nothing
// needs to tell them apart. TestRequestContextUsagePayload_ZeroValue_KeyPresent
// reddens if an omitempty is added later for tidiness — and the zero value is a
// REACHABLE state here, being exactly what the tolerated decode failure leaves.
type RequestContextUsagePayload struct {
	ConversationID string `json:"conversation_id"`
}

// ContextUsageCategory is one named contribution to a ContextUsagePayload reading.
// Both keys remain present even when Name is empty. Name is claude-authored
// descriptive text, already bounded by the producer; it is a label, never a selector
// a client may branch on for authority.
type ContextUsageCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// ContextUsageMCPTool is one MCP tool's contribution to a ContextUsagePayload
// reading. All three keys remain present even when the strings are empty.
//
// SERVERNAME HERE IS INERT, and the name collision with MCPReconnectPayload.ServerName
// is the trap this comment exists to defuse. That one crosses an actuation seam
// verbatim and is validated by nothing in this package or in internal/relay. This one
// names a contributor to a reading: it is never an actuation target, never an
// authorization input, and must not be fed to an MCP verb on the strength of having
// appeared here. Name carries the same constraint.
type ContextUsageMCPTool struct {
	Name       string `json:"name"`
	ServerName string `json:"server_name"`
	Tokens     int    `json:"tokens"`
}

// ContextUsageMemoryFile is one memory file's contribution to a ContextUsagePayload
// reading. All three keys remain present even when the strings are empty.
//
// PATH IS PATH-SHAPED DESCRIPTIVE TEXT, NOT A FILE HANDLE. Nothing on this path joins,
// cleans, resolves, or opens it — not this package, and not a later mapper either:
// normalising the string would imply it names a real file this frame acts on, which it
// does not. It is workspace-authored and unvalidated, so a client must render it as
// inert text rather than as a link, and must not open it on the strength of this frame.
// A committed fixture carries a traversal-shaped path so the pass-through is pinned by
// a test rather than by this comment alone. Type is claude's own label for the entry
// and carries the same constraint.
type ContextUsageMemoryFile struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Tokens int    `json:"tokens"`
}

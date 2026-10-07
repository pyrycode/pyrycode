package turnevent

// ModelAnnounced reports the model claude says it is running. It maps claude's
// system/init line (#1600) — the sixth claude line the parser translates rather
// than drops, and the fourth `system` subtype.
//
// It exists because it is the only thing that answers WHAT IS RUNNING. The daemon
// already carries a model on protocol.ScreenSnapshotPayload and
// protocol.SessionSettingsPayload, but both mean the PER-SESSION OVERRIDE — ""
// there means "inherited default, no per-session override" — so in the ordinary
// case the daemon publishes an empty string while claude has named a concrete
// model on every turn. The daemon knows what it ASKED FOR; only claude knows what
// it GOT.
//
// PER LINE, NOT PER SESSION, and that is the consumer hazard worth stating first.
// claude emits init once per TURN, so one session produces several of these and
// they need not agree: #1582 measured three in one session —
// [claude-sonnet-5, claude-sonnet-5, claude-haiku-4-5-20251001] — because the
// /model turn emits its OWN init and THAT one still reports the OLD model. A
// consumer that latches the first announcement shows a stale value; one that
// renders the latest has no problem to solve. The producer does not dedup: that
// would need turn state the parser deliberately does not hold. Documented here
// rather than mechanised, in the manner of ThinkingProgress's accumulator-residue
// hazard.
//
// It opens and closes no turn, exactly as the background-task variants do not: a
// per-turn announcement is not a turn boundary.
//
// The captured init line carries 24 keys and this variant carries the substance of
// ONE. Two of the omissions are why that matters: cwd is the operator's local
// filesystem path, and session_id is claude's session identity, NOT the daemon's
// conversation identity (#1380). Neither is even declared on the producer's decode
// target (streamsup's systemInitLine) — absent from the DECODE TARGET is a
// stronger guarantee than a reflection sweep, because a field that is never
// declared cannot leak.
//
// The count was 22 until #2252 corrected it against the capture #2251 committed,
// where all three init lines carry 24 and the newcomers are memory_paths and
// messaging_socket_path — two more of the operator's filesystem, and undeclared
// with the rest. Two of the 24 now have a variant: SessionFacts carries
// claude_code_version and permissionMode, for the reason this doc gives one
// paragraph up applied to two more facts of the same shape.
//
// The string field is claude-derived and bounded by the producer AT CONSTRUCTION
// (streamsup's maxModelField), following Unrecognized's precedent, so an oversized
// value never enters the event stream, a queue, or a log. Like every variant here
// it carries no conversation identity — the bridge injects that.
type ModelAnnounced struct {
	// Model is claude's announced identifier, VERBATIM: no lowercasing, no alias
	// expansion, no date-stamping, no family mapping, and no lookup against any
	// published model list. Never empty — the producer's gate does not emit on an
	// empty model.
	//
	// What claude announces is MEASURED, and the measurement is weaker than the
	// obvious guess. The rule the data supports is that claude echoes an identifier
	// AT LEAST AS SPECIFIC as the one it was given: it dates a bare family alias
	// (`haiku` → claude-haiku-4-5-20251001, the committed capture) and passes
	// through anything already fully formed (claude-haiku-4-5 in the
	// permission_protocol captures; claude-sonnet-5 for a machine default, #1582's
	// recorded run). So the value is NOT reliably dated, and it need not appear in
	// any published model list — claude-haiku-4-5 does not. That is an argument for
	// carrying the value untouched, not for repairing it here.
	//
	// BOUNDED AND UTF-8-VALID IS ALL IT IS. streamsup's truncateField scrubs
	// invalid UTF-8 (its cut can land mid-rune), but nothing on this path strips
	// control characters or terminal escape sequences, and the value's provenance is
	// only partly validated: a phone-supplied override passes internal/relay's
	// validModel charset check, but a --model flag or a config default never does.
	// A client renders it today — turnbridge.MapEvent maps the variant onto
	// protocol.ModelAnnouncedPayload (#1638) — so the client-facing slice owes the
	// sanitization at its own render boundary. Said here because that is where the
	// consumer reads.
	Model string
	// Truncated reports whether Model was cut to fit the producer's cap.
	//
	// A bool rather than the siblings' TruncatedFields []string, following
	// Unrecognized: the payload is a single string, so a named-field list would be
	// permanently either nil or ["model"] — a variable-length container carrying one
	// bit, plus a name the reader has to check against the only field there is. The
	// siblings use the slice because they bound TWO TO FOUR fields and the report
	// has to say which.
	Truncated bool
}

// SessionFacts reports what claude's own build IS and what posture it is running
// under. It maps the SECOND and THIRD substantive keys of the same system/init
// line ModelAnnounced maps (#2252), and it exists for that variant's stated
// reason applied to two more facts of the same shape: the daemon knows what it
// ASKED FOR, and only claude knows what it GOT. An unexpected posture is visible
// here instead of being discarded with the rest of the line.
//
// THE NAME IS #2253's, and the tension in it is worth meeting head-on rather than
// leaving a reader to notice. This variant declares no session identity at all:
// claude's session_id is claude's session and NOT the daemon's conversation
// (#1380), and it is not even declared on the producer's decode target. "Session"
// here names the CHILD RUN these two facts describe, not an identifier the event
// carries. The wire type came first because the downstream client needed a frozen
// shape (#2253), and the naming rule runs that way round — internal/protocol's
// codes doc states it: the wire follows this package's VARIANT — so the two agree
// deliberately.
//
// A SEPARATE VARIANT, NOT A WIDENED ModelAnnounced, and that was a scope decision
// rather than a shape that fell out. Widening a shipped payload is a
// wire-compatibility change, and the two carry facts with different consumers: a
// client renders the model on every turn, where a version and a posture are what an
// operator checks when something looks wrong. ModelAnnounced and its per-turn
// emission are untouched by this ticket.
//
// PER LINE, NOT PER SESSION, and ModelAnnounced's hazard transfers verbatim: claude
// emits init once per TURN, so one session produces several of these. They will
// ordinarily agree — a child's build cannot change under it, and a posture changes
// only when someone changes it — but the producer does not dedup and does not hold
// the turn state a dedup would need. A consumer wanting change-detection does it on
// its own side.
//
// It opens and closes no turn, exactly as ModelAnnounced does not: a per-turn
// report is not a turn boundary.
//
// NO effort FIELD, and its absence is MEASURED rather than an omission anyone
// forgot. #2251 captured the init line under the production spawn shape with an
// effort actually set — `--effort low` on the launch argv and `/effort high`
// acknowledged in band — and no init line carries an effort key. That measurement
// is machine-enforced in internal/streamsup by effortInitPins, whose reader refuses
// to compare a key set until both witnesses fire, so "claude does not publish it"
// is told apart from "nobody asked". Should a later claude start sending one, that
// pin reddens; declaring the field is the ticket to write then.
//
// The captured init line carries 24 keys and this variant carries two. Four of the
// omissions are why that matters: cwd is the operator's local filesystem path,
// session_id is claude's session identity, and memory_paths and
// messaging_socket_path are more of the operator's filesystem. None is declared on
// the producer's decode target (streamsup's systemInitLine) — absent from the
// DECODE TARGET is a stronger guarantee than a reflection sweep, because a field
// that is never declared cannot leak. MCP server status is a DIFFERENT frame's
// (#2202) and deliberately not folded in here.
//
// Both string fields are claude-derived and bounded by the producer AT
// CONSTRUCTION (streamsup's maxClaudeVersionField and maxPermissionModeField), so
// an oversized value never enters the event stream, a queue, or a log. Like every
// variant here it carries no conversation identity — the bridge injects that.
type SessionFacts struct {
	// ClaudeCodeVersion is claude's own build, VERBATIM, per ModelAnnounced.Model's
	// rule: no normalising, no zero-padding of components, no parse into a semver
	// triple, and no comparison against any release list. What claude prints is what
	// this carries.
	//
	// A STRING RATHER THAN A PARSED VERSION, and that is the field's whole judgement.
	// Two committed captures show 2.1.220 and 2.1.259, which would parse — but a
	// producer that parsed would have to decide what to do with the first build that
	// carries a suffix, a date, or a channel name, and every answer to that is
	// inventing rather than reporting. A consumer comparing versions does its own
	// parsing and keeps its own fallback.
	//
	// It may be empty. See the type's gate note: the producer emits when EITHER field
	// is present, so a line naming only a permission mode lands here as "".
	ClaudeCodeVersion string
	// PermissionMode is the posture claude says the child is running under —
	// `default`, `bypassPermissions`, `plan` and whatever claude ships next —
	// VERBATIM, per ModelAnnounced.Model's rule: no lowercasing, no alias expansion,
	// and no lookup against any published list.
	//
	// AN OPEN SET, DELIBERATELY, and this is the field where a membership check is
	// most tempting and most wrong. internal/streamsup's permissionModeAllowed does
	// bound a permission mode — the one the DAEMON may ask for on a control request,
	// where refusing an unknown value is correct because the daemon controls what it
	// sends. This value is claude's report of what it IS running, which the daemon
	// neither controls nor may reject, so an allow-list here would drop a real posture
	// report the first time claude ships a mode we have not heard of. That is the one
	// case an operator most needs to see.
	//
	// IT IS A CLAIM, NOT A GUARANTEE, and a consumer must not read it as an
	// authorization decision. A buggy or compromised claude can report `default` while
	// running under any posture at all; what this field proves is what claude SAID.
	// Nothing in the daemon gates on it, and the value is not fed back into any spawn
	// or control request.
	//
	// It may be empty, for ClaudeCodeVersion's reason.
	PermissionMode string
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, by their DAEMON names: "claude_code_version" and
	// "permission_mode" — the second is NOT claude's own `permissionMode`, exactly as
	// RateLimited's is "limit_type" and not claude's `rateLimitType`. nil when nothing
	// was cut, never an empty non-nil slice.
	//
	// The siblings' []string rather than ModelAnnounced's Truncated bool, and that
	// variant's doc states the rule both follow: a bool is right when the payload is a
	// single string and the list would be permanently either nil or one known name; a
	// named list is right the moment the report has to say WHICH of several was cut.
	// Two fields is where the rule flips.
	//
	// BOUNDED AND UTF-8-VALID IS ALL EITHER VALUE IS. streamsup's truncateField scrubs
	// invalid UTF-8 because its cut can land mid-rune, but nothing on this path strips
	// control characters or terminal escape sequences. A client rendering either value
	// owes the sanitization at its own render boundary — ModelAnnounced.Model's caveat,
	// applying to both fields here.
	TruncatedFields []string
}

// ContextUsage is one solicited reading of claude's context-window arithmetic.
// It is informational only: consumers may display it, but it does not replace the
// daemon-owned contextwindow.Read value used for control decisions.
//
// Every Claude-authored string and list count are bounded by streamsup at
// construction. Each list is independently ordered by descending token count so
// a producer-side count cut preserves its heaviest entries. The scalar integers
// remain Claude's own values; the daemon neither recomputes nor normalizes them.
type ContextUsage struct {
	Model              string
	TotalTokens        int
	MaxTokens          int
	Percentage         int
	Categories         []ContextUsageCategory
	DroppedCategories  int
	MCPTools           []ContextUsageMCPTool
	DroppedMCPTools    int
	MemoryFiles        []ContextUsageMemoryFile
	DroppedMemoryFiles int
}

// ContextUsageCategory is one named contribution to a ContextUsage reading. Name
// is bounded by the producer before this value enters the event stream.
type ContextUsageCategory struct {
	Name   string
	Tokens int
}

// ContextUsageMCPTool is one MCP tool's contribution to a ContextUsage reading.
// Name and ServerName are bounded by the producer before this value enters the
// event stream.
type ContextUsageMCPTool struct {
	Name       string
	ServerName string
	Tokens     int
}

// ContextUsageMemoryFile is one memory file's contribution to a ContextUsage
// reading. Path and Type are bounded by the producer before this value enters the
// event stream; Path is descriptive and is not opened by this event path.
type ContextUsageMemoryFile struct {
	Path   string
	Type   string
	Tokens int
}

// MCPStatus is one shape-recognised reading of the MCP servers Claude reports.
// It is internal-only in this slice: no wire adapter publishes it and no daemon
// behaviour is keyed on it. A present empty Servers slice is meaningful — Claude
// supplied mcpServers:[] — so the producer emits that value rather than collapsing
// it with a missing or unusable payload.
//
// Servers preserves Claude's order and retains at most the producer's
// maxMCPStatusServers entries. DroppedServers reports every entry omitted from the
// tail, so len(Servers) + DroppedServers is the decoded array's original length.
// The count is daemon-derived; every string below remains Claude-authored.
type MCPStatus struct {
	Servers        []MCPServerStatus
	DroppedServers int
}

// MCPServerStatus is one entry of MCPStatus, not an Event itself. It deliberately
// carries only the server object's top-level name, status, error and scope plus
// serverInfo.version. Config, tools and serverInfo.name are structurally absent, so
// consumers cannot accidentally recover credentials, argv, tool metadata or a
// second spelling of the server name from this value.
//
// All fields are copied without canonicalisation and remain untrusted,
// unsanitized text. Error is the one free-form prose field and is capped by the
// producer at maxMCPStatusError bytes with valid UTF-8 output. A future renderer
// must treat every string as inert Claude-authored content; publishing this report
// does not make any field suitable as an actuator or command argument.
type MCPServerStatus struct {
	Name    string
	Status  string
	Error   string
	Scope   string
	Version string
}

// ConversationReset reports that claude reset the conversation and mounted a
// fresh transcript under a new id. It maps claude's top-level
// `conversation_reset` line (#2134) — the announcement claude writes on its own
// stdout when a `/clear`, a plan-mode exit, or a fresh-session flow runs.
//
// It opens and closes no turn, exactly as ModelAnnounced does not: an
// announcement that a conversation was replaced is not a boundary inside one.
//
// IT CARRIES CLAUDE'S OWN IDENTITY ON PURPOSE, and that INVERTS the rule every
// sibling here follows — stated rather than left implicit, because a reader
// applying the family rule would delete the only field on the struct.
// BackgroundTaskStarted's doc and streamsup's systemTaskStartedLine both omit
// claude's session_id precisely because claude's session identity is NOT the
// daemon's conversation identity. Here that identity IS the payload: following
// claude to the transcript it just mounted is the entire reason the event
// exists. Like every variant here it still carries no conversation identity of
// the DAEMON's — the bridge injects that.
//
// This variant deliberately has no consumer arm on the interactive lane
// (cmd/pyry's interactiveTurnEmitterV2.Handle) and no wire shape
// (turnbridge.MapEvent drops it): the boundary a client draws comes from the
// session_transition frame, not from this event. #2135 is the consumer.
type ConversationReset struct {
	// NewConversationID is the id claude says it mounted the fresh transcript
	// under — a canonical lowercase UUID stem, VERBATIM per ModelAnnounced.Model's
	// rule: no lowercasing, no trimming, no re-formatting.
	//
	// CANONICAL BY CONSTRUCTION, AND NEVER EMPTY. The producer
	// (streamsup's emitConversationReset) runs the value through
	// transcript.ValidStem BEFORE constructing this event and emits nothing when
	// it fails, so a consumer holds a 36-character lowercase hex-and-hyphen stem
	// or holds no event at all. That gate is what lets a downstream resolver of
	// <dir>/<id>.jsonl treat the value as a filename component without re-deriving
	// the question — the alphabet excludes every path metacharacter, so traversal
	// cannot survive it. Validating in the producer is also the only option: this
	// package is standard-library-only (TestImportBoundary_StdlibOnly), so the
	// predicate cannot be imported here.
	//
	// NO CAP AND NO Truncated REPORT — a first for a claude-derived string in this
	// package, and the gate is the reason rather than an oversight. ValidStem is an
	// anchored full match at a FIXED length of 36 over a 17-character alphabet,
	// which is a strictly stronger bound than the producer's truncateField gives
	// any sibling field. A Truncated bool beside a fixed-length field would be
	// permanently false, which is what ModelAnnounced.Truncated's own doc argues
	// against carrying.
	//
	// WELL-FORMED IS NOT AUTHENTIC, and the distinction is load-bearing for the
	// consumer. The gate proves the id is SHAPED like a session stem; it does not
	// prove claude was entitled to name this one. A buggy or compromised claude can
	// announce any well-formed stem, including another session's. That is the same
	// trust the daemon already extends to claude for session ids, so the gate adds
	// protection without adding authority — a consumer that re-keys on this must
	// own the authorization question itself.
	NewConversationID string
}

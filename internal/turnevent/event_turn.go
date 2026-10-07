package turnevent

import "encoding/json"

// TextChunk is incremental assistant text, grouped by message.
// ParentToolCallID names the Agent/Task call that spawned the subagent producing
// the text, or is empty on the main conversation; it follows
// ToolStart.ParentToolCallID's rules.
type TextChunk struct {
	MessageID        string
	ParentToolCallID string
	Text             string
}

// ThoughtChunk is streaming reasoning ("thinking") text, grouped by message.
type ThoughtChunk struct {
	MessageID string
	// ParentToolCallID names the spawning Agent/Task call, or is empty for
	// main-thread thinking.
	ParentToolCallID string
	Text             string
}

// ToolStart announces a new tool invocation.
//
// RawInput is opaque tool input, carried as json.RawMessage so nothing on this
// path is forced to parse it; consumers decode it on their own terms.
//
// ParentToolCallID names the Agent/Task call that spawned the subagent making
// this call, or is empty on the main conversation. It is byte-identical to the
// parent's own ToolCallID, so a consumer groups children by joining on ids; a
// call from a nested subagent names the inner Agent call, and following ids
// rebuilds the tree. Empty has one meaning, "main thread": streamsup's
// parentToolUseID drops, never cuts, an unreadable value or one past
// maxTaskFieldID, because a cut join key matches no call while still looking
// like one. A dropped value degrades to top-level rendering, never to a wrong
// parent.
type ToolStart struct {
	ToolCallID       string
	ParentToolCallID string
	Title            string
	Kind             ToolKind
	RawInput         json.RawMessage
	Locations        []Location
}

// ToolUpdate carries changed fields of an existing tool call. Content may be
// nil for a status-only update. Content carries claude's own bytes and is capped
// downstream (turnbridge's maxResultSummaryRunes).
//
// ResultDetail is a short daemon-composed précis of an edit's or write's outcome
// ("+10 −3", "created · 54 lines"), built by streamsup's toolResultDetail from the
// tool_use_result sidecar claude writes beside each result. Reads, shell, search
// and unrecognised shapes send "". It holds no claude-supplied byte: only digits,
// spaces, ASCII letters and the separators U+2212 MINUS SIGN and U+00B7 MIDDLE
// DOT, with length bounded by int64's range. Render it verbatim and never parse
// it; the unit words are in the string so a client need not switch on a tool
// name.
//
// ParentToolCallID is ToolStart's field with ToolStart's meaning, read off the
// `user` line this update was mapped from rather than copied from the ToolStart:
// the parser holds no cross-line state for it, so the two agreeing is a fact
// about claude's wire.
type ToolUpdate struct {
	ToolCallID       string
	ParentToolCallID string
	Status           ToolStatus
	Content          ToolContent
	ResultDetail     string
}

// ToolProgress reports the elapsed time of a tool call that ToolStart already
// announced. It maps claude's top-level tool_progress heartbeat and, unlike a
// ToolUpdate from streamsup, is a non-terminal reading that may repeat until the
// tool_result closes the row.
//
// ToolCallID is read from the heartbeat's parent_tool_use_id, the handle
// ToolStart published, not from its synthetic per-tick tool_use_id. The
// producer requires a non-empty JSON string of at most streamsup's
// maxTaskFieldID and drops the whole event otherwise; it never cuts the id.
//
// ElapsedSeconds is claude's signed integer reading, verbatim. Zero means claude
// omitted it or reported zero; a negative value stays observable. The daemon
// infers no cadence, accumulates nothing and keeps no per-call state.
//
// The heartbeat's tool_name is not carried: ToolStart already published it.
type ToolProgress struct {
	ToolCallID     string
	ElapsedSeconds int
}

// TurnEnd marks the end of a claude turn. Every field except ErrorCategory is
// read off claude's `result` line; ErrorCategory comes from an `assistant` line
// earlier in the same turn, which is why streamsup's Parser holds it across
// lines (see that type's doc).
//
// Two classifications ride this variant and are never reconciled. Reason is the
// daemon's two-value reading (streamsup's resultTurnEndReason); Outcome is
// claude's own token for the same stop. A turn that hit --max-turns is
// end_turn/error_max_turns and both halves are true; a consumer that prefers one
// makes a truncated run read as a finished answer. ACP models end-of-turn as
// session/prompt's stopReason return, not an event; converting back is the ACP
// adapter's job.
//
// The window fields ride here because claude reports them on the `result` line
// that is the turn boundary. They are not published: turnbridge.MapEvent builds
// protocol.TurnEndPayload field by field and names every field here except
// ModelWindows and DroppedModelWindows.
//
// TurnEnd holds a slice, so it is not comparable: == on two Event values that
// both hold a TurnEnd panics.
//
// Background: turnevent-package-outbound-event-variants.md and
// streamsup-package-result-stop-shape-second-decode-target-and-dr.md.
type TurnEnd struct {
	Reason TurnEndReason
	// ModelWindows is claude's reported context window for each model the turn
	// touched, one entry per model id claude used, sorted by ModelID. claude
	// sends a JSON object, so the sort is the daemon's; it makes which entries
	// survive the count cap reproducible.
	//
	// Every entry carries a usable window: the producer drops an entry whose
	// contextWindow is absent, zero or negative. Two entries need not mean two
	// models: the captures show a helper model beside the session's own, and
	// alias pairs naming one model twice (claude-haiku-4-5 and
	// claude-haiku-4-5-20251001), so the list is carried whole and keyed by
	// claude's id. An absent, null, empty or undecodable modelUsage, and a map
	// whose every entry was dropped, all read as nil.
	//
	// Bounded at construction in both dimensions: entry count by streamsup's
	// maxModelWindowEntries, id length by maxModelWindowID.
	ModelWindows []ModelWindow
	// DroppedModelWindows counts entries claude sent that this event does not
	// carry, for any cause (unusable window, over-long id, count cap); 0 when
	// none. len(ModelWindows) + DroppedModelWindows is the size of claude's map.
	// Every bound here drops rather than truncates, so one daemon-derived counter
	// is enough.
	DroppedModelWindows int
	// Outcome is the subtype on claude's `result` line: how the turn stopped,
	// where Reason says only whether it was cancelled. Observed values are
	// success, error_during_execution, error_max_turns, error_max_budget_usd and
	// error_max_structured_output_retries. It is an open set carried verbatim, and
	// is not claude's `stop_reason` key, which the daemon does not forward.
	//
	// Empty means absent or over cap: streamsup's maxTurnEndStopField drops an
	// over-long value rather than cutting it, because a cut token matches nothing
	// a consumer could act on.
	//
	// Security: claude-authored, bounded and not sanitized. It reaches clients, so
	// the client's render boundary owes control-character and escape stripping;
	// see docs/protocol-mobile.md § turn_end.
	Outcome string
	// IsError is claude's is_error flag off the same line, false when absent. It
	// is not derived from Outcome and must not be: claude sends subtype success
	// with is_error true when a turn ends on an API error (a context overflow is
	// the documented case).
	IsError bool
	// TerminalReason is claude's terminal_reason, the finer-grained cause beside
	// the subtype (max_turns, budget_exhausted, prompt_too_long, hook_stopped,
	// completed, and more: claude documents nineteen and does not close the
	// list). It is what makes a context overflow legible, since that subtype is
	// success. Outcome's rules apply: open set, verbatim, empty when absent or
	// over maxTurnEndStopField, untrusted.
	TerminalReason string
	// ErrorCategory is the API error a wrapper-level `error` key reported on an
	// `assistant` line in this turn (a sibling of `message`, never a content
	// block). Documented values are authentication_failed, oauth_org_not_allowed,
	// account_on_hold, billing_error, rate_limit, overloaded, invalid_request,
	// model_not_found, server_error, max_output_tokens and unknown. Outcome's
	// rules apply.
	//
	// It is claude's report, not the daemon's finding, and several values name an
	// account state rather than a turn state. Attribute it to claude wherever it
	// is shown; never present it as daemon chrome. Nothing in the daemon acts on
	// it (no retry, backoff, teardown or routing); making it an actuator needs a
	// security review.
	ErrorCategory string
	// DurationMS is how long this turn took, in milliseconds, per the `result`
	// line. Per turn, not a running total: it falls between turns in 13 of the
	// 21 multi-turn captures under internal/e2e/realclaude/testdata.
	DurationMS int
	// DurationAPIMS is claude's duration_api_ms for the session so far: a running
	// total, not this turn's API time. It exceeds DurationMS on 53 of 57 observed
	// result lines, including the first line of 19 of the 21 multi-turn captures,
	// so differencing consecutive turns does not recover a per-turn value either.
	// The daemon neither differences it nor remembers earlier lines.
	DurationAPIMS int
	// NumTurns is claude's num_turns for this turn: model round-trips, not a count
	// of session turns. Per turn, like DurationMS.
	NumTurns int
	// CostUSDTotal is claude's total_cost_usd: what the session has cost so far,
	// in US dollars, a running total like DurationAPIMS. Spelled cost_usd_total
	// on the wire. It is claude's estimate, not a billing statement; the daemon
	// neither computes nor verifies it.
	CostUSDTotal float64
	// InputTokens, OutputTokens, CacheReadTokens and CacheCreationTokens are the
	// four counts in the result line's usage object for this turn. InputTokens is
	// uncached input only and the two cache counts are the cached-input parts, so
	// InputTokens + OutputTokens is not the turn's total.
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	// The numbers above, from DurationMS on, share these rules.
	//
	// They are copied without summing, conversion, clamping, range or ordering
	// checks. Absent, null and unreadable read as zero, and zero is also a value
	// claude sends: compaction_v2.1.259.json reports duration_api_ms 0 and
	// num_turns 0 on a line with non-zero duration and cost. There is
	// deliberately no DurationAPIMS <= DurationMS check, which would reject most
	// observed lines, and the fields are signed so a negative reading is not
	// wrapped into a huge positive one.
	//
	// Security: a JSON number cannot carry control characters or markup, and the
	// Go types bound the formatted size, so no cap or sanitization applies. They
	// are still claude's unverified numbers: attribute them to claude, and never
	// show CostUSDTotal as the surface's own accounting. Nothing in the daemon
	// acts on any of them, including no budget or spend enforcement.
}

// ModelWindow is one entry of TurnEnd.ModelWindows: the context window claude
// reported for one model. It is not an Event.
//
// claude's entry also carries maxOutputTokens, which nothing reads, and
// canonicalModel, which only some claude versions send (present at v2.1.220 and
// v2.1.239, absent at v2.1.143, v2.1.158 and v2.1.199). Neither is on the
// producer's decode target (streamsup's resultLine), so no consumer can come to
// depend on them.
type ModelWindow struct {
	// ModelID is the key claude used in its modelUsage map, verbatim per
	// ModelAnnounced.Model's rule. It need not be dated, and an alias pair may
	// name one model twice (see TurnEnd.ModelWindows).
	//
	// Security: claude-authored, bounded by streamsup's maxModelWindowID and not
	// sanitized. It is not published to clients; whoever publishes it owes
	// sanitization at the client's render boundary.
	//
	// An id past the cap is dropped with its entry, never cut: a cut id names no
	// model, and a consumer joining on it would silently match nothing.
	ModelID string
	// WindowTokens is the context window claude reported for this model, in
	// tokens, always > 0. An int, matching contextwindow.Usage.WindowTokens, the
	// value it feeds. It is claude's reported number rather than one inferred from
	// the model name, which matters: a session announced as plain claude-sonnet-5,
	// with no [1m] marker, reported a 1M window.
	WindowTokens int
}

// Location is a file a tool call touches (ACP tool-call location). Line is
// 1-based; 0 means unspecified.
type Location struct {
	Path string
	Line int
}

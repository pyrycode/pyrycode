package protocol

import "encoding/json"

// Interactive v2 event payloads. These are additive, capability-gated
// application events sent binary → phone when the phone has advertised the
// "interactive" capability at handshake (docs/protocol-mobile.md
// § Interactive events). They are the mobile adapter's wire representation
// of internal/turnevent's neutral turn-event model; the mapping from
// internal events to these envelopes, the push, and the capability
// intersection live in the consumer (#608), NOT here. This file is wire
// vocabulary only: pure structs and their (de)serialization.
//
// No field carries omitempty: every field is always present on the wire so
// the testdata fixtures pin the full shape and boundary values like seq:0
// and is_error:false do not silently vanish.

// TurnStatePayload is the body of an Envelope whose Type == TypeTurnState
// (docs/protocol-mobile.md § turn_state). Binary → phone direction; signals
// a coarse change in the turn's lifecycle. State is one of "thinking",
// "responding", "idle"; it stays a plain string (not a named enum),
// matching the MessagePayload.Role precedent. The exact internal
// event → state mapping is #608's.
type TurnStatePayload struct {
	ConversationID string `json:"conversation_id"`
	State          string `json:"state"`
}

// AssistantDeltaPayload is the body of an Envelope whose Type ==
// TypeAssistantDelta (docs/protocol-mobile.md § assistant_delta). Binary →
// phone direction; an incremental, coalesced chunk of assistant text. Seq
// is a per-turn, non-negative delta-ordering counter that resets each turn
// (distinct from the session-monotonic Envelope.ID).
//
// ParentToolUseID names the Agent call that produced attributed text and is
// empty for main-thread text. It has ToolUsePayload.ParentToolUseID's exact
// grouping-hint-not-a-capability semantics: clients may join it to a parent
// ToolUseID for display, but must not treat a match as authority. The key has
// no omitempty so the main-thread value is always emitted as an empty string.
type AssistantDeltaPayload struct {
	ConversationID  string `json:"conversation_id"`
	TurnID          string `json:"turn_id"`
	Seq             int    `json:"seq"`
	ParentToolUseID string `json:"parent_tool_use_id"`
	Text            string `json:"text"`
}

// ToolUsePayload is the body of an Envelope whose Type == TypeToolUse
// (docs/protocol-mobile.md § tool_use). Binary → phone direction; announces
// a tool invocation. InputSummary is a human-readable précis of the tool
// input, not the raw input.
//
// Input is the tool input's own top-level fields (#1678), each value the
// input's value verbatim — a JSON string decoded, any other JSON type in its
// compact JSON form — so a client can show what a call ACTS ON instead of the
// first 200 runes of a compacted blob. It is what InputSummary could never be:
// per-field rather than one flattened line, so an Edit's file_path survives
// alongside the replaced text rather than being buried inside it.
//
// The bounds are the bridge's, decided at construction
// (internal/turnbridge's maxInputValueRunes / maxInputKeyRunes /
// maxInputFields / maxInputTotalRunes), so this struct re-decides no maximum,
// for RateLimitedPayload's reason: a second cap here would be a second place
// the limit is decided and the two could disagree silently. A value the bridge
// shortened ends in "…" — this wire's value-level truncation convention, the
// one InputSummary already uses — and there is deliberately no
// truncated_fields list to name the cut or dropped fields (#1678 decided this
// explicitly). The known cost is that a value legitimately ending in "…" is
// indistinguishable from a cut one.
//
// A field can be ABSENT because the total bound dropped it; InputSummary
// remains the whole-input fallback. Key order on the wire is alphabetical, a
// marshalling artefact of the map rather than the input's own order, so
// display order is the client's choice. The key is always present and never
// null — see MarshalJSON.
//
// This map is what makes ToolUsePayload non-comparable with ==. Every existing
// comparison already goes through reflect.DeepEqual or byte equality; a future
// == against an any-typed copy would compile and panic at runtime.
//
// SECURITY: the values are DISPLAY STRINGS, NOT CAPABILITIES. They are
// model-authored text that crossed the subprocess trust boundary and that the
// daemon neither resolved nor validated — a file_path is not canonicalised and
// may be relative or traversing, and a Bash command value is a literal shell
// command line. A client may render them as inert text; it must never open one
// as a path on its own filesystem, execute or re-shell one, or feed one to an
// HTML sink, an attribute, or a URL.
// ParentToolUseID (#2191) names the Agent/Task call that spawned the subagent
// making this call, and is EMPTY on the main conversation. A client joins a child
// to its parent on the ToolUseID these frames already carry — the two are
// byte-identical — so three parallel subagents render as three collapsible groups
// instead of thirty rows interleaved into the main thread. Nesting needs no extra
// field: the value is claude's own, read verbatim with no branch on depth, so a
// call made by a subagent that a subagent spawned names the INNER Agent call and
// following ids rebuilds the whole tree.
//
// IT IS A GROUPING HINT, NOT A CAPABILITY, and it takes Input's rule above. It is
// model-authored text that crossed the subprocess trust boundary; the daemon
// neither resolved it nor checked that it names a call this client has seen.
// Render the row under a parent when the id matches one, render it at top level
// when it does not, and never dereference it as anything else. Nothing in the
// daemon branches on the value.
//
// Empty has exactly ONE meaning, "main thread", which is what makes it safe to
// drop an unusable value into: the producer (streamsup's parentToolUseID) empties
// a value it cannot read or that exceeds its cap rather than cutting it, because a
// cut join key matches no ToolUseID while still looking like one. So an emptied
// id degrades to top-level rendering — the behaviour before this field existed —
// rather than to a wrong parent. There is deliberately no report naming which
// emptiness it is; unlike ToolDeniedPayload's fields, this one has no second
// meaning for a report to disambiguate.
//
// It carries no omitempty, per this file's rule: absence and "" mean the same
// thing, and always emitting the key keeps the testdata fixture pinning the full
// shape. A client built before this landed ignores the unknown key.
type ToolUsePayload struct {
	ConversationID  string            `json:"conversation_id"`
	TurnID          string            `json:"turn_id"`
	ToolUseID       string            `json:"tool_use_id"`
	ParentToolUseID string            `json:"parent_tool_use_id"`
	Name            string            `json:"name"`
	InputSummary    string            `json:"input_summary"`
	Input           map[string]string `json:"input"`
}

// MarshalJSON normalises a nil Input to an empty map, so a tool call with no
// sendable fields always serialises as "input":{} and never as "input":null.
//
// This is BackgroundTaskRosterPayload.MarshalJSON's pattern applied to a map,
// and its doc comment is the single source of truth for why the deviation is
// worth the lines and why a doc comment alone would not have been enough. The
// same reasoning holds here: the bridge (#1678) returns a nil map for all three
// no-fields cases — an absent input, an empty object, and an input that is not
// a JSON object at all — so without this method those would ship null while the
// fixture went on asserting {}, and nothing in `make check` would notice.
//
// Between null and {}, {} is the better client contract for the reason [] beats
// null there: it is iterable without a branch in every client language. The
// payload deliberately does not distinguish the three no-fields cases, so there
// is nothing for null to mean that {} does not.
func (p ToolUsePayload) MarshalJSON() ([]byte, error) {
	if p.Input == nil {
		p.Input = map[string]string{}
	}
	type alias ToolUsePayload
	return json.Marshal(alias(p))
}

// ToolResultPayload is the body of an Envelope whose Type == TypeToolResult
// (docs/protocol-mobile.md § tool_result). Binary → phone direction;
// reports the outcome of the tool_use with the matching ToolUseID.
// ResultSummary is a human-readable précis of the result, not the raw
// output.
//
// ResultDetail (#2024, #2025) is a short précis of the call's STRUCTURED outcome
// — "265 lines", "110 of 1676 lines", "+10 −3", "created · 54 lines", "5 files"
// — composed by the daemon from the tool_use_result sidecar. Empty means "no
// count", which is the answer for every sidecar shape the producer does not
// recognise; a client renders it beside the row and never parses it. The unit
// words are carried here on purpose, because a client cannot tell a read from a
// search without switching on a tool name.
//
// RENDER IT VERBATIM. The separators are U+2212 MINUS SIGN (not a hyphen) and
// U+00B7 MIDDLE DOT spaced on both sides; they are part of the contract, so a
// client that substitutes ASCII lookalikes is showing something this daemon did
// not send.
//
// The two summary fields do NOT share a provenance, and a reader who assumes
// they do will reason wrongly about both. ResultSummary is claude's own text
// passed through under a rune cap (turnbridge's maxResultSummaryRunes).
// ResultDetail contains no claude-supplied byte: its producer formats decoded
// integers and its own literals, so its alphabet is digits, spaces, ASCII
// letters and the two glyphs named above, and its length is bounded at
// construction rather than by a cap here.
//
// It carries no omitempty, per this file's rule: absence and "" mean the same
// thing (no count), and always emitting the key keeps the testdata fixture
// pinning the full shape. A client built before this landed ignores the unknown
// key; one built after it decodes a frame that lacks the key to "" without
// error, so the field is optional in the sense that binds.
// ParentToolUseID (#2191) is ToolUsePayload's field with ToolUsePayload's meaning
// and ToolUsePayload's grouping-hint-not-a-capability rule, carried here so a
// result row groups with the call row it completes. Read that docblock; it is the
// single source of truth for both.
//
// The BOUND matters more on this frame than on that one, which is why the producer
// caps it at all. tool_result is never-droppable control class (§ Error codes,
// 4413): a frame over the application-envelope cap is LOST, not truncated, so an
// unbounded claude-authored string here would cost the whole row rather than
// shorten it. The value is bounded at construction at 256 bytes, which is why the
// worst case computed for ResultSummary above still holds.
type ToolResultPayload struct {
	ConversationID  string `json:"conversation_id"`
	TurnID          string `json:"turn_id"`
	ToolUseID       string `json:"tool_use_id"`
	ParentToolUseID string `json:"parent_tool_use_id"`
	IsError         bool   `json:"is_error"`
	ResultSummary   string `json:"result_summary"`
	ResultDetail    string `json:"result_detail"`
}

// ToolProgressPayload is the body of an Envelope whose Type == TypeToolProgress.
// It updates the open ToolUsePayload row with the matching ToolUseID. The elapsed
// value is claude's signed reading, forwarded verbatim rather than computed,
// clamped, or accumulated by the daemon.
//
// The join id was already bounded at construction by the turnevent producer. It
// remains an untrusted display handle rather than a capability: neither this
// package nor the outbound bridge acts on it. No field uses omitempty, so zero
// values remain explicit on the wire like every other interactive-event payload.
type ToolProgressPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	ToolUseID      string `json:"tool_use_id"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
}

// ToolDeniedPayload is the body of an Envelope whose Type == TypeToolDenied
// (docs/protocol-mobile.md § tool_denied, #2233). Binary → phone direction; the wire
// form of turnevent.ToolCallDenied, which reports that claude REFUSED to run a tool
// call it had already announced. Turn-scoped like the two frames it joins, so unlike
// the conversation-scoped status peers below it carries a turn_id.
//
// IT IS NOT A WIDER tool_result, AND IT COULD NOT HAVE BEEN. claude states the denial
// on a line that arrives BEFORE the tool result, so folding it in would need the parser
// to latch cross-line state keyed by tool_use_id; and #2234's result-line recovery
// reports denials for calls whose tool_result frame has already shipped, which a field
// on a sent frame cannot answer. A client joins this row to the tool_use and
// tool_result carrying the same ToolUseID — the identifier is byte-identical across all
// three, which is the whole point of publishing it.
//
// ToolName IS SPELLED tool_name WHERE ToolUsePayload SPELLS THE SAME VALUE name, and
// the divergence is deliberate rather than drift. On that frame the tool IS the
// subject, so an unqualified name is unambiguous; here it is one named thing among
// several. The binding reason is the report slices below: they name fields by keys THIS
// frame carries, so the token and the wire key have to be the same string.
//
// THE TWO REPORT SLICES ARE THE FRAME'S OWN VOCABULARY, NOT THE DAEMON'S INTERNAL ONE.
// turnevent.ToolCallDenied names the id tool_call_id — its own field name — and the
// bridge (internal/turnbridge's deniedReportKeys) rewrites exactly that one token to
// tool_use_id on the way here. A token naming a key the frame does not carry is one a
// client cannot look up, which would make the report unusable precisely where it
// matters most. Every other token already names a key declared below.
//
// THE THREE-VALUED READING THE SLICES MAKE DECIDABLE is what they are for, and it is
// the reason a second slice exists at all. For any field named in either:
//
//   - empty and named in NEITHER slice — claude sent nothing.
//   - empty and named in dropped_fields — the daemon emptied an over-cap value.
//   - present and named in truncated_fields — the daemon cut claude's text to fit.
//
// Without dropped_fields an empty ToolName could not be told from a tool_name claude
// never sent. RateLimitedPayload.TruncatedFields carries only the first distinction
// because on that event nothing drops; CompactionBoundaryPayload.Trigger drops with no
// report because it is the only droppable value there, so an empty trigger is already
// unambiguous. Here three fields drop, and the ambiguity is real.
//
// BOTH SLICES ARE nil WHEN NOTHING FIRED, AND nil REACHES THE WIRE AS null. This type
// therefore has NO MarshalJSON, and the absence is deliberate: RateLimitedPayload is the
// precedent, and BackgroundTaskRosterPayload.MarshalJSON's nil→[] means the OPPOSITE and
// does not generalise. An empty roster is a positive statement; nothing-was-cut is an
// absence. An allocated [] here would tell a phone that claude's cut text is complete.
// No omitempty either, per this file's rule, so both keys are always present.
//
// SECURITY: Message and DecisionReason are claude-authored PROSE that crossed the
// subprocess trust boundary — bounded by the daemon (internal/streamsup's
// maxDenialProse) and NOT sanitized. A rule-based denial may quote THE COMMAND LINE THAT
// WAS REFUSED, which carries whatever an operator typed, and every captured denial names
// absolute host paths of the session's allowed working directories. They take
// UnrecognizedMessagePayload.Raw's rule: render as inert text ATTRIBUTED TO claude, never
// as the daemon's own finding, and never feed either to an HTML sink, an attribute, a
// URL, or anything that executes or re-shells it. ToolName and DecisionReasonType are
// short TOKENS from open sets and take the NARROWER TurnEndPayload.Outcome rule instead —
// switch on them against known values, treat an unrecognised one as unknown — the same
// prose-versus-token split CompactingPayload.ErrorText and CompactionBoundaryPayload.Trigger
// draw next door. Every bound is the producer's, decided at construction, so this struct
// re-decides no maximum: a second cap here would be a second place the limit is decided
// and the two could disagree silently. The frame is a REPORT, never a control input —
// NOTHING IN THE DAEMON KEYS A BEHAVIOUR ON ANY FIELD HERE, which is what keeps a
// fabricated denial a misleading label rather than an actuator.
type ToolDeniedPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	// ToolUseID is the refused call, byte-identical to the tool_use and tool_result
	// frames for the same call. Empty and named in dropped_fields means the daemon
	// emptied an over-cap id rather than cutting it: a cut join key joins to nothing
	// while still looking like a real handle, which is strictly worse than an absence
	// a client can see.
	ToolUseID string `json:"tool_use_id"`
	// ToolName is claude's name for the tool it refused ("Bash" in every captured
	// denial). An OPEN SET carried verbatim; dropped rather than cut when over-cap,
	// since a consumer matches this token against claude's tool set.
	ToolName string `json:"tool_name"`
	// DecisionReasonType is claude's word for WHAT denied the call — "classifier",
	// "asyncAgent", "mode" and "rule" are the documented values. OBSERVED ABSENT IN
	// EVERY CAPTURED DENIAL at claude 2.1.239, so empty is the ordinary reading here
	// rather than the exceptional one; the slices above say which emptiness it is.
	DecisionReasonType string `json:"decision_reason_type"`
	// DecisionReason is claude's free-form explanation, absent in every captured
	// denial exactly as DecisionReasonType is. Cut rather than dropped and reported in
	// truncated_fields, on Message's reasoning: a cut sentence still reads as what it
	// is. Carries Message's SECURITY reading verbatim.
	DecisionReason string `json:"decision_reason"`
	// Message is claude's rejection text — the prose it also writes into the
	// tool_result. Cut rather than dropped, reported in truncated_fields.
	Message string `json:"message"`
	// TruncatedFields names the fields the daemon CUT to fit their caps, in this
	// struct's declaration order, under this frame's wire keys: "decision_reason",
	// "message". nil when nothing was cut, never an empty slice.
	TruncatedFields []string `json:"truncated_fields"`
	// DroppedFields names the fields the daemon EMPTIED for exceeding their caps, in
	// declaration order, under this frame's wire keys: "tool_use_id", "tool_name",
	// "decision_reason_type" — tool_use_id rather than the daemon-internal
	// tool_call_id, which is what the bridge's rename exists to guarantee. nil when
	// nothing was dropped.
	DroppedFields []string `json:"dropped_fields"`
}

// TurnEndPayload is the body of an Envelope whose Type == TypeTurnEnd
// (docs/protocol-mobile.md § turn_end). Binary → phone direction; marks the
// end of an assistant turn. StopReason carries the
// internal/turnevent.TurnEndReason string values verbatim ("end_turn",
// "max_tokens", "max_turn_requests", "refusal", "cancelled"); it stays a
// plain string because internal/protocol is a stdlib-only leaf data package
// and does NOT import internal/turnevent — #608 produces the field via
// string(turnevent.TurnEnd.Reason). The wire-value/taxonomy alignment is
// documented, not enforced by a shared type.
//
// THREE FIELDS CARRY THE STOP SHAPE claude itself reported (#2223), beside
// StopReason rather than instead of it. StopReason's values and its derivation are
// unchanged and byte-identical for every subtype; the three below are what let a
// client tell a turn that hit --max-turns, exhausted a budget or overflowed its
// context apart from a finished answer, all of which used to arrive as a clean
// "end_turn".
//
// TWO FIELDS ARE SPELLED LIKE A stop_reason AND NEITHER IS THE OTHER. StopReason
// is the daemon's two-value classification; Outcome is claude's own subtype token.
// claude's `result` line ALSO carries a key literally named stop_reason, and this
// daemon does not forward it under any name — a value seen on this wire is always
// one of the two fields declared here.
//
// A FOURTH FIELD, ErrorCategory (#2224), rides beside them without joining them: it
// is read off an `assistant` line rather than the `result` line, and it names why the
// API stopped serving the turn rather than how the turn ended. It takes every wire
// rule the three below state — open set, no omitempty, claude-authored, bounded and
// unsanitized — and adds one they do not need, at its own declaration.
//
// FOUR NUMBERS RIDE BESIDE THEM (#2260) and they are a different CLASS from every
// field above: claude-authored like the four strings, but numeric, so none of the
// string rules transfers. They are declared individually below and share the rules in
// the four paragraphs after this one.
//
// TWO OF THE FOUR ARE RUNNING TOTALS AND TWO ARE PER TURN, and the pair that looks
// most alike is the pair that disagrees — DurationAPIMS is a session total while
// DurationMS is this turn's. The daemon differences neither and holds no previous
// line's value to difference one against.
//
// A ZERO IS A NUMBER CLAUDE SENT, not a field the daemon failed to read, and a client
// that treats one as a gap mislabels a real turn. claude reports duration_api_ms 0 and
// num_turns 0 on an observed line whose duration_ms is 15617 and whose cost is
// non-zero. Absent, null, an unreadable value and an explicit 0 are one reading, and
// no field distinguishes them because nothing a client does depends on which it is.
//
// NOTHING IS CLAMPED, RANGE-CHECKED OR ORDERED — RateLimitedPayload's posture for its
// own two numbers. In particular the daemon applies NO duration_api_ms <= duration_ms
// consistency check, because it would reject the ordinary case, and a negative arrives
// as claude sent it.
//
// SECURITY: claude-authored, and threat 1 lands OUTWARD — but NOT in the shape
// Outcome's SECURITY paragraph states, so a client must not carry that paragraph
// across. A JSON number holds no control character, terminal escape, markup or URL, so
// there is no sanitization owed and no length bound to look for; these grow no frame
// because the bound is over the type's range rather than over claude's input length.
// What DOES land is the misattribution half: the daemon verifies none of these
// numbers, so rendering cost_usd_total as the daemon's own accounting of the
// operator's spend presents model-authored data as trusted chrome — error_category's
// trap reached through a number instead of a token. Attribute all four to claude.
// Nothing in the daemon acts on any of them, spend enforcement included.
//
// No omitempty on any of them, per this file's rule as stated at ToolResultPayload:
// absence and the zero value mean the same thing, always emitting the key keeps the
// testdata fixture pinning the full shape, a client built before this landed ignores
// the unknown keys, and one built after decodes a frame lacking them to the zero
// value without error — which is the sense in which they are optional. It holds for
// the four numbers as written: a pointer would offer an absent-versus-zero
// distinction, and there is none to offer, since claude sends zeros itself.
type TurnEndPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	StopReason     string `json:"stop_reason"`
	// Outcome is claude's `result` subtype — an OPEN SET, carried verbatim. The
	// observed values are success, error_during_execution, error_max_turns,
	// error_max_budget_usd and error_max_structured_output_retries; a client must
	// treat an unrecognised token as unknown rather than as an error, because
	// claude may ship one at any time.
	//
	// SECURITY: claude-authored text. The daemon BOUNDS it (internal/streamsup's
	// maxTurnEndStopField) and does NOT sanitize it — no control-character or
	// terminal-escape stripping happens on this path — so the render boundary
	// owing that sanitization is the CLIENT's, exactly as ModelOption's SECURITY
	// paragraph states for the strings beside it. Empty means claude sent none or
	// the value was past its bound; a client answers both the same way.
	Outcome string `json:"outcome"`
	// IsError is claude's own is_error flag for the turn. NOT derivable from
	// Outcome: claude sends subtype `success` with is_error true when the turn
	// ended on an API error, a context overflow being the documented case, so a
	// client inferring this from the subtype reads exactly that turn as clean.
	IsError bool `json:"is_error"`
	// TerminalReason is claude's finer-grained cause beside the subtype — the most
	// open of the three sets, nineteen values documented and explicitly not closed
	// (max_turns, budget_exhausted, prompt_too_long, hook_stopped, completed, …).
	// It is what makes a context overflow legible at all, since that turn's subtype
	// is plain `success`. Carries Outcome's SECURITY paragraph unchanged.
	TerminalReason string `json:"terminal_reason"`
	// ErrorCategory is the API error an `assistant` line in the turn reported at the
	// wrapper level (#2224): rate_limit, overloaded, account_on_hold,
	// authentication_failed, billing_error, invalid_request, model_not_found,
	// server_error, oauth_org_not_allowed, max_output_tokens, unknown — an OPEN set
	// on the three fields above's rule, empty when claude sent none or the value was
	// past its bound. It answers a question the other three cannot: WHY the API
	// stopped serving the turn, rather than how the turn ended.
	//
	// A DIFFERENT KIND OF CLAIM FROM ITS NEIGHBOURS, and a client that renders it
	// like them gets it wrong. Outcome and TerminalReason describe the TURN; half of
	// these values describe the ACCOUNT, and the daemon verifies none of them — it
	// carried a string off claude's stdout. Show it as claude's report, never as the
	// daemon's own finding about the operator's billing or credentials.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized —
	// Outcome's SECURITY paragraph verbatim, including that the render boundary
	// owing the sanitization is the CLIENT's. It is spelled `error_category` rather
	// than `error` because a key of that name beside is_error reads as its detail,
	// and this is neither that nor an error object.
	ErrorCategory string `json:"error_category"`
	// DurationMS is how long THIS TURN took in milliseconds (#2260). PER TURN: it is
	// non-monotonic across the committed captures, falling as often as it rises.
	DurationMS int `json:"duration_ms"`
	// DurationAPIMS is claude's duration_api_ms in milliseconds, and it is a RUNNING
	// TOTAL for the session — NOT the API time inside this turn, which is the reading
	// its name invites and the one a client is most likely to get wrong. It is larger
	// than DurationMS on 53 of the 57 observed result lines, so reading it per turn
	// renders eleven seconds of API work for a three-second turn.
	//
	// Differencing consecutive frames does not recover a per-turn figure either: the
	// value already exceeds its own turn's DurationMS on the FIRST result line of 19
	// of the 21 multi-turn captures, so whatever it sums is not bounded by the turn's
	// wall clock. The daemon publishes what claude sent and differences nothing.
	DurationAPIMS int `json:"duration_api_ms"`
	// NumTurns is how many model round-trips THIS TURN made — per turn, on DurationMS's
	// side of the split, not a count of turns in the session. It holds constant across
	// consecutive frames of one session where a cumulative counter would climb.
	NumTurns int `json:"num_turns"`
	// CostUSDTotal is what the SESSION has cost so far in US dollars — a RUNNING TOTAL
	// with DurationAPIMS, and undifferenced for the same reason.
	//
	// The wire spells it `cost_usd_total` where claude spells it total_cost_usd. That
	// is deliberate and is the only respelling in this group; the other three keep
	// claude's own key.
	//
	// IT IS CLAUDE'S ESTIMATE, NOT A BILLING STATEMENT, and on a subscription it is
	// informational. See the four fields' shared rules below.
	CostUSDTotal float64 `json:"cost_usd_total"`
	// The four token counts are claude's per-turn usage readings, carried without
	// summing or conversion. InputTokens is uncached input only; CacheReadTokens
	// and CacheCreationTokens are input-side too, while OutputTokens is output-side.
	// InputTokens + OutputTokens is therefore not the turn total.
	//
	// The cache keys are deliberately shorter than claude's
	// cache_read_input_tokens and cache_creation_input_tokens. Plain values without
	// omitempty keep every key present; old, absent, null and unreadable readings are
	// all zero. The numbers are unverified claude output and drive no daemon action.
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
}

// StallPayload is the body of an Envelope whose Type == TypeStall
// (docs/protocol-mobile.md § stall). Binary → phone direction; the wire form
// of the internal-only turnevent.Stall onset marker. It carries conversation
// identity only — like turn_state, a stall is a coarse conversation-level
// signal, not turn-scoped, so there is no turn_id; and it is onset-only, so
// there is no clearing field (the phone self-clears on the next turn
// activity). The bridge (#608) supplies ConversationID because the internal
// Stall marker carries none.
type StallPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ApiRetryPayload is the body of an Envelope whose Type == TypeApiRetry
// (docs/protocol-mobile.md § api_retry). Binary → phone direction; the wire
// form of the internal-only turnevent.ApiRetry status peer. Like turn_state it
// is a coarse conversation-level signal, not turn-scoped, so there is no
// turn_id. Active is the show (true) / clear (false) edge; Current/Total are the
// parsed `attempt N/M` counter ({0,0} when claude's counter did not parse). No
// field carries raw banner or screen text. The bridge (#608) supplies
// ConversationID because the internal ApiRetry marker carries none.
type ApiRetryPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	Current        int    `json:"current"`
	Total          int    `json:"total"`
}

// CompactingPayload is the body of an Envelope whose Type == TypeCompacting
// (docs/protocol-mobile.md § compacting). Binary → phone direction; the wire
// form of the internal-only turnevent.Compacting status peer. Active is the show
// (true) / clear (false) edge, and ConversationID is bridge-supplied because the
// internal marker carries none. Not turn-scoped, so there is no turn_id.
//
// CORRECTED 2026-09-08 (#2236): this doc called the frame "banner-only (tui-driver
// streams no compaction progress)", and both halves were already false when #2227
// gave the frame its first real producer — #1348 had deleted the driver the
// parenthetical rests on, and the stream-json seam that replaced it reads claude's
// outcome off the closing system/status line. The frame now carries that outcome.
//
// IT IS THE FIRST claude-AUTHORED TEXT ON THIS FRAME, which is a class change rather
// than two more fields. Before this, every value here was the daemon's own — an id it
// assigned and a bool it computed from a string comparison — so a reader could treat
// the whole payload as trusted. Two of the four fields are now claude's, bounded but
// unsanitized, and the SECURITY paragraph at ErrorText is what a renderer must read
// before drawing either.
//
// No omitempty on either, per this file's rule as stated at ToolResultPayload:
// absence and the zero value mean the same thing, always emitting the key keeps the
// testdata fixture pinning the full shape, and a client built before this landed
// ignores the unknown keys while one built after decodes an older frame lacking them
// to the zero value without error.
type CompactingPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	// Result is claude's own compact_result for the compaction that just ended —
	// "success" on the observed success path (claude 2.1.259). An OPEN SET carried
	// verbatim, on TurnEndPayload.Outcome's rule: treat an unrecognised token as
	// unknown rather than as an error, because claude may ship one at any time.
	//
	// ALWAYS EMPTY ON A RISING EDGE. A compaction that has just started has no
	// outcome to report, so a client rendering this field must gate on Active being
	// false. Empty on a FALLING edge means one of three things a client answers
	// identically: claude sent no key, claude sent an empty one, or the daemon closed
	// the banner at its own turn boundary with no closing line to read.
	//
	// Spelled compact_result rather than result because a bare `result` beside
	// `active` reads as the frame's own status — TurnEndPayload.ErrorCategory's
	// spelling argument, and it also keeps the key identical to claude's own, which
	// is what makes a daemon log line and a captured frame comparable by eye.
	Result string `json:"compact_result"`
	// ErrorText is claude's compact_error: free-form prose describing why a
	// compaction failed, absent entirely on the observed success path. It is the
	// field this frame exists to carry — before #2236 a failed compaction and a
	// successful one were the same two bytes on the wire, so a client could only
	// draw an ordinary banner for both.
	//
	// BOUNDED AT 256 BYTES BY THE DAEMON and CUT rather than dropped, unlike
	// ErrorCategory beside it on turn_end: that is a token set, where a cut token
	// would match no known value while looking like one; this is prose, where a cut
	// sentence still reads as what it is. The cut is NOT reported — a client cannot
	// distinguish a cut value from a short one and needs no such distinction.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// TurnEndPayload.Outcome's SECURITY paragraph applies for provenance — the render
	// boundary owing control-character and terminal-escape stripping is the CLIENT's.
	// IT DOES NOT APPLY FOR SHAPE, and a client that renders this like its turn_end
	// neighbours gets it wrong. Those are short category tokens; this is arbitrary
	// prose, and newlines, terminal escapes, markup, a URL and text impersonating
	// daemon chrome all fit inside 256 bytes. Render it as inert text —
	// UnrecognizedMessagePayload.Raw's rule, the closer neighbour on shape, never an
	// HTML sink, an attribute or a URL — and attribute it to claude rather than
	// showing it as the daemon's own finding, which is the trap
	// QuestionDismissedPayload's outcome field exists to avoid.
	ErrorText string `json:"compact_error"`
}

// CompactionBoundaryPayload is the body of an Envelope whose Type ==
// TypeCompactionBoundary (docs/protocol-mobile.md § compaction_boundary, #2237).
// Binary → phone direction; the wire form of turnevent.CompactionBoundary. Like
// compacting it is conversation-scoped rather than turn-scoped, so there is no
// turn_id.
//
// IT IS NOT A WIDER `compacting`, AND IT COULD NOT HAVE BEEN. claude states the
// trigger and the counts on a separate system/compact_boundary line that arrives
// AFTER the closing system/status line the falling edge is read off, so the numbers
// do not exist until that frame has shipped. A client applies this to the divider it
// has ALREADY drawn from compacting's falling edge — and, per the next paragraph,
// sometimes draws the divider from this frame alone.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BOUNDARY. That is a class change
// rather than a wider payload, and it is a step beyond the one #2236 made next door:
// CompactingPayload.active is a bool the DAEMON computes from a string comparison it
// makes itself, so a client can read that field as the daemon's own observation. No
// field here is. The daemon publishes this frame for a boundary line that followed no
// compacting edge at all — deliberately, so an auto-compaction announcing itself
// differently is still published — which means this frame can be the ONLY evidence a
// compaction happened. A fabricated line reading pre_tokens 999999 and post_tokens 1
// draws a plausible compaction mark where nothing was compacted. Render it as
// claude's ASSERTION, attributed to claude, never as the daemon's own finding, which
// is the trap QuestionDismissedPayload's outcome field exists to avoid. NOTHING IN
// THE DAEMON ACTS ON ANY FIELD HERE — no retry, no backoff, no teardown, no routing —
// which is what keeps a fabricated value a misleading label rather than an actuator.
//
// NOTHING ELSE FROM claude's compact_metadata CROSSES, and the exclusion is
// structural rather than a scrub: the daemon's decode target (streamsup's
// compactMetadata) declares these three fields, so encoding/json discards every other
// key including ones claude has not shipped yet. The line carries four more, and they
// are two different classes — cumulative_dropped_tokens and duration_ms are simply
// unasked-for, while preserved_segment, preserved_messages and logical_parent_uuid
// carry UUIDS NAMING ENTRIES IN THE OPERATOR'S OWN TRANSCRIPT. Neither claude's
// session_id nor claude's uuid crosses either, per this file's standing rule.
//
// THE TWO COUNTS DEPART FROM THIS FILE'S NO-omitempty RULE IN SHAPE BUT NOT IN
// SPIRIT, and the departure is the frame's whole point. Elsewhere here absence and
// the zero value mean the same thing; here they do not — post_tokens is optional in
// claude's own shape, so a client that collapses them renders "24k → 0 tokens" for a
// boundary claude reported without a post count. They are therefore POINTERS, and
// they still carry NO omitempty: an absent count marshals as a literal `null` rather
// than dropping the key, which keeps every key always present, keeps the testdata
// fixtures pinning the full shape, and states the absence instead of leaving a client
// to infer it. SessionTransitionPayload.WorkspaceCwd is the same shape for the same
// reason, and SessionSettingsPayload's pointer-plus-omitempty is what this
// deliberately is not. A client built before this landed ignores the whole frame; the
// daemon always emits all four keys.
type CompactionBoundaryPayload struct {
	ConversationID string `json:"conversation_id"`
	// Trigger is claude's own word for what started the compaction — `manual` on the
	// observed path (claude 2.1.259, a typed /compact), with `auto` claude's other
	// documented value. An OPEN SET carried verbatim, on TurnEndPayload.Outcome's rule:
	// treat an unrecognised token as unknown rather than as an error, because claude may
	// ship one at any time. Empty means claude named no trigger this client can be
	// offered — either it sent none, or the daemon dropped an oversized one.
	//
	// BOUNDED AT 256 BYTES BY THE DAEMON and DROPPED rather than cut, which is the
	// opposite of compact_error next door and the same answer turn_end's three strings
	// get. That field is prose, where a cut sentence still reads as what it is; this is
	// a token a client MATCHES, where a cut token would match no known value while
	// looking like one. The drop is not reported and needs no report: an empty value is
	// directly observable, unlike an absence a client would have to infer.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// CompactingPayload.ErrorText's SECURITY paragraph applies for provenance — the
	// render boundary owing control-character and terminal-escape stripping is the
	// CLIENT's. IT DOES NOT APPLY FOR SHAPE, and this one is the safer half of that
	// pair rather than the riskier: this is a short token from an open set, so it takes
	// TurnEndPayload.Outcome's rule — switch on it against known values — and never
	// UnrecognizedMessagePayload.Raw's prose latitude. A client that renders this token
	// verbatim into a sentence is rendering up to 256 bytes claude chose.
	Trigger string `json:"trigger"`
	// PreTokens and PostTokens are claude's context size before and after the
	// compaction, exactly as claude stated them.
	//
	// `null` MEANS claude STATED NO SUCH COUNT and `0` means claude stated zero, and a
	// client MUST NOT collapse them: rendering "24k → 0 tokens" for a boundary with no
	// post count is the failure this shape exists to prevent. Degrade where a count is
	// absent — say the conversation was compacted without claiming a size.
	//
	// NEITHER IS CLAMPED, RANGE-CHECKED OR ORDERED by the daemon, on
	// RateLimitedPayload's rule: they are claude's numbers, not the daemon's. post
	// greater than pre is not rejected and not corrected, and neither is a negative.
	// What the daemon does guarantee is that a value it could not decode as an integer
	// produces no frame at all rather than a frame with an invented number.
	PreTokens  *int `json:"pre_tokens"`
	PostTokens *int `json:"post_tokens"`
}

// BannerPayload is the body of an Envelope whose Type == TypeBanner
// (docs/protocol-mobile.md § banner, #2256). Binary → phone direction; the wire form
// of turnevent.Banner. Like compacting and unrecognized_message it is
// conversation-scoped rather than turn-scoped, so there is no turn_id.
//
// THE ABSENT turn_id IS FORCED BY THE PRODUCER SET, not chosen for symmetry with those
// two. A prompt a hook refuses is never answered, so no turn exists to attribute the
// refusal to; a notification belongs to claude's own queue and rides no turn at all.
// There is no turn the daemon could honestly name, and naming one anyway would attach
// operator-facing text to work it did not come from.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BANNER — CompactionBoundaryPayload's
// class change, landing somewhere sharper. That frame at least reports a compaction the
// daemon can corroborate from its own parser state; this one reports that claude had
// something to say, which nothing else on the wire confirms or contradicts.
//
// IT IS THE FIRST FRAME HERE WHOSE WHOLE PURPOSE IS ARBITRARY claude-AUTHORED PROSE
// with no machine state anchoring it, and that is a class change rather than another
// prose field. CompactingPayload.ErrorText is prose attached to a compaction the daemon
// observed; UnrecognizedMessagePayload.Raw is prose explicitly labelled a parser gap. A
// client renders THIS one as a first-class notice, so text impersonating daemon chrome
// at level "warning" is the realistic abuse rather than a hypothetical one. Render the
// whole frame as claude's ASSERTION, attributed to claude, never as the daemon's own
// finding — the trap QuestionDismissedPayload's outcome field exists to avoid — and
// render Text as inert text on UnrecognizedMessagePayload.Raw's rule.
//
// NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE — no retry, no backoff, no teardown, no
// routing — which is what keeps a fabricated banner a misleading label rather than an
// actuator. StopsTurn is where that constraint is most load-bearing and it is restated
// at the field, because the field NAME is what invites the mistake.
//
// IT CARRIES NO DATA CLASS AN INTERACTIVE GRANT DOES NOT ALREADY RECEIVE, which is why
// no narrower capability gate exists. The observed text embeds a host filesystem path
// and echoes the operator's own prompt back, and ToolUsePayload.Input's verbatim
// top-level fields already carry both to the same grant.
//
// No omitempty on any field, per this file's rule as stated at ToolResultPayload:
// absence and the zero value mean the same thing, always emitting the key keeps the
// testdata fixture pinning the full shape, and a client built before this landed
// ignores the unknown keys. Every field is a value type — two strings and two bools, no
// pointer and no slice, unlike CompactionBoundaryPayload's counts and ToolDeniedPayload's
// report slices — so this frame raises no aliasing question at either seam.
type BannerPayload struct {
	ConversationID string `json:"conversation_id"`
	// Level is claude's own key, adopted VERBATIM — the one field here that keeps
	// claude's spelling, where text and stops_turn are renames. It is consequently the
	// one word a vocabulary pin on this frame may NOT check for, the trap
	// TestSlashCommandListType_IsNotClaudesVocabulary records for `commands`.
	//
	// An OPEN SET, on TurnEndPayload.Outcome's rule: treat an unrecognised token as
	// unknown rather than as an error, because claude may ship one at any time.
	// `warning` is the OBSERVED value (claude 2.1.259, the single informational line in
	// the committed operator_system_lines capture); `info`, `notice` and `suggestion`
	// are claude's other DOCUMENTED values for the key and have not been observed on
	// this repo. The two groups are named separately on purpose — presenting four as a
	// set this repo has seen would overstate the evidence.
	//
	// BOUNDED BY THE PRODUCER AND DROPPED RATHER THAN CUT, which is the opposite answer
	// text beside it gets, and the pairing is the trap worth naming on this frame:
	// CompactionBoundaryPayload.Trigger argues it in full. This is a token a client
	// MATCHES, where a cut token matches no known value while still looking like one;
	// text is prose, where a cut sentence still reads as what it is. Empty means claude
	// named no level this client can be offered — either it sent none, or the daemon
	// dropped an oversized one. The drop is not reported and needs none: an emptied
	// scalar is directly observable.
	Level string `json:"level"`
	// Text is claude's `content`, renamed — the field this frame exists to carry. The
	// rename is half of the translation layer the vocabulary pin holds: `content`
	// beside `level` would read as the frame's own body rather than as what claude
	// printed.
	//
	// BOUNDED AT 4 KiB BY THE DAEMON, at the PRODUCER (#2319), and CUT rather than
	// dropped. The bound is stated here as the contract THAT ticket owed rather than as
	// a fact this package enforces: the enforcing constant is streamsup's maxBannerText,
	// landed beside maxCompactTrigger and maxDenialProse, on this repo's standing division
	// that claude's text is bounded where it crosses the subprocess boundary. This
	// shape and the bridge re-decide no maximum — a second cap site is a second place
	// the limit is decided and the two could disagree silently, which is the rule
	// ToolDeniedPayload's doc states.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// CompactingPayload.ErrorText's SECURITY paragraph applies for provenance — the
	// render boundary owing control-character and terminal-escape stripping is the
	// CLIENT's. IT APPLIES FOR SHAPE TOO, and this is the RISKIER half of the pair
	// CompactionBoundaryPayload.Trigger is the safer half of. Newlines, terminal
	// escapes, markup, a URL and text impersonating daemon chrome all fit inside 4 KiB,
	// and the observed line contains a host filesystem path and echoes the operator's
	// own prompt back. Never an HTML sink, an attribute or a URL.
	Text string `json:"text"`
	// Truncated says whether the daemon cut text to fit the bound above. It is the
	// PRODUCER's answer carried verbatim, never one the bridge recomputed from the
	// string's length: a second authority on the same fact is a second place it can be
	// decided differently.
	//
	// There is no companion dropped_fields slice, unlike ToolDeniedPayload's pair, and
	// none is owed. text is the only field the daemon cuts, so one bool names it
	// unambiguously — that frame needs a slice because it has six candidate fields —
	// and an emptied level is directly observable, exactly as
	// CompactionBoundaryPayload.Trigger's unreported drop is.
	Truncated bool `json:"truncated"`
	// StopsTurn is claude's `prevent_continuation`, renamed. It says claude will not
	// continue past this banner; the observed value is true, on a hook that refused a
	// prompt.
	//
	// A REPORT, NEVER AN ACTUATOR, and the constraint is restated here rather than left
	// to the type's doc because THIS FIELD'S NAME IS WHAT INVITES THE MISTAKE. It reads
	// like a lever, and a daemon that ever keyed a teardown, a retry suppression or a
	// queue decision on it would hand claude a self-service turn abort — a fabricated
	// line stopping work the operator asked for. A CLIENT owes the same restraint:
	// render it, do not let it cancel anything the operator did not cancel.
	StopsTurn bool `json:"stops_turn"`
}

// UnrecognizedMessagePayload is the body of an Envelope whose Type ==
// TypeUnrecognizedMessage (docs/protocol-mobile.md § unrecognized_message).
// Binary → phone direction; the wire form of the internal-only
// turnevent.Unrecognized diagnostic marker. Like turn_state it is a coarse
// conversation-level signal, not turn-scoped, so there is no turn_id — an
// unrecognized message has no turn we can honestly attribute it to.
//
// Site is where the parser dropped the payload ("line_type", "assistant_block",
// "user_block", "undecodable"). MessageType is the offending message or block
// `type`, empty when Site is "undecodable" (nothing decoded, so no type was
// read). Raw is the offending JSON, truncated by the producer to a fixed byte
// cap; Truncated says whether that happened.
//
// SECURITY: Raw is the ONLY interactive payload field carrying unbounded
// model-adjacent JSON, so two properties are load-bearing. The producer
// truncates at construction to 16 KiB, so an oversized payload never reaches
// this struct; that is roughly a quarter of the v2 application-envelope cap of
// 65519 bytes (NOT v1's 1 MiB, which v2 superseded), leaving room for the other
// fields plus JSON escaping. And Raw is a plain string, not
// json.RawMessage, because a truncated blob is no longer valid JSON — typing it
// as raw JSON would be a lie and would break marshalling. A consumer must render
// it as inert text and never feed it to an HTML sink, an attribute, or a URL.
type UnrecognizedMessagePayload struct {
	ConversationID string `json:"conversation_id"`
	Site           string `json:"site"`
	MessageType    string `json:"message_type"`
	Raw            string `json:"raw"`
	Truncated      bool   `json:"truncated"`
}

// BackgroundTaskStartedPayload is the body of an Envelope whose Type ==
// TypeBackgroundTaskStarted (docs/protocol-mobile.md § background_task_started,
// #1394). Binary → phone direction; the wire form of
// turnevent.BackgroundTaskStarted, which announces that claude started work
// outliving the turn that spawned it. Not turn-scoped, so there is no turn_id —
// a background task's lifecycle is orthogonal to its turn, which is the whole
// #1240 point. The bridge (#1394) supplies ConversationID because the internal
// event carries none.
//
// ToolCallID is claude's tool_use_id under the name ToolUsePayload and
// ToolResultPayload already use for the same identifier, so a client joins the
// three with no vocabulary lookup — and it is the name TruncatedFields reports
// for the field. claude's session_id is deliberately absent: it is claude's
// session identity, not the daemon's conversation identity, and no v2 payload
// carries it.
//
// TruncatedFields names the fields the producer cut to fit their caps, using
// these wire names ("task_id", "tool_call_id", "description", "task_type"); it
// is null when nothing was cut. It is load-bearing, not decoration — a payload
// that dropped it would present claude's truncated text to a phone as complete.
//
// SECURITY: Description is claude's label for the task, and for the local_bash
// task type it is the LITERAL command line. It is safe to RENDER as inert text
// and never to execute, re-shell, or feed to an HTML sink, an attribute, or a
// URL. Its bound is the producer's, decided at construction
// (internal/streamsup/parser.go's maxTaskFieldID / maxTaskDescription), so this
// struct re-decides no maximum: a second cap here would be a second place the
// limit is decided, and the two could disagree silently.
type BackgroundTaskStartedPayload struct {
	ConversationID  string   `json:"conversation_id"`
	TaskID          string   `json:"task_id"`
	ToolCallID      string   `json:"tool_call_id"`
	Description     string   `json:"description"`
	TaskType        string   `json:"task_type"`
	TruncatedFields []string `json:"truncated_fields"`
}

// BackgroundTaskUpdatedPayload is the body of an Envelope whose Type ==
// TypeBackgroundTaskUpdated (docs/protocol-mobile.md § background_task_updated,
// #1394). Binary → phone direction; the wire form of
// turnevent.BackgroundTaskUpdated, the peer of background_task_started: that
// frame opens the task, this one reports what happened to it afterwards. It
// opens and closes no turn either, so like its sibling there is no turn_id, and
// the bridge (#1394) supplies ConversationID because the internal event carries
// none. TaskID is the join key back to the background_task_started that opened
// the task. claude's session_id is absent for its sibling's reason.
//
// TWO OF CLAUDE'S LINES PRODUCE THIS FRAME (#2245), and they fill DISJOINT
// fields. system/task_updated fills patch and leaves status and summary empty;
// system/task_notification fills status and summary and leaves patch empty. A
// non-empty status is therefore what says a terminal state was reported on this
// frame, and it is the transition a client needs to close a task row that
// background_task_started opened. No field names the producing line: that would
// be content the daemon invented, on a frame whose neighbouring field is
// contractually claude's bytes alone.
//
// Status is CLAUDE'S REPORT, NOT THE DAEMON'S DETECTION. The daemon does not
// verify that a task reporting a finish has stopped running, and it has observed
// exactly one token ("completed"); the documented "failed" and "stopped" states
// have never been captured. It is a plain string rather than a closed set for
// that reason.
//
// TruncatedFields names the cut fields ("task_id", "patch", "status",
// "summary"), null when nothing was cut, and carries the same load as its
// sibling's. Because the two producing lines fill disjoint fields, one frame's
// list can only name fields from one of them plus "task_id", which both carry.
//
// SECURITY: Summary is model-authored FREE TEXT with no documented length bound,
// and this warning is stated here rather than delegated to the sibling section
// for two reasons. It is the family's SECOND field that can carry a literal
// command line — BackgroundTaskStartedPayload.Description is the first, and in
// the one captured task_notification line summary IS the task's command. And
// unlike patch, which a client is told to treat as an opaque blob, summary is
// prose a client will actually render, so it reaches an HTML sink by the normal
// path rather than by a mistake. Render it as INERT TEXT; never execute,
// re-shell, or feed it to an HTML sink, an attribute, or a URL. Status is a short
// token today but is claude-authored under the same rule. Both bounds are the
// producer's, decided at construction (internal/streamsup/parser.go's
// maxTaskFieldID / maxTaskSummary), so this struct re-decides no maximum.
//
// claude's output_file is deliberately ABSENT from this payload. It is documented
// as a path on the operator's host, and it is not merely unset here — it is never
// decoded anywhere in the daemon, so no code path holds it. A field that is never
// declared cannot leak, which is a stronger guarantee than redacting one.
//
// SECURITY: Patch is claude's patch object — what CHANGED about the task —
// carried WHOLE and unparsed as its serialized text, so nothing here is declared
// about its contents and no key is enumerated. It is a plain string and NOT
// json.RawMessage, for UnrecognizedMessagePayload.Raw's reason: the producer
// truncates it at construction (internal/streamsup/parser.go's maxTaskPatch) and
// a truncated object is no longer valid JSON, so typing it as raw JSON would be
// a lie and would break marshalling. A consumer MUST NOT assume it parses, must
// render it as inert text, and must never feed it to an HTML sink, an attribute,
// a URL, or anything that executes or re-shells it — a patch's structured shape
// makes it the more tempting thing to feed somewhere that runs it, and its
// sibling's Description already carries a literal command line.
//
// The producer also scrubs invalid UTF-8 with an EMPTY replacement and reports
// the cap cut ONLY, so Patch can differ from claude's bytes without appearing in
// TruncatedFields. That is a stated limitation of the upstream producer, not of
// this type.
type BackgroundTaskUpdatedPayload struct {
	ConversationID  string   `json:"conversation_id"`
	TaskID          string   `json:"task_id"`
	Patch           string   `json:"patch"`
	Status          string   `json:"status"`
	Summary         string   `json:"summary"`
	TruncatedFields []string `json:"truncated_fields"`
}

// BackgroundTaskRosterPayload is the body of an Envelope whose Type ==
// TypeBackgroundTaskRoster (docs/protocol-mobile.md § background_task_roster,
// #1394). Binary → phone direction; the wire form of
// turnevent.BackgroundTaskRoster, the aggregate peer of the two scalar frames:
// they report what happened to ONE task, this reports what is alive at one
// moment. A SNAPSHOT, not a delta. Not turn-scoped, and the bridge (#1394)
// supplies ConversationID because the internal event carries none.
//
// Tasks is in claude's own order, truncated from the tail by the producer.
// An EMPTY roster is meaningful and is still sent: it says nothing is alive,
// which is exactly the signal a consumer of #1240's symptom needs. The key is
// therefore always present on the wire and never null — see MarshalJSON.
//
// DroppedTasks is how many entries claude sent beyond the producer's entry cap
// (internal/streamsup/parser.go's maxTaskRosterEntries) that this frame does NOT
// carry; 0 when nothing was dropped, so the roster's true size is
// len(Tasks) + DroppedTasks. The count reports here rather than as a name in a
// top-level truncated_fields — which is why this payload has none — because a
// name-only report loses HOW MANY were lost, and each dimension reports where it
// is decided: a text cut is a property of one entry and rides that entry.
type BackgroundTaskRosterPayload struct {
	ConversationID string           `json:"conversation_id"`
	Tasks          []BackgroundTask `json:"tasks"`
	DroppedTasks   int              `json:"dropped_tasks"`
}

// MarshalJSON normalises a nil Tasks to an empty array, so an empty roster
// always serialises as "tasks":[] and never as "tasks":null.
//
// This was the file's first custom marshaller (ToolUsePayload's is the second,
// #1678, and follows this one), and the deviation is deliberate.
// omitempty is out — eliding the key would erase the frame's whole point, since
// an empty roster is a POSITIVE statement rather than an absence. Between null
// and [], [] is the better client contract: it reads as an empty list where null
// reads as absent/unknown, and a client decoding into a non-optional array type
// never has to branch. Protocol shapes are expensive to change once a client has
// shipped against one, so the better shape is worth the nine lines now.
//
// A doc comment would not have been enough. turnevent.BackgroundTaskRoster.Tasks
// is nil both for an empty roster and when claude omits the key, so the bridge
// (#1394) mapping it straight through would hand this type a nil slice; without
// this method that would ship null to a phone while the empty-roster fixture
// went on asserting [], and nothing in `make check` would notice.
//
// truncated_fields is deliberately NOT normalised the same way: nil and [] say
// the identical thing there ("nothing was cut") and no consumer branches on the
// difference, whereas tasks is the frame's subject and its empty value is the
// signal.
//
// Value receiver, so it applies to the value form the round-trip and the bridge
// both take, and so the substitution lands on a copy rather than on the caller's
// slice. The type alias is the standard indirection that keeps json.Marshal from
// recursing back into this method.
func (p BackgroundTaskRosterPayload) MarshalJSON() ([]byte, error) {
	if p.Tasks == nil {
		p.Tasks = []BackgroundTask{}
	}
	type alias BackgroundTaskRosterPayload
	return json.Marshal(alias(p))
}

// BackgroundTaskProgressPayload is the body of an Envelope whose Type ==
// TypeBackgroundTaskProgress (docs/protocol-mobile.md § background_task_progress,
// #2246). Binary → phone direction; the wire form of
// turnevent.BackgroundTaskProgress, the fourth background-task frame: the other
// three open a task, report what happened to it, and list what is alive, and this
// one says a running task is still working and what it is doing right now.
//
// It opens and closes no turn, so like its siblings there is no turn_id, and the
// bridge supplies ConversationID because the internal event carries none. TaskID is
// the join key back to the background_task_started that opened the task. claude's
// session_id and uuid are absent for their sibling's reason, and so is tool_use_id —
// the join key is task_id and the opening frame already published the tool call.
//
// A SEPARATE FRAME FROM background_task_updated, NOT A WIDER ONE, and the captured
// field set is what decided it — the opposite call from #2245's, made on the same
// test. `description` here is the task's CURRENT ACTIVITY, where `description` on
// background_task_started is the task's OPENING description; carrying two meanings
// under one name on one task row is a wire-contract trap a client cannot undo. And
// `subagent_type` and `last_tool_name` describe the AGENT DOING THE WORK, not what
// happened to the task, which is what background_task_updated reports. No `patch` is
// synthesized here or anywhere: that field is contractually claude's own bytes.
//
// RATE. The daemon emits at most one of these per **2 tool calls** a task's own
// counter advances, so fewer frames cross this wire than claude emits lines — on the
// committed capture, 2 lines became 1 frame. Two consequences a client must not get
// wrong, and they are thinking_progress's: the frames do NOT enumerate claude's
// lines, and the ABSENCE of one within any window does NOT mean the task stalled. A
// third rides the per-task bound: past the daemon's task-cardinality cap a task
// receives no progress frames at all, and its opening and terminal frames are
// unaffected, so a silent row still opens and closes correctly.
//
// TotalTokens, ToolUses and DurationMS are claude's own readings off the emitting
// line, carried verbatim. CUMULATIVE PER TASK, so two frames for one task carry
// growing values and MUST NOT be summed — diff them if a rate is wanted. They are
// not guaranteed monotonic either: a client that subtracts two readings must
// tolerate a negative result. The daemon accumulates nothing, so no number here is
// arithmetic it did.
//
// TruncatedFields names the cut fields ("task_id", "description", "subagent_type",
// "last_tool_name"), null when nothing was cut. The three integers can never appear
// in it: nothing cuts an int.
//
// SECURITY: Description is model-authored FREE TEXT with no documented length bound,
// and it is the field this frame exists to carry. Both captured values NAME A FILE
// the subagent is reading, so in practice a stream of these frames is a stream of
// path fragments from the operator's host — even though claude's documented
// path-shaped key is not present on this subtype at all. A client must not read it as
// a safe label. SubagentType and LastToolName are short model- and tool-authored
// tokens under the same rule; a short token today is not a guarantee about
// tomorrow's. Render all three as INERT TEXT; never execute, re-shell, or feed them
// to an HTML sink, an attribute, or a URL — the sibling warning at
// BackgroundTaskUpdatedPayload.Summary applies verbatim, and this frame repeats it
// rather than delegating because these values arrive REPEATEDLY for one row, which is
// the shape a client is most likely to bind straight into a template. All bounds are
// the producer's, decided at construction (internal/streamsup/parser.go's
// maxTaskFieldID / maxTaskDescription), so this struct re-decides no maximum.
type BackgroundTaskProgressPayload struct {
	ConversationID  string   `json:"conversation_id"`
	TaskID          string   `json:"task_id"`
	Description     string   `json:"description"`
	SubagentType    string   `json:"subagent_type"`
	LastToolName    string   `json:"last_tool_name"`
	TotalTokens     int      `json:"total_tokens"`
	ToolUses        int      `json:"tool_uses"`
	DurationMS      int      `json:"duration_ms"`
	TruncatedFields []string `json:"truncated_fields"`
}

// ThinkingProgressPayload is the body of an Envelope whose Type ==
// TypeThinkingProgress (docs/protocol-mobile.md § thinking_progress, #1386).
// Binary → phone direction; the wire form of turnevent.ThinkingProgress, the
// daemon's translation of claude's system/thinking_tokens line. It reports that
// claude is actively reasoning and roughly how much — claude's only mid-turn
// proof of life on the stream-json surface.
//
// Like turn_state it is a coarse conversation-level signal, NOT turn-scoped, so
// there is no turn_id, and receiving one neither opens nor closes a turn. The
// bridge supplies ConversationID because the internal event carries none;
// claude's own session_id is deliberately absent for BackgroundTaskStartedPayload's
// reason, and the parser drops it before the event exists (#1380/#1385).
//
// Both fields are claude's own integer readings, carried verbatim. There is no
// TruncatedFields, and its absence is a decision rather than an omission: unlike
// every sibling above this payload carries no claude-authored TEXT at all, so
// nothing is ever cut, and a permanently-nil field would claim a bound that does
// not exist. For the same reason there is no producer byte cap to mirror here.
//
// Two consumer hazards ride these numbers and are NOT restated here, because
// turnevent.ThinkingProgress's field comments are their single source of truth
// (with the measured numbers): EstimatedTokens is not monotonic across a turn,
// and the EstimatedTokensDelta values a client receives do not sum to the turn's
// total. A consumer-facing statement of both, plus the two reasons absence proves
// nothing, is in docs/protocol-mobile.md § thinking_progress.
type ThinkingProgressPayload struct {
	ConversationID       string `json:"conversation_id"`
	EstimatedTokens      int    `json:"estimated_tokens"`
	EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}

// BackgroundTask is one row of a BackgroundTaskRosterPayload
// (docs/protocol-mobile.md § background_task_roster, #1394). Its fields are
// exactly the per-entry keys claude's roster line carries and nothing invented —
// in particular there is no tool_call_id and no patch, which the scalar frames
// carry because their LINES do. TaskID is the join key back to the
// background_task_started that opened the task.
//
// TruncatedFields names THIS row's cut fields ("task_id", "task_type",
// "description"), null when nothing was cut.
//
// SECURITY: Description carries the same literal command line as
// BackgroundTaskStartedPayload.Description, under a tighter producer cap
// (internal/streamsup/parser.go's maxTaskRosterDescription — here it is a label
// in a list whose length claude chooses, and the full-length copy already
// crossed the wire on the background_task_started this row joins back to). The
// render-never-execute warning is repeated rather than delegated because a LIST
// of command lines is a more tempting shape to feed somewhere structured than a
// single one.
type BackgroundTask struct {
	TaskID          string   `json:"task_id"`
	TaskType        string   `json:"task_type"`
	Description     string   `json:"description"`
	TruncatedFields []string `json:"truncated_fields"`
}

// RateLimitedPayload is the body of an Envelope whose Type == TypeRateLimited
// (docs/protocol-mobile.md § rate_limited, #1405). Binary → phone direction; the
// wire form of turnevent.RateLimited, which reports that claude's usage-limit
// window is in a state other than the one measured-benign one.
//
// Like ThinkingProgressPayload it is conversation-scoped rather than turn-scoped,
// so there is no turn_id, and receiving one neither opens nor closes a turn: a
// usage-limit window is orthogonal to whichever turn happened to observe it
// (turnevent.RateLimited's own doc). The bridge (#1410) supplies ConversationID
// because the internal event carries none. claude's session_id and uuid are
// deliberately absent for BackgroundTaskStartedPayload's reason plus #1380's —
// they are claude's session identity and claude's per-line message id, neither of
// which is the daemon's conversation identity, and the parser never decodes them
// so this payload cannot carry them even by accident.
//
// Status is the field that says WHY the frame fired, and its value set beyond the
// benign one is ALMOST ENTIRELY UNMEASURED: exactly one non-benign value is on
// record (allowed_warning, the weekly warning band) and no capture of a limit
// actually in force exists on any claude version. A
// plain string, not a closed enum, so the set gets measured the first time a real
// limit fires rather than the daemon inventing one it has no evidence for. The
// producer's gate is deliberately loud in the same direction: any non-empty status
// other than the measured-benign one emits, so an unrecognised status surfaces and a
// human looks rather than a real limit vanishing. Dropping Status from this payload
// would silence that one layer later.
//
// THE BENIGN STATUS IS NOT ALWAYS SILENT, and a client that assumed it was reads
// this frame backwards. Since #2250 the gate publishes a benign reading that FOLLOWS
// a non-benign one on the same parser — the falling edge, which exists so a client
// can take a quota banner down. Two things follow for a decode. The benign value is
// the ONE value worth comparing against, and it is the only one measured stable on
// every claude version on file; everything else stays an opaque label. And the frame
// is not the limit lifting: claude reports one window per line and chooses which, so
// a benign reading is claude declining to report a non-benign window. See
// docs/protocol-mobile.md § rate_limited, which states both readings for a client,
// and turnevent.RateLimited for the daemon-side argument.
//
// LimitType is WHICH limit is in force (five_hour and seven_day are the observed
// values, the second riding the one non-benign status), a
// plain string for Status's reason. The falling edge therefore names a DIFFERENT
// limit than the warning it clears — every benign reading on record says five_hour —
// so a client that keys a banner by this value never matches the frame that takes it
// down; key it on the conversation. ResetsAt is when claude says it lifts, as
// unix seconds, 0 when claude did not report it. Neither wire name tracks
// claude's key: claude's are rateLimitType and resetsAt under rate_limit_info,
// while these are turnevent.RateLimited's own field names in snake_case, so a
// claude rename does not move them. status coincides with claude's spelling but
// is the daemon's chosen name for the field — it is what the producer's bound()
// reports it as — and a generic English word rather than a vocabulary import.
// utilization coincides the same way and for the same reason, which is why it is
// NOT a member of the claude-key enumeration TestRateLimitedType_IsNotClaudesVocabulary
// forbids on the wire.
//
// Utilization is how much of the window claude says is spent, and it is a POINTER
// so null on the wire means claude reported nothing while 0 means claude reported
// an untouched window. A client that reads a missing reading as zero renders a
// fresh quota as an exhausted one, which is the failure this shape exists to
// prevent — turnevent.RateLimited.Utilization's own doc is the single source of
// truth for that argument and for why 0 cannot absorb absence the way ResetsAt's 0
// can. The daemon always emits the key, so an absent reading is a literal null and
// never a dropped key. It is not in TruncatedFields and has no cap, for ResetsAt's
// reason: a float64 cannot grow.
//
// TruncatedFields names the fields the producer cut to fit its cap, using these
// wire names ("status", "limit_type", in that order); it is null when nothing was
// cut, never an empty array, which is why this type has no MarshalJSON. The
// nil-normalising guard above is BackgroundTaskRosterPayload.Tasks's and does not
// generalise: an empty roster is a positive statement, whereas nothing-was-cut is
// an absence. It is load-bearing, not decoration — a payload that dropped it
// would present claude's truncated text to a phone as complete.
//
// SECURITY: Status and LimitType are claude-authored strings that crossed the
// subprocess trust boundary. They are safe to RENDER as inert text and must never
// be fed to an HTML sink, an attribute, or a URL; the daemon bounds them but does
// not sanitize them, so they stay untrusted, model-influenced text all the way to
// the client. Their bound is the producer's, decided at construction
// (internal/streamsup/parser.go's maxRateLimitField), so this struct re-decides no
// maximum: a second cap here would be a second place the limit is decided, and the
// two could disagree silently. The constraint on turnevent.RateLimited follows the
// data onto the wire — it is a REPORT, never a control input, so a client MUST NOT
// branch security-relevant behaviour on Status.
//
// BOTH NUMBERS ARE CLAUDE'S AND BOTH ARE UNVALIDATED IN BOTH DIRECTIONS, and saying
// so of one while adding a second is how a client concludes the second was checked.
// ResetsAt must not be assumed to lie in the future, or in a sane range at all.
// Utilization is NOT a bounded fraction: it must not be assumed to lie in 0..1
// either, so scaling a progress bar or a gauge by it without a range check is this
// field's realistic bug, and a value above one is not evidence of anything but what
// claude sent. Neither is clamped, rounded or rejected anywhere on the path.
type RateLimitedPayload struct {
	ConversationID  string   `json:"conversation_id"`
	Status          string   `json:"status"`
	LimitType       string   `json:"limit_type"`
	ResetsAt        int64    `json:"resets_at"`
	Utilization     *float64 `json:"utilization"`
	TruncatedFields []string `json:"truncated_fields"`
}

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

// ModelListPayload is the body of an Envelope whose Type == TypeModelList
// (docs/protocol-mobile.md § model_list — that section lands with the fixtures in
// #1705). Binary → phone direction; the wire form of the model inventory claude
// returns from a control_request with subtype initialize: the set of models it
// will accept for this conversation. A SNAPSHOT of what claude will accept, not a
// delta, and conversation-scoped rather than turn-scoped — receiving one neither
// opens nor closes a turn.
//
// Declared here (#1704) ahead of its producer so a client can be written against
// the shape — the sequencing #1405 used ahead of #1410 and #1616 ahead of #1638.
// The producer has since landed: turnbridge.MapEvent's turnevent.ModelList arm
// constructs it (#1848), and cmd/pyry's resolveBoundModelList (#1857) is a second
// production path that routes a session's retained list back through that same
// arm rather than filling the fields itself.
//
// ConversationID is present and unfilled by this ticket — the producer supplies
// it at mapping time, the seam every v2 interactive payload uses. claude's own
// session_id is deliberately absent for BackgroundTaskStartedPayload's reason:
// claude's session identity is not the daemon's conversation identity.
//
// Models is in claude's own order, truncated from the tail by the producer. The
// key is always present on the wire and never null — see MarshalJSON.
//
// DroppedModels is how many entries the producer cut beyond its entry cap that
// this frame does NOT carry; 0 when nothing was dropped, so the list's true size
// is len(Models) + DroppedModels. The decode now COUNTS IT (#1812): streamsup's
// maxModelListEntries bounds the entry count and turnevent.ModelList.DroppedModels
// carries what it cut, which is this field's honest source. #1848 joined the two:
// MapEvent's arm carries turnevent.ModelList.DroppedModels through verbatim and
// never recomputes it from len(Models). The field was declared ahead of both
// (#1704) because a wire with nowhere to put a drop discards it silently, and a
// permanent 0 reads as "nothing was dropped", which is a lie rather than a gap.
// The count reports here rather than as a name in a top-level truncated_fields —
// which is why this payload has none, BackgroundTaskRosterPayload's stated
// reason — because a name-only report loses HOW MANY were lost, and each
// dimension reports where it is decided: a text cut is a property of one entry
// and rides that entry as ModelOption.TruncatedFields.
//
// A lookup can MISS, and that is ordinary rather than an error. claude announces
// an identifier at least as specific as the one it was given
// (turnevent.ModelAnnounced's own doc, measured in #1601), so a client resolving a
// model_announced identifier against this list may find nothing; this shape does
// not assume every announced identifier appears here. ModelOption.DisplayName is
// the intended join — the announcement names a concrete dated identifier while a
// client's rows are alias families.
type ModelListPayload struct {
	ConversationID string        `json:"conversation_id"`
	Models         []ModelOption `json:"models"`
	DroppedModels  int           `json:"dropped_models"`
}

// MarshalJSON normalises a nil Models to an empty array, so a model list always
// serialises as "models":[] and never as "models":null.
//
// This implements BackgroundTaskRosterPayload.MarshalJSON's reason unchanged, and
// that whole rationale transfers: omitempty is out because an empty list is a
// POSITIVE statement rather than an absence, and between null and [], [] reads as
// an empty list where null reads as absent/unknown, so a client decoding into a
// non-optional array type never has to branch. ModelOption.MarshalJSON normalises
// its own EffortLevels for a DIFFERENT reason — read it there, so the asymmetry
// is not taken for an accident — and deliberately leaves TruncatedFields alone.
//
// This method cannot do the entry's job for it. Assigning a fresh slice to this
// copy's own Models field is safe, but reaching THROUGH it into p.Models[i] would
// mutate the caller's backing array, which for a shared payload is a data race as
// well as a correctness bug; and a payload-level normalisation would not fire at
// all when a ModelOption is marshalled on its own.
//
// Value receiver, so it applies to the value form a round trip and a bridge both
// take, and so the substitution lands on a copy rather than on the caller's
// slice. The type alias is the standard indirection that keeps json.Marshal from
// recursing back into this method.
func (p ModelListPayload) MarshalJSON() ([]byte, error) {
	if p.Models == nil {
		p.Models = []ModelOption{}
	}
	type alias ModelListPayload
	return json.Marshal(alias(p))
}

// RequestModelListPayload is the body of an Envelope whose Type ==
// TypeRequestModelList (docs/protocol-mobile.md § model_list, published by #2125).
// The frame a client sends to ask for a conversation's model menu on demand,
// rather than waiting for the live turn lane or the next connect.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. The frame it is answered with —
// ModelListPayload above — rides the other way and shares no type with it, so the
// never-empty and DroppedModels contracts stated there say nothing about this one.
//
// IT NAMES A CONVERSATION, because a model menu is conversation-scoped on this
// wire and this wire is multi-conversation; TypeRequestSessionSettings and
// TypeRequestHistory both name one. THE ID IS A LOOKUP KEY, NEVER A VALUE TRUSTED
// AS SENT: it is resolved against the daemon's own registry, the reported id in
// the reply comes out of the RESOLVED RECORD rather than being echoed back, and
// NAMING A CONVERSATION IS NOT AUTHORIZATION — authorization is pairing, enforced
// structurally at the Noise_IK handshake.
//
// UNLIKE RequestHistoryPayload THE ID NEVER BECOMES A PATH COMPONENT. That single
// difference is why a decode failure here is TOLERATED rather than rejected: it
// leaves ConversationID empty, which reaches only a registry membership check and
// is refused there, where an empty path component would have resolved to a
// directory root. Do not copy this tolerance to a verb that joins the id into a
// path.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO
// REQUEST-ID KEY — TypeAttachmentStored's decision, transferred unchanged.
// TestRequestModelListPayload_WireKeys pins the key set so this is checked rather
// than reviewed.
//
// NO omitempty AND NO MarshalJSON, matching RequestSessionSettingsPayload — its
// stated reason applies verbatim. There is no presence contract: absent and empty
// are the SAME case, "no conversation named", which names nothing and is refused,
// so nothing needs to tell them apart. Keeping the key always on the wire lets a
// fixture pin the full shape, and TestRequestModelListPayload_ZeroValue_KeyPresent
// reddens if an omitempty is added later for tidiness.
//
// SECURITY: SENDING THIS FRAME IS NOT A CAPABILITY, and neither is receiving an
// answer. There is no per-verb gate beyond the negotiated interactive capability,
// which is settled at handshake and cannot be influenced by anything in this
// payload. The id is loggable only AFTER it has been resolved against the
// registry — raw it is an arbitrary client string, and that is the log-injection
// shape § Attachments already forbids for a filename. The payload carries no
// count and no length, so there is nothing here a hostile value could size.
type RequestModelListPayload struct {
	// ConversationID names the conversation whose model menu is wanted. A lookup
	// key resolved against the daemon's registry, and not authorization; the empty
	// string names nothing and resolves nothing.
	ConversationID string `json:"conversation_id"`
}

// ModelOption is one row of a ModelListPayload (docs/protocol-mobile.md
// § model_list, #1704). Its fields are a subset of the per-entry keys claude's
// initialize reply carries and nothing invented. description and supportsFastMode
// are deliberately not carried — neither has a named consumer — and supportsEffort
// is subsumed by EffortLevels once the empty encoding below is decided. Adding a
// field later is cheap, and every entry multiplies against the 65519-byte v2
// application-envelope cap.
//
// ResolvedModel is what Value resolves to RIGHT NOW: the concrete identifier. It
// closes the question pyrycode-desktop#561 raised and could not — that ticket's
// shape is "send the family name, it resolves to the newest in the family", and
// it flags that the operator is then moved to a new model without choosing to.
// Publishing the resolution BEFORE the first turn is what lets a client show
// which model a family currently means, instead of inferring it from an
// announcement after the fact.
//
// Value is the argument you pass, and it is NOT a dated identifier: an alias
// (sonnet), a bracketed variant (opus[1m]), or default. A client cannot derive a
// family by splitting it on "-".
//
// Value round-trips, and a client author reading only this struct has to be told
// what shape does. The only inbound path that accepts a model is
// set_session_settings, gated by internal/relay's validModel, whose rule (widened
// at #1838 for exactly these rows) accepts "" or, within a 64-byte bound, a value
// whose first byte is alphanumeric, whose remaining bytes are in [A-Za-z0-9._-],
// and which may carry ONE trailing bracket group — non-empty, balanced, unnested,
// the value's final element, and drawn from that same closed byte class. Every
// value claude has been measured to publish satisfies it, the bracketed variant
// rows included. The group is bounded that way rather than by adding two bytes to
// the charset because the rule is #845's argv-injection defense: read validModel
// for what each clause buys and for the two sinks that depend on it.
//
// DisplayName is claude's human label, carried because it is the cleanest way to
// match a per-turn model_announced identifier to a client's alias-family row
// without a mapping table.
//
// EffortLevels are the reasoning-effort levels this model supports. The key is
// always present on the wire and never null — see MarshalJSON, and read its
// rationale, which is NOT ModelListPayload.MarshalJSON's. Measured 2026-08-22,
// all five levels claude returns (low, medium, high, xhigh, max) are accepted by
// internal/relay's validEffort, whose enum is CLOSED — so a level claude adds in
// future would be published here and refused inbound. Since #1838 widened
// validModel this is the ONLY field in the struct carrying that direction hazard;
// Value carried the same one until then, and validEffort is deliberately not
// widened alongside it because no level claude publishes is refused today.
//
// SupportsAutoMode is whether claude accepts auto permission mode for this model:
// claude refuses the request per model, so a client greys the option out when
// this is false (pyrycode-desktop#682). It collides with nothing in the daemon's
// own vocabulary — set_permission_mode carries default / acceptEdits /
// bypassPermissions / plan, and auto is claude's mode name, which the daemon does
// not currently send. Absent in claude's reply (Haiku's entry omits it) decodes to
// false, which is the correct reading.
//
// TruncatedFields names THIS row's cut fields, null when nothing was cut. The
// producer records FOUR names, in this order: "resolved_model", "value",
// "display_name", "effort_levels". "effort_levels" is the one reporting on a
// LIST rather than a value, and it covers three outcomes — an element cut to fit
// the per-element cap, the list shortened to fit the count cap, or both —
// appearing at most once per entry in every case, because the report names FIELDS
// and a list is one field. turnevent.ModelOption's TruncatedFields is the source
// of truth for that vocabulary. Deliberately NOT normalised the way EffortLevels is — see
// MarshalJSON. It is load-bearing rather than decoration: a row that dropped it
// would present claude's cut text to a phone as complete, and would offer back a
// Value the client was never told was truncated.
//
// SECURITY: ResolvedModel, Value, DisplayName and every string in EffortLevels are
// claude-authored strings that crossed the subprocess trust boundary. They are
// safe to RENDER as inert text and must never be fed to an HTML sink, an
// attribute, or a URL; the daemon bounds them but does not sanitize them — no
// control-character or terminal-escape stripping happens on this path — so they
// stay untrusted, model-influenced text all the way to the client, and the render
// boundary owing the sanitization is the CLIENT's. Their bound is the producer's,
// decided at construction, so this struct re-decides no maximum and declares no
// charset check: a second cap here would be a second place the limit is decided,
// and the two could disagree silently.
//
// The family's convention sentence — it is a REPORT, never a control input — needs
// one amendment here, because Value is the first field in the family a client is
// meant to send BACK. Publishing a value does not make it trusted: it is still
// claude's text arriving on an inbound path, and the daemon re-validates it at
// internal/relay's validModel rather than trusting that it came from a list the
// daemon itself published.
type ModelOption struct {
	ResolvedModel    string   `json:"resolved_model"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"display_name"`
	EffortLevels     []string `json:"effort_levels"`
	SupportsAutoMode bool     `json:"supports_auto_mode"`
	TruncatedFields  []string `json:"truncated_fields"`
}

// MarshalJSON normalises a nil EffortLevels to an empty array, so a row always
// serialises as "effort_levels":[] and never as "effort_levels":null. It
// deliberately leaves TruncatedFields alone, which stays null when nothing was
// cut.
//
// The reason is NOT ModelListPayload.MarshalJSON's, and the asymmetry between the
// two is not an accident. An empty effort list is not a positive statement here,
// it is a COLLAPSE: Haiku's entry omits supportedEffortLevels entirely, and a
// client's behaviour is identical for absent and empty (no effort control). The
// wire therefore states ONE position for both, and [] is the one that spares every
// row an optional-array branch. The daemon-internal value does NOT keep the
// absent/empty distinction either: turnevent.ModelOption.EffortLevels reads an
// absent key, a JSON null and a published empty array as ONE reading, spelled nil
// (#1828). The wire's position is stated here independently of that spelling,
// because an undeclared position is one #1848 would have had to invent — and, the
// two having landed on the same collapse, #1848's mapping needed no fork to
// bridge.
//
// TruncatedFields is exempt for BackgroundTaskRosterPayload.MarshalJSON's own
// carve-out reason, unchanged: nil and [] say the identical thing there ("nothing
// was cut") and no consumer branches on the difference.
//
// This method cannot be folded into the payload's: a payload marshaller
// normalising entries in place would mutate the caller's backing array unless the
// slice were copied first, and it would not fire at all when a ModelOption is
// marshalled on its own.
//
// Value receiver and the type alias, for ModelListPayload.MarshalJSON's reasons.
func (o ModelOption) MarshalJSON() ([]byte, error) {
	if o.EffortLevels == nil {
		o.EffortLevels = []string{}
	}
	type alias ModelOption
	return json.Marshal(alias(o))
}

// SlashCommandListPayload is the body of an Envelope whose Type ==
// TypeSlashCommandList (docs/protocol-mobile.md § slash_command_list). Binary →
// phone direction; the wire
// form of the slash-command inventory claude returns from a control_request with
// subtype initialize, alongside the models array ModelListPayload carries: the
// set of commands this session in this working directory will accept. A SNAPSHOT
// of what claude will accept, not a delta, and conversation-scoped rather than
// turn-scoped — receiving one neither opens nor closes a turn.
//
// Declared here (#1727) ahead of its producer so a client can be written against
// the shape — the sequencing #1405 used ahead of #1410, #1616 ahead of #1638 and
// #1704 ahead of #1848. THE PRODUCER HAS SINCE LANDED: turnbridge.MapEvent's
// turnevent.SlashCommandList arm constructs it (#2001), which is the sibling
// sequencing paid off — that arm is the one every later consumer maps through
// rather than forking. Two consumers are blocked on the shape today: pyrycode-desktop#681,
// the Actions-menu grey-out that matches its menu entries against this list, and
// pyrycode-desktop#694, a type-ahead that filters the whole list live and renders
// each row as a name, an argument hint and a description.
//
// ConversationID is present and unfilled by this ticket — the producer supplies
// it at mapping time, the seam every v2 interactive payload uses. claude's own
// session_id is deliberately absent for BackgroundTaskStartedPayload's reason:
// claude's session identity is not the daemon's conversation identity.
//
// Commands is in claude's own order. The key is always present on the wire and
// never null — see MarshalJSON.
//
// The COUNT is workspace- and version-dependent, and no client may assume one:
// 51 entries against claude 2.1.239 in this repository, while an earlier hand
// count against 2.1.220 in a different working directory reported 74. That
// variation is the feature's whole point, and it is why the list is per session
// and per working directory rather than a one-off global — so a client must not
// cache one list across working directories.
//
// The payload's cost lives in the descriptions: the capture's 51 entries
// serialise to 14,277 bytes of compact UTF-8 against the 65519-byte v2
// application-envelope cap. Comfortable but not free, so a cut has to be
// REPORTABLE rather than silent, which is what DroppedCommands and
// SlashCommand.TruncatedFields are for.
//
// DroppedCommands is how many entries the producer cut that this frame does NOT
// carry; 0 when nothing was dropped. AN ENTRY CAP NOW EXISTS AND SOMETHING NOW
// COUNTS IT: streamsup's maxSlashCommandListEntries bounds the decoded entry count
// and turnevent.SlashCommandList.DroppedCommands carries what it cut (#1826). What
// is still missing is a PATH TO A CLIENT, so read the state of this key precisely
// rather than by inference. SOMETHING NOW WRITES IT — turnbridge.MapEvent's arm
// takes turnevent.SlashCommandList.DroppedCommands as its BASE, never recomputing
// the count from len(Commands) (#2001), and ADDS whatever its own frame-size cut
// drops on top (#2002), so this field is the sum of two cuts where the event-side
// field is the producer's alone — but cmd/pyry's
// interactiveTurnEmitterV2.Handle still has no case, so no frame of this type is
// produced at all, which means every value a client could observe here today is
// STILL the zero one and there is still no frame on which len(Commands) +
// DroppedCommands is the menu's true size. It becomes that the moment #2003 lands
// the emission, and not before. It was declared ahead of all of this
// because a wire with nowhere to put a drop discards it silently, while a permanent
// 0 reads as "nothing was dropped", which is a lie rather than a gap. #1719 owned
// the decode and is CLOSED; #1826 owned the cap and the count and is closed too;
// #2001 is where the field and that counter met, and is also where the CAUSES were
// settled — the field says entries were cut without naming a cause, deliberately,
// so that a drop for a shape reason lands in the same count. The count reports
// here rather than as a name in a top-level truncated_fields — which is why this
// payload has none, BackgroundTaskRosterPayload's stated reason — because a
// name-only report loses HOW MANY were lost, and each dimension reports where it
// is decided: a text cut is a property of one entry and rides that entry as
// SlashCommand.TruncatedFields.
//
// The per-entry key set is COMPLETE, and that is measured rather than assumed.
// Against the committed capture
// internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json (claude
// 2.1.239), name, description, argumentHint and aliases are the entire per-entry
// vocabulary: 42 of the 51 entries carry the first three, the other 9 carry all
// four, and there is no third key set. This shape adopts ALL FOUR, so unlike
// ModelOption it drops nothing — which means a key a later claude adds arrives as
// a documented gap against a stated measurement rather than as a silent drop.
type SlashCommandListPayload struct {
	ConversationID  string         `json:"conversation_id"`
	Commands        []SlashCommand `json:"commands"`
	DroppedCommands int            `json:"dropped_commands"`
}

// MarshalJSON normalises a nil Commands to an empty array, so a slash-command
// list always serialises as "commands":[] and never as "commands":null.
//
// This implements BackgroundTaskRosterPayload.MarshalJSON's reason unchanged, and
// that whole rationale transfers: omitempty is out because an empty list is a
// POSITIVE statement — it says claude offered nothing — rather than an absence,
// and between null and [], [] reads as an empty list where null reads as
// absent/unknown, so a client decoding into a non-optional array type never has
// to branch. SlashCommand.MarshalJSON normalises its own Aliases for a DIFFERENT
// reason — read it there, so the asymmetry is not taken for an accident — and
// deliberately leaves TruncatedFields alone.
//
// This method cannot do the entry's job for it. Assigning a fresh slice to this
// copy's own Commands field is safe, but reaching THROUGH it into p.Commands[i]
// would mutate the caller's backing array, which for a payload shared between an
// emitter goroutine and a per-connection fan-out is a data race as well as a
// correctness bug; and a payload-level normalisation would not fire at all when a
// SlashCommand is marshalled on its own.
//
// Value receiver, so it applies to the value form a round trip and a bridge both
// take, and so the substitution lands on a copy rather than on the caller's
// slice. The type alias is the standard indirection that keeps json.Marshal from
// recursing back into this method.
func (p SlashCommandListPayload) MarshalJSON() ([]byte, error) {
	if p.Commands == nil {
		p.Commands = []SlashCommand{}
	}
	type alias SlashCommandListPayload
	return json.Marshal(alias(p))
}

// SlashCommand is one row of a SlashCommandListPayload (docs/protocol-mobile.md
// § slash_command_list, #1727). Its fields are exactly the four per-entry keys
// claude's initialize reply carries and nothing invented; the measurement behind
// "exactly" is in SlashCommandListPayload's doc.
//
// All three strings cross, not just the name. The Actions-menu grey-out needs
// names to match against, but the type-ahead renders all three: at 51 entries the
// description is what makes the list usable rather than a wall of names, and the
// argument hint is what tells the operator that a command takes something after
// it.
//
// Name is NOT an identifier. One name in the capture is __remote-workflow, so no
// charset assumption belongs in this struct or in a client.
//
// ArgumentHint is EMPTY on 33 of the capture's 51 entries, and no entry omits the
// key — so an empty hint is the ordinary case rather than missing data. That is
// why no string key here is optional: eliding it would make the common row
// indistinguishable from a malformed one.
//
// Description may contain NEWLINES — claude-api's does — and 0x0a is the ONLY
// sub-0x20 byte anywhere across the 51 entries' four string fields. Newlines are
// therefore the control characters on this path rather than one class among
// several, and a client rendering a single-line row must handle that specific
// case.
//
// Aliases is what makes the grey-out correct, and a shape dropping it would break
// the first consumer. The desktop Actions menu's own reset entry is an ALIAS of
// clear, not a command name, so a client matching against Name alone greys out a
// command that works. It is also why the cheap source cannot answer the question:
// the same capture's system/init line carries slash_commands, the identical 51
// names in the identical order as bare strings, and not one of the 11 aliases.
// The key is always present on the wire and never null — see MarshalJSON, and
// read its rationale, which is NOT SlashCommandListPayload.MarshalJSON's.
//
// TruncatedFields names THIS row's cut fields — "name", "argument_hint",
// "description", "aliases", the wire names rather than the Go ones, as
// RateLimitedPayload.TruncatedFields uses its JSON tags — and is null when
// nothing was cut. All four are enumerated because all four are strings the
// SECURITY paragraph credits and a producer may cut; #1720 reads this line to
// pick the names its producer emits, so an under-enumeration here would
// under-cover a real field. Deliberately NOT normalised the way Aliases is — see
// MarshalJSON. It is load-bearing rather than decoration: a row that dropped it
// would present claude's cut text to a phone as complete.
//
// SECURITY: Name, ArgumentHint, Description and every string in Aliases are
// WORKSPACE-authored strings that crossed the subprocess trust boundary. That
// strengthens ModelOption's claude-authored warning rather than restating it: a
// command defined in a repository was written by whoever wrote that repository,
// which is a lower-trust origin than claude. They are safe to RENDER as inert
// text and must never be fed to an HTML sink, an attribute, or a URL; the daemon
// bounds them but does not sanitize them — no control-character or
// terminal-escape stripping happens on this path — so they stay untrusted text
// all the way to the client, and the render boundary owing the sanitization is
// the CLIENT's. Their bound is the producer's (#1719/#1720), decided at
// construction, so this struct re-decides no maximum and declares no charset
// check: a second cap here would be a second place the limit is decided, and the
// two could disagree silently.
//
// The family's convention sentence — it is a REPORT, never a control input —
// needs the amendment ModelOption.Value carries, for a different reason: a client
// is meant to send a Name BACK, as the text of an ordinary message, because
// sending the slash command IS the feature. Publishing a name does not make it
// trusted. It arrives inbound as ordinary message text, on a path that does not
// treat it as a command vocabulary and does not consult this list, and no field
// here reaches a child as an argv element. This frame declares no inbound verb —
// TypeSlashCommandList's own doc has that reasoning — and grants nothing.
type SlashCommand struct {
	Name            string   `json:"name"`
	ArgumentHint    string   `json:"argument_hint"`
	Description     string   `json:"description"`
	Aliases         []string `json:"aliases"`
	TruncatedFields []string `json:"truncated_fields"`
}

// MarshalJSON normalises a nil Aliases to an empty array, so a row always
// serialises as "aliases":[] and never as "aliases":null. It deliberately leaves
// TruncatedFields alone, which stays null when nothing was cut.
//
// The reason is NOT SlashCommandListPayload.MarshalJSON's, and the asymmetry
// between the two is not an accident. An empty alias list is not a positive
// statement here, it is a COLLAPSE — and that is measured rather than argued.
// claude never sends "aliases": []: zero of the capture's 51 entries carry an
// empty array, 42 omit the key entirely and 9 carry a non-empty one. An entry
// with no aliases and an entry with the key absent are the same statement, and a
// client must not have to branch on absent-vs-empty to match an alias, so the
// wire states ONE position for both and [] is the position that spares every row
// an optional-array branch. This is exactly ModelOption.MarshalJSON's
// EffortLevels collapse with the frequency INVERTED: the majority case here, the
// single exception (Haiku) there.
//
// THE DAEMON-INTERNAL VALUE COLLAPSES TOO, and #1825 settled it — the question
// this comment used to hand forward. turnevent.SlashCommand.Aliases reads an
// absent key, a JSON null and a published empty array as ONE reading, spelled
// nil, and owns the argument for it. It reached that answer the way
// turnevent.ModelOption.EffortLevels reached its own (#1828), by WEIGHING that
// precedent rather than inheriting it: the two lists' frequencies are inverted,
// so what carried was not the levels' conclusion but the observation that the
// frequency changes how often a collapse fires and not what either shape MEANS.
// So the two sides of this boundary agree, which is what makes this method's
// normalisation a one-shape job rather than a two-shape reconciliation. The
// wire's position would have been stated here either way, because an undeclared
// position is one #1720 would have to invent.
//
// TruncatedFields is exempt for BackgroundTaskRosterPayload.MarshalJSON's own
// carve-out reason, unchanged: nil and [] say the identical thing there ("nothing
// was cut") and no consumer branches on the difference.
//
// This method cannot be folded into the payload's: a payload marshaller
// normalising entries in place would mutate the caller's backing array unless the
// slice were copied first, and it would not fire at all when a SlashCommand is
// marshalled on its own.
//
// Value receiver and the type alias, for SlashCommandListPayload.MarshalJSON's
// reasons.
func (c SlashCommand) MarshalJSON() ([]byte, error) {
	if c.Aliases == nil {
		c.Aliases = []string{}
	}
	type alias SlashCommand
	return json.Marshal(alias(c))
}

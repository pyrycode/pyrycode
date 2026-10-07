package protocol

import (
	"encoding/json"
)

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
//
// ONE FIELD IN THIS FILE DEPARTS FROM THAT RULE, and it is the only one:
// ContextUsagePayload.AsOf (#2461), whose own block carries the argument.
// Adding a second requires the same argument made again — that absence and
// presence are DIFFERENT FACTS a client must branch on, and that the frame
// already ships from a producer whose bytes a criterion freezes.
// CompactionBoundaryPayload's two counts are how the rule is normally bent
// instead: pointers with NO omitempty, so an absent value marshals as a
// literal null and every key stays present.

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

// ReplySuggestionPayload is the body of an Envelope whose Type ==
// TypeReplySuggestion. Declared, not yet emitted: a daemon-to-client v2 state
// frame gated by the negotiated "interactive" capability.
//
// Revision is positive and increases per conversation within one daemon
// lifetime. Clients replace suggestion state by conversation/session, ignore
// lower revisions, and discard cached suggestions on a fresh handshake.
// SuggestedReply is inert, single-line UTF-8 text of at most 1024 bytes. Only
// explicit null clears state; omission and an empty string do not. No field
// uses omitempty, so a nil SuggestedReply always emits the clear signal.
//
// Emission and semantic validation belong to later consumers. Ordinary JSON
// decoding into this DTO does not distinguish an omitted suggestion from null.
type ReplySuggestionPayload struct {
	ConversationID string  `json:"conversation_id"`
	SessionID      string  `json:"session_id"`
	Revision       uint64  `json:"revision"`
	SuggestedReply *string `json:"suggested_reply"`
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
// ResultDetail is a short précis of an edit or write's structured outcome —
// "+10 −3", "created · 54 lines" — composed by the daemon from the
// tool_use_result sidecar. Read, shell, search and unrecognised shapes send an
// empty detail (#2745). A client renders it beside the row and never parses it.
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

// UnrecognizedMessagePayload is the body of an Envelope whose Type ==
// TypeUnrecognizedMessage (docs/protocol-mobile.md § unrecognized_message).
// Binary → phone direction; the wire form of the internal-only
// turnevent.Unrecognized diagnostic marker. Like turn_state it is a coarse
// conversation-level signal, not turn-scoped, so there is no turn_id — an
// unrecognized message has no turn we can honestly attribute it to.
//
// Site is where the parser dropped the payload: "line_type", "assistant_block",
// "user_block" and "undecodable" from claude's stream-json, and "codex_method"
// and "codex_item" from codexsup's Codex translator. MessageType is the
// offending message or block `type` (the Codex method or item type on the
// Codex sites), empty when Site is "undecodable" (nothing decoded, so no type
// was read). Raw is the offending JSON, truncated by the producer to a fixed byte
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

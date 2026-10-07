package turnevent

import "encoding/json"

// TextChunk is incremental assistant text, grouped by message.
// ParentToolCallID names the Agent/Task call that spawned the subagent producing
// the text, or is empty on the main conversation.
type TextChunk struct {
	MessageID        string
	ParentToolCallID string
	Text             string
}

// ThoughtChunk is streaming reasoning ("thinking") text, grouped by message.
type ThoughtChunk struct {
	MessageID string
	// ParentToolCallID names the spawning Agent/Task call, or is empty for main-thread thinking.
	ParentToolCallID string
	Text             string
}

// ToolStart announces a new tool invocation.
//
// RawInput is opaque pass-through tool input the model never parses or mutates;
// it is carried as json.RawMessage (undecoded bytes) precisely because that
// does not force a parse — consumers decode it on their own terms.
//
// ParentToolCallID names the Agent/Task call that spawned the subagent making
// this call, or is EMPTY on the main conversation (#2191). A consumer joins a
// child to its parent on the parent's own ToolCallID, which is byte-identical
// across the two — so three parallel subagents read as three groups rather than
// thirty interleaved rows. Nesting is carried by carrying nothing: the value is
// read off claude's line verbatim with no branch on depth, so a call made by a
// subagent that a subagent spawned names the INNER Agent call and a consumer
// rebuilds the whole tree by following ids.
//
// EMPTY HAS ONE MEANING, "main thread", and that is what makes it safe to drop
// an unusable value into. streamsup drops rather than cuts a value it cannot
// read or that exceeds its cap, because this is a JOIN KEY and a cut id matches
// no ToolCallID while still looking like one — see the constant named at
// streamsup's parentToolUseID. So a dropped value degrades to top-level
// rendering, which is honest, rather than to a wrong parent, which is not.
type ToolStart struct {
	ToolCallID       string
	ParentToolCallID string
	Title            string
	Kind             ToolKind
	RawInput         json.RawMessage
	Locations        []Location
}

// ToolUpdate carries changed fields of an existing tool call. Content may be
// nil for a status-only update.
//
// ResultDetail is a short, DAEMON-COMPOSED précis of the call's structured
// outcome for edits and writes — "+10 −3", "created · 54 lines" — derived
// from the tool_use_result sidecar claude writes alongside each result.
// Read, shell, search and unrecognised shapes send an empty detail (#2745).
//
// Its provenance is the opposite of Content's and the distinction matters to
// every consumer. Content carries claude's own bytes, so it needs a cap
// downstream (turnbridge's maxResultSummaryRunes). ResultDetail contains NO
// claude-supplied byte at all: streamsup's toolResultDetail formats decoded
// integers and its own literals, so the alphabet is digits, spaces, ASCII
// letters and exactly two further runes — U+2212 MINUS SIGN and U+00B7 MIDDLE
// DOT, which the edit and write forms use as separators — and the length is
// bounded by int64's range at construction. Treat it as display text — render it
// verbatim, never parse it; the unit words and both glyphs live here precisely
// so a client need not switch on a tool name to know what the number counts.
// ParentToolCallID is ToolStart's field with ToolStart's meaning, carried here so
// the result row can be grouped with the call row it completes. It is read off the
// `user` line this update was mapped from, not copied from the ToolStart — the
// parser holds no cross-line state for it — so the two agreeing is a fact about
// claude's wire rather than something this package arranges.
type ToolUpdate struct {
	ToolCallID       string
	ParentToolCallID string
	Status           ToolStatus
	Content          ToolContent
	ResultDetail     string
}

// ToolProgress reports the elapsed time of a tool call that ToolStart already
// announced. It maps claude's top-level tool_progress heartbeat and is distinct
// from ToolUpdate: a ToolUpdate from streamsup is terminal, while this event is a
// non-terminal reading that may repeat until the tool_result closes the row.
//
// ToolCallID is read from the heartbeat's parent_tool_use_id, not tool_use_id.
// Claude gives each heartbeat a synthetic tool_use_id ending in `-heartbeat-N`;
// parent_tool_use_id is the byte-identical handle published by ToolStart. The
// producer requires a non-empty JSON string no longer than
// streamsup.maxTaskFieldID and drops the whole event otherwise. It never cuts the
// id, because a cut join key matches no row while still looking usable.
//
// ElapsedSeconds is claude's signed integer reading carried verbatim. Zero means
// claude omitted the key or explicitly reported zero; a negative value stays
// observable rather than wrapping or being normalized. The daemon does not infer
// cadence, accumulate readings, or keep per-call state.
//
// The captured session_id, uuid, and tool_name are deliberately absent. The first
// two do not identify the daemon conversation or tool row, and ToolStart already
// published the tool name. Like every event here, this carries no daemon
// conversation identity; adapters inject it.
type ToolProgress struct {
	ToolCallID     string
	ElapsedSeconds int
}

// TurnEnd marks the end of a claude turn, carrying the reason, — since #2101 —
// the context window claude reported for each model the turn touched, since
// #2223 the STOP SHAPE claude itself reported for the turn, and — since #2224 —
// the API ERROR CATEGORY an assistant line in the turn reported.
//
// ACP models end-of-turn as the stopReason return value of session/prompt, not
// as an event; converting TurnEnd back into that RPC return is the ACP
// adapter's job, not this model's. Here we just carry the reason.
//
// EVERY OTHER FIELD IS READ OFF THE `result` LINE; ErrorCategory IS NOT. It comes
// off an `assistant` line earlier in the same turn and is REMEMBERED until the
// boundary, which makes it the only field here whose value the producer had to hold
// across lines rather than read at the moment of emit. That is a property of the
// producer, not of this type — a consumer reads all six the same way — but it is
// why streamsup's Parser doc, and not this one, carries the residual argument.
//
// TWO CLASSIFICATIONS RIDE THIS VARIANT AND THEY ARE NEVER RECONCILED. Reason is
// the DAEMON's two-value reading, derived from the subtype by streamsup's
// resultTurnEndReason and unchanged since #1075; Outcome is claude's own token
// for the same stop, carried verbatim. A turn that hit --max-turns is
// end_turn/error_max_turns, and both halves of that are true — the first is what
// the ACP taxonomy can say, the second is what actually happened. #2223 added the
// second precisely because collapsing every stop into the first made a truncated
// run read as a finished answer, so a consumer that "resolves the disagreement"
// by preferring one is undoing the ticket.
//
// THE WINDOW FIELDS RIDE THIS VARIANT RATHER THAN ONE OF THEIR OWN, and the
// reason is that the window is learned at exactly the moment this event is
// emitted: claude reports it on the `result` line, which IS the turn boundary,
// so a separate variant would carry a second copy of one boundary. The
// alternative — #1600's shape, a new variant off its own line — is the right
// answer when the line is its own line, and this one is not.
//
// WIDENING THIS VARIANT STILL CANNOT WIDEN THE ENVELOPE, by construction rather
// than by omission: turnbridge's MapEvent builds protocol.TurnEndPayload field by
// field rather than embedding this struct, so every field's publication is a
// decision someone made rather than a consequence of declaring it here.
//
// WHICH FIELDS ARE PUBLISHED CHANGED WITH #2223, and this paragraph used to say
// none were. The window pair (ModelWindows, DroppedModelWindows) still is not —
// no wire consumer wants it yet and #2102 owns whatever publication it needs — so
// neither window cap states a percentage of the v2 application-envelope, quoting
// one being a bound measured against a wire that data never touches. The three
// stop fields below ARE published, and so is #2224's ErrorCategory, so their
// shared cap (streamsup's maxTurnEndStopField) does state one.
//
// This variant is therefore no longer comparable with ==. Nothing compared it
// (the only TurnEnd{} in the tree is the sealed-interface assertion below), and
// ModelList has put a slice-carrying variant through every emitter path this one
// travels since #1812.
type TurnEnd struct {
	Reason TurnEndReason
	// ModelWindows is claude's reported context window for each model the turn
	// touched, one entry per model id claude used, SORTED BY ModelID.
	//
	// The sort is the daemon's and is not claude's order restored: claude sends a
	// JSON OBJECT, which has no order to preserve, and Go randomises map iteration
	// — so without a sort, which entries survived the count cap would differ
	// between two runs on identical bytes. Sorting is what makes the cut
	// reproducible. It is the one place this family orders anything itself; the
	// list-shaped variants beside it (Models, Tasks) keep claude's order precisely
	// because they HAVE one.
	//
	// EVERY ENTRY CARRIES A USABLE WINDOW. The producer drops an entry whose
	// contextWindow is absent, zero or negative rather than reporting a zero, so a
	// consumer joining on ModelID never has to re-ask whether the number means
	// anything — which is the same answer contextwindow.Usage.WindowTokens gives
	// with its own 0 (#2100), reached by not carrying the entry at all.
	//
	// TWO ENTRIES DO NOT IMPLY TWO MODELS. The committed captures show both shapes:
	// a helper model beside the session's own (haiku at 200K, sonnet at 1M), and —
	// in 27 of 30 capture files — an ALIAS PAIR naming one model twice
	// (claude-haiku-4-5 and claude-haiku-4-5-20251001, identical window). So this
	// list is carried whole and keyed by the id claude used: taking the max, the
	// first, or "the one that isn't haiku" would each be wrong on one of those
	// shapes.
	//
	// AN ABSENT KEY, A JSON null, A PUBLISHED EMPTY OBJECT, A modelUsage THAT DOES
	// NOT DECODE, AND A MAP WHOSE EVERY ENTRY WAS DROPPED ARE ONE READING, SPELLED
	// nil — ModelOption.EffortLevels' collapse (#1828), extended by two shapes that
	// field has no equivalent of. Nothing downstream has to ask which of the five it
	// is holding, because there is no question any of them answers differently.
	//
	// BOTH DIMENSIONS ARE BOUNDED at construction by the producer — how many
	// entries (streamsup's maxModelWindowEntries) and how long one id is
	// (streamsup's maxModelWindowID) — so neither an inflated map nor an oversized
	// id enters the event stream, a queue, or a log.
	ModelWindows []ModelWindow
	// DroppedModelWindows is how many entries claude sent that this event does NOT
	// carry; 0 when nothing was dropped. The map's true size is
	// len(ModelWindows) + DroppedModelWindows.
	//
	// ONE COUNTER COVERS ALL THREE CAUSES — an unusable window, an id past the
	// length cap, and the entry count past its cap. That is a deliberate departure
	// from ModelList, whose DroppedModels counts a cardinality overflow only and
	// leaves text cuts to a per-entry TruncatedFields. Here there IS no per-entry
	// report to leave anything to: every bound on this shape DROPS rather than
	// truncates, so a per-cause split would buy a consumer nothing — nothing renders
	// this list, and #2102 joins on ids it either has or does not — while costing
	// two fields to keep in step. What the single counter preserves is the property
	// that matters: the sum above is exactly what claude sent, so an overflow is
	// never silent.
	//
	// Like ModelList.DroppedModels it is DAEMON-derived rather than claude-derived:
	// an int computed from map and slice lengths, carrying none of claude's bytes.
	DroppedModelWindows int
	// Outcome is the subtype claude put on its `result` line — the token naming
	// HOW the turn stopped, where Reason names only whether it was cancelled.
	// Observed values are success, error_during_execution, error_max_turns,
	// error_max_budget_usd and error_max_structured_output_retries.
	//
	// AN OPEN SET, and carried VERBATIM per ModelAnnounced.Model's rule: no
	// lowercasing, no mapping onto the values above, no rejection of a token this
	// list does not name. A subtype claude ships tomorrow reaches a consumer as
	// itself, which is the whole reason this is a string rather than an enum.
	//
	// EMPTY MEANS ABSENT-OR-OVER-CAP and the two are deliberately one reading.
	// claude omits the key on some lines, and streamsup's maxTurnEndStopField
	// DROPS an over-long value rather than cutting it — a cut token matches
	// nothing a consumer could act on, so it would be a value that lies rather
	// than a gap that shows. Neither shape is something a consumer answers
	// differently, so neither gets a field to say which it was.
	//
	// NOT the same field as Reason and NOT claude's own `stop_reason` key, which
	// this daemon does not forward at all. See this variant's doc for why the two
	// classifications are never reconciled.
	//
	// SECURITY: claude-authored text. The daemon BOUNDS it and does NOT sanitize
	// it — no control-character or terminal-escape stripping happens on this path
	// — so it stays untrusted, model-influenced text, exactly as ModelWindow.ModelID
	// states beside it. Unlike that field this one DOES reach a client, so the
	// render boundary owing the sanitization is the CLIENT's and is named as such
	// in docs/protocol-mobile.md § turn_end.
	Outcome string
	// IsError is claude's own is_error flag off the same line, false when absent.
	//
	// IT IS NOT DERIVED FROM Outcome and must not be. claude sends subtype
	// `success` WITH is_error true when the turn ended on an API error — a context
	// overflow is the documented case, where the `result` text is the error text —
	// so a consumer inferring the flag from the subtype reads exactly that turn as
	// a clean answer, which is the failure #2223 exists to remove.
	//
	// A plain bool: absent, JSON null and an explicit false are one reading, and
	// nothing acts differently on the three, so a pointer would buy a distinction
	// no consumer answers. DAEMON-observable rather than claude-authored TEXT — it
	// carries no bytes of claude's — so it needs no cap.
	IsError bool
	// TerminalReason is claude's terminal_reason for the turn: the finer-grained
	// cause beside the subtype, as in max_turns, budget_exhausted, prompt_too_long,
	// hook_stopped or completed.
	//
	// THE MOST OPEN OF THE THREE SETS. claude documents nineteen values and the
	// list is explicitly not closed, so this is carried verbatim under Outcome's
	// rule with no mapping and no rejection — and it is the field that makes a
	// context overflow legible at all, since its subtype is plain `success`.
	//
	// Empty means absent-or-over-cap, is bounded by the same maxTurnEndStopField,
	// and carries Outcome's SECURITY paragraph unchanged.
	TerminalReason string
	// ErrorCategory is the API error an `assistant` line in this turn reported at
	// the WRAPPER level — a sibling of `message`, never a content block (#2224).
	// Documented values are authentication_failed, oauth_org_not_allowed,
	// account_on_hold, billing_error, rate_limit, overloaded, invalid_request,
	// model_not_found, server_error, max_output_tokens and unknown.
	//
	// AN OPEN SET carried VERBATIM, empty meaning absent-or-over-cap, bounded by the
	// same maxTurnEndStopField that drops rather than cuts: Outcome's four rules
	// hold here unchanged and are not restated.
	//
	// IT IS CLAUDE'S REPORT, NOT THE DAEMON'S FINDING, and this is the one thing a
	// consumer must not get wrong. Half these values name an ACCOUNT state rather
	// than a turn state — account_on_hold, billing_error, authentication_failed —
	// where Outcome and TerminalReason describe only how the turn stopped. The
	// daemon does not verify any of them: it read a string off claude's stdout and
	// carried it. A surface that renders one as its own assertion about the
	// operator's account is presenting model-authored text as daemon chrome, which
	// is the trap protocol.QuestionDismissedPayload's outcome field exists to avoid.
	// Attribute it to claude wherever it is shown.
	//
	// NOTHING IN THE DAEMON ACTS ON IT, deliberately and as of #2224 structurally:
	// no retry, no backoff, no teardown and no routing is keyed on this value
	// anywhere. That is what keeps a fabricated rate_limit a misleading label rather
	// than an actuator, and whoever first makes the daemon behave differently on it
	// owes the review that turns it into one.
	//
	// SECURITY: claude-authored text, bounded and NOT sanitized, so Outcome's
	// SECURITY paragraph applies verbatim — the render boundary owing
	// control-character and terminal-escape stripping is the CLIENT's, named as such
	// in docs/protocol-mobile.md § turn_end.
	ErrorCategory string
	// DurationMS is how long THIS TURN took, in milliseconds, as claude reported it
	// on the `result` line that ends the turn (#2260).
	//
	// PER TURN, NOT A RUNNING TOTAL, and the evidence is that it FALLS: measured
	// 2026-09-09 across the 32 committed captures under internal/e2e/realclaude/testdata,
	// it is non-monotonic in 13 of the 21 captures carrying more than one result line.
	// Its neighbour DurationAPIMS is the opposite reading, which is the distinction the
	// four fields' shared doc below exists to state.
	DurationMS int
	// DurationAPIMS is claude's duration_api_ms for the session so far, in milliseconds.
	//
	// A RUNNING TOTAL, AND THIS IS THE ONE READING MOST LIKELY TO BE GOT WRONG. The
	// name invites "the API time inside this turn"; it is not. It is strictly
	// monotonic across all 21 multi-turn captures and is LARGER than DurationMS on 53
	// of the 57 observed result lines, so a consumer reading it as this turn's API
	// time renders eleven seconds of API work for a three-second turn.
	//
	// DIFFERENCING CONSECUTIVE LINES DOES NOT RESCUE THAT READING EITHER. The value
	// already exceeds its own turn's DurationMS on the FIRST result line of 19 of the
	// 21 multi-turn captures, where a running total has accumulated nothing but that
	// one turn — so whatever it sums, it is not bounded by the turn's wall clock.
	//
	// The daemon does NOT convert it into a per-turn delta, and holds no previous
	// line's value to subtract one from: this decode publishes what claude sent for
	// this line and remembers nothing across lines.
	DurationAPIMS int
	// NumTurns is claude's num_turns for THIS TURN — how many model round-trips it
	// took, not a count of turns in the session.
	//
	// PER TURN, on DurationMS's side of the split. It holds at 2 across all three
	// result lines of each bypass_reescalation_v2.1.239_* capture, where a cumulative
	// counter would read 2, 4, 6; within one capture it rises 1 → 5 when a turn
	// actually made more round-trips.
	NumTurns int
	// CostUSDTotal is claude's total_cost_usd: what the SESSION has cost so far, in
	// US dollars. Spelled cost_usd_total on the wire, per #2199's shape — the one
	// deliberate respelling in this group.
	//
	// A RUNNING TOTAL, with DurationAPIMS, and strictly monotonic across all 21
	// multi-turn captures. Undifferenced, for that field's stated reason.
	//
	// IT IS CLAUDE'S ESTIMATE, NOT A BILLING STATEMENT. The daemon does not verify
	// it, reconcile it against anything, or compute it — it read a number off
	// claude's stdout and carried it. On a subscription it is informational.
	CostUSDTotal float64
	// InputTokens, OutputTokens, CacheReadTokens and CacheCreationTokens are the
	// four counts claude reports in the result line's usage object for THIS TURN.
	// They are copied without summing, conversion, clamping or ordering checks.
	//
	// THREE ARE INPUT-SIDE: InputTokens is only uncached input, while
	// CacheReadTokens and CacheCreationTokens are the cached-input components.
	// OutputTokens is output-side. InputTokens + OutputTokens is therefore NOT the
	// turn's total token use.
	//
	// These are claude-authored observations, not daemon measurements. Nothing in
	// the daemon acts on them: no budget, retry, routing, teardown or lifecycle
	// decision reads them. A negative value remains observable as claude sent it.
	// Absent, null, unreadable and explicit zero are one zero-value reading.
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	// THE FOUR TURN-TOTAL NUMBERS ABOVE THE TOKEN COUNTS SHARE ONE SET OF RULES,
	// stated once here rather than four times.
	//
	// A ZERO IS A NUMBER CLAUDE SENDS, not only a decode fallback, and a consumer
	// reading one as "the daemon could not get this" mislabels a real turn.
	// compaction_v2.1.259.json reports duration_api_ms 0 and num_turns 0 on a line
	// whose duration_ms is 15617 and whose cost is non-zero. It is the only such line
	// in the corpus, which is exactly why the collapse has to be documented rather
	// than left to be discovered.
	//
	// NOTHING IS CLAMPED, RANGE-CHECKED OR ORDERED — RateLimited's posture for its own
	// two numbers, and in particular there is NO DurationAPIMS <= DurationMS
	// consistency check anywhere on this path, because it would reject 53 of the 57
	// observed lines. A negative reading is published as claude sent it, which is why
	// the three counts are signed: an unsigned type would wrap one into an enormous
	// positive duration and report it as fact.
	//
	// SECURITY: claude-authored, and threat 1 lands OUTWARD — but NOT in the shape
	// Outcome's SECURITY paragraph states, so that paragraph is deliberately not
	// borrowed here. A JSON number cannot hold a control character, a terminal
	// escape, markup or a URL, so there is no sanitization obligation to hand a
	// client and no cap over claude's input length to apply; the bound is over the Go
	// type's RANGE, so an int formats to at most 20 bytes and a float64 to at most 24
	// and no hostile value can grow the frame. What DOES land is threat 1's
	// misattribution half: these are claude's numbers and the daemon verifies none of
	// them, so a surface rendering CostUSDTotal as its own accounting presents
	// model-authored data as trusted chrome — ErrorCategory's trap, reached through a
	// number instead of a token. Attribute all four to claude wherever they are shown.
	//
	// NOTHING IN THE DAEMON ACTS ON ANY OF THEM, deliberately and structurally: no
	// retry, no backoff, no teardown, no routing — and, the one a future reader will
	// reach for first, no budget or spend enforcement keyed on CostUSDTotal. That is
	// what keeps a fabricated cost a misleading label rather than an actuator, and
	// whoever first makes the daemon behave differently on one owes the review that
	// turns it into one.
}

// ModelWindow is one entry of TurnEnd.ModelWindows: the context window claude
// reported for one model. Not an Event, so it carries no marker —
// ModelOption's and BackgroundTask's shape, for their reason.
//
// The pair is claude's whole answer for one model. maxOutputTokens and
// canonicalModel ride the same wire entry and are deliberately absent here: the
// first is nothing this daemon reads, and the second is version-dependent —
// present in the v2.1.220 and v2.1.239 captures, absent in v2.1.143 / v2.1.158 /
// v2.1.199 — so a field for it would invite a consumer to depend on a key three
// of the five observed claude versions do not send. Absence from the producer's
// DECODE TARGET is the stronger half of that guarantee; see streamsup's
// resultLine.
type ModelWindow struct {
	// ModelID is the key claude used in its modelUsage map. VERBATIM, per
	// ModelAnnounced.Model's rule: no lowercasing, no alias expansion, no
	// date-stamping, no family mapping, and no lookup against any published model
	// list. It need not be dated and an alias pair may name one model twice — see
	// TurnEnd.ModelWindows.
	//
	// SECURITY: this is claude-authored text. The daemon BOUNDS it (streamsup's
	// maxModelWindowID) and does NOT sanitize it — no control-character or
	// terminal-escape stripping happens on this path — so it stays untrusted,
	// model-influenced text, exactly as protocol.ModelOption's SECURITY paragraph
	// states for the three strings beside it. The render boundary owing the
	// sanitization is the CLIENT's. Nothing renders it today, this variant being
	// unreachable from the wire; #2102 inherits the obligation if it publishes it.
	//
	// It is never TRUNCATED. An id past the cap is dropped with its entry, because
	// a cut id names no model and a consumer joining on it would silently match
	// nothing — strictly worse than an absent entry, which it can see.
	ModelID string
	// WindowTokens is the context window claude reported for this model, in tokens.
	// Always > 0: the producer drops an entry whose reading is absent, zero or
	// negative rather than carrying one here.
	//
	// An int, matching contextwindow.Usage.WindowTokens — the field a gauge sizes
	// against and the join this event exists to feed. It is claude's REPORTED
	// number rather than one inferred from a model string, which is what makes it
	// right by construction: the two-model capture was launched `--model sonnet`
	// and announced the plain string claude-sonnet-5 with no [1m] variant marker,
	// yet reports a 1M window — so any map keyed on the announced name would have
	// been wrong there.
	WindowTokens int
}

// Location is a file a tool call touches (ACP tool-call location). Line is
// 1-based; 0 means unspecified.
type Location struct {
	Path string
	Line int
}

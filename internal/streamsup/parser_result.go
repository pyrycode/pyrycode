package streamsup

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxModelWindowID caps the model id of one turnevent.ModelWindow — the key
// claude uses in the `result` line's modelUsage map. Applied at CONSTRUCTION like
// every cap above, so an oversized value never enters the event stream, the push
// queue, or any log.
//
// MEASURED, not chosen, and against a wider base than the caps above it: every
// modelUsage in the tree, re-counted 2026-09-05 — 55 `result` objects across 30
// committed capture files spanning five claude versions (2.1.143, 2.1.158,
// 2.1.199, 2.1.220, 2.1.239). The longest id is 25 bytes
// (claude-haiku-4-5-20251001), so 256 is roughly 10.2x the observation. That is
// maxModelField's and maxModelResolved's multiple over the SAME identifier shape,
// and for their reason verbatim: room for a naming scheme claude has not shipped
// yet, and still a hard cut on anything that has stopped being an identifier.
//
// A separate constant even though it currently equals maxModelField,
// maxModelResolved and maxModelValue: maxRateLimitField's paragraph applies —
// they bound different fields for different reasons, and folding them into one
// would make a future change to one budget silently move this one.
//
// OVERFLOW DROPS THE ENTRY RATHER THAN TRUNCATING IT, which is where this cap
// parts company with all three of those siblings, and the departure is the whole
// point rather than an inconsistency. truncateField is deliberately NOT called
// here. #2102 JOINS on this id, so a cut id names no model and matches nothing —
// a consumer holding it cannot tell a truncation from a model it has never heard
// of, where an absent entry it can see. A mangled identifier is the cheaper
// failure when the field is DISPLAYED, which is what the sibling caps bound; it
// is the more expensive one when the field is a KEY. There is consequently no
// TruncatedFields on this shape at all: the report is
// turnevent.TurnEnd.DroppedModelWindows, and its doc states why one counter
// covers every cause.
//
// No RATE bound, and none is owed: a `result` line is the turn boundary, so this
// fires once per TURN — maxModelField's own situation, below the ~1-2 per turn
// that minThinkingTokensPerEvent's gate already accepts.
const maxModelWindowID = 256

// maxModelWindowEntries caps how many entries turnevent.TurnEnd.ModelWindows
// carries. The family's third cardinality bound, after maxTaskRosterEntries and
// maxModelListEntries, and it follows their doctrine: a per-entry text cap alone
// leaves the total a function of a number claude chooses, so the count bound
// supplies the missing factor. Overflow is REPORTED
// (turnevent.TurnEnd.DroppedModelWindows), not silent.
//
// The number is DERIVED:
//
//   - Multiplicand: 256 bytes per entry (maxModelWindowID). The int beside it is
//     DAEMON-decoded and carries none of claude's bytes, so it is excluded exactly
//     as maxModelListEntries excludes turnevent.ModelOption.SupportsAutoMode.
//   - Ceiling: maxUnrecognizedRaw's whole-line 16 KiB, at 1/4 of it — the package's
//     ordering rule that a whole KNOWN event must not approach the cap on an entire
//     UNKNOWN line, measured retained-against-retained. The fraction is RE-DERIVED
//     for this shape rather than inherited from the roster's 1/2 or the model
//     list's 5/8, which is ADR 036's rule. 16 * 256 = 4096 = 16384/4 exactly.
//   - Floor: the observed map is TWO entries, in all 55 of them, across all five
//     versions. But entries are not models: an alias pair names ONE model twice
//     (claude-haiku-4-5 beside claude-haiku-4-5-20251001, identical window) in 27
//     of the 30 capture files, so entries run at roughly twice the models a turn
//     touched. 16 entries is therefore about 8 models in one turn, against an
//     observation of at most two.
//   - Product: 16 * 256 = 4096 bytes = 4 KiB retained. NO application-envelope
//     percentage is quoted, and its absence is deliberate rather than an omission:
//     turnbridge's MapEvent builds protocol.TurnEndPayload field by field, so this
//     event is unreachable from the v2 envelope by construction and a percentage of
//     it would measure a bound against a wire this data never touches. If #2102
//     publishes the field, that arithmetic is owed THERE.
//
// A POWER OF TWO, matching every constant in this family except
// maxModelListEntries, whose own doc explains why it alone is decimal.
//
// NOT 4. Twice an observation of two is not room, and under the alias doubling 4
// entries is only 2 MODELS — a turn that spawns a subagent on a third model is
// claude's ORDINARY output and would be cut. That is maxModelListEntries' NOT 8
// failure one scale down: a cap firing on the everyday case.
//
// NOT 8. Four models. A turn using the session model, a haiku helper and two
// subagent models, each aliased, is exactly 8 — a cap sitting ON the boundary of
// plausible ordinary output rather than above it. The doubling is what makes 8
// read tighter here than the same number reads on a list of models.
//
// NOT 32. 8192 bytes is half of maxUnrecognizedRaw, which is maxTaskRosterEntries'
// fraction, and nothing observed asks for it. Taking half the unknown-line budget
// for a field observed at two inverts the ordering rule the ceiling comes from.
//
// THE TRANSIENT FIGURE IS THIS SHAPE'S OWN, computed rather than inherited, and it
// is the one place this decode costs more than its siblings. Both caps are applied
// AFTER json.Unmarshal, so a hostile map is materialised before any of it is
// bounded — maxTaskRosterEntries' accepted trade. defaultMaxParseBuf caps the whole
// line at 4 MiB before the decoder sees it, and the densest window-CARRYING entry
// is roughly `"aaaa":{"contextWindow":1},` at ~26 bytes, so one pathological line
// is order 10^5 decoded entries. Unlike its siblings this shape then SORTS the
// survivors, which is the one super-linear step in the package's decode paths: an
// order-10^5-element slice at O(n log n) on the parser's reader goroutine. It is a
// constant-factor multiple on top of the json.Unmarshal spike the family already
// accepts, bounded by defaultMaxParseBuf and by nothing else, and reclaimed with
// the line. What is RETAINED is only the capped result.
const maxModelWindowEntries = 16

// maxTurnEndStopField caps ALL THREE claude-authored strings turnevent.TurnEnd
// publishes — Outcome (the `result` line's subtype) and TerminalReason (#2223), plus
// ErrorCategory (the `assistant` line's wrapper-level error, #2224). Applied at
// CONSTRUCTION, exactly as every cap above is, so an oversized value never enters the
// event stream, the push queue, or any log.
//
// CORRECTED 2026-09-08 (#2224): this doc said "BOTH claude-authored strings" and
// carried two-field arithmetic. Three fields share the constant now, and the numbers
// below are recomputed rather than left reading as true.
//
// ONE CONSTANT OVER THREE FIELDS, on maxTaskFieldID's precedent — that one bounds
// TaskID, ToolCallID and TaskType together because they are one SHAPE. These three
// are one shape in the same sense: short open-set tokens off a single line,
// matched by a consumer against a known list rather than read as prose. Their
// budgets are therefore not independent things a future change could want to move
// apart, which is the condition maxRateLimitField's separate-constant paragraph
// sets for splitting one. That #2224's value comes off a DIFFERENT line than the
// other two does not split the shape: what the constant governs is how a consumer
// reads the value, not which line it was read from.
//
// MEASURED against the three documented sets, which is the check maxModelResolved's
// doc requires: the longest subtype is error_max_structured_output_retries at 35
// bytes, the longest terminal_reason structured_output_retry_exhausted at 33, and
// the longest error category authentication_failed / oauth_org_not_allowed at 21,
// so 256 is roughly 7x the largest observation — maxTaskFieldID's own multiple over
// an identifier, and for its reason verbatim: room for a token claude has not shipped
// yet, and still a hard cut on anything that has stopped being a token. The
// committed capture's own values are far shorter (max_turns at 9, completed at 9).
//
// The envelope arithmetic, in maxTaskDescription's style: worst case one turn_end
// carries 3 * 256 = 768 bytes of claude-derived text. That is ~1.2% of the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) — still the smallest share of any cap in this
// family, which is what a three-token frame should cost. Unlike the window caps
// beside it this one DOES owe an envelope percentage, because unlike them these
// fields reach the wire; see turnevent.TurnEnd's doc, which states the split.
//
// OVERFLOW DROPS THE VALUE RATHER THAN TRUNCATING IT, so truncateField is
// deliberately NOT called on either field. This is maxModelWindowID's departure
// from the text caps, taken for its stated reason rather than by resemblance: that
// cap drops because #2102 JOINS on the id and a cut id matches nothing. These two
// are matched the same way — a client switches on them against known tokens — so a
// cut token is indistinguishable from a token the client has never heard of, which
// is a state it must already handle because the set is open. Carrying the empty
// value says exactly that and invents nothing.
//
// There is consequently NO truncation report on this shape and none is owed.
// turnevent.ModelAnnounced.Truncated exists because that field is DISPLAYED, where
// a mangled value is worth flagging; and unlike ModelWindows there is no counter
// either, because a dropped scalar is directly observable as the empty value the
// wire documents rather than an absence a consumer would have to infer.
//
// No RATE bound, and none is owed — but the reason is no longer the one-line one it
// was. Two of the three are read off the `result` line, which IS the turn boundary,
// so they fire once per TURN (maxModelWindowID's situation exactly). ErrorCategory's
// bound fires once per ASSISTANT LINE, which is oftener; what is still once per turn
// is its PUBLICATION, since only the latched value reaches an event. Each application
// is a length test against a line already bounded by defaultMaxParseBuf, so the
// oftener firing costs O(1) per line and retains nothing — which is what makes a rate
// bound unnecessary rather than merely unmeasured.
const maxTurnEndStopField = 256

// resultLine is the decoded payload of one `result` line's modelUsage map — the
// per-model context windows claude reports at every turn end (#2101). Kept
// separate from streamLine, which is the line-level SEGMENTATION struct and stays
// at Type/Subtype/Message; systemTaskStartedLine's doc argues the boundary and
// TestStreamLine_StaysSegmentationOnly enforces it.
//
// The SEPARATION is also what makes AC 4 structural rather than careful. The turn
// boundary and its reason come from the already-decoded streamLine; this is a
// SECOND, independent unmarshal off the same bytes, so a modelUsage of a hostile
// type — a number, a string, an array, an object whose values are not objects —
// fails THIS decode and never the line. There is no input that can both fail here
// and disturb segmentation.
//
// The captured line carries twenty-two keys; one is declared. Absence from the
// DECODE TARGET is a stronger guarantee than a test sweep, because a field that is
// never declared cannot leak.
type resultLine struct {
	ModelUsage map[string]resultModelUsage `json:"modelUsage"`
}

// resultModelUsage is one entry of that map. The captured entry carries three
// keys and one is declared: maxOutputTokens is nothing this daemon reads, and
// canonicalModel is VERSION-DEPENDENT — present in the v2.1.220 and v2.1.239
// captures, absent in the v2.1.143 / v2.1.158 / v2.1.199 ones — so declaring it
// would invite a later reader to depend on a key three of the five observed
// claude versions do not send. See turnevent.ModelWindow's doc, which states the
// same omission from the other side of the boundary.
//
// ContextWindow is an int and SIGNED on purpose. A negative reading has to be
// observable for decodeModelWindows to reject it; an unsigned type would wrap one
// into an enormous positive window and report it as fact. An absent key, a JSON
// null and an explicit 0 all decode to 0 here, which is deliberate — the rejection
// below collapses all three into one reading, none of them being a window a
// consumer could size anything against.
type resultModelUsage struct {
	ContextWindow int `json:"contextWindow"`
}

// resultStopLine is the decoded payload of the two keys that say HOW one `result`
// line's turn stopped (#2223). Kept separate from streamLine for
// systemTaskStartedLine's reason, and TestStreamLine_StaysSegmentationOnly
// enforces that boundary.
//
// A SEPARATE TARGET FROM resultLine, THOUGH BOTH READ THE SAME LINE, and the
// reason is failure isolation rather than tidiness. resultLine decodes modelUsage,
// a map whose VALUE SHAPE claude controls; a hostile one — a number, an array, an
// object of numbers — fails that unmarshal, which is the property
// decodeModelWindows' doc rests on. Folded into one struct, that same hostile
// modelUsage would ALSO erase terminal_reason: one field claude controls would
// silently suppress another. Two targets fail independently, and neither can
// disturb the turn boundary, which comes from the already-decoded streamLine.
//
// The captured line carries eighteen keys on the budget-stopped arm and
// twenty-two on the clean one; two are declared. Absence from the DECODE TARGET is
// a stronger guarantee than a test sweep, because a field that is never declared
// cannot leak — and the field this omission is really about is `result`, which
// carries claude's free-text answer for the turn (its API error text, when
// is_error rides a `success`). A turn_end frame has never carried claude's prose
// and this ticket does not start.
//
// IsError is a plain bool, not *bool: absent, null and an explicit false are one
// reading and nothing acts differently on the three, so a pointer would buy a
// distinction no consumer answers. systemThinkingTokensLine argues the same for
// int over *int. A value of any other JSON type fails the whole decode and takes
// the both-fields-absent path, which is the conservative direction — an is_error
// this parser cannot read must not read as an error.
//
// TerminalReason is a plain string, which is why truncateField's json.RawMessage
// exception does not reach this shape: encoding/json has already
// U+FFFD-replaced invalid input on decode, so no verbatim byte survives to be
// scrubbed. It is bounded rather than scrubbed anyway — see maxTurnEndStopField.
type resultStopLine struct {
	IsError        bool   `json:"is_error"`
	TerminalReason string `json:"terminal_reason"`
}

// resultTurnTotalsLine is the decoded payload of the four numeric keys one `result`
// line carries about the turn it ends — how long the turn took, how long the API has
// spent, how many round-trips the turn made, and what the session has cost (#2260).
// Kept separate from streamLine for systemTaskStartedLine's reason, and
// TestStreamLine_StaysSegmentationOnly enforces that boundary.
//
// A FOURTH TARGET ON THE SAME LINE, beside resultLine, resultStopLine and
// resultDenialsLine, and the reason is the failure isolation resultStopLine's doc
// argues — held now in four directions rather than three. Two of the siblings decode
// shapes claude controls the INSIDE of: a map whose value shape is its own, and an
// array whose element shape is its own. Folded into either, a hostile shape there
// would also zero four numbers that decoded perfectly well, and a hostile number here
// would erase the windows or the denials the daemon could otherwise recover. Four
// targets fail independently, and none can disturb the turn boundary, which comes
// from the already-decoded streamLine.
//
// INSIDE THIS TARGET THE FOUR FAIL AS A UNIT, deliberately, and that is the family's
// posture rather than an oversight: resultStopLine states it for its pair and
// resultDenialEntry for its two strings — a value of any other JSON type fails the
// whole decode and takes the everything-absent path, which is the fail-closed
// direction. userLine's json.RawMessage-per-field alternative would make this decode
// infallible and isolate the four from each other; it is declined because that
// property exists there to protect a field carrying a whole file body and an
// IsSynthetic flag whose loss is a disclosure regression, and nothing of that weight
// rides here. The entire cost of the unit failure is four informational numbers
// reading zero — a state claude's own bytes already produce, per TurnEnd's shared doc.
//
// A JSON null decodes to the zero value WITHOUT failing, which is the one absent-shaped
// value encoding/json accepts against a scalar; absent, null and an explicit 0 are one
// reading here, as they are for resultModelUsage.ContextWindow.
//
// The captured line carries twenty-two keys; four are declared. Absence from the DECODE
// TARGET is a stronger guarantee than a test sweep, because a field that is never
// declared cannot leak — and the keys this omission is about are ttft_ms,
// ttft_stream_ms and time_to_request_ms, which ride the same line on some claude
// versions and are nothing this daemon publishes.
//
// THE THREE COUNTS ARE SIGNED ON PURPOSE, resultModelUsage.ContextWindow's argument
// reused for a value nothing rejects: an unsigned type would wrap a negative reading
// into an enormous positive duration and report it as fact. Unlike that field there is
// no rejection here at all — see decodeTurnTotals for the no-clamp rule and why an
// ordering check in particular is forbidden.
type resultTurnTotalsLine struct {
	DurationMS    int     `json:"duration_ms"`
	DurationAPIMS int     `json:"duration_api_ms"`
	NumTurns      int     `json:"num_turns"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// resultTurnUsageLine is the fifth independent decode target for a `result` line.
// Keeping usage out of streamLine preserves the segmentation-only boundary, and
// keeping it out of the four sibling targets means a hostile nested shape can zero
// only these counts. The turn boundary and every other result field still decode.
//
// An absent or null Usage decodes to the nested struct's zero value. Within a valid
// object, an absent or null member does the same without disturbing its siblings.
// Any non-numeric member fails this target as a unit; decodeTurnUsage then returns
// four zeros. Other usage details claude sends are outside this allowlist.
type resultTurnUsageLine struct {
	Usage resultTurnUsage `json:"usage"`
}

type resultTurnUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// resultTurnEndReason maps a result line's subtype to its TurnEnd reason.
// error_during_execution is claude's interrupt-terminated turn (spike T1,
// #1075) → cancelled; every other subtype (success, and any unknown) keeps
// end_turn — correct for a clean turn and a safe default otherwise. The change
// is scoped to error_during_execution only: this is not a general subtype→reason
// table (max_tokens/refusal classification remains future work).
func resultTurnEndReason(subtype string) turnevent.TurnEndReason {
	switch subtype {
	case "error_during_execution":
		return turnevent.TurnEndReasonCancelled
	default:
		return turnevent.TurnEndReasonEndTurn
	}
}

// decodeStopShape reads the two stop-shape keys off one `result` line and returns
// them bounded (#2223). A pure function of the bytes: no receiver, no parser state
// read or written, nothing logged on any path.
//
// IT CANNOT DISTURB THE TURN BOUNDARY, which is decodeModelWindows' property held
// for a second pair of fields and the reason this is another unmarshal rather than
// a wider streamLine. The reason and the emit are the caller's; every failure here
// returns a value, never an error, and (false, "") is a complete answer to "claude
// said nothing usable".
//
// NOTHING IS LOGGED, on any path, and the decode error in particular is DISCARDED
// rather than logged — decodeModelWindows' reason verbatim: encoding/json QUOTES
// the offending input bytes into its error text, so `"err", err` would route
// claude's own token into the daemon log through a channel no per-attribute check
// can see. Logging nothing at all is what makes "no claude-authored byte from this
// decode reaches a log line" structural rather than a rule each future attribute
// has to be checked against.
//
// THE BOUND IS APPLIED HERE rather than at the emit, so no caller can publish an
// unbounded terminal_reason by forgetting to. The subtype's bound cannot live here
// — it is not read off these bytes — which is why boundStopField is a named
// function both sites call rather than an inline length test.
func decodeStopShape(line []byte) (isError bool, terminalReason string) {
	var sl resultStopLine
	if err := json.Unmarshal(line, &sl); err != nil {
		return false, ""
	}
	return sl.IsError, boundStopField(sl.TerminalReason)
}

// decodeTurnTotals reads the four numeric keys off one `result` line (#2260). A pure
// function of the bytes: no receiver, no parser state read or written, nothing logged
// on any path.
//
// decodeStopShape's three properties hold here verbatim and are not restated: it
// cannot disturb what the line emits, every failure returns a value rather than an
// error, and the decode error is DISCARDED rather than logged because encoding/json
// quotes the offending input into its error text. (0, 0, 0, 0) is a complete answer to
// "claude said nothing usable" — and, unlike its siblings, it is ALSO an answer claude
// itself can send, which is why TurnEnd's shared doc has to say so rather than leaving
// a consumer to read a zero as a daemon failure.
//
// THERE IS NO BOUND TO APPLY HERE, and that is the one way this differs from every
// sibling decode in the family rather than an omission. Those bound claude-authored
// TEXT, whose length claude chooses; these are numbers, whose worst case is the Go
// type's own range — 20 bytes for an int, 24 for a float64 — so the frame cannot be
// grown by anything claude sends. RateLimitedPayload.ResetsAt states the same argument
// as "a float64 cannot grow".
//
// NOTHING IS CLAMPED, ROUNDED, RANGE-CHECKED OR ORDERED. In particular there is NO
// duration_api_ms <= duration_ms check, and adding one would be a defect rather than a
// hardening: duration_api_ms is a RUNNING TOTAL and exceeds the turn's own duration on
// 53 of the 57 committed result lines, so the check would reject the ordinary case. A
// negative is published as claude sent it.
//
// NEITHER RUNNING TOTAL IS DIFFERENCED, and this function is structurally incapable of
// differencing one: it holds no previous line's value and the parser keeps none, so
// the per-turn delta a consumer might expect is not something the daemon could produce
// even by mistake.
func decodeTurnTotals(line []byte) (durationMS, durationAPIMS, numTurns int, costUSDTotal float64) {
	var tl resultTurnTotalsLine
	if err := json.Unmarshal(line, &tl); err != nil {
		return 0, 0, 0, 0
	}
	return tl.DurationMS, tl.DurationAPIMS, tl.NumTurns, tl.TotalCostUSD
}

// decodeTurnUsage reads the four per-turn token counts from a result line's usage
// object. It is pure, holds no previous reading, and cannot turn the counts into
// running totals or deltas.
//
// Decode failure is silent. encoding/json can quote hostile subprocess bytes in an
// error, so logging it would create another unbounded output path. A failed target
// returns the same all-zero reading as absent, null, or explicit zeros. Signed counts
// pass through without clamping, summing, conversion, or ordering checks.
func decodeTurnUsage(line []byte) (input, output, cacheRead, cacheCreation int) {
	var ul resultTurnUsageLine
	if err := json.Unmarshal(line, &ul); err != nil {
		return 0, 0, 0, 0
	}
	return ul.Usage.InputTokens, ul.Usage.OutputTokens,
		ul.Usage.CacheReadTokens, ul.Usage.CacheCreationTokens
}

// boundStopField answers maxTurnEndStopField for one of the three claude-authored
// strings turn_end publishes: the value unchanged, or empty when it exceeds the cap.
//
// IT DROPS RATHER THAN CUTS, which is the whole of the judgement made about these
// values and is argued at the constant. The boundary is <=, matching
// truncateField's, so a value of exactly the cap is carried.
//
// No strings.ToValidUTF8 scrub, unlike truncateField, and its absence is
// deliberate: that function scrubs because it CUTS, and a cut can land mid-rune.
// Nothing here cuts. All three inputs are decoded into Go strings, where
// encoding/json has already U+FFFD-replaced invalid input, so there is no ill-formed
// sequence left for a scrub to remove — the exception truncateField names is
// json.RawMessage, which none of these is.
func boundStopField(s string) string {
	if len(s) > maxTurnEndStopField {
		return ""
	}
	return s
}

// decodeModelWindows reads the modelUsage map off one `result` line and returns
// the bounded, sorted per-model windows it reports plus how many entries claude
// sent that the result does NOT carry (#2101). A pure function of the bytes: no
// receiver, no parser state read or written, nothing logged on any path.
//
// IT CANNOT DISTURB THE TURN BOUNDARY, which is the property AC 4 rests on and
// the reason this is a second unmarshal rather than a wider streamLine. The
// reason and the emit are the caller's; every failure here returns a value, never
// an error, and (nil, 0) is a complete answer to "claude said nothing usable".
//
// NOTHING IS LOGGED, on any path, and the decode error in particular is DISCARDED
// rather than logged — emitModelList's undecodable arm argues it at length:
// encoding/json QUOTES the offending input bytes into its error text, so `"err",
// err` would route claude's own model ids into the daemon log through a channel
// no per-attribute check can see. Logging nothing at all is what makes "no model
// id and no window value reaches a log line" structural here rather than a rule
// each future attribute has to be checked against.
//
// THE ORDER OF THE THREE BOUNDS IS LOAD-BEARING, and it is content-filters-first
// deliberately. claude's output is untrusted input to this parser. Were the
// cardinality cap taken first, a map padded with unusable entries could evict the
// real readings before either was examined; filtering first makes that padding
// inert. Padding with plausible entries can still evict, which is inherent to any
// cardinality bound — what answers that is the cap's headroom over the observed
// two and the fact that the eviction is REPORTED rather than silent.
func decodeModelWindows(line []byte) ([]turnevent.ModelWindow, int) {
	var rl resultLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return nil, 0
	}
	// Absent, null and {} land here as one reading, and so does a map whose values
	// claude changed the shape of — that fails the unmarshal above. The count is 0
	// on every one of them BY CONSTRUCTION: no entry decoded, so none was dropped.
	if len(rl.ModelUsage) == 0 {
		return nil, 0
	}
	windows := make([]turnevent.ModelWindow, 0, len(rl.ModelUsage))
	var dropped int
	for id, entry := range rl.ModelUsage {
		// Both rejections DROP the entry rather than repairing it, and both are
		// counted into the one total turnevent.TurnEnd.DroppedModelWindows carries.
		// ContextWindow <= 0 collapses absent, null, an explicit 0 and a negative
		// into one reading — none of them is a window anything could be sized
		// against. The id bound is maxModelWindowID's departure from truncateField,
		// argued at that constant: a cut id names no model.
		if entry.ContextWindow <= 0 || len(id) > maxModelWindowID {
			dropped++
			continue
		}
		windows = append(windows, turnevent.ModelWindow{
			// claude's key VERBATIM: no lowercasing, no alias expansion, no
			// date-stamping, no family mapping, no lookup against any published model
			// list (#1600's rule). The cap is the only judgement made about it.
			ModelID:      id,
			WindowTokens: entry.ContextWindow,
		})
	}
	// THE SORT IS WHAT MAKES THE CUT BELOW REPRODUCIBLE, and it is why this shape
	// returns a slice rather than claude's map. Go randomises map iteration, so
	// capping an unordered collection would make WHICH entries survive differ
	// between two runs on identical bytes. The list-shaped siblings truncate "from
	// the tail, claude's order preserved" because a JSON array HAS an order; an
	// object has none, so there is nothing to preserve and the daemon's own total
	// order is the only deterministic choice available. Ids are map keys and
	// therefore unique, so no two entries tie and stability is not at issue.
	slices.SortFunc(windows, func(a, b turnevent.ModelWindow) int {
		return strings.Compare(a.ModelID, b.ModelID)
	})
	if len(windows) > maxModelWindowEntries {
		dropped += len(windows) - maxModelWindowEntries
		// CLONED, NOT RESLICED, and this is the one place this function departs from
		// emitModelList's cut. That one reslices because its result is iterated and
		// discarded inside the call; this one RETURNS the slice, and it rides the
		// event for the event's whole life. A bare windows[:cap] would keep the
		// decoder's full backing array reachable — every entry past the cap, and every
		// model id string in it — which would make maxModelWindowEntries' claim that
		// "what is RETAINED is only the capped result" false. The clone is on the
		// over-cap path only; the ordinary two-entry map allocates once, exactly.
		windows = slices.Clone(windows[:maxModelWindowEntries])
	}
	if len(windows) == 0 {
		// A map whose every entry was rejected reads as an absent one — nil, per
		// turnevent.TurnEnd.ModelWindows' five-shape collapse. The COUNT still
		// reports, so "claude sent entries and none was usable" stays distinguishable
		// from "claude sent none" without a second field to say so.
		return nil, dropped
	}
	return windows, dropped
}

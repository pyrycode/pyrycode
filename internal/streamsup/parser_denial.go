package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxDenialProse caps the TWO claude-authored prose fields one
// system/permission_denied line publishes — message and decision_reason (#2232).
// Applied at CONSTRUCTION, exactly as the caps above are, so an oversized value
// never enters the event stream, the push queue, or any log.
//
// ONE CONSTANT OVER TWO FIELDS, as maxTaskFieldID serves three and maxCompactField
// serves two: both are claude's prose about the same refusal, and a future change
// to one budget should move the other.
//
// IT CUTS RATHER THAN DROPS, which is the opposite answer to the three token fields
// on the same event and is maxCompactField's reasoning: a cut sentence still reads
// as what it is, where a cut token would match nothing while still looking like one.
// The cut IS reported (turnevent.ToolCallDenied.TruncatedFields) because the same
// event drops three other fields, so an empty value cannot speak for itself here the
// way maxCompactTrigger's can.
//
// MEASURED against the committed captures (#2232, claude 2.1.239, seven denials
// across three arms of bypass_reescalation_v2.1.239_*): the longest message is 400
// bytes — a sandbox refusal naming the session's allowed working directories — so
// 2048 is roughly 5x the observation. decision_reason is UNOBSERVED (absent from all
// seven) and rides this constant rather than earning one from an observation nobody
// has: it is claude's prose about the same refusal, which is the strongest thing
// that can be said about a field never seen.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// ToolCallDenied carries 3*256 + 2*2048 = 4864 bytes of claude-derived text. That is
// 7.4% of the v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) and DELIBERATELY the same worst case
// turnevent.BackgroundTaskStarted carries, so this event family keeps ONE number a
// reader can hold rather than a per-variant figure to re-derive. Escaping is mild
// for maxUnrecognizedRaw's reason. Nothing reaches the wire in this slice — #2233
// owns the frame — so the number is the budget that ticket inherits, not a live one.
//
// No RATE bound and none is owed here. One application per denial line, an O(1)
// length test against a line already capped by defaultMaxParseBuf, reaching a
// synchronous sink with no queue in this package. A model looping on refused calls
// is a fan-out question, and it belongs to the ticket that puts the event on a wire.
//
// AMENDED 2026-09-09 (#2267): it caps TWO MORE prose fields on a second event,
// turnevent.ModelRefusalFallback's RefusalExplanation and Banner, so "the TWO
// claude-authored prose fields one system/permission_denied line publishes" now names
// half of what this constant bounds. Four fields over two events, on the
// one-constant-over-several-fields form above: all four are claude's prose about a
// refusal, and a change to one budget should move the others. Both new fields CUT and
// are reported, on the same reasoning.
//
// THE ENVELOPE ARITHMETIC ABOVE NO LONGER COVERS EVERY EVENT THIS CONSTANT BOUNDS,
// and the new number is stated rather than left to be re-derived from a sentence that
// reads as if it did. A ModelRefusalFallback carries FOUR token fields, not three, for
// a worst case of 4*256 + 2*2048 = 5120 bytes — 7.8% of the 65519-byte v2
// application-envelope cap (docs/protocol-mobile.md § Application-envelope size cap)
// against ToolCallDenied's 4864 and 7.4%. The family's "one number a reader can hold"
// claim is therefore now a range, 4864 to 5120, and the reason it was not held flat by
// shaving a cap is that the alternative — minting a fifth constant to save 256 bytes
// out of 65519 — buys a number to keep in step for a quarter of one percent. Nothing
// reaches the wire in this slice, so it is the budget #2265 inherits.
//
// The rate paragraph above holds unchanged, and for one more reason of its own: a
// session-scoped fallback stops re-announcing by definition, and a local-scoped one
// fires at most once per refused turn. Both are turn-paced rather than model-paced.
//
// AMENDED 2026-09-10 (#2268): it caps TWO MORE prose fields on a third event,
// turnevent.ModelRefusalNoFallback's RefusalExplanation and Banner. Both take the
// existing CUT-and-report answer. Its worst case is 2*256 + 2*2048 = 4608 bytes,
// below the range already established above; no envelope ceiling changes. An outright
// refusal fires at most once per refused turn, so the existing rate reasoning holds.
const maxDenialProse = 2 << 10

// maxTurnDenials caps BOTH dimensions of one turn's denial bookkeeping (#2234): how
// many tool_use_ids Parser.deniedThisTurn remembers, and how many permission_denials
// entries emitRecoveredDenials reads off a `result` line. maxTaskRosterEntries'
// answer to an array length claude controls, applied to a second array — and
// maxTaskFieldID's one-constant-over-several-fields form, because the two dimensions
// are the same population counted at its two ends and a budget change to one is a
// budget change to the other.
//
// MEASURED across every committed capture that reports a denial (2026-09-08, at
// 6e6f926e): the longest permission_denials array is ONE entry, and no turn carries
// more than one system/permission_denied line. 16 is a wide multiple of that — wider
// than maxTaskRosterEntries' 8 over its own observation — because a model looping on
// refused calls is a plausible turn shape where a live background-task roster is not,
// and the retention is cheap: 16 ids, each already bounded by maxTaskFieldID, is
// 4 KiB of worst-case parser state per session.
//
// ITS SECOND DIMENSION FAILS CLOSED, and that is the reason one constant can serve
// both. A set AT the cap no longer proves it holds every id this turn announced, so
// emitRecoveredDenials recovers nothing rather than risking a SECOND marker for a
// call already reported. What that gives up is only reachable in the posture where
// denial LINES do arrive — the posture this whole slice exists because the daemon
// does not run in — so the set is empty rather than full exactly where recovery
// matters. See Parser.deniedThisTurn.
const maxTurnDenials = 16

// resultDenialsLine is the decoded payload of the one `result` key that lists the
// tool calls claude refused during the turn it ends (#2234). Kept separate from
// streamLine for systemTaskStartedLine's reason, and TestStreamLine_StaysSegmentationOnly
// enforces that boundary.
//
// A THIRD TARGET ON THE SAME LINE, beside resultLine and resultStopLine, and the
// reason is the failure isolation resultStopLine's doc argues — held now in three
// directions rather than two. This key's payload is an ARRAY whose element shape
// claude controls; folded into either sibling, a hostile one would ALSO erase
// modelUsage or terminal_reason, and a hostile modelUsage would erase the denials the
// daemon could otherwise recover. Three targets fail independently, and none can
// disturb the turn boundary, which comes from the already-decoded streamLine. That
// three-way independence is AC 3 in full.
//
// The captured line carries twenty-two keys; one is declared.
type resultDenialsLine struct {
	PermissionDenials []resultDenialEntry `json:"permission_denials"`
}

// resultDenialEntry is one element of that array. Every captured entry carries
// EXACTLY three keys — tool_name, tool_use_id, tool_input — measured 2026-09-08
// across every committed capture that reports a denial, per emitPermissionDenied's
// rule that the mapping comes from captures and never from a hand-built payload.
//
// TWO ARE DECLARED, AND tool_input IS THE REFUSAL THIS SHAPE IS ABOUT. It is the
// tool's full input — a shell command in every captured entry, which may carry
// whatever an operator typed — and it is the only one of the three the marker does
// not already have from the system/permission_denied line's mapping. The client
// already holds it from the `tool_use` frame for the same tool_use_id, so decoding it
// here would buy a second copy of something nothing reads, which is how a field
// leaks later. Absence from the DECODE TARGET is the stronger guarantee, exactly as
// systemPermissionDeniedLine states for session_id and uuid: a field that is never
// declared cannot leak.
//
// Both are plain strings, so an entry of any other shape — a number, an array, an
// object whose values are not strings — fails the WHOLE decode and recovers nothing,
// which is the fail-closed direction systemPermissionDeniedLine takes for the same
// reason.
type resultDenialEntry struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

// consumePermissionDeniedLine maps a system/permission_denied line that FAILED
// streamLine's decode, reporting whether it handled it. Called from consumeLine's
// decode-failure branch, beside dropHarnessProseLine and for the same class of
// reason.
//
// IT IS THE PRODUCTION PATH, not a fallback, and the switch arm beside it is the
// theoretical one — which is the opposite of how the pair reads. MEASURED against
// the seven committed denials (#2232): every one carries `message` as a STRING,
// streamLine declares that key as *streamMessage, and encoding/json fails the WHOLE
// line on the mismatch. So a real denial never reaches sl.Type at all; before this
// gate it fell straight to emitUnrecognized and cost the operator a per-denial
// unrecognized_message row. emitSystemSubtype's arm still handles the shapes that DO
// decode — a line with no message key, or one whose message is an object — and both
// paths run the same mapping, which is what makes the subtype mapped whatever shape
// it arrives in.
//
// THE MATCH IS DELIBERATELY NARROW, on dropHarnessProseLine's and
// consumeToolProgress' argument: this gate can only take a line away from
// emitUnrecognized, so its safety is entirely in how little it matches. Two exact
// keywords from the top-level envelope and nothing else — no subtype prefix, no
// type-only match. A line that is not valid JSON at all fails this decode too and
// falls through to the surfaced tier, where it belongs.
//
// Its own two-field envelope rather than streamLine, necessarily: the struct this
// line already failed cannot be the one that recognises it.
func (p *Parser) consumePermissionDeniedLine(line []byte) bool {
	var envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}
	if envelope.Type != "system" || envelope.Subtype != "permission_denied" {
		return false
	}
	return p.emitPermissionDenied(line)
}

// emitPermissionDenied decodes a system/permission_denied line and emits one
// turnevent.ToolCallDenied, reporting that it consumed the line either way. Field
// mapping and cap numbers come from the committed captures
// (internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_*, seven denials
// across three arms), never from a hand-built payload.
//
// TWO CALLERS, both handing it the TOP-LEVEL bytes: emitSystemSubtype's case arm
// for a line that decoded into streamLine, and consumePermissionDeniedLine for one
// that could not because claude spells `message` as a string. See that function for
// which of the two a real denial takes — it is not the one this arm's position
// suggests.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// here that is the whole forgery argument rather than a call-shape convention. Both
// callers gate on the top-level type and hand this arm the same bytes, so a tool
// result whose text is literally a permission_denied line cannot forge a denial:
// streamLine's doc states the property, that control shapes are read from the top
// level only and nested content is never re-scanned. Decoding this payload from
// anywhere else would let claude's own tool output claim a call was blocked.
//
// IT GATES ON NOTHING, and that is the decision this arm's shape rests on. The two
// precedents beside it both gate — emitModelAnnounced emits nothing for a model-less
// init, emitCompactionBoundary nothing without compact_metadata — and both do so
// because the gated field IS the payload while a sibling event has already reported
// the fact. Neither holds here. The SUBTYPE is the payload: nothing else on this
// surface separates a denied call from one that ran and failed, which is the entire
// defect being fixed, so a field-less line is still news. And a gate would re-drop
// the line SILENTLY the first time claude renames a key — restoring the defect in
// the one case nobody would look. emitBackgroundTaskStarted's rule applies instead:
// absence is claude's to choose, the field lands empty, the event still fires. AC
// 4's TestDropcapClassification row pins this: a line with no fields at all is
// recorded there as mapped, so a gate added later reddens that row.
//
// TWO CONSUMING PATHS:
//
//   - undecodable (a numeric tool_name, say) → Debug naming the subtype, no event.
//     emitBackgroundTaskStarted's arm verbatim and on its ground: the subtype is a
//     message-name keyword rather than payload, and no claude-authored field was
//     decoded on this path. NOT surfaced as an Unrecognized — keeping system whole
//     on ignoredLineTypes is what makes "no system line reaches the unrecognized
//     lane" structural, and that outranks surfacing a malformed line of a known
//     subtype.
//   - decodable → exactly one event, whatever the five fields hold.
//
// NOTHING IS LOGGED ON THE EMITTING PATH, and that is load-bearing rather than
// tidy. `message` names absolute host paths in every captured line and may quote a
// refused command line, so it is the field a drop site would be most tempted to
// explain itself with and the one that must never reach a log. emitThinkingProgress'
// posture otherwise applies: everything decoded reaches the event, so a second sink
// would be a record to keep in step for no diagnostic gain.
//
// OVERFLOW IS CUT-OR-DROP PER FIELD, and both answers are live here for the reasons
// stated at each one below. The event reports BOTH, which is what lets a consumer
// tell a value the daemon emptied from one claude never sent.
func (p *Parser) emitPermissionDenied(line []byte) bool {
	var dl systemPermissionDeniedLine
	if err := json.Unmarshal(line, &dl); err != nil {
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "permission_denied")
		return true
	}

	var cut, dropped []string
	// cutField is emitBackgroundTaskStarted's `bound` helper under a name that says
	// which of the two answers it is, because this arm has both.
	cutField := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// dropField is maxCompactTrigger's answer, generalised to report itself.
	// truncateField is deliberately not called: the value is emptied, not shortened.
	// No UTF-8 scrub is owed on this path either — encoding/json already replaced
	// invalid input bytes with U+FFFD on the way into a Go string, which
	// truncateField's own doc states, and the only mid-rune hazard is a cut this
	// branch does not make.
	dropField := func(value, name string, limit int) string {
		if len(value) > limit {
			dropped = append(dropped, name)
			return ""
		}
		return value
	}
	// Sequential statements rather than a composite literal: both reports are ordered
	// by these calls, and inside a literal that order would rest on the left-to-right
	// operand rule rather than on something a reader sees. The names are the
	// DAEMON's — tool_call_id, not claude's tool_use_id.
	toolName := dropField(dl.ToolName, "tool_name", maxTaskFieldID)
	toolCallID := dropField(dl.ToolUseID, "tool_call_id", maxTaskFieldID)
	message := cutField(dl.Message, "message", maxDenialProse)
	decisionReasonType := dropField(dl.DecisionReasonType, "decision_reason_type", maxTaskFieldID)
	decisionReason := cutField(dl.DecisionReason, "decision_reason", maxDenialProse)

	// The ONE write site of the turn's announced-id set (#2234). The value recorded is
	// the one PUBLISHED — after dropField, never dl.ToolUseID — so an id too long to
	// carry on the event is also too long to remember, and the set inherits
	// maxTaskFieldID's bound without restating it. An emptied or absent id records
	// nothing: it is no join key, so it could only conflate every id the daemon
	// dropped into one entry that suppressed all of them.
	//
	// Recorded BEFORE the emit and unconditionally on the emitting path, so no
	// ordering between the two can leave a marker published under an id the set does
	// not hold. Growth stops at maxTurnDenials — see that constant for why a full set
	// suppresses recovery rather than evicting.
	if toolCallID != "" && len(p.deniedThisTurn) < maxTurnDenials {
		if p.deniedThisTurn == nil {
			p.deniedThisTurn = make(map[string]struct{}, 1)
		}
		p.deniedThisTurn[toolCallID] = struct{}{}
	}

	p.emit(turnevent.ToolCallDenied{
		ToolName:           toolName,
		ToolCallID:         toolCallID,
		Message:            message,
		DecisionReasonType: decisionReasonType,
		DecisionReason:     decisionReason,
		// nil when nothing was cut or dropped: neither append ran.
		TruncatedFields: cut,
		DroppedFields:   dropped,
	})
	return true
}

// recoveredDenialsCutMsg is the Debug message the cut site emits, as a literal so a
// test asserting the record's attribute set is closed can find it by message rather
// than by position — compactingEndedMsg's shape and its reason.
const recoveredDenialsCutMsg = "streamsup: capping result permission_denials"

// emitRecoveredDenials reads the `result` line's permission_denials array and emits
// one turnevent.ToolCallDenied for each entry that no system/permission_denied line
// announced this turn (#2234). announced is the turn's id set, read off the parser
// and cleared by the caller before this runs.
//
// WHY THE RECOVERY EXISTS AT ALL is measured rather than a hedge against a vendor
// sentence. Across every committed capture that reports a denial (2026-09-08, at
// 6e6f926e), the five bypass_approval_argv_v2.1.239_* arms — launched with
// --permission-prompt-tool, which is how cmd/pyry/mcp_config.go launches claude in
// PRODUCTION — carry nine denials in `result` and ZERO permission_denied lines. So on
// the daemon's own posture #2232's mapping reports nothing at all for a blocked call,
// and this arm is the only thing that makes one reach the client there.
//
// The decode's input is `line` — the TOP-LEVEL bytes handed down by consumeLine's
// `result` arm — never a nested field, and that is emitPermissionDenied's whole
// forgery argument held for a second entry point. streamLine's doc states the
// property: control shapes are read from the top level only and nested content is
// never re-scanned, so a tool result whose text is literally a `result` line cannot
// mint a denial.
//
// NOTHING IS LOGGED FROM CLAUDE'S BYTES on any path, and the decode error in
// particular is DISCARDED rather than logged, for decodeModelWindows' reason:
// encoding/json QUOTES the offending input into its error text, so `"err", err` would
// route claude's own tool names and ids into the daemon log through a channel no
// per-attribute check can see. The one Debug below carries a single daemon-computed
// integer.
//
// THE CUT IS REPORTED IN A LOG RATHER THAN ON THE EVENT, which departs from
// emitBackgroundTaskRoster — the bound this follows — and the departure is stated
// rather than left to be noticed. That arm reports its count on
// BackgroundTaskRoster.DroppedTasks because it has an event to carry one; here every
// event is a per-entry marker and a count belongs to none of them, while a field for
// it would widen turnevent and protocol for a number no consumer acts on. What is
// logged is the LENGTH of input the daemon refused, never any of it — the same class
// as the oversized-partial-line drop's "bytes". The roster's own refusal to log a
// count is not contradicted: that one covers its UNDECODABLE path, where nothing
// decoded and the length would be the only thing said about a line.
//
// FOUR OUTCOMES PER ENTRY, and only the first two cost the client anything:
//
//   - id empty, or over maxTaskFieldID → dropped, counted. The id is the client's
//     join key (turnevent.ToolCallDenied.ToolCallID) and a recovered marker carries no
//     prose to stand on instead, so an unattributable one is strictly worse than none.
//   - beyond maxTurnDenials → dropped, counted, from the TAIL: claude's order is
//     preserved because no ranking is invented, emitBackgroundTaskRoster's rule.
//   - already announced → SUPPRESSED, and deliberately not counted. It is the correct
//     outcome rather than a loss, and folding it into the cut report would make the
//     ordinary line-bearing posture look like it was overflowing.
//   - otherwise → one marker, carrying the two fields the entry has and empty prose.
func (p *Parser) emitRecoveredDenials(line []byte, announced map[string]struct{}) {
	// A set at its cap no longer proves it holds every id this turn announced, so
	// recovering could publish a SECOND marker for a call already reported. Going
	// silent instead is the fail-closed direction and costs nothing where it matters —
	// see maxTurnDenials.
	if len(announced) >= maxTurnDenials {
		return
	}
	var dl resultDenialsLine
	if err := json.Unmarshal(line, &dl); err != nil {
		return
	}

	// The COUNT bound runs before the loop and truncates from the TAIL, which is
	// emitBackgroundTaskRoster's shape and its reason: claude's order is preserved
	// because no ranking is invented, its ordering semantics being unobserved.
	entries := dl.PermissionDenials
	var dropped int
	if len(entries) > maxTurnDenials {
		dropped = len(entries) - maxTurnDenials
		entries = entries[:maxTurnDenials]
	}

	// recovered is the ids this loop has already emitted, so an array naming one id
	// twice mints one marker rather than two — the same "no second marker per id"
	// property announced gives across the two paths, held within this one. A SECOND
	// set rather than adding to announced: that map is the parser's own state,
	// cleared-off but still the caller's value, and a helper that mutates what it was
	// handed is a side effect nothing here needs. Lazily allocated, so the ordinary
	// one-entry array allocates nothing.
	var recovered map[string]struct{}
	for _, entry := range entries {
		if entry.ToolUseID == "" || len(entry.ToolUseID) > maxTaskFieldID {
			dropped++
			continue
		}
		if _, ok := announced[entry.ToolUseID]; ok {
			continue
		}
		if _, ok := recovered[entry.ToolUseID]; ok {
			continue
		}
		var droppedFields []string
		// The name DROPS rather than cuts, on turnevent.ToolCallDenied.ToolName's
		// reasoning: a consumer switches on it against claude's tool set, so a cut name
		// matches nothing while still looking like a tool. The report uses the DAEMON's
		// field name, as emitPermissionDenied's does.
		toolName := entry.ToolName
		if len(toolName) > maxTaskFieldID {
			toolName = ""
			droppedFields = append(droppedFields, "tool_name")
		}
		p.emit(turnevent.ToolCallDenied{
			ToolName:   toolName,
			ToolCallID: entry.ToolUseID,
			// Message, DecisionReasonType and DecisionReason are left EMPTY and nothing
			// is synthesized into them. The entry says only that the call was refused;
			// inventing prose here would be the daemon speaking in claude's voice about a
			// denial claude described nowhere. TruncatedFields stays nil for the same
			// reason — no value on this path is cut, so none can be reported as cut.
			DroppedFields: droppedFields,
		})
		if recovered == nil {
			recovered = make(map[string]struct{}, 1)
		}
		recovered[entry.ToolUseID] = struct{}{}
	}
	if dropped > 0 {
		p.log.Debug(recoveredDenialsCutMsg, "dropped", dropped)
	}
}

// emitModelRefusalFallback decodes a system/model_refusal_fallback line and emits one
// turnevent.ModelRefusalFallback, reporting that it consumed the line either way. It
// is emitPermissionDenied's shape throughout — decode target, no gate, cut-or-drop per
// field, two report slices, undecodable → Debug — and every place it departs is
// called out below rather than left for a reader to spot.
//
// THE FIELD SET IS DOCUMENTATION-DERIVED, NOT CAPTURE-DERIVED, and that inverts this
// arm's relationship to its evidence. emitPermissionDenied's caps and field mapping
// come from seven committed captures; nothing here does. The keys were read
// 2026-09-07 from the Claude Code headless docs and
// @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts, with the daemon on claude
// 2.1.259, and no capture of this line exists or can be taken — a refusal cannot be
// provoked without a prompt this repo should not contain. So every key is treated as
// optional and nothing is gated (see below), which is the only posture a decode can
// take against a field set nobody has observed.
//
// ONE CALLER, and the prediction that makes that true is worth writing down because
// its analogue's failed. streamLine declares type, subtype and message, the documented
// field set carries NO message key, so the line decodes cleanly and reaches
// emitSystemSubtype's arm. That is the opposite of permission_denied, whose message is
// a string where streamLine declares *streamMessage — one wrongly-typed key on the
// shared target failed the WHOLE top-level decode and left #2232's correctly-written
// arm unreachable in production until consumePermissionDeniedLine recovered it.
// BECAUSE THE FIELD SET HERE IS DOCUMENTATION-DERIVED, THE CLEAN DECODE IS A
// PREDICTION AND NOT A MEASUREMENT: should the real line carry a message key of any
// scalar type, consumeLine's decode fails and this mapping is unreachable exactly as
// that one was. No recovery gate is built for it speculatively — such a gate can only
// take a line away from the surfaced tier, so its safety is entirely in how little it
// matches, and one written against a shape nobody has seen is unbounded in the wrong
// direction. Whoever sees the first real line should read this paragraph first.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, on
// streamLine's stated property that control shapes are read from the top level and
// nested content is never re-scanned. A tool result whose text is literally a
// model_refusal_fallback line therefore cannot forge one.
//
// IT GATES ON NOTHING, emitPermissionDenied's decision on its ground and one step
// more forcefully. The SUBTYPE is the payload: nothing else on this surface explains
// why the model changed, so a field-less line is still news. A gate would re-drop the
// line SILENTLY the first time claude renames a key — the defect this arm exists to
// end — and here it would additionally rest on a guess, since no observation says any
// key is reliably present.
//
// FIVE OF CLAUDE'S ELEVEN KEYS ARE NOT DECLARED on the decode target, which is what
// keeps them out rather than a scrub; systemModelRefusalFallbackLine states which and
// why. trigger and direction restate the subtype; request_id is API-side; the two
// message-uuid keys name claude's message identity, which no daemon surface can join
// against.
//
// TWO CONSUMING PATHS:
//
//   - undecodable (a numeric scope, say) → Debug naming the subtype, no event.
//     emitPermissionDenied's arm verbatim and on its ground. Per-field type tolerance
//     would be a real divergence from the family and is deliberately not built: the
//     failure has not been observed, and a capture is what would justify it.
//   - decodable → exactly one event, whatever the six fields hold.
//
// NOTHING IS LOGGED ON THE EMITTING PATH, and it is load-bearing here for a reason
// one degree past emitPermissionDenied's. Its message describes a tool call the daemon
// made; these two prose fields are claude's writing about a request that was REFUSED,
// so they can quote or paraphrase the USER's own words back out. They must never reach
// a log, and the undecodable Debug above carries the subtype keyword only.
func (p *Parser) emitModelRefusalFallback(line []byte) bool {
	var fl systemModelRefusalFallbackLine
	if err := json.Unmarshal(line, &fl); err != nil {
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "model_refusal_fallback")
		return true
	}

	var cut, dropped []string
	// The two bounding closures are emitPermissionDenied's, kept LOCAL rather than
	// lifted into a shared helper. That is a decision: this file already has three such
	// closures (emitBackgroundTaskStarted's `bound` and that arm's pair), each carrying
	// its own doc saying which of the two answers it is, so a local pair is the file's
	// established shape; and extracting one would edit a shipped arm for no behavioural
	// gain. Revisit on a third caller, not a second.
	cutField := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// truncateField is deliberately not called here: the value is emptied, not
	// shortened. No UTF-8 scrub is owed on this path either — encoding/json already
	// replaced invalid input bytes with U+FFFD on the way into a Go string, and the
	// only mid-rune hazard is a cut this branch does not make.
	dropField := func(value, name string, limit int) string {
		if len(value) > limit {
			dropped = append(dropped, name)
			return ""
		}
		return value
	}
	// Sequential statements rather than a composite literal, per emitPermissionDenied:
	// both reports are ordered by these calls, and inside a literal that order would
	// rest on the left-to-right operand rule rather than on something a reader sees.
	//
	// The names are the DAEMON's, and three of the six differ from claude's key —
	// refusal_category not api_refusal_category, refusal_explanation not
	// api_refusal_explanation, banner not content. The report names the FIELD it
	// describes, which is turnevent.ModelRefusalFallback.DroppedFields' rule; the api_
	// prefix is API-side vocabulary the daemon does not adopt.
	//
	// The four tokens DROP and the two prose fields CUT, each on the reasoning stated
	// at its field: a cut token matches nothing while still looking like one, and
	// fallback_model is additionally JOINED against the ModelAnnounced a client already
	// holds; a cut sentence still reads as prose.
	scope := dropField(fl.Scope, "scope", maxTaskFieldID)
	originalModel := dropField(fl.OriginalModel, "original_model", maxTaskFieldID)
	fallbackModel := dropField(fl.FallbackModel, "fallback_model", maxTaskFieldID)
	refusalCategory := dropField(fl.APIRefusalCategory, "refusal_category", maxTaskFieldID)
	refusalExplanation := cutField(fl.APIRefusalExplanation, "refusal_explanation", maxDenialProse)
	banner := cutField(fl.Content, "banner", maxDenialProse)

	// No parser state is read or written — not p.compacting, not p.deniedThisTurn, not
	// the accumulator — which is emitCompactionBoundary's shape and means no
	// turn-boundary reset has anything of this arm's to reset.
	p.emit(turnevent.ModelRefusalFallback{
		Scope:              scope,
		OriginalModel:      originalModel,
		FallbackModel:      fallbackModel,
		RefusalCategory:    refusalCategory,
		RefusalExplanation: refusalExplanation,
		Banner:             banner,
		// nil when nothing was cut or dropped: neither append ran.
		TruncatedFields: cut,
		DroppedFields:   dropped,
	})
	return true
}

// emitModelRefusalNoFallback maps one documentation-derived
// system/model_refusal_no_fallback shape to one descriptive event. No capture of the
// line exists or can safely be provoked, so the decode target treats every declared
// key as optional and emission gates on none of them: the subtype itself is news.
//
// The documented shape has no `message` key and therefore reaches this arm through
// streamLine's ordinary top-level dispatch. That is a prediction, not an observation;
// a future scalar message would justify a recovery entry point, but none is added
// speculatively.
//
// A wrong type on any declared string fails the whole decode target. The line remains
// consumed, no event is emitted, and the Debug record carries only this package's
// fixed subtype keyword. In particular, no decoded prose may reach a log: it can echo
// the user's refused request.
func (p *Parser) emitModelRefusalNoFallback(line []byte) bool {
	var refusal systemModelRefusalNoFallbackLine
	if err := json.Unmarshal(line, &refusal); err != nil {
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "model_refusal_no_fallback")
		return true
	}

	var cut, dropped []string
	cutField := func(value, name string) string {
		out, truncated := truncateField(value, maxDenialProse)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	dropField := func(value, name string) string {
		if len(value) > maxTaskFieldID {
			dropped = append(dropped, name)
			return ""
		}
		return value
	}

	// Sequential construction makes the report ordering visible rather than relying
	// on evaluation order inside a composite literal. Reports name daemon fields, not
	// claude's api_ prefixed keys.
	originalModel := dropField(refusal.OriginalModel, "original_model")
	refusalCategory := dropField(refusal.APIRefusalCategory, "refusal_category")
	refusalExplanation := cutField(refusal.APIRefusalExplanation, "refusal_explanation")
	banner := cutField(refusal.Content, "banner")

	p.emit(turnevent.ModelRefusalNoFallback{
		OriginalModel:      originalModel,
		RefusalCategory:    refusalCategory,
		RefusalExplanation: refusalExplanation,
		Banner:             banner,
		TruncatedFields:    cut,
		DroppedFields:      dropped,
	})
	return true
}

// systemModelRefusalFallbackLine is the decoded payload of one
// system/model_refusal_fallback line. Kept separate from streamLine for
// systemTaskStartedLine's reason: that is the line-level SEGMENTATION struct and stays
// at Type/Subtype/Message.
//
// SIX OF CLAUDE'S ELEVEN DOCUMENTED KEYS ARE DECLARED, AND THE OTHER FIVE ARE THE
// CONTROL RATHER THAN A SCRUB — systemTaskUpdatedLine's rule, which
// systemPermissionDeniedLine already applies to session_id and uuid. A field that is
// never declared cannot leak. The five, and why each is refused:
//
//   - trigger ("refusal") and direction ("retry") are constants restating the subtype,
//     which turnevent.ModelRefusalFallback's own identity already carries.
//   - request_id is an API-side identifier nothing in the daemon reads.
//   - retracted_message_uuids and refused_user_message_uuid name claude's MESSAGE
//     identity, which no daemon surface can join against — assistant_delta carries
//     turn_id and seq, not these. A consumer therefore cannot honour the retraction of
//     the refused partial response. That is a stated limit of this wire, recorded on
//     the event type, and not something to solve by carrying ids nothing can resolve.
//
// EVERY FIELD IS A PLAIN STRING, so a non-string value fails the whole decode and
// takes the undecodable path — fail-closed, per emitModelRefusalFallback. That
// includes retracted_message_uuids being an array on the real line: undeclared, so
// encoding/json never looks at its type at all.
//
// NO FIELD SET HERE IS AN OBSERVATION. The keys are documentation-derived (see
// emitModelRefusalFallback for the sources and the date), and no capture of this line
// exists to check them against, so a key may simply not arrive. That is why the arm
// gates on none of them.
type systemModelRefusalFallbackLine struct {
	Scope         string `json:"scope"`
	OriginalModel string `json:"original_model"`
	FallbackModel string `json:"fallback_model"`
	// The Go names keep claude's api_ prefix while the daemon's field names drop it, so
	// the rename happens once, visibly, at the emit site rather than silently here.
	APIRefusalCategory    string `json:"api_refusal_category"`
	APIRefusalExplanation string `json:"api_refusal_explanation"`
	// Content is claude's banner text for the swap. Named for the key here and for its
	// role on the event, per the note above about where renames happen.
	Content string `json:"content"`
}

// systemModelRefusalNoFallbackLine declares exactly the four documented keys this
// daemon event carries. request_id and refused_user_message_uuid are omitted rather
// than decoded and scrubbed, so those identifiers cannot leak through this target.
// Every field is a plain string: a wrong type rejects the target as a unit in
// emitModelRefusalNoFallback, while an absent field remains empty.
//
// It stays separate from systemModelRefusalFallbackLine because scope and
// fallback_model do not belong to this subtype. The narrower target makes that
// exclusion structural and follows the task-line family's separation rule.
type systemModelRefusalNoFallbackLine struct {
	OriginalModel         string `json:"original_model"`
	APIRefusalCategory    string `json:"api_refusal_category"`
	APIRefusalExplanation string `json:"api_refusal_explanation"`
	Content               string `json:"content"`
}

// systemPermissionDeniedLine is the decoded payload of one system/permission_denied
// line. Kept separate from streamLine for systemTaskStartedLine's reason: that is
// the line-level SEGMENTATION struct and stays at Type/Subtype/Message.
//
// The field set is exactly what the committed captures show plus the two the vendor
// declares, and nothing invented. The two keys the captured lines also carry are
// deliberately absent — uuid, which nothing in the daemon reads, and session_id,
// which is claude's session identity and NOT the daemon's conversation identity.
// Absent from the DECODE TARGET is a stronger guarantee than a scrub or a test
// sweep, because a field that is never declared cannot leak.
//
// DecisionReasonType and DecisionReason are declared although NO CAPTURED LINE
// CARRIES EITHER. They are on SDKPermissionDeniedMessage in
// @anthropic-ai/claude-agent-sdk@0.3.263 and the consumer wants them, the decode
// costs nothing when claude omits them, and their absence is pinned as an
// observation by TestParser_DenialCaptureYieldsEmptyDecisionReasons rather than
// assumed away. Every field is a plain string, so a non-string value fails the whole
// decode and takes the undecodable path — fail-closed, per emitPermissionDenied.
type systemPermissionDeniedLine struct {
	ToolName           string `json:"tool_name"`
	ToolUseID          string `json:"tool_use_id"`
	Message            string `json:"message"`
	DecisionReasonType string `json:"decision_reason_type"`
	DecisionReason     string `json:"decision_reason"`
}

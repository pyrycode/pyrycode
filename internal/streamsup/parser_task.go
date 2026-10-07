package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxTaskFieldID caps each machine-generated identifier on a
// turnevent.BackgroundTaskStarted — TaskID, ToolCallID, TaskType. Applied at
// CONSTRUCTION, exactly as maxUnrecognizedRaw is, so an oversized payload never
// enters the event stream, the push queue, or any log.
//
// The longest such field in the committed capture is tool_use_id at 29 bytes
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), so 256 is
// roughly 9x the observed maximum: room for a format claude has not shipped yet,
// and still a hard cut on anything that has stopped being an identifier.
//
// AMENDED 2026-09-08 (#2232): it now caps three fields on a SECOND event as well —
// turnevent.ToolCallDenied's ToolName, ToolCallID and DecisionReasonType — so the
// enumeration in the first sentence names one event of the two. Reused rather than
// duplicated because those are identifiers and short vendor tokens of exactly this
// shape (the longest is a 30-byte tool_use_id in the #2232 captures, and the
// observed tool_name is 4), and a second constant of the same value bounding the
// same shape would be a number to keep in step for nothing. It does NOT follow that
// the overflow ANSWER is shared: this constant's own event cuts and reports, while
// every field it caps on ToolCallDenied is dropped — the cap is a size, the answer
// is the field's, and turnevent.ToolCallDenied.ToolCallID states why the two events
// part company on the same identifier.
//
// AMENDED 2026-09-09 (#2267): a THIRD event now, turnevent.ModelRefusalFallback's
// Scope, OriginalModel, FallbackModel and RefusalCategory — so the first sentence's
// enumeration names one event of three and the count is deliberately not restated
// here, since the authority is the constant's call sites. Reused on the paragraph
// above's grounds: two documented scope values, two model labels and a short
// classification token are exactly this shape. Its overflow answer follows
// ToolCallDenied's rather than this constant's own event's — all four DROP — for the
// reason stated at each field, and the divergence is the same one that paragraph
// already records.
//
// AMENDED 2026-09-10 (#2268): a FOURTH event now,
// turnevent.ModelRefusalNoFallback's OriginalModel and RefusalCategory. They take
// the same DROP answer for the same reason as the corresponding fallback fields.
// The documented no-fallback line is also uncaptured, so this reuse adds no new
// measurement — only two more tokens of the same shape.
//
// The multiple is the weakest it has been, and that is stated rather than hidden.
// The two earlier events multiplied over an OBSERVED maximum; #2267's field set is
// documentation-derived with no capture in existence, so 256 is a multiple of nothing
// for those four fields. What justifies it instead is the shape argument above plus
// the envelope arithmetic at maxDenialProse: a model identifier at 256 bytes has long
// since stopped being one, whatever claude ships.
const maxTaskFieldID = 256

// maxTaskDescription caps the Description field, which is model-authored and
// genuinely variable — for claude's local_bash task type it is the command line
// itself, so it earns a far larger cap than the identifiers above.
//
// The capture's description reads 9 bytes, but it is REDACTED: the record's
// payload_len_bytes_captured (377) minus payload_len_bytes (235) is 142 bytes of
// redaction across two sites ($SESSION_ID and $FIFO), which puts the real
// description at roughly 126 bytes. 4096 is about 32x that.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one event
// carries 3*256 + 4096 = 4864 bytes of claude-derived text. That is 7.4% of the
// v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) and under a third of maxUnrecognizedRaw's
// whole-line 16 KiB, which leaves room for the envelope's other fields plus the
// JSON escaping these strings pick up on the way out. It is also far more command
// line than a human reads off a timeline row, which is the other reason not to
// raise it.
const maxTaskDescription = 4 << 10

// maxTaskPatch caps the Patch field on a turnevent.BackgroundTaskUpdated —
// claude's patch object, serialized and carried whole. Applied at CONSTRUCTION,
// exactly as the caps above are, so an oversized payload never enters the event
// stream, the push queue, or any log. That the field is decoded permissively
// (json.RawMessage takes ANY valid JSON value) is what makes this cap
// load-bearing rather than cosmetic: it is the only shape constraint on the
// value.
//
// Neither existing constant fits. maxTaskFieldID (256) caps machine-generated
// identifiers, which have a bounded format; a patch has none, and 256 bytes is
// about three short keys — one prose-ish value (an error string, a status
// message) would be cut on arrival. And the observation is too weak to multiply:
// the captured patch is 24 bytes with ONE key, and a single-key patch says
// nothing about a two-key one. maxTaskDescription could anchor on its
// observation because a command line's size distribution is something we can
// reason about; this cannot, so the binding constraint comes from the other
// side — the envelope.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// BackgroundTaskUpdated carries 256 + 4096 = 4352 bytes of claude-derived text.
// That is 6.6% of the v2 application-envelope cap of 65519 bytes
// (docs/protocol-mobile.md § Application-envelope size cap), deliberately in
// line with BackgroundTaskStarted's 4864 bytes / 7.4% so the event family has
// ONE worst case a reader can hold rather than a per-variant number to
// re-derive. Escaping is mild for maxUnrecognizedRaw's reason, which applies
// here verbatim: a patch is already JSON text, so its control characters arrive
// pre-escaped as printable pairs and the growth is quotes and backslashes, not
// a \u00XX expansion of every byte. Pathological all-quote content roughly
// doubles it — ~8.7 KB, 13.3% of the envelope, still comfortable.
//
// 4096 is a quarter of maxUnrecognizedRaw's whole-line 16 KiB, which is the
// ordering that must hold: one field of one KNOWN line must not approach the cap
// on an entire UNKNOWN line.
//
// A separate constant even though it currently equals maxTaskDescription: they
// bound different fields for different reasons, and folding them into one would
// make a future change to the command-line budget silently move the patch
// budget.
const maxTaskPatch = 4 << 10

// maxTaskSummary caps the Summary field on a turnevent.BackgroundTaskUpdated —
// claude's account of what a background task did, carried from its
// system/task_notification line (#2245). Applied at CONSTRUCTION, exactly as the
// caps above are, so an oversized payload never enters the event stream, the push
// queue, or any log.
//
// Neither existing constant fits BY REASON, which is the test this family applies
// rather than by value. maxTaskFieldID (256) caps machine-generated identifiers
// and short vendor tokens, which have a bounded format; this is model-authored
// free text with no documented bound, and 256 bytes would cut an ordinary summary
// on arrival. maxTaskPatch caps an opaque blob a consumer is told not to parse;
// this is prose a client renders.
//
// The observation spans both extremes of the field, which is why the value comes
// from the envelope rather than from a multiple. The #2245 capture's summary is
// the task's own command line at 9 bytes REDACTED (the record's
// payload_len_bytes_captured minus payload_len_bytes is 143 bytes of redaction
// across five sites, so the real value is longer); the same subtype in
// parent_tool_use_v2.1.259.json carries multi-line model prose of roughly 120
// bytes. Neither is a distribution to multiply.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style, carries TWO numbers
// deliberately. The task_notification ARM's worst case is one event holding
// 256 + 256 + 4096 = 4608 bytes of claude-derived text, 7.0% of the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) and deliberately in line with the family's
// existing 4352 and 4864 so the family keeps ONE worst case a reader can hold.
// The EVENT TYPE's ceiling is higher: the two producing subtypes fill disjoint
// fields today, but nothing structural stops a third arm filling all four, which
// would be 256 + 256 + 4096 + 4096 = 8704 bytes, 13.3%. Both are written down so
// that a third arm is a decision somebody makes rather than a silent envelope
// regression discovered later.
//
// 4096 is a quarter of maxUnrecognizedRaw's whole-line 16 KiB, the ordering that
// must hold: one field of one KNOWN line must not approach the cap on an entire
// UNKNOWN line.
//
// A separate constant even though it currently equals maxTaskDescription and
// maxTaskPatch, for maxTaskPatch's stated reason: they bound different fields for
// different reasons, and folding them into one would make a future change to any
// one budget silently move the others.
const maxTaskSummary = 4 << 10

// maxTaskRosterEntries caps how many entries a turnevent.BackgroundTaskRoster
// carries. It is the family's first CARDINALITY bound and the one dimension with
// no precedent in this package: every cap above bounds text on a fixed field
// set, and a per-entry text cap alone would leave a roster's total size a
// function of a number claude chooses. Applied at CONSTRUCTION like the others,
// so an oversized payload never enters the event stream, the push queue, or any
// log. Overflow is REPORTED (BackgroundTaskRoster.DroppedTasks), not silent, so
// an under-sized count is visible rather than a lie.
//
// Claude's three-field input is 256 + 256 + 512 = 1024 field bytes per
// entry. The daemon-enriched event also carries a bounded 256-byte ToolCallID,
// joined outside the parser: 1280 unescaped field bytes per row, 8 * 1280 =
// 10240 bytes (10 KiB), about 15.6% of the 65519-byte application-envelope cap.
// This excludes keys and truncation metadata and is separate from escaped-wire
// measurement: TestBackgroundTaskPayloads_FitV2EnvelopeCap fills every field
// with characters that expand to six bytes in JSON and measures the envelope.
// Eight rows remain 8x the one-row observation; scalar and aggregate shapes
// have separate worst cases. The old 8 KiB/half-of-16-KiB arithmetic applies
// only to the input fields, not the enriched event.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened. That is bounded, not unbounded:
// defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees it,
// the densest legal entry is ~55 bytes of input for a ~64-byte struct, so the
// amplification is linear and near 1. This cap bounds what is RETAINED and what
// crosses the wire, which is the property that matters.
const maxTaskRosterEntries = 8

// maxTaskRosterDescription caps each roster entry's Description — the same
// model-authored field maxTaskDescription bounds on a
// turnevent.BackgroundTaskStarted, deliberately given a smaller budget here.
//
// Not thrift: the MULTIPLICATION. This is the one field in the family whose
// budget is multiplied by a count claude chooses, and a multiplied field earns a
// smaller unit budget than the same field carried once. It is also a different
// ROLE: on task_started the description is the event's payload, the one thing
// the event is about; in a roster it is a label in a list whose authoritative
// full-length copy already crossed the wire on the BackgroundTaskStarted this
// entry's task_id joins back to. A cut here loses nothing a consumer holding
// that event cannot recover, and TruncatedFields says it happened.
//
// 512 is ~4x the ~126-byte real description the capture's redaction arithmetic
// implies (payload_len_bytes_captured 354 - payload_len_bytes 212 = 142 bytes
// across $SESSION_ID and $FIFO). A thinner multiple than maxTaskFieldID's 9x or
// maxTaskDescription's 32x, and deliberately so, for the multiplication reason
// above. The weak point is a consumer that never saw the BackgroundTaskStarted —
// connected mid-session, or the task predates the connection — for which 512
// bytes is all there is; TruncatedFields is what will surface that if it bites.
const maxTaskRosterDescription = 512

// maxTaskProgressTasks caps how many task ids Parser.taskProgressToolUses
// remembers within one turn — the cardinality half of #2246's rate bound, and the
// family's SECOND cardinality bound after maxTaskRosterEntries.
//
// It bounds a different SHAPE from that one, which is why it is a constant rather
// than a reuse. maxTaskRosterEntries caps a list inside ONE event, decided and spent
// at construction; this caps a map the parser RETAINS across lines, keyed by ids
// claude chooses. maxTurnDenials is the closer structural neighbour — the family's
// other retained, claude-keyed collection — and Parser.taskProgressToolUses follows
// its every dimension.
//
// The count is 8, derived the way maxTaskRosterEntries derives its own rather than
// borrowed from it: the committed capture holds ONE task, so 8 is 8x the observation,
// the multiple-of-observation form maxTaskFieldID uses. That it lands on the family's
// existing figure for concurrent background tasks is a check on the derivation, not
// its source — and the two are free to diverge, since one caps what a client is shown
// at once and this caps what the parser remembers.
//
// NOT maxTurnDenials' 16, which is the other number it could have been reached for by
// analogy. That constant's own doc argues 16 from a model looping on a refused tool,
// a mechanism with no counterpart here: a background task is opened by a tool call
// that succeeded, and nothing retries one.
//
// The RETAINED size, in maxTaskRosterEntries' arithmetic style:
//
//   - One entry: maxTaskFieldID + one int = 256 + 8 = 264 bytes, and the key is the
//     value the event PUBLISHED, after the cut — emitPermissionDenied's rule, so an
//     id too long to publish is also too long to remember.
//   - Worst case: 8 * 264 = 2112 bytes held for at most one turn. Two orders of
//     magnitude under the whole-line 4 MiB defaultMaxParseBuf already admits, and it
//     contributes NO envelope term at all: nothing here crosses the wire.
//
// PAST THE CAP A TASK GETS NO PROGRESS EVENTS, which is a behavioural choice and not
// a fallout. The alternative — treat an untracked task's previous value as zero and
// emit — restores the one-frame-per-line rate the bound exists to refuse, for exactly
// the tasks a runaway input arranges to be past the cap, so the bound would invert
// under the pressure it was built for. Silence for a ninth concurrent task is the
// cheaper failure because progress is a liveness decoration rather than a
// state-machine edge: it silences one task and not the turn, and that task's opening
// and terminal frames are untouched, so its row still opens and closes correctly.
// deniedThisTurn stops growing at its own cap and accepts an analogous cost.
const maxTaskProgressTasks = 8

// minTaskToolCallsPerEvent is the advance in one task's cumulative usage.tool_uses
// that earns one turnevent.BackgroundTaskProgress (#2246). The package's SECOND
// constant bounding FREQUENCY rather than size, and a `min` for
// minThinkingTokensPerEvent's reason: it is the smallest quantum that earns an event.
//
// WHY THAT COUNTER AND NOT THE OTHER TWO the line carries. tool_uses is the only one
// of the three whose absent baseline is genuinely zero — a task begins having made no
// tool calls — so a first line's advance against nothing is a true reading. In the
// committed capture total_tokens reads 16207 on the FIRST line, the subagent's whole
// context already counted, so a delta against an absent baseline would be 16207: a
// number that says nothing about progress and would emit on every task's first line
// whatever bound was chosen. duration_ms is wall clock, so a bound on it would key
// emission on the subagent's SPEED rather than on its activity, and a subagent blocked
// inside one long tool call would report nothing while doing the most work. tool_uses
// is also the counter measured to track the line count, which is what makes a bound on
// it a bound on the frame rate at all.
//
// THE ARGUMENT IS THINNER THAN minThinkingTokensPerEvent'S, and that is stated rather
// than dressed up. That constant had four bursts of 126-197 tokens to take a ceiling
// and a halving margin from. Here the record is one task and two lines.
//
//   - CEILING, from the data, in that constant's own form: the observed task's whole
//     tool-call advance is 2 (tool_uses reads 1 then 2). Any bound above 2 emits
//     NOTHING for the only task ever measured — the whole-burst-goes-silent failure,
//     one order of magnitude thinner than the one behind 126.
//   - FLOOR: 1 is not a bound. At 1 every line emits and the mapping is the
//     one-frame-per-line shape this constant exists to refuse.
//
// The two meet, so the observation pins the value exactly rather than leaving a range
// to take a margin inside. It is a real reduction — the captured turn's 2 lines become
// 1 event, and a subagent making fifty tool calls produces twenty-five events rather
// than fifty. Power of two by arithmetic rather than by taste: 2 is the only value the
// bounds admit. Raising it needs a NEW capture whose task advances further, never a
// wish for a quieter wire.
//
// THE BOUND IS ON THE COUNTER'S ADVANCE, NOT ON THE LINE COUNT, and the difference is
// a limit rather than a detail. A tool_uses advancing by two or more per line emits on
// every line, so the halving above describes the measured shape and not a guarantee.
// minThinkingTokensPerEvent has the identical property — a delta of 64 on every line
// emits on every line — and the three background-task variants beside this one have no
// rate bound at all, so this is the family's posture rather than a regression in it.
// These events are not droppable deltas (the droppable set is assistant_delta only,
// #610), so a burst holds queue slots; that is the cost maxRateLimitField's doc
// already records as accepted across the family, bounded by the same existing
// backpressure. Named rather than mechanised, per evidence-based fix selection:
// revisit on an OBSERVED rate, as #1385 did.
//
// There is deliberately no envelope arithmetic, for minThinkingTokensPerEvent's
// reason: what this bounds is how often an event fires, and the event's SIZE is
// bounded by the field caps at turnevent.BackgroundTaskProgress.
const minTaskToolCallsPerEvent = 2

// systemTaskStartedLine is the decoded payload of one system/task_started line.
// Kept separate from streamLine, which is the line-level SEGMENTATION struct and
// stays at Type/Subtype/Message; these fields belong to a single subtype and
// widening the segmentation struct with them would blur that boundary.
//
// The field set is exactly what the committed capture shows and nothing
// invented. Two keys the captured line also carries are deliberately absent:
// uuid, which nothing in the daemon reads, and session_id, which is claude's
// session identity and NOT the daemon's conversation identity — see
// turnevent.BackgroundTaskStarted's doc.
type systemTaskStartedLine struct {
	TaskID      string `json:"task_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
	TaskType    string `json:"task_type"`
}

// systemTaskUpdatedLine is the decoded payload of one system/task_updated line.
// Kept separate from streamLine for systemTaskStartedLine's reason, and separate
// from systemTaskStartedLine because the two subtypes share no payload shape
// beyond task_id.
//
// The field set is exactly what the committed capture shows and nothing
// invented, and it is the whole mapped set: the same two keys the captured line
// also carries, uuid and session_id, are deliberately absent — see
// turnevent.BackgroundTaskUpdated's doc. Absent from the DECODE TARGET is a
// stronger guarantee than the test's reflection sweep, because a field that is
// never declared cannot leak.
//
// The two fields' types are deliberately asymmetric. TaskID stays a string, so a
// non-string task_id fails the whole decode and takes the undecodable path.
// Patch is json.RawMessage, which accepts ANY valid JSON value — an object, a
// string, a number, null — and carries claude's bytes verbatim. Both halves of
// that matter: decoding into map[string]any and re-marshalling would normalize
// key order and round every number through float64 (a large integer id in a
// future patch would lose precision), and declaring a shape would DISCARD every
// unknown field, which is streamMessage.Content's reasoning for the same choice.
// The permissiveness is deliberate — "whatever claude puts there" is the point,
// and inventing a validation rule for a shape we have one observation of is
// exactly what #1380 declined to do for missing fields. maxTaskPatch is what
// makes it safe.
type systemTaskUpdatedLine struct {
	TaskID string          `json:"task_id"`
	Patch  json.RawMessage `json:"patch"`
}

// systemTaskNotificationLine is the decoded payload of one system/task_notification
// line — the subtype that reports a background task ENDING (#2245). Kept separate
// from streamLine for systemTaskStartedLine's reason, and separate from both task
// siblings because the three subtypes share no payload shape beyond task_id.
//
// The field set is what the committed capture shows, MINUS four keys, and nothing
// invented. The capture pins nine top-level keys (taskNotificationPinnedKeys in
// task_notification_capture_test.go); three are declared here. Of the six that are
// not, type and subtype are segmentation and belong to streamLine, uuid and
// session_id are the family's standing omissions, and the remaining two are
// deliberate exclusions worth stating:
//
//   - output_file is documented as A PATH ON THE OPERATOR'S HOST, the only
//     path-shaped value this family's lines carry. Excluding it from the DECODE
//     TARGET is a stronger guarantee than redacting it downstream, and it is the
//     one systemTaskUpdatedLine's doc already states: a field that is never
//     declared cannot leak. No code path in the daemon ever holds the value. The
//     captured value happens to be the empty string, so the capture does not
//     DEMONSTRATE the hazard — the documented field class is the reason, and
//     #2247's capture built a whole third redaction mechanism around that class.
//   - tool_use_id has no reader. The join key is task_id, and the
//     BackgroundTaskStarted this line joins back to already published the tool
//     call. Declaring it would also leave it empty on every event the task_updated
//     arm produces, indistinguishable from claude omitting it.
//
// The Agent SDK additionally describes ambient, skip_transcript and usage. The
// capture files all three under keys_documented_not_observed, so none is declared:
// a docs page is a thing to CHECK a capture against, never a thing to declare from.
//
// All three fields are string, so a non-string value in ANY of them fails the whole
// decode and takes the undecodable path. systemTaskUpdatedLine's asymmetry does not
// arise here because no field on this line carries an unconstrained shape.
type systemTaskNotificationLine struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// systemTaskProgressLine is the decoded payload of one system/task_progress line —
// the subtype that reports a background task still WORKING (#2246). Kept separate
// from streamLine for systemTaskStartedLine's reason, and separate from all three
// task siblings because no two of the four subtypes share a payload shape beyond
// task_id.
//
// The field set is what the committed capture shows, MINUS five keys, and nothing
// invented. The capture pins ten top-level keys (taskProgressPinnedKeys in
// task_progress_capture_test.go); five are declared here. Of the five that are not,
// type and subtype are segmentation and belong to streamLine, and the remaining
// three are the family's standing omissions, stated at
// turnevent.BackgroundTaskUpdated: session_id, uuid and tool_use_id.
//
// claude's documented `summary` is NOT declared, and its absence is a MEASUREMENT.
// The capture files it under keys_documented_not_observed — the SDK describes it for
// a local agent only with the progress-summaries option, and always for an MCP task,
// and this record is a local agent without that option. A docs page is a thing to
// CHECK a capture against, never a thing to declare from, so measuring what those
// stagings send is a new capture rather than a field added here.
//
// The four strings are string, so a non-string value in any of them fails the whole
// decode and takes the undecodable path. Usage is a NESTED STRUCT rather than a
// json.RawMessage: systemTaskUpdatedLine's permissive typing is for a value whose
// shape claude owns and nothing reads, whereas every key inside this object is read
// and one of them decides whether the line emits at all. A usage that is not an
// object, or whose counters are not numbers, therefore fails the decode rather than
// arriving as bytes nobody can compare.
type systemTaskProgressLine struct {
	TaskID       string                  `json:"task_id"`
	Description  string                  `json:"description"`
	SubagentType string                  `json:"subagent_type"`
	LastToolName string                  `json:"last_tool_name"`
	Usage        systemTaskProgressUsage `json:"usage"`
}

// systemTaskProgressUsage is that line's nested usage object, whose three keys are
// exactly what taskProgressPinnedUsageKeys pins and nothing invented. A top-level key
// set says nothing about a nested shape, which is why the capture pins both.
//
// An ABSENT usage key decodes to this struct's zero value, so all three read 0. That
// is deliberate and is not a validation rule: absence is claude's to choose, and
// emitBackgroundTaskProgress's guard treats a zero ToolUses as nothing to report,
// which is the same reading an explicit 0 gets.
//
// The three are int and SIGNED on purpose, for resultModelUsage.ContextWindow's
// reason: a negative reading has to be OBSERVABLE for the emit function to handle it,
// and an unsigned type would wrap one into an enormous positive count and act on it
// as fact. That matters most for ToolUses, which the rate bound subtracts.
type systemTaskProgressUsage struct {
	TotalTokens int `json:"total_tokens"`
	ToolUses    int `json:"tool_uses"`
	DurationMS  int `json:"duration_ms"`
}

// systemBackgroundTasksLine is the decoded payload of one
// system/background_tasks_changed line. Kept separate from streamLine for
// systemTaskStartedLine's reason, and separate from both scalar targets because
// this subtype's payload is an ARRAY — the shape difference the whole ticket
// sits on.
//
// The two keys the captured line also carries, uuid and session_id, are
// deliberately absent — see turnevent.BackgroundTaskRoster's doc. Absent from
// the DECODE TARGET is a stronger guarantee than the test's reflection sweep,
// because a field that is never declared cannot leak.
type systemBackgroundTasksLine struct {
	Tasks []systemBackgroundTaskEntry `json:"tasks"`
}

// systemBackgroundTaskEntry is Claude's three-field roster input. It deliberately
// has no tool_use_id, tool_call_id or patch. The daemon's enriched BackgroundTask
// row joins its id from BackgroundTaskStarted outside the parser; the parser
// neither reads an id from this entry nor retains per-task join state.
//
// All three are plain strings, which is why truncateField's json.RawMessage
// exception does not reach this subtype: encoding/json has already
// U+FFFD-replaced invalid input on decode, so our own cut is the only mid-rune
// hazard. A non-string value for any of them fails the whole decode and takes
// the undecodable path, exactly as systemTaskUpdatedLine.TaskID does.
type systemBackgroundTaskEntry struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
}

// emitBackgroundTaskStarted decodes a system/task_started line and emits one
// turnevent.BackgroundTaskStarted, reporting that it consumed the line either
// way. Field mapping and cap numbers come from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property that makes a tool result whose text is
// literally `{"type":"result"}` unable to forge a turn boundary: control shapes
// are read from the top level only, and nested content is never re-scanned.
// Decoding this payload from anywhere else would make a background task forgeable
// out of claude's own tool output.
//
// A payload that will not decode into the shape (a numeric task_id, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized. Keeping system whole on ignoredLineTypes is what makes "no system
// line reaches the unrecognized lane" structural, and that guarantee is worth
// more than surfacing a malformed line of a subtype we already know. A missing
// field is not an error either: absence is claude's to choose, there is no
// captured negative case, so the field lands empty rather than inventing a
// validation rule.
func (p *Parser) emitBackgroundTaskStarted(line []byte) bool {
	var tl systemTaskStartedLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. None of the decoded fields is logged.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_started")
		return true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal: TruncatedFields is
	// ordered by these calls, and inside a literal that order would rest on the
	// left-to-right operand rule rather than on something a reader sees. The names
	// are the DAEMON's — tool_call_id, not claude's tool_use_id.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	toolCallID := bound(tl.ToolUseID, "tool_call_id", maxTaskFieldID)
	description := bound(tl.Description, "description", maxTaskDescription)
	taskType := bound(tl.TaskType, "task_type", maxTaskFieldID)

	p.emit(turnevent.BackgroundTaskStarted{
		TaskID:      taskID,
		ToolCallID:  toolCallID,
		Description: description,
		TaskType:    taskType,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskUpdated decodes a system/task_updated line and emits one
// turnevent.BackgroundTaskUpdated, reporting that it consumed the line either
// way. Peer of emitBackgroundTaskStarted, and its every structural choice is the
// same one for the same reason. Field mapping comes from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// That is not a call-shape convention: streamLine's doc states the property it
// preserves, that control shapes are read from the top level only and nested
// content is never re-scanned, which is what stops a tool result whose text is
// literally `{"type":"result"}` from forging a turn boundary. Decoding this
// payload from anywhere else would make a background-task update forgeable out
// of claude's own tool output.
//
// A payload that will not decode into the shape (a numeric task_id, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized, because keeping system whole on ignoredLineTypes is what makes
// "no system line reaches the unrecognized lane" structural. An absent patch is
// not an error either: absence is claude's to choose, so the field lands empty
// rather than inventing a validation rule.
func (p *Parser) emitBackgroundTaskUpdated(line []byte) bool {
	var tl systemTaskUpdatedLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Neither the decoded fields nor the patch is logged, and the patch
		// is the thing this handler is most tempted to explain itself with.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_updated")
		return true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal, for
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
	// calls, and inside a literal that order would rest on the left-to-right
	// operand rule rather than on something a reader sees. Neither name is
	// translated here — claude's keys and the daemon's fields agree.
	//
	// string(tl.Patch) is "" when claude omits the key, which is the empty-Patch
	// contract; json.RawMessage COPIES its input on decode, so this does not alias
	// p.buf and nothing outlives the buffer it came from.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	patch := bound(string(tl.Patch), "patch", maxTaskPatch)

	p.emit(turnevent.BackgroundTaskUpdated{
		TaskID: taskID,
		Patch:  patch,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskNotification decodes a system/task_notification line and emits
// one turnevent.BackgroundTaskUpdated, reporting that it consumed the line either
// way. It is the SECOND producer of that event (#2245): claude's task_notification
// line reports a background task ENDING, which nothing in this family could express
// before, and the event's doc states which fields each producer fills.
//
// Named for the LINE rather than for the event, unlike its three siblings. Those
// map one subtype to one variant and take the variant's name; here two functions
// produce one event, so a name matching the event would collide with the peer that
// already has it. Field mapping comes from the committed capture
// (internal/e2e/realclaude/testdata/task_notification_v2.1.259.json), never from a
// hand-built payload and never from the Agent SDK's documented key list.
//
// NO PATCH IS SYNTHESIZED, and that is a contract rather than an omission. The
// captured line carries no patch key at all, and turnevent.BackgroundTaskUpdated's
// Patch is documented as claude's own object carried whole with nothing declared
// about its contents. Reporting the terminal state as a manufactured patch object
// would have been the obvious-looking move and would have made that doc — and
// protocol.BackgroundTaskUpdatedPayload's — false, breaking the one guarantee a
// consumer reads them for. The terminal state travels as its own declared fields
// instead, and Patch stays empty on every event this function emits.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// the reason is load-bearing rather than a call-shape convention. streamLine's doc
// states the property it preserves: control shapes are read from the top level only
// and nested content is never re-scanned. Decoding this payload from anywhere else
// would let a tool result whose text is literally a task_notification line forge a
// task's DEATH — a client's row would close for a task still running, which is the
// inverse of the symptom this family exists to fix.
//
// A payload that will not decode into the shape (a numeric task_id, a status that
// is an object) is dropped with a content-free Debug and no event — NOT surfaced as
// an Unrecognized, because keeping system whole on ignoredLineTypes is what makes
// "no system line reaches the unrecognized lane" structural. An absent field is not
// an error either: absence is claude's to choose, so the field lands empty rather
// than inventing a validation rule.
func (p *Parser) emitBackgroundTaskNotification(line []byte) bool {
	var tl systemTaskNotificationLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. None of the decoded fields is logged, and summary is the thing
		// this handler is most tempted to explain itself with: it is the field most
		// likely to hold a readable description of what went wrong.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_notification")
		return true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal, for
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
	// calls, and inside a literal that order would rest on the left-to-right
	// operand rule rather than on something a reader sees. No name is translated —
	// claude's keys and the daemon's fields agree.
	//
	// status takes maxTaskFieldID, reused rather than given a constant of its own:
	// a short token from a set claude owns is the shape that constant's amendments
	// already cover. summary takes its own, because model-authored free text is a
	// different shape with a different reason — see maxTaskSummary.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	status := bound(tl.Status, "status", maxTaskFieldID)
	summary := bound(tl.Summary, "summary", maxTaskSummary)

	p.emit(turnevent.BackgroundTaskUpdated{
		TaskID: taskID,
		Status: status,
		// Patch is left at its zero value deliberately — see the no-synthesis
		// paragraph above. It is written out rather than omitted silently because a
		// reader's first instinct here is that a field was forgotten.
		Patch:   "",
		Summary: summary,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskProgress decodes a system/task_progress line and emits at most
// ONE turnevent.BackgroundTaskProgress, reporting that it consumed the line either
// way. Field mapping comes from the committed capture
// (internal/e2e/realclaude/testdata/parent_tool_use_v2.1.259.json, pinned in
// task_progress_capture_test.go), never from a hand-built payload and never from the
// Agent SDK's documented key list.
//
// Named for the EVENT, like its three task siblings and unlike
// emitBackgroundTaskNotification: one subtype produces one variant here, so there is
// no peer for the variant's name to collide with.
//
// LIKE emitThinkingProgress AND UNLIKE ITS TASK SIBLINGS, this mapping is not a pure
// function of one line: it is rate-bounded, so most lines accumulate and emit
// nothing. The rule, with `seen` the line's usage.tool_uses and `prev` the remembered
// value for this task (0 when untracked):
//
//	seen <= 0                    -> consume, no event               (guard)
//	untracked and map full       -> consume, no event               (cardinality)
//	seen <= prev                 -> prev = seen; consume, no event  (re-baseline)
//	seen - prev < bound          -> consume, no event               (accumulate)
//	otherwise                    -> prev = seen; emit
//
// with the map cleared at the `result` arm in consumeLine. The accumulate branch
// stores NOTHING, which is what keeps prev the last EMITTED value and makes the
// accumulation implicit in claude's own cumulative counter — see
// Parser.taskProgressToolUses for why that is AC 4's enforcement rather than a
// convenience.
//
// TWO DEPARTURES FROM emitThinkingProgress, and both are why this function is longer
// than a copy of it would be.
//
// THE DELTA IS COMPUTED, NOT GIVEN. That function reads a delta claude supplied and
// already made non-negative; here the counters are cumulative per task, so any delta
// is arithmetic against a previous value the daemon had to keep. So the crossing test
// is written SUBTRACTED — `seen - prev >= bound`, never `prev + bound <= seen` —
// and the reason is stronger here than there. Both operands are in [0, MaxInt] by the
// invariant at the field, so the difference is representable whatever claude sends and
// the failure is unrepresentable rather than guarded against; the additive form
// overflows on a large prev, reads false forever, and silences that task for the rest
// of the turn from one version-drifted line. Do not "simplify" it back.
//
// A COUNTER THAT FAILS TO ADVANCE ARRIVES, rather than being ruled out. seen == prev
// is a case claude can produce (a progress line fired for something other than a tool
// call), and seen < prev is claude restarting a counter — which it demonstrably does
// on the neighbouring subtype, at every inference request. Both re-baseline DOWN
// rather than being ignored, and that is the computed-delta analogue of that
// function's `d <= 0` guard: ignoring a backwards counter would leave a stale
// high-water mark no realistic advance climbs out of, and silence is the single
// outcome this family's rate-bounded mappings refuse. The re-baseline stores a value
// already > 0, for a key already present, so it can neither break the invariant nor
// grow the map, and an alternating counter emits at most every other line.
//
// AN EMPTY task_id IS A BUCKET LIKE ANY OTHER, not a drop and not a bypass. The
// family's rule is that absence is claude's to choose and the field lands empty;
// keying on "" keeps that rule AND keeps the bound, where dropping would depart from
// it and treating unkeyed lines as unbounded would defeat it. The cost is that two
// simultaneous unkeyed tasks share one counter and each reports less often, which
// degrades a report a client could not attach to a row anyway.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and that
// is not a call-shape convention. streamLine's doc states the property it preserves:
// control shapes are read from the top level only and nested content is never
// re-scanned. Decoding this payload from anywhere else would let a tool result whose
// text is literally a task_progress line forge a progress report — a liveness claim
// about a task, on a frame a remote client draws a row from. Lower value to forge than
// a task's death or a whole roster, and refused on the same rule rather than on a
// judgement about its value.
//
// A payload that will not decode into the shape (a numeric task_id, a usage that is
// not an object) is dropped with a content-free Debug and no event — NOT surfaced as
// an Unrecognized, because keeping system whole on ignoredLineTypes is what makes "no
// system line reaches the unrecognized lane" structural. NOTHING DERIVED FROM THE LINE
// IS LOGGED on any path, and the four SILENT branches are where that rule is most
// tempting to bend: a drop with no diagnostic invites "just the task id" or "just the
// count". emitThinkingProgress refuses the identical bend for a token number and
// emitBackgroundTaskRoster for an entry count.
func (p *Parser) emitBackgroundTaskProgress(line []byte) bool {
	var tl systemTaskProgressLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Neither the counters nor description is logged, and description is
		// the thing this handler is most tempted to explain itself with: it names what
		// the subagent is reading, which reads like a diagnostic and is a file path.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "task_progress")
		return true
	}

	// Absent, zero and negative all land here and are treated identically: no tool
	// call to report, and nothing remembered. It also establishes the field's
	// invariant for every store below — see Parser.taskProgressToolUses.
	seen := tl.Usage.ToolUses
	if seen <= 0 {
		return true
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal, for
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these calls,
	// and inside a literal that order would rest on the left-to-right operand rule
	// rather than on something a reader sees. No name is translated — claude's keys
	// and the daemon's fields agree.
	//
	// task_id is bound BEFORE it is used as the map key, which is
	// emitPermissionDenied's rule and not an ordering accident: the key is the value
	// the event publishes, so an id too long to publish is also too long to remember.
	taskID := bound(tl.TaskID, "task_id", maxTaskFieldID)
	description := bound(tl.Description, "description", maxTaskDescription)
	subagentType := bound(tl.SubagentType, "subagent_type", maxTaskFieldID)
	lastToolName := bound(tl.LastToolName, "last_tool_name", maxTaskFieldID)

	prev, tracked := p.taskProgressToolUses[taskID]
	// Growth stops at maxTaskProgressTasks — see that constant for why an untracked
	// task past the cap goes silent rather than emitting on every line.
	if !tracked && len(p.taskProgressToolUses) >= maxTaskProgressTasks {
		return true
	}
	if seen <= prev {
		// Re-baseline. Reached only when the key is already present (prev is 0 for an
		// untracked task and seen is > 0 here), so this cannot grow the map, and seen
		// has passed the guard, so it cannot break the invariant.
		p.taskProgressToolUses[taskID] = seen
		return true
	}
	// The COMPLEMENT of the doc's `seen - prev >= bound -> emit`, written this way so
	// the accumulate branch returns early and the emit is the function's tail.
	// Subtracted, never additive — see the doc above. Nothing is stored here: prev
	// stays the last EMITTED value, so the accumulation lives in claude's cumulative
	// counter rather than in a total the parser keeps.
	if seen-prev < minTaskToolCallsPerEvent {
		return true
	}

	if p.taskProgressToolUses == nil {
		p.taskProgressToolUses = make(map[string]int, 1)
	}
	p.taskProgressToolUses[taskID] = seen
	// The line's OWN values, never a running total: the event stays a pure function of
	// the line that produced it, and the counters' cumulative-per-task reading is a
	// documented consumer hazard on turnevent.BackgroundTaskProgress rather than a
	// number invented here. No caps on the three ints — an int cannot blow the
	// envelope.
	p.emit(turnevent.BackgroundTaskProgress{
		TaskID:       taskID,
		Description:  description,
		SubagentType: subagentType,
		LastToolName: lastToolName,
		TotalTokens:  tl.Usage.TotalTokens,
		ToolUses:     seen,
		DurationMS:   tl.Usage.DurationMS,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
	return true
}

// emitBackgroundTaskRoster decodes a system/background_tasks_changed line and
// emits one turnevent.BackgroundTaskRoster, reporting that it consumed the line
// either way. Peer of emitBackgroundTaskUpdated, and its every structural choice
// is the same one for the same reason. Field mapping comes from the committed
// capture (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never
// from a hand-built payload; both bounds come from lines synthesized to exceed
// them, which the capture's single 212-byte entry cannot.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// the reason matters more on this subtype than on either sibling. streamLine's
// doc states the property it preserves: control shapes are read from the top
// level only and nested content is never re-scanned, which is what stops a tool
// result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would make a whole
// background-task roster forgeable out of claude's own tool output — and this is
// the most valuable variant to forge, because it is the one that claims what is
// ALIVE.
//
// A payload that will not decode into the shape (tasks as an object, say) is
// dropped with a content-free Debug and no event — NOT surfaced as an
// Unrecognized, because keeping system whole on ignoredLineTypes is what makes
// "no system line reaches the unrecognized lane" structural. An absent or empty
// tasks array is not an error either: an EMPTY roster is the signal that nothing
// is alive, so the event is still emitted rather than dropped.
//
// No terminal, finish, or completion event is synthesized here or anywhere —
// see turnevent.BackgroundTaskRoster's doc. The parser holding no ROSTER and no
// per-task memory is that refusal's enforcement mechanism, not an incidental
// property: detecting a task's disappearance would require remembering the
// previous roster.
//
// CORRECTED 2026-08-09 (#1385): the enforcement is stated above in terms of what
// the parser remembers about TASKS, because the broader claim this sentence used
// to make — that the parser holds no cross-line state at all — is no longer
// literally true. It now holds one int, thinkingSinceEmit, a token counter reset
// at the turn boundary. That does not weaken the refusal by a step: the counter
// remembers no task, no roster, and nothing any line said, so nothing about it
// brings a synthesized finish event any closer to being derivable.
//
// RE-SCOPED 2026-09-03 (#2077): the paragraph above is still true of the PARSER,
// which holds no roster and no per-task memory — but it is scoped to the parser
// and can no longer be read as a daemon-wide impossibility argument. cmd/pyry's
// sessionBackgroundTaskHold now retains the newest roster for the session's life,
// one layer up, so the previous roster does outlive its line somewhere. The
// refusal survives that intact and by an explicit rule rather than by an
// accident of forgetting: the hold REPLACES and never diffs, and it holds one
// roster rather than a previous-and-current pair, so a disappearance is still not
// derivable from anything the daemon keeps. Whoever adds a second retention in
// this family owes the same statement — the amnesia argument no longer carries it
// on its own.
//
// RE-SCOPED 2026-09-08 (#2224): the parser now remembers something claude SAID —
// assistantErrorCategory, an API error token read off an `assistant` line and held
// until the turn boundary — so the CORRECTED 2026-08-09 paragraph's defence of the
// counter ("remembers no task, no roster, and nothing any line said") no longer
// describes everything the parser holds, and it is not stretched to.
//
// The refusal survives, and by the explicit rule the #2077 entry above already had
// to state rather than by amnesia. What makes a synthesized task-finish event
// underivable is that nothing anywhere retains a PREVIOUS roster to diff a current
// one against. #2224's field is a scalar off a different line type, holds no task and
// no roster, and is overwritten rather than accumulated, so it brings a disappearance
// no closer to being detectable. What has finally expired is the shape of argument:
// this doc can no longer say the parser forgets everything, only that it remembers
// nothing a finish event could be computed from. A third retention owes that same
// sentence about itself — the general claim is gone for good.
//
// MEASURED 2026-09-22 (#2525): on claude 2.1.280, for a task backgrounded on request,
// internal/e2e/realclaude/testdata/roster_after_finish_v2.1.280.json records the verdict
// roster-omits-the-finished-task, prompted by nothing — an empty roster arrived at the
// completion, one line BEFORE the task_updated carrying status completed — so the count
// comes down from claude's own line and nothing here needs to synthesize a finish.
func (p *Parser) emitBackgroundTaskRoster(line []byte) bool {
	var tl systemBackgroundTasksLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Nothing decoded is logged, and neither is the entry COUNT: a
		// roster is a list, lists read as diagnostics, and "just the length" is the
		// leak a content-free rule is most often bent for.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "background_tasks_changed")
		return true
	}

	// The COUNT bound runs before the loop, and truncation is FROM THE TAIL:
	// claude's order is preserved because no ranking is invented, its ordering
	// semantics being unobserved.
	entries := tl.Tasks
	var dropped int
	if len(entries) > maxTaskRosterEntries {
		dropped = len(entries) - maxTaskRosterEntries
		entries = entries[:maxTaskRosterEntries]
	}

	// nil for an empty or absent array: append never runs, which is the contract
	// BackgroundTaskRoster.Tasks states.
	var tasks []turnevent.BackgroundTask
	for _, entry := range entries {
		// The TEXT bound is per entry, so `cut` is per entry — which is the whole
		// reason this closure cannot be hoisted out of the loop.
		var cut []string
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements rather than a composite literal, for
		// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
		// calls, and inside a literal that order would rest on the left-to-right
		// operand rule rather than on something a reader sees. No name is translated
		// — claude's keys and the daemon's fields agree on this subtype.
		taskID := bound(entry.TaskID, "task_id", maxTaskFieldID)
		taskType := bound(entry.TaskType, "task_type", maxTaskFieldID)
		description := bound(entry.Description, "description", maxTaskRosterDescription)

		tasks = append(tasks, turnevent.BackgroundTask{
			TaskID:      taskID,
			TaskType:    taskType,
			Description: description,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}

	// Each dimension reports where it happens: the text cut rides its entry, the
	// count rides the event. A count folded into a top-level TruncatedFields
	// naming "tasks" would lose HOW MANY were lost.
	p.emit(turnevent.BackgroundTaskRoster{
		Tasks:        tasks,
		DroppedTasks: dropped,
	})
	return true
}

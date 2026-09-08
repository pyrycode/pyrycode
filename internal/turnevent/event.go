// Package turnevent defines the neutral, daemon-owned outbound turn-event
// model for Phase 2 structured streaming (EPIC #596).
//
// These are pure value types — no transport, no I/O, standard library only.
// They are the stable internal contract that the event-stream bridge (#608)
// maps tui-driver Events() INTO and that the v2 wire types (#607) map OUT of.
// The mobile wire (now) and the future pyry acp adapter (#600) are thin
// adapters over this one model: it is shaped ~90% like ACP so the ACP adapter
// is near pass-through, but it is owned by us — so churn in the external ACP
// spec stays inside the ACP adapter and never reaches the daemon core or the
// mobile wire. Same containment logic the tui-driver substrate seal applies to
// claude's screen.
//
// This is the outbound turn-event core only. No Events() draining and no
// envelope mapping live here. Inbound commands (Prompt, Cancel, …) and the
// internal-only BusyState event are out of scope and get a home in a later
// ticket. The internal-only Stall event now lives here (mobile-sent,
// ACP-dropped); see its type doc.
package turnevent

import "encoding/json"

// Event is the sealed sum type of outbound turn events: TextChunk,
// ThoughtChunk, ToolStart, ToolUpdate, TurnEnd, BackgroundTaskStarted,
// BackgroundTaskUpdated, BackgroundTaskRoster, ThinkingProgress, the
// internal-only status peers Stall, ApiRetry, and Compacting, the compaction
// boundary CompactionBoundary, and the diagnostic marker Unrecognized.
// The unexported marker
// keeps the variant set
// closed to this package, so external ACP-spec churn cannot inject a variant.
// The bridge (#608) ranges a stream of Event and the wire adapter (#607)
// type-switches to map each kind.
type Event interface{ isTurnEvent() }

// TextChunk is incremental assistant text, grouped by message.
type TextChunk struct {
	MessageID string
	Text      string
}

// ThoughtChunk is streaming reasoning ("thinking") text, grouped by message.
type ThoughtChunk struct {
	MessageID string
	Text      string
}

// ToolStart announces a new tool invocation.
//
// RawInput is opaque pass-through tool input the model never parses or mutates;
// it is carried as json.RawMessage (undecoded bytes) precisely because that
// does not force a parse — consumers decode it on their own terms.
type ToolStart struct {
	ToolCallID string
	Title      string
	Kind       ToolKind
	RawInput   json.RawMessage
	Locations  []Location
}

// ToolUpdate carries changed fields of an existing tool call. Content may be
// nil for a status-only update.
//
// ResultDetail is a short, DAEMON-COMPOSED précis of the call's structured
// outcome — "265 lines", "110 of 1676 lines", "+10 −3", "created · 54 lines",
// "5 files" — derived from the tool_use_result sidecar claude writes alongside
// each result (#2024, #2025). Empty means "no count", which is the answer for
// every shape the producer does not recognise.
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
type ToolUpdate struct {
	ToolCallID   string
	Status       ToolStatus
	Content      ToolContent
	ResultDetail string
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

// BackgroundTaskStarted announces that claude started a background task — work
// that outlives the turn that spawned it. It maps claude's system/task_started
// line (#1380), the first system subtype the parser translates rather than
// drops.
//
// It opens and closes no turn, and that is the point of the variant: a
// backgrounded command is still alive when the turn ends, which is exactly the
// #1240 symptom (turn_end/end_turn and state idle while the command runs) that
// nothing reaching a client could previously separate from a genuine finish.
//
// The NAME is the daemon's, not claude's. "Task" alone collides with ACP
// tool-call vocabulary, so the domain concept ("background task") is the word
// used here; translating at this boundary is what keeps a claude rename of
// task_started landing in the parser and nowhere else.
//
// Two keys the captured line carries are deliberately NOT fields here:
//
//   - session_id — claude's session identity, which is NOT the daemon's
//     conversation identity. A field of that name would invite a consumer, or
//     a later wire mapper, to route on it.
//   - uuid — claude's per-line message id. Nothing in the daemon reads it, and
//     carrying it would add a fourth claude-derived string to the truncation
//     surface for no consumer. If a use appears, it is added then with a reason.
//
// Every string field is claude-derived and is bounded by the producer AT
// CONSTRUCTION (streamsup's maxTaskFieldID / maxTaskDescription), following
// Unrecognized's precedent, so an oversized payload never enters the event
// stream, a queue, or a log. Like every variant here it carries no conversation
// identity — the bridge injects that.
type BackgroundTaskStarted struct {
	// TaskID is claude's opaque handle for the task. It is the join key the
	// later lifecycle lines carry.
	TaskID string
	// ToolCallID is the tool call that spawned the task: claude's tool_use_id,
	// which is the same identifier ToolStart and ToolUpdate already carry under
	// this name — so a consumer joins the two with no vocabulary lookup.
	ToolCallID string
	// Description is the task's label. For claude's local_bash task type it is
	// the literal command line: safe to RENDER as text, never to execute or
	// re-shell.
	Description string
	// TaskType is claude's kind for the task ("local_bash" in the one captured
	// line). A plain string rather than a closed enum — one observation does not
	// earn a closed set.
	TaskType string
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "task_id",
	// "tool_call_id" (not claude's tool_use_id — the report names the field it
	// describes), "description", "task_type". nil when nothing was cut, never an
	// empty non-nil slice, so a consumer can emit it as absent rather than [].
	TruncatedFields []string
}

// BackgroundTaskUpdated announces that a background task claude already started
// changed state. It maps claude's system/task_updated line (#1382), the second
// system subtype the parser translates rather than drops, and is the peer of
// BackgroundTaskStarted: that variant opens the task, this one reports what
// happened to it afterwards.
//
// It exists so a task's state AFTER it starts is representable at all. Without
// it, everything claude says about a running task is discarded at the parser and
// a client can only ever know a task began.
//
// It opens and closes no turn, exactly as BackgroundTaskStarted does not — a
// background task's lifecycle is orthogonal to the turn that spawned it, which
// is the whole #1240 point.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives: translating at this boundary keeps a claude rename of task_updated
// landing in the parser and nowhere else.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380):
//
//   - session_id — claude's session identity, which is NOT the daemon's
//     conversation identity. A field of that name would invite a consumer, or
//     a later wire mapper, to route on it.
//   - uuid — claude's per-line message id, which nothing in the daemon reads.
//
// Every string field is claude-derived and is bounded by the producer AT
// CONSTRUCTION (streamsup's maxTaskFieldID / maxTaskPatch), following
// Unrecognized's precedent, so an oversized payload never enters the event
// stream, a queue, or a log. Like every variant here it carries no conversation
// identity — the bridge injects that.
type BackgroundTaskUpdated struct {
	// TaskID is claude's opaque handle for the task: the join key back to the
	// BackgroundTaskStarted that opened it. Same name, no translation.
	TaskID string
	// Patch is claude's patch object — what CHANGED about the task — carried
	// WHOLE and unparsed as its serialized text. One key has been observed
	// (is_backgrounded), and a mapping that enumerated known patch keys would
	// silently discard every key claude ships next, so nothing here is declared
	// about its contents. Empty when claude omits the key; "" and "{}" stay
	// distinguishable for free.
	//
	// A plain string, not json.RawMessage, for Unrecognized.Raw's reason: the
	// producer truncates it at construction, and a truncated object is no longer
	// valid JSON, so typing it as raw JSON would be a lie. A consumer must not
	// assume it parses.
	//
	// Safe to RENDER as text, never to execute or re-shell. Its sibling's
	// Description already carries a literal command line for the local_bash task
	// type, and a future patch key may carry command text too; a patch's
	// structured shape makes it the more tempting thing to feed somewhere that
	// runs it.
	//
	// The producer also scrubs invalid UTF-8 with an EMPTY replacement, so
	// invalid bytes are DELETED rather than replaced, and TruncatedFields reports
	// the cap cut ONLY — not scrub removals. Patch can therefore differ from
	// claude's bytes without being listed as truncated. That is a stated
	// limitation, not an oversight: the value is a display blob whose JSON
	// validity is already not guaranteed, so a consumer cannot act differently
	// either way. Unlike a string-decoded field, Patch is the one place the scrub
	// bites on the UNtruncated path too — see streamsup's truncateField.
	Patch string
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "task_id",
	// "patch". Neither is translated from claude's key here (contrast
	// BackgroundTaskStarted's tool_use_id -> "tool_call_id"). nil when nothing
	// was cut, never an empty non-nil slice, so a consumer can emit it as absent
	// rather than [].
	TruncatedFields []string
}

// BackgroundTask is one entry of a BackgroundTaskRoster: the roster's element
// type, NOT an Event, so it carries no marker. Its three fields are exactly the
// per-entry keys claude's system/background_tasks_changed line shows and nothing
// invented — in particular there is no tool_use_id and no patch, which the
// scalar siblings carry because their LINES do.
type BackgroundTask struct {
	// TaskID is claude's opaque handle for the task: the join key back to the
	// BackgroundTaskStarted that opened it and every BackgroundTaskUpdated since.
	// Same name, no translation.
	TaskID string
	// TaskType is claude's kind for the task ("local_bash" in the one captured
	// roster). A plain string rather than a closed enum, for
	// BackgroundTaskStarted.TaskType's reason.
	TaskType string
	// Description is the task's label. For claude's local_bash task type it is
	// the literal command line: safe to RENDER as text, never to execute or
	// re-shell. A roster carries these once per entry, and a LIST of command
	// lines is a more tempting shape to feed somewhere structured than a single
	// one, which is why the warning is repeated here rather than delegated.
	//
	// It is bounded by a tighter cap than BackgroundTaskStarted.Description
	// (streamsup's maxTaskRosterDescription, not maxTaskDescription): here the
	// value is a label in a list whose length claude chooses, and the
	// authoritative full-length copy already crossed the wire on the
	// BackgroundTaskStarted this entry's TaskID joins back to.
	Description string
	// TruncatedFields names THIS entry's fields the producer cut to fit their
	// caps, in declaration order, using the DAEMON's snake_case names: "task_id",
	// "task_type", "description". No name is translated — claude's keys and these
	// fields agree. nil when nothing was cut, never an empty non-nil slice.
	TruncatedFields []string
}

// BackgroundTaskRoster carries the complete set of background tasks claude is
// tracking at one moment. It maps claude's system/background_tasks_changed line
// (#1381), the third and last captured system subtype the parser translates
// rather than drops, and it is the aggregate peer of the two scalar variants
// above: they report what happened to ONE task, this reports what is alive.
//
// The NAME is the daemon's, not claude's, and this is the one place in the
// family where the translation earns more than insulation against a claude
// rename. claude's background_tasks_changed names the TRIGGER; the payload is a
// SNAPSHOT. A variant called "…Changed" invites a consumer to read it as a
// delta, and a consumer reading a snapshot as a delta is one short step from
// inferring a finish the daemon has never observed.
//
// No terminal, finish, or completion event exists in this family, deliberately.
// The capturing probe ended its turn with the task still alive, so nothing on
// record shows a task finishing; a task's disappearance from a later roster is
// the AVAILABLE finish signal, but that transition has never been observed and
// the daemon does not report a finish it cannot detect. Diffing successive
// snapshots is a legitimate thing for a CONSUMER to do on its own terms — it is
// not the daemon's inference to make.
//
// It opens and closes no turn, exactly as its two siblings do not.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380): session_id, which is claude's session identity
// and NOT the daemon's conversation identity, and uuid, claude's per-line
// message id, which nothing in the daemon reads.
//
// Every string is claude-derived and bounded by the producer AT CONSTRUCTION in
// BOTH dimensions — the entry COUNT (streamsup's maxTaskRosterEntries) as well
// as the text inside each entry (maxTaskFieldID / maxTaskRosterDescription) — so
// an oversized payload never enters the event stream, a queue, or a log. The
// count bound is what a per-entry text cap alone cannot supply: the array's
// length is claude's to choose. Like every variant here it carries no
// conversation identity — the bridge injects that.
type BackgroundTaskRoster struct {
	// Tasks is the roster in claude's own order, truncated FROM THE TAIL when it
	// exceeds the producer's entry cap — no ranking is invented, because claude's
	// ordering semantics are unobserved. nil for an empty roster and nil when
	// claude omits the key, never an empty non-nil slice.
	//
	// An empty roster is MEANINGFUL and is still emitted: it says nothing is
	// alive, which is exactly the signal a consumer of #1240's symptom needs.
	Tasks []BackgroundTask
	// DroppedTasks is how many entries claude sent beyond the producer's cap that
	// this event does NOT carry; 0 when nothing was dropped. The roster's true
	// size is len(Tasks) + DroppedTasks.
	//
	// The count dimension reports HERE rather than in a top-level TruncatedFields
	// naming "tasks", and that is why this variant has no top-level
	// TruncatedFields at all: a name-only report loses how many were lost, and
	// the count is the strictly more informative signal. Each dimension reports
	// at the level where it happens — a text cut is a property of one entry and
	// rides that entry.
	DroppedTasks int
}

// ThinkingProgress reports that claude is actively reasoning, and roughly how
// much. It maps claude's system/thinking_tokens line (#1385), the fourth system
// subtype the parser translates rather than drops.
//
// It exists because this is claude's ONLY mid-turn proof of life on the
// stream-json surface: during a long assistant turn nothing else crosses stdout,
// so without it a client showing "thinking" cannot separate a slow answer from a
// wedged one.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives — but here it also disambiguates, and that is worth stating because
// the colliding name sits a few lines above in this same file. ThoughtChunk
// carries the CONTENT of claude's reasoning; this variant carries NONE — only
// that reasoning is happening and an estimate of its size. A consumer that
// renders this as text has nothing to render.
//
// RATE. The producer emits at most one of these per
// streamsup.minThinkingTokensPerEvent tokens of accumulated delta, so the event
// stream carries strictly fewer of them than claude emits lines (33 lines → 8
// events on the committed capture). Two consequences a consumer must not get
// wrong: the events do NOT enumerate claude's lines, and — the important one —
// the ABSENCE of an event within any particular window does NOT mean thinking
// stopped. It may only mean the accumulated delta has not yet crossed the bound.
// Do not build a "thinking stalled" inference on the gap between two of these.
//
// STALL DETECTION is untouched by this variant, in both directions, and the note
// is here so a future wiring slice does not re-open the question. turnevent.Stall
// has exactly one producer — internal/turnbridge/mapper.go, from
// tuidriver.EventKindStallDetected — on the PTY surface, which never sees this
// parser. streamsup.Watchdog consumes its own copy of raw stdout via
// io.MultiWriter and never reads parser events; it already counts every complete
// line as activity, thinking_tokens included. So this event neither masks nor
// triggers a stall, and no suppression-avoidance mechanism is needed or wanted.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380): session_id, which is claude's session identity
// and NOT the daemon's conversation identity, and uuid, claude's per-line message
// id, which nothing in the daemon reads.
//
// It opens and closes no turn. Unlike every sibling above it carries no
// claude-authored TEXT at all — both fields are claude's own integers — so it
// needs no producer-side byte caps and has no TruncatedFields: nothing is ever
// cut, and a permanently-nil field would claim a bound that does not exist. Like
// every variant here it carries no conversation identity — the bridge injects
// that.
type ThinkingProgress struct {
	// EstimatedTokens is claude's estimate of the tokens it has spent thinking, as
	// of the emitting line.
	//
	// It is cumulative within ONE INFERENCE REQUEST, not within a turn, and it is
	// NOT monotonic across a turn: it restarts near zero at every inference-request
	// boundary. That is measured, not speculative — the committed capture's single
	// turn contains four such restarts (running 5→184, 4→167, 3→126, 1→197). Treat
	// it as a progress reading, never as a turn total, and never diff two of them
	// expecting a non-negative result.
	EstimatedTokens int
	// EstimatedTokensDelta is claude's per-line increment, exactly as it appears on
	// the line that produced this event.
	//
	// The deltas a consumer RECEIVES do not sum to the turn's total, because the
	// rate bound drops most of the lines: on the committed capture the turn's 674
	// tokens of delta arrive as 243 across 8 events. It is a rate reading, not an
	// accumulator input. Summing it undercounts by whatever the dropped lines
	// carried, and no field here reports that residue.
	EstimatedTokensDelta int
}

// RateLimited reports that claude's usage-limit window is in a state other than
// the one measured-benign one. It maps claude's top-level rate_limit_event line
// (#1404) — the fifth claude line-type the parser translates rather than drops,
// and the first that is not a `system` subtype.
//
// It exists so a turn that stops making progress because of a usage limit is
// representable inside the daemon at all. Until #1404 the line was dropped whole
// and the information existed nowhere in pyrycode.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives — and, as with BackgroundTaskRoster, the translation earns more here
// than insulation from a rename. claude emits rate_limit_event ONCE PER RUN
// whatever the window's state (status read "allowed" in all three captures on
// record, i.e. every run that produced one hit no limit at all), so a variant
// called RateLimitEvent or RateLimitStatus would read as a periodic report and
// invite a consumer to draw one row per healthy turn. This variant fires only
// when the producer's gate says a limit is in force; the name states that
// condition, and it sits with the family's other condition-named variants
// (Stall, Compacting, ApiRetry).
//
// It is a REPORT, never a control input. Nothing in the daemon may key a
// behaviour on it: no backoff, no throttle, no retry, no turn suspension, no
// reconnect delay. Every field is claude-authored text or a claude-authored
// integer crossing the subprocess trust boundary, and the only thing downstream
// of it is display (#1405). That is what keeps a wrong — or hostile — status
// value costing at most one misleading row, rather than a resource action the
// daemon takes on itself. A slice that wants the daemon to ACT on a rate limit is
// re-opening that trust analysis, not extending this one.
//
// It opens and closes no turn, exactly as the background-task variants do not: a
// usage-limit window is orthogonal to whichever turn happened to observe it.
//
// The same two keys the captured line carries are deliberately NOT fields here,
// for the same reasons (#1380): session_id, which is claude's session identity
// and NOT the daemon's conversation identity, and uuid, claude's per-line message
// id, which nothing in the daemon reads. So are the payload's four overage keys —
// overageStatus and isUsingOverage are org-policy detail nothing in the daemon or
// in the user story reads, and overageResetsAt / overageDisabledReason are
// MEASURED version-variable (claude 2.1.158 carries the first and not the second;
// 2.1.199 and 2.1.220 the reverse). Declaring a version-variable key would be
// exactly the field-structure invention this family's decode targets each refuse.
//
// Both string fields are claude-derived and bounded by the producer AT
// CONSTRUCTION (streamsup's maxRateLimitField), following Unrecognized's
// precedent, so an oversized payload never enters the event stream, a queue, or a
// log. Like every variant here it carries no conversation identity — the bridge
// injects that.
type RateLimited struct {
	// Status is claude's own rate_limit_info.status, verbatim.
	//
	// It is on the event because it is the only field that says WHY the event
	// fired, and its value set beyond the benign one is UNMEASURED: no capture of
	// a limit actually in force exists. Carrying claude's raw string is how that
	// set gets measured the first time a real limit fires, instead of the daemon
	// inventing an enum it has no evidence for — so a plain string rather than a
	// closed enum, for BackgroundTaskStarted.TaskType's reason taken one step
	// further. Never empty: the producer's gate does not emit on an empty status.
	Status string
	// LimitType is WHICH limit is in force: claude's rateLimitType ("five_hour" in
	// all three captures). The name is translated because RateLimitType inside a
	// type called RateLimited stutters; the daemon's snake_case name for
	// TruncatedFields purposes is "limit_type". A plain string, not a closed enum,
	// for Status's reason.
	LimitType string
	// ResetsAt is when claude says the limit lifts, as UNIX SECONDS. 0 when claude
	// did not report it — and the event still fires, because absence of the detail
	// is claude's to choose and the status is the report.
	//
	// It is CLAUDE's number, not the daemon's clock, and it is unvalidated in BOTH
	// directions: a consumer must not assume it lies in the future, and must not
	// assume it lies in a sane range at all. Negative, zero and year-40000 values
	// are all representable and none is rejected here, because rejecting one would
	// be a validation rule with no captured negative case behind it. Formatting it
	// as a date without a range check is the realistic bug.
	//
	// Not a time.Time: converting would invent a claim the bytes do not make (that
	// the number is a valid instant), create a second absent-value question
	// (time.Time{} versus 0), and drag in the project's time.Time round-trip
	// discipline for a field that is only ever a number on a wire. int64 rather
	// than int because a unix timestamp is a 64-bit quantity by nature.
	ResetsAt int64
	// TruncatedFields names the fields the producer cut to fit its cap, in
	// declaration order, using the DAEMON's snake_case names: "status",
	// "limit_type" (not claude's rateLimitType — the report names the field it
	// describes). nil when nothing was cut, never an empty non-nil slice, so a
	// consumer can emit it as absent rather than [].
	//
	// Unlike ThinkingProgress this variant DOES carry one: two of its three
	// payload fields are claude-authored strings that can be cut, so the report
	// describes a bound that exists. ResetsAt is absent from it and needs no cap —
	// an int64 cannot grow.
	TruncatedFields []string
}

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
// The captured init line carries 22 keys and this variant carries the substance of
// ONE. Two of the omissions are why that matters: cwd is the operator's local
// filesystem path, and session_id is claude's session identity, NOT the daemon's
// conversation identity (#1380). Neither is even declared on the producer's decode
// target (streamsup's systemInitLine) — absent from the DECODE TARGET is a
// stronger guarantee than a reflection sweep, because a field that is never
// declared cannot leak.
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

// ModelOption is one entry of a ModelList: the list's element type, NOT an
// Event, so it carries no marker — BackgroundTask's shape, for BackgroundTask's
// reason. Its five claude-authored fields — three strings, one string LIST and one
// bool — are exactly what protocol.ModelOption carries (#1704), in that type's
// declaration order, with nothing missing and nothing invented.
//
// Four per-entry keys the captured reply also carries are deliberately absent, and
// the ABSENCE is the guarantee: description — the longest string in the capture at
// 66 bytes, and the most tempting to carry — supportsFastMode, which only one of
// the six entries has, and supportsEffort and supportsAdaptiveThinking, which the
// four richer entries carry beside the keys this type does decode. None has a named
// consumer and protocol.ModelOption carries none of them at all, so decoding any
// would be untrusted prose bounded, retained and carried for nothing. That is the
// argument streamsup's systemInitLine makes about its own twenty-one omissions,
// and a field never declared on the producer's decode target cannot leak whatever
// a later sweep forgets to check. Of the two capability keys the richer entries do
// carry, BOTH are decoded here — supportsAutoMode by #1819 and supportedEffortLevels
// by #1827 — so neither is an omission. supportsEffort stays out with the other
// three: EffortLevels subsumes it completely, now that a zero-length effort menu has
// ONE reading (#1828). A nil EffortLevels says exactly what supportsEffort: false or
// an absent supportsEffort would say, so the key would add a second spelling of an
// answer this type already carries. The capture's six entries show the two co-varying
// perfectly — supportsEffort: true with five levels, both absent together — which is
// evidence for the subsumption rather than a guarantee of it; claude could publish
// them apart tomorrow and the omission would still hold, because nothing consumes
// supportsEffort.
type ModelOption struct {
	// ResolvedModel is what Value resolves to RIGHT NOW: the concrete identifier,
	// and the field a consumer wanting a dated one wants. VERBATIM, per
	// ModelAnnounced.Model's rule — no lowercasing, no alias expansion, no
	// date-stamping, no family mapping, and no lookup against any published model
	// list.
	//
	// It need not be dated and need not appear in any published list: the capture's
	// six entries resolve to claude-sonnet-5, claude-opus-5, claude-fable-5 and
	// claude-haiku-4-5-20251001, only the last of which carries a date.
	ResolvedModel string
	// Value is the argument you PASS to select this model, and it is NOT a dated
	// identifier: an alias (sonnet), a bracketed variant (claude-fable-5[1m]), or
	// default. A consumer cannot derive a family by splitting it on "-", and cannot
	// assume it round-trips — see protocol.ModelOption.Value for the inbound gap and
	// for why internal/relay's validModel is not to be widened to close it.
	Value string
	// DisplayName is claude's human LABEL for the entry ("Default (recommended)",
	// "Haiku 4.5"). PROSE, not an identifier: safe to RENDER as inert text, never a
	// key to match on. The daemon bounds it and does not sanitize it — no
	// control-character or terminal-escape stripping happens on this path — so it
	// stays untrusted, model-influenced text and the render boundary owing the
	// sanitization is the CLIENT's, exactly as protocol.ModelOption's SECURITY
	// paragraph states for the same three strings.
	DisplayName string
	// EffortLevels are the reasoning-effort levels claude published for THIS model,
	// so a client's effort control can offer exactly the levels claude accepts.
	// VERBATIM, per ModelAnnounced.Model's rule: claude's own strings in claude's own
	// order, with no lowercasing, no canonicalisation into any effort vocabulary of
	// the daemon's own, and no reordering — the capture's order (low, medium, high,
	// xhigh, max) is neither alphabetical nor sorted, and preserving it is what
	// carries claude's answer rather than the daemon's opinion of it.
	//
	// BOTH THE ELEMENT AND THE COUNT ARE BOUNDED. The producer caps every level string
	// AT CONSTRUCTION (streamsup's maxModelEffortLevel) and how MANY levels this list
	// retains (streamsup's maxModelEffortLevelCount), so neither an oversized level nor
	// an inflated menu enters the event stream, a queue, or a log. The count bound is
	// what a per-element cap alone cannot supply: the array's length is claude's to
	// choose, so without it a per-entry size stayed a function of a number claude
	// picks. See ModelList.Models, which states all three of the list's dimensions
	// together.
	//
	// AN ABSENT KEY, A JSON null AND A PUBLISHED EMPTY ARRAY ARE ONE READING, AND IT
	// IS SPELLED nil (#1828). The producer normalises a zero-length list at
	// construction — streamsup's emitModelList, in its boundEach closure's
	// zero-length arm — so nothing downstream has to ask which of the three it is
	// holding. That was a real fork rather than a shape Go forced: a nil slice and an
	// empty non-nil one are distinguishable at no cost, and encoding/json lands an
	// absent key and a null on the first and a [] on the second. Keeping them apart
	// was available and was deliberately not taken.
	//
	// THE ARGUMENT IS ABOUT AN EMPTY MENU, AND IT IS NOT SupportsAutoMode'S. That
	// field collapses because both of its readings drive the same client CONTROL and
	// the safe direction is asymmetric — the unsafe inverse being to grant on silence.
	// This one collapses because both readings leave the daemon holding the same EMPTY
	// HAND. The only reading under which absent and [] could differ is "absent means
	// UNKNOWN, [] means AFFIRMATIVELY NONE", and UNKNOWN is actionable only if there is
	// a fallback menu to offer instead. There is none: the daemon's one effort
	// vocabulary is internal/relay's validEffort, which the paragraph below forbids
	// applying to this list in either direction, for two separate reasons. So the two
	// readings issue the identical instruction to every consumer that can exist — THIS
	// IS NOT A MENU YOU MAY OFFER — not because they mean the same thing in the
	// abstract, but because the daemon holds no vocabulary in which they could differ
	// and has twice decided it never will.
	//
	// Three facts close it. First, claude's observed absence is not "declined to answer
	// this one question": in the committed capture the two entries omitting
	// supportedEffortLevels omit the ENTIRE capability block with it — no
	// supportsAutoMode, no supportsEffort, no supportsAdaptiveThinking — while the four
	// answering any capability question answer all of them and publish the full five
	// levels. The shape a genuine per-question refusal would take is exactly the shape
	// claude does not send. Second, a kept distinction is one NO CLIENT COULD EVER
	// OBSERVE, however long the daemon held it. It is not short-lived: cmd/pyry's
	// sessionModelHold keeps this value for the session's life (#1840), and
	// turnbridge.MapEvent's ModelList arm crosses this field as the slice it is, nil
	// left nil (#1848), so the distinction even survives the mapping. It dies at the
	// WIRE, because every path to a client ends at protocol.ModelOption.MarshalJSON,
	// which normalises nil to [] and states at its own type that an empty effort list
	// on the wire is a COLLAPSE rather than a positive statement (#1704). The two
	// sides having landed on the same collapse independently is why #1848's mapping
	// needed no fork to bridge them. Third, a kept distinction is one this house's own
	// comparison idiom cannot see: slices.Equal(nil, []string{}) reports TRUE where
	// reflect.DeepEqual reports false, so the next assertion written against this field
	// with the idiom every existing one uses would drop the distinction silently, with
	// nothing going red. That is a trap, and it is a fact about SLICES rather than
	// anything inherited from the bool.
	//
	// nil rather than []string{} because nil is this struct's own spelling for a
	// zero-length list — TruncatedFields below names the convention and its single
	// source. The other direction would leave two list fields of one struct disagreeing
	// about how "nothing" is spelled, and would allocate on a shape a third of the
	// capture's entries have.
	//
	// IT NORMALISES HOW GO SPELLS ZERO, AND NOTHING ELSE. The verbatim rule above
	// governs the ELEMENTS and their ORDER, and a list with no elements has exactly the
	// elements claude sent, in exactly claude's order. Said rather than left to be
	// noticed, because this field's doc opens by calling it VERBATIM and the collapse
	// would otherwise read as the first exception to that.
	//
	// WHAT IT GIVES UP is real and is accepted rather than waved away: the decode now
	// discards the evidence that claude sent [] rather than nothing at all. Should
	// claude one day send [] deliberately AND mean by it something that omitting the key
	// does not, this reading is wrong, and reopening it costs a ticket plus a *[]string
	// or a companion bool. That is the price of one reading, paid knowingly.
	//
	// A CUT LEVEL IS NOT A LEVEL CLAUDE PUBLISHED, AND A LEVEL CLAUDE PUBLISHED MAY BE
	// MISSING ENTIRELY. When TruncatedFields names "effort_levels", at least one
	// element is the daemon's prefix of a string claude sent, or the count bound
	// shortened the list from the tail, or both. Either way this list is no longer
	// claude's menu and must not be offered as one. The instruction is the same for
	// both, which is why one name covers them: a list that is not claude's whole
	// published menu is unofferable whether one level was mangled or ninety were
	// dropped.
	//
	// WHAT THAT GIVES UP, stated rather than waved away: the TRUE level count is not
	// recoverable from this event, where ModelList's true entry count is recoverable as
	// len(Models) + DroppedModels. A per-entry dropped-level integer is what would
	// recover it, and protocol.ModelOption has no field to carry one, so it would be
	// preserved only long enough for the mapping (#1848) to discard it — the argument
	// SupportsAutoMode makes against a *bool, one field over. Reopening it costs a
	// ticket plus a wire field. The daemon's own operational signal for the bound is
	// streamsup's control_response record, not this event.
	//
	// internal/relay's validEffort is NOT applied to this list, and is not to be
	// widened or narrowed to match it. It is a CLOSED enum bounding a phone-supplied
	// override on an INBOUND path: running claude's outbound list through it would
	// silently drop a level claude adds next, making the daemon's menu a lie, and
	// widening it to whatever claude published would let the subprocess extend what an
	// untrusted inbound frame may set. The two rules look interchangeable and are
	// deliberately not, which is why the separation is stated rather than left to be
	// noticed.
	//
	// BOUNDED AND UTF-8-VALID IS ALL THEY ARE, exactly as DisplayName is: these are
	// claude-authored strings that crossed the subprocess trust boundary, and nothing
	// on this path strips control characters or terminal escape sequences, so they
	// stay untrusted, model-influenced text and the render boundary owing the
	// sanitization is the CLIENT's — which is what protocol.ModelOption's SECURITY
	// paragraph already states for these same strings.
	EffortLevels []string
	// SupportsAutoMode is claude's own answer to whether it accepts AUTO permission
	// mode for this model, so a client's permission-mode menu can grey the option out
	// where claude refuses it. VERBATIM, per ModelAnnounced.Model's rule: the key's
	// value as claude sent it, with no daemon policy folded in and no inference from
	// any other field of the entry.
	//
	// AN ABSENT KEY, A JSON null AND AN EXPLICIT false ARE ONE READING — false. The
	// collapse is deliberate rather than fallen into: in Go the choice IS the field's
	// shape, and a *bool is the answer that keeps them apart. What decides it is that
	// the safe direction here is asymmetric and points at false. This field describes
	// a permission GRANT, so "claude refused auto for this model" and "claude said
	// nothing about auto for this model" drive the SAME client behaviour — grey the
	// option out — and no decision hangs between them. The unsafe collapse is the
	// inverse, granting on silence, which a claude that merely stopped sending the
	// key would walk into; nothing here does that.
	//
	// Three facts support it. protocol.ModelOption.SupportsAutoMode is already a
	// plain bool whose doc calls absent-decodes-to-false "the correct reading"
	// (#1704), so a pointer here would preserve a distinction only long enough for
	// the mapping (#1848) to discard it. claude has never sent false at all — in the
	// committed capture four entries carry true and two carry no capability key
	// whatsoever, identically in all three of #1763's arms — so a pointer would
	// defend a shape observed nowhere. And this file declares no pointer field of
	// this kind today; a field with no consumer asking for the third state is not
	// where the event vocabulary grows its first one.
	//
	// Never named in TruncatedFields, because a bool is never cut: it has no length,
	// truncateField never sees it, and it carries none of claude's bytes into any
	// per-entry budget.
	//
	// It is a REPORT to a client's menu and NEVER an authorization input. Nothing in
	// the daemon may branch on it to decide what it may SEND claude — claude decides
	// that when asked — because a daemon reading its own subprocess's claim as
	// permission is a subprocess authorizing itself.
	//
	// EffortLevels faced the same absent-versus-empty question, RE-DERIVED its answer
	// rather than inheriting this one, and landed on a collapse too — BY A DIFFERENT
	// ARGUMENT, which is why the coincidence must not be read as this paragraph having
	// set a precedent. The bool collapses because the safe direction is ASYMMETRIC:
	// silence and refusal drive the same control, and the unsafe inverse is granting on
	// silence. The list collapses because both of its readings leave the daemon holding
	// the same EMPTY HAND, there being no effort vocabulary of its own to fall back on
	// and a standing decision never to author one. A withheld grant is settled by asking
	// which direction is safe; an empty menu is settled by asking what there is to
	// offer. See EffortLevels for that argument in full (#1828).
	SupportsAutoMode bool
	// TruncatedFields names THIS entry's fields the producer cut to fit their caps,
	// in declaration order, using the DAEMON's snake_case names: "resolved_model",
	// "value", "display_name", "effort_levels". No name is translated — claude's
	// camelCase keys and these fields agree on which field they mean. nil when
	// nothing was cut, never an empty non-nil slice; BackgroundTask.TruncatedFields
	// is the convention's single source.
	//
	// "effort_levels" is the one name reporting on a LIST rather than a value, and it
	// covers three outcomes: one or more of that entry's levels were cut to fit the
	// per-element cap, the list was shortened to fit the count cap, or both. It appears
	// at most once per entry in every case, because this report names FIELDS and a list
	// is one field. It is last for the declaration-order reason and no other — the
	// producer's list bound runs after the three strings'.
	TruncatedFields []string
}

// ModelList is claude's inventory of selectable models: the models array of the
// initialize reply (#1811), which the daemon solicits once per child with
// streamsup's WriteInitialize and claude answers on a control_response line.
//
// It exists because it is the only thing that answers WHAT CAN BE RUN, and it
// answers it BEFORE the first turn — where ModelAnnounced reports what one turn
// got, this reports the whole set a client may choose from, with each alias's
// current resolution alongside it.
//
// An Event rather than parser-held session state, and the deciding fact is
// protocol.ModelListPayload's own doc: its ConversationID is supplied at MAPPING
// time, and mapping time is turnbridge.MapEvent, whose input is an Event. Session
// state would have obliged the publishing slice to build a second parser→relay path
// beside the one every other interactive payload already uses. It did not: #1849
// publishes through cmd/pyry's interactiveTurnEmitterV2.Handle, the same emitter
// every other interactive payload goes through, so the prediction held.
//
// IT IS PUBLISHED ON ONE DELIVERY PATH OF TWO, and the two have different answers,
// so a claim about where this value goes has to say which one it means.
//
// THE LIVE LANE, SHIPPED. turnbridge.MapEvent's ModelList arm maps this variant
// onto protocol.ModelListPayload, DroppedModels included (#1848), and cmd/pyry's
// interactiveTurnEmitterV2.Handle emits the mapped frame on the interactive turn
// lane (#1849); #1845 proves it reaches a connected client end to end. It is
// BEST-EFFORT rather than guaranteed: cmd/pyry's turnMarkFor answers turnMarkNone,
// so the fan-in classes the event droppable and can refuse it at droppableCap under
// load — so a client on that lane receives it as a property of the PATH, not a
// guarantee about any one exchange. Two holders retain it. cmd/pyry's
// sessionModelHold keeps this decoded value for the session's life, sitting ABOVE
// that droppable send (#1840); and #1849's emit appends the MAPPED payload to the
// eventring, retaining it per conversation as the replay source for a phone that
// reconnects.
//
// CONNECT-TIME DELIVERY, NOT SHIPPED. A client that connects AFTER the initialize
// exchange receives this today by no path at all. The eventring does not close
// that: replay is a RECONNECT mechanism driven by a last_event_id the client must
// already hold, and a client never connected for the exchange has none to
// advertise. #1863 landed the relay-side connect seam — internal/relay's
// V2SessionConfig.RetainedModelLists, drained by reconcileModelLists — but nothing
// in the tree fills it, and #1867 is the outstanding slice that will.
//
// A client can therefore read this producer's output, which is why the producer
// takes the false NEGATIVE on every ambiguous line rather than emitting a list it
// did not observe: a missing menu beats a wrong one, and that choice matters more
// now that either outcome is visible than it did when neither was.
//
// It opens and closes no turn, exactly as the background-task variants do not, and
// it is not even per-turn: one initialize exchange per child produces one of
// these. cmd/pyry's turnMarkFor answers it correctly by construction — its opener
// set is a whitelist and its default is turnMarkNone.
//
// claude's session_id is deliberately not a field, for BackgroundTaskStarted's
// reason: claude's session identity is NOT the daemon's conversation identity, and
// like every variant here this one carries no conversation identity at all — the
// bridge injects that.
type ModelList struct {
	// Models is the inventory in claude's own order, unchanged: no ranking is
	// invented, its ordering semantics being unobserved. Never empty — the
	// producer's gate does not emit on an empty array, because a ModelList naming no
	// model cannot serve the purpose this variant exists for.
	//
	// Each entry's three strings are bounded by the producer AT CONSTRUCTION
	// (streamsup's maxModelResolved / maxModelValue / maxModelDisplayName), so an
	// oversized payload never enters the event stream, a queue, or a log. So is the
	// entry COUNT (streamsup's maxModelListEntries), which is what a per-entry text
	// cap alone cannot supply: the array's length is claude's to choose, and a
	// per-entry cap alone would leave the total a function of that number. The list is
	// truncated FROM THE TAIL when the count cap fires, and the true size stays
	// recoverable as len(Models) + DroppedModels.
	//
	// THERE ARE THREE DIMENSIONS AND ALL THREE ARE BOUNDED. The per-entry TEXT by the
	// three string caps above and by streamsup's maxModelEffortLevel on each level,
	// reported per entry in ModelOption.TruncatedFields; the ENTRY count by streamsup's
	// maxModelListEntries, reported here as DroppedModels; and the per-entry LEVEL
	// count by streamsup's maxModelEffortLevelCount, reported on the entry it happened
	// to, as "effort_levels" in that entry's TruncatedFields. Each dimension reports at
	// the level where it happens, which is why the level count reports per entry and
	// the entry count reports on the list. The bool beside those strings is bounded by
	// nothing and needs no cap: it carries none of claude's bytes.
	Models []ModelOption
	// DroppedModels is how many entries claude sent beyond the producer's cap that
	// this event does NOT carry; 0 when nothing was dropped. The list's true size is
	// len(Models) + DroppedModels.
	//
	// The count dimension reports HERE rather than in a top-level TruncatedFields
	// naming "models", and that is why this variant has no top-level
	// TruncatedFields at all — BackgroundTaskRoster.DroppedTasks' stated reason,
	// unchanged: a name-only report loses how many were lost, and the count is the
	// strictly more informative signal. Each dimension reports at the level where it
	// happens — a text cut is a property of one entry and rides that entry as
	// ModelOption.TruncatedFields.
	//
	// It is the first field on this variant that is DAEMON-derived rather than
	// claude-derived: an int computed from a slice length, carrying none of claude's
	// bytes.
	DroppedModels int
}

// SlashCommand is one entry of a SlashCommandList: the list's element type, NOT
// an Event, so it carries no marker — BackgroundTask's and ModelOption's shape,
// for their reason.
//
// Its five fields are ALL FIVE of protocol.SlashCommand's (#1727), in that
// type's own declaration order: the command's name, its argument hint, its
// description, its aliases, and the report naming which of this entry's fields
// the producer cut. The set is COMPLETE as of #1825 — the vocabulary grew a
// field at a time across #1877 / #1904 / #1957 / #1825, exactly as ModelOption
// grew across #1819 / #1827 / #1828, and there is no further field promised.
//
// FIXING THE ORDER BEFORE THE SECOND FIELD EXISTS is the whole point of choosing
// it now, and Description (#1904) is the first evidence that the promise was
// kept: it landed BETWEEN Name and TruncatedFields — its mirrored position,
// leaving the gap ArgumentHint fills later — rather than being appended to the
// end where a field added without this rule would have gone. ArgumentHint
// (#1957) is the second, and it is the case the promise was actually WRITTEN
// for: Description had one declared field to land after, so no reordering could
// have got it wrong, where this field had to be INSERTED BETWEEN two that
// already existed and appending it would have compiled just as well. Aliases
// (#1825) is the third and last, and it pays the promise off at the END of the
// order rather than by insertion: it is the only one of the three whose
// mirrored slot sits after every field declared before it, so appending it was
// CORRECT here where appending ArgumentHint would have been wrong — the rule
// earns its keep by making that a checked fact rather than a coincidence.
// The mapping onto the wire type is now a field-for-field copy across the whole
// struct rather than a reordering a reader has to check.
type SlashCommand struct {
	// Name is claude's command name, VERBATIM, per ModelAnnounced.Model's rule:
	// no lowercasing, no canonicalisation, no prefix stripping, and no leading
	// "/" added or removed.
	//
	// IT IS NOT AN IDENTIFIER. One name in the committed capture is
	// __remote-workflow, so no charset assumption belongs in this struct or in a
	// consumer — protocol.SlashCommand's measured fact, carried to the daemon side
	// because this is the type a daemon-side consumer reads.
	//
	// Bounded by the producer AT CONSTRUCTION and never sanitized: see
	// SlashCommandList's SECURITY paragraph, which owns that statement for every
	// string on this type rather than having it diluted into a restatement here.
	Name string
	// ArgumentHint is claude's synopsis of what the command takes AFTER its
	// name, VERBATIM, per ModelAnnounced.Model's rule: no lowercasing, no
	// trimming, no charset filtering.
	//
	// AN EMPTY HINT IS THE ORDINARY CASE, NOT MISSING DATA, and a consumer must
	// not read one as absent or as a defect: 33 of the committed capture's 51
	// entries carry "" and NONE omits the key, so a command taking no argument
	// is the majority row. That measurement is also why
	// protocol.SlashCommand's wire tag carries no omitempty — eliding an empty
	// hint would make the common row indistinguishable from a malformed one —
	// and this doc does not re-derive it.
	//
	// IT IS A SYNOPSIS, NOT AN INVOCABLE TOKEN, which is what separates it from
	// Name rather than making it a second copy of Name's warning. A client is
	// meant to send a Name back as ordinary message text, sending the slash
	// command being the feature; this string is never sent back at all. It is
	// no more an identifier than Name is, and less: 13 of the capture's 18
	// non-empty hints carry `[` and `]` and ten carry `<` and `>`, so a
	// consumer rendering one is handling syntax-shaped text and not a slug.
	//
	// Bounded by the producer AT CONSTRUCTION under its OWN cap (streamsup's
	// maxSlashCommandArgumentHint, not Name's or Description's) and never
	// sanitized: see SlashCommandList's SECURITY paragraph, which owns that
	// statement for every string on this type.
	ArgumentHint string
	// Description is claude's own description of the command, VERBATIM, per
	// ModelAnnounced.Model's rule: no lowercasing, no trimming, no charset
	// filtering — and NO NEWLINE STRIPPING, which is named rather than left under
	// "verbatim" because this is the one string on this type a captured value
	// actually carries newlines in. protocol.SlashCommand's doc carries that
	// measurement and its narrowness, and this doc does not re-derive either.
	//
	// IT IS PROSE, NOT A LABEL, which is what separates it from Name rather than
	// making it a second copy of Name's warning: 14 of the capture's 51
	// descriptions carry non-ASCII where no name does, so a consumer rendering it
	// as a single-line row is handling multi-line, non-ASCII text and not an
	// identifier. It is also the field a truncation is least recoverable from —
	// nothing else on this lane carries a second copy of it — which is the trade
	// streamsup's maxSlashCommandDescription argues.
	//
	// Bounded by the producer AT CONSTRUCTION under its OWN cap
	// (streamsup's maxSlashCommandDescription, not Name's) and never sanitized:
	// see SlashCommandList's SECURITY paragraph, which owns that statement for
	// every string on this type.
	Description string
	// Aliases are claude's alternative names for the command, VERBATIM and in
	// claude's own order, per ModelAnnounced.Model's rule: no lowercasing, no
	// trimming, no charset filtering, no leading "/" added or removed, no
	// deduplication and no sorting.
	//
	// AN ALIAS IS NOT AN ENTRY. #1600's verbatim rule forbids expanding one into a
	// synthetic command of its own: the committed capture's `clear` carries `reset`
	// and `new`, and a producer that turned those into two more rows would be
	// inventing commands claude never published. They belong to the entry that
	// declares them and are matched against it.
	//
	// THEY ARE WHAT MAKES A CONSUMER'S MATCH CORRECT, which is the whole reason the
	// field exists rather than a nicety: the desktop Actions menu's own reset entry
	// is an alias of clear and not a command name, so a consumer matching its menu
	// against Name alone finds nothing for it and greys out a command that works.
	//
	// AN ABSENT KEY, A JSON null AND A PUBLISHED EMPTY ARRAY ARE ONE READING, spelled
	// nil, and the collapse is deliberate rather than an artefact of how Go decodes.
	// It is DECIDED HERE because protocol.SlashCommand.MarshalJSON asks this type to
	// decide it (#1825). The measurement behind it is that the distinction has never
	// been observed: ZERO of the committed capture's 51 entries carry an empty array,
	// 42 omit the key and 9 carry a non-empty one, so what a kept distinction would
	// separate is one observed shape from one claude has never sent. It also could
	// not survive the trip — protocol.SlashCommand.MarshalJSON publishes [] for both,
	// deliberately, so a client never has to branch on absent-versus-empty to match
	// an alias — and a daemon-internal difference erased one hop downstream is a
	// difference no consumer can act on.
	//
	// It is ModelOption.EffortLevels' collapse WEIGHED rather than inherited, with
	// the frequencies INVERTED: absence is the single exception there and the
	// majority here. What the frequency changes is how often the collapse fires, not
	// what either shape MEANS — an entry with no aliases and an entry with the key
	// absent issue a consumer the identical instruction, that there is no alternative
	// spelling to match against, and no behaviour branches on which claude meant.
	//
	// nil rather than []string{} for TruncatedFields' reason: nil is this struct's
	// own spelling for an empty list, and two list fields disagreeing on how to spell
	// empty is worse than picking a direction once. The trap that makes the choice
	// worth stating: slices.Equal(nil, []string{}) reports TRUE, so a design keeping
	// the distinction would carry a difference invisible to the comparison idiom
	// every assertion on such a field uses.
	//
	// Bounded by the producer AT CONSTRUCTION in TWO dimensions under TWO caps of
	// their own — streamsup's maxSlashCommandAlias on each string and
	// maxSlashCommandAliasCount on how many this slice retains — and never sanitized:
	// see SlashCommandList's SECURITY paragraph, which owns that statement for every
	// string on this type. It is the only field here a cut can SHORTEN THE LIST of
	// rather than only shorten a value of, and a cut on either dimension reports
	// once under "aliases".
	Aliases []string
	// TruncatedFields names THIS entry's fields the producer cut to fit their
	// caps, in declaration order, using the DAEMON's snake_case names. They agree
	// with protocol.SlashCommand.TruncatedFields' wire names, so a later mapping
	// is a copy rather than a translation. nil when nothing was cut, never an
	// empty non-nil slice; BackgroundTask.TruncatedFields is the convention's
	// single source.
	//
	// IT CAN CARRY "name", "argument_hint", "description" AND "aliases", IN THAT
	// ORDER, AND THE ENUMERATION GREW WITH THE FIELD SET UNTIL IT WAS COMPLETE.
	// #1904 was the first slice to extend it, #1957 the second and #1825 the last,
	// exactly as ModelOption.TruncatedFields grew to include "effort_levels" in
	// #1827 — and it is what turned the declaration ORDER above into a claim a test
	// can see, an enumeration of one having nothing to order.
	//
	// "argument_hint" is the only name here that is NOT byte-identical to claude's
	// own key for the field, which is argumentHint. These are the daemon's names, as
	// the paragraph above says; the other three coincide with claude's key and with
	// protocol.SlashCommand's wire name, and until that field landed the distinction
	// had no difference to see.
	//
	// "aliases" IS THE ONE NAME HERE THAT CAN MEAN TWO DIFFERENT CUTS — a string in
	// the list shortened, or the list itself shortened — and it says the same thing
	// either way and at most once per entry. A consumer reading it learns that the
	// alias set it holds is incomplete, which is the actionable fact for both; WHICH
	// dimension fired is not recoverable from this slice, exactly as
	// ModelOption.TruncatedFields' "effort_levels" does not distinguish its own two.
	//
	// A NAME FOR A FIELD THIS TYPE DOES NOT DECLARE MUST NEVER APPEAR. A producer
	// that cut a value this type does not carry has nothing to report here, because
	// the value is not on the type. That is the one way a partial field set could
	// produce a lie: a report telling a consumer that text it holds is incomplete,
	// when the type never held that text at all. The rule stands with an EMPTY
	// EXTENSION since #1825 — the type now declares every per-entry key claude sends,
	// so there is no field left for a producer to report and not carry — and it is
	// kept rather than retired because what it forbids is a producer inventing a
	// name, which no field count makes impossible.
	TruncatedFields []string
}

// SlashCommandList is claude's inventory of slash commands for this session in
// this working directory: the commands array of the initialize reply (#1854),
// the same exchange ModelList carries the models array of, which the daemon
// solicits once per child with streamsup's WriteInitialize and claude answers on
// a control_response line.
//
// It exists because it is the only thing that answers WHAT CAN BE INVOKED, and
// it answers it per WORKING DIRECTORY rather than globally: a command defined in
// a repository exists for that repository's sessions and nowhere else.
//
// DECLARED AHEAD OF ITS PRODUCER, which is this family's own sequencing — #1616
// ahead of #1638, #1704 ahead of #1848, #1727 ahead of #1720. The type
// declaration is where the field set and the security posture get decided, and
// deciding those in the same slice that also writes the decode is what makes such
// a slice oversized.
//
// THE PRODUCER HAS SINCE ARRIVED, and the work is split four ways — worth naming
// because the attributions are the easy thing to get wrong here.
// internal/streamsup's commandEntryLine holds the DECODE (#1853, in the tree, and
// COMPLETE since #1825 declared the fourth and last per-entry key); its
// emitSlashCommandList applies the PER-FIELD CAPS, constructs the entries and EMITS
// this list (#1877, in the tree — #1886 moved that construction out of emitModelList
// into an emitter of its own, so more than one call site reaches it); the ENTRY-COUNT
// BOUND, and the drop count that arrives with it, is #1826's and is IN THE TREE —
// maxSlashCommandListEntries, cut in emitModelList above both rungs that read the
// array, reported here as DroppedCommands; and the PUBLISH, which #1720 owned as one
// piece and which was SPLIT — turnbridge.MapEvent's arm is #2001 and is IN THE TREE,
// mapping this variant onto protocol.SlashCommandListPayload, with the FRAME-LEVEL
// byte bound that arm deliberately omitted added by #2002, also in the tree, which is
// why the wire's DroppedCommands is this event's count plus that cut rather than this
// count carried; cmd/pyry's interactiveTurnEmitterV2.Handle case is #2003 and is still
// open.
// #1719 is CLOSED and was the decode, so it names no future producer. What remains of
// the four is the EMISSION alone, and it is the only piece that was ever a WIRE change
// — the mapping is a daemon-internal translation onto a shape already declared.
//
// EVERY DIMENSION IS NOW BOUNDED and there is no unbounded one left to name. The
// per-entry TEXT by streamsup's four field caps, reported per entry in
// SlashCommand.TruncatedFields; the per-entry ALIAS count by
// maxSlashCommandAliasCount, reported on the entry it happened to; and the ENTRY
// count by maxSlashCommandListEntries, reported here as DroppedCommands — which is
// the factor a per-field cap alone cannot supply, exactly as maxModelListEntries
// supplied it for ModelList. What that does NOT give is a bound the WIRE can rely
// on, and that one is no longer missing either: turnbridge's
// maxSlashCommandListBytes (#2002) cuts the mapped list by MEASURED bytes where it
// reaches the wire. DroppedCommands' own doc states how the two cuts compose and
// why the count on this event is not the count on the wire.
//
// IT IS PUBLISHED BY NO PATH TODAY, AND THE REASON IS NOW THE HANDLE CASE ALONE.
// turnbridge.MapEvent grew its arm for this variant in #2001, so MapEvent's default
// no longer drops it — but cmd/pyry's interactiveTurnEmitterV2.Handle still has no
// case, so an event of this variant is logged by kind and discarded before anything
// reaches that arm. The mapping is therefore reachable only by a caller that hands
// MapEvent this variant explicitly, which is what #2003's Handle case and #2005's
// connect-time resolver will each be. ModelList is NOT the example of a variant the
// default drops any more — it grew its own MapEvent arm in #1848 and its own Handle
// case in #1849 — and this variant is now the example of one MAPPED but not yet
// EMITTED, a state the family had not previously had; both precedents have to be
// read off MapEvent and Handle themselves rather than inherited from this family's
// earlier tickets.
//
// It opens and closes no turn, exactly as the background-task variants do not,
// and it is not even per-turn: one initialize exchange per child produces one of
// these. cmd/pyry's turnMarkFor answers it correctly by construction — its opener
// set is a whitelist and its default is turnMarkNone.
//
// THE COUNT IS WORKSPACE- AND VERSION-DEPENDENT and no consumer may assume one;
// protocol.SlashCommandListPayload carries the measurement rather than this doc
// re-deriving it. That variation is why the list is per session and per working
// directory, and why a client must not cache one across working directories.
//
// THE DroppedCommands FIELD ARRIVED WITH ITS BOUND (#1826), one slice after the
// wire type declared its own, and the sequencing was the decision rather than an
// accident of it. protocol.SlashCommandListPayload declared its count AHEAD of any
// counter because a WIRE with nowhere to put a drop discards it silently, and
// adding a key later is a compatibility event. A daemon-internal struct is not a
// compatibility surface: adding a field to it is a local change, so this one waited
// for the ENTRY-COUNT BOUND that produces it — which is how ModelList.DroppedModels
// arrived with streamsup's maxModelListEntries and BackgroundTaskRoster.DroppedTasks
// with maxTaskRosterEntries. The asymmetry a reader who knew the wire type would
// once have read as an oversight is CLOSED, and so is the mapping between the two
// fields: turnbridge.MapEvent's arm carries this count onto
// protocol.SlashCommandListPayload.DroppedCommands verbatim (#2001), never
// recomputed from len(Commands) and never zeroed.
//
// claude's session_id is deliberately not a field, for BackgroundTaskStarted's
// reason: claude's session identity is NOT the daemon's conversation identity,
// and like every variant here this one carries no conversation identity at all —
// the bridge injects that.
//
// SECURITY: every string this type carries is WORKSPACE-AUTHORED and crossed the
// subprocess trust boundary. That STRENGTHENS ModelOption's claude-authored
// warning rather than restating it: a command defined in a repository was written
// by whoever wrote that repository, which is a LOWER-trust origin than claude's
// own strings. They are safe to RENDER as inert text and must never be fed to an
// HTML sink, an attribute, or a URL; the daemon bounds them but does not sanitize
// them — no control-character or terminal-escape stripping happens on this path —
// so they stay untrusted text all the way out, and the render boundary owing the
// sanitization is the CLIENT's.
//
// THE BOUND IS THE PRODUCER'S and is not decided here, so this type declares no
// maximum and no charset check. A second cap would be a second place the limit is
// decided and the two could disagree silently — protocol.SlashCommand's own
// stated reason, holding identically one layer in.
//
// IT IS A REPORT, NEVER A CONTROL INPUT, with protocol.SlashCommand's amendment:
// a client is meant to send a Name BACK, as the text of an ordinary message,
// because sending the slash command IS the feature. Publishing a name does not
// make it trusted. Nothing in the daemon may treat a value from this type as a
// command vocabulary, and no field here may reach a child as an argv element.
type SlashCommandList struct {
	// Commands is the inventory in claude's own order, unchanged: no ranking is
	// invented, its ordering semantics being unobserved. nil for a zero-length
	// list, never an empty non-nil slice; BackgroundTask.TruncatedFields is the
	// convention's single source.
	//
	// WHETHER AN EMPTY LIST IS EMITTED AT ALL IS THE PRODUCER'S GATE and is still
	// not decided here. It has been ANSWERED, one slice later than ModelList.Models
	// answered its own — #1811 declared that type and wrote its producer together,
	// where this one was declared first: streamsup's emitSlashCommandList SUPPRESSES
	// the empty list (#1877), so nothing reaches this field with zero entries. The
	// WIRE's position is what that producer slice READ, and it did not govern:
	// protocol.SlashCommandListPayload.MarshalJSON states that [] is a POSITIVE
	// statement, that claude offered nothing, but the decode collapses an absent
	// `commands`, a null one and a published [] onto one nil slice, so the producer
	// cannot tell that from "claude said nothing about commands" and will not
	// assert it. That position still governs how a list that WAS emitted
	// serialises, which is what #1720 reads it for.
	//
	// The entry COUNT is bounded too since #1826 (streamsup's
	// maxSlashCommandListEntries), which is what a per-entry text cap alone cannot
	// supply: the array's length is claude's — really the WORKSPACE's — to choose,
	// so a per-field cap alone leaves the total a function of a number the daemon
	// does not control. The list is truncated FROM THE TAIL when that cap fires,
	// and the true size stays recoverable as len(Commands) + DroppedCommands.
	Commands []SlashCommand
	// DroppedCommands is how many entries the producer cut beyond its entry cap
	// that this event does NOT carry; 0 when nothing was dropped. The list's true
	// size is len(Commands) + DroppedCommands.
	//
	// The count dimension reports HERE rather than in a top-level TruncatedFields
	// naming "commands", and that is why this variant has no top-level
	// TruncatedFields at all — BackgroundTaskRoster.DroppedTasks' stated reason,
	// unchanged: a name-only report loses how many were lost, and the count is the
	// strictly more informative signal. Each dimension reports at the level where
	// it happens — a text cut and an alias-list cut are both properties of ONE
	// entry and ride that entry as SlashCommand.TruncatedFields.
	//
	// It is the only field on this variant that is DAEMON-derived rather than
	// workspace-derived: an int computed from a slice length, carrying none of the
	// workspace's bytes. That is what makes it the one field here a consumer may
	// trust without the render boundary this type's SECURITY paragraph demands of
	// every other one.
	//
	// WHAT IT DOES NOT BOUND is worth stating where a reader will look for it: the
	// count cap makes the retained size a function of a daemon constant instead of
	// a workspace's, but 128 entries at the 1280-byte per-entry term is 163,840 raw
	// bytes, well past the 65519-byte v2 application-envelope cap, and Go's escaping
	// puts the wire worst case higher still. No count cap can close that gap —
	// maxSlashCommandListEntries carries the arithmetic.
	//
	// THE FRAME-LEVEL BOUND IS turnbridge's maxSlashCommandListBytes (#2002), a
	// MEASURED byte cut applied where this list is mapped onto the wire. It does not
	// change what THIS field counts: the number here is still the producer's cut
	// alone, entries streamsup dropped past its entry cap, because that is the only
	// cut that has happened by the time this event exists. The mapping ADDS its own
	// drops to protocol.SlashCommandListPayload.DroppedCommands rather than
	// replacing this one, so len(Commands) + DroppedCommands is this event's true
	// size here and the WIRE field is the true size after both cuts. A reader taking
	// the two fields for the same number will misattribute a frame cut to the
	// producer.
	DroppedCommands int
}

// Stall is an internal-only onset marker: tui-driver raised a one-shot
// stall_detected signal (no payload, no clearing edge). It carries no fields —
// onset only, no "cleared" state, and (like every variant here) no
// conversation identity; the bridge injects that when mapping to the wire. The
// mobile adapter sends it as the wire "stall" event; the future ACP adapter
// (#600) drops it (no ACP equivalent).
type Stall struct{}

// ApiRetry is a PTY-derived status peer of Stall carrying claude's live
// API-error retry state. Active is the rising (true) / falling (false) edge;
// Current/Total are the parsed `attempt N/M` counter ({0,0} when the counter
// did not parse). Like every variant here it carries no conversation identity —
// the bridge injects it when mapping to the wire.
type ApiRetry struct {
	Active  bool
	Current int
	Total   int
}

// Compacting reports that claude is compacting the conversation. Active is the
// rising (true) / falling (false) edge; like every variant here it carries no
// conversation identity, which the bridge injects when mapping to the wire.
//
// CORRECTED 2026-09-08 (#2227): this doc said "a PTY-derived status peer of Stall",
// and that was false in both directions rather than merely dated. #1348 deleted the
// tui-driver path that made it true, leaving the variant with no producer at all
// from then until now; and the producer this ticket supplies is the stream-json one,
// streamsup's emitCompactingStatus, which maps claude's system/status line — the
// seam #2229's live capture observed. The parenthetical the sentence rested on
// ("tui-driver streams no progress payload") went with it: Active is the only field
// because the EDGE is what lights the banner, not because a deleted driver was
// silent about the rest. #2228 is the ticket that adds claude's trigger and token
// counts on top of this edge.
//
// AMENDED 2026-09-08 (#2236): "Active is the only field" held for one day. The
// closing system/status line carries claude's own outcome across compact_result and
// compact_error, and emitCompactingStatus had been decoding and capping both since
// #2227 — into a Debug record the production daemon does not print. So a failed
// compaction and a successful one were indistinguishable everywhere a client can
// see, which is the whole gap this amendment closes. What changed is which sink two
// already-bounded strings reach, not what bounds them.
type Compacting struct {
	Active bool
	// Result is claude's compact_result off the CLOSING system/status line —
	// "success" on the observed success path (claude 2.1.259, #2229's live lap).
	// An OPEN SET carried verbatim, on TurnEnd.Outcome's rule: a consumer treats an
	// unrecognised token as unknown rather than as an error, because claude may ship
	// one at any time.
	//
	// EMPTY ON EVERY RISING EDGE, and empty is honest on two further paths. claude
	// sends no key at all where compaction succeeded silently, and streamsup's
	// turn-boundary reset — the second producer of a falling edge — has no claude
	// line to read an outcome off at all. Absent, empty and daemon-reset are one
	// reading, which is why this is a plain string: a *string would buy a
	// distinction no consumer answers. #2237 owns the presence-versus-zero question
	// for the fields on the sibling compact_boundary line.
	//
	// RESULT IS NOT A DISCRIMINATOR ANYWHERE IN THE DAEMON, and that is structural
	// rather than incidental: emitCompactingStatus's falling edge is a function of
	// `status` leaving "compacting" and of nothing else, so a failed compaction
	// closes the banner exactly as a successful one does. This field says which it
	// was; it never decides whether the edge fell.
	Result string
	// ErrorText is claude's compact_error off the same line — free-form prose
	// describing why a compaction failed, absent entirely on the observed success
	// path. Named ErrorText rather than Error because a struct field called Error
	// invites confusion with the error interface at every call site that touches it.
	//
	// BOUNDED AT 256 BYTES BY streamsup's maxCompactField, and CUT rather than
	// dropped — which is where it parts company with #2224's ErrorCategory, whose
	// producer drops past its bound. That field is a token set, where a cut token
	// would match no known value while looking like one; this is prose, where a cut
	// sentence still reads as what it is. The cut is not reported: a consumer cannot
	// distinguish a cut value from a short one and needs no such distinction. The
	// producer scrubs the cut for invalid UTF-8 (truncateField), so a slice landing
	// mid-rune cannot reach a JSON string field malformed.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized —
	// TurnEnd.ErrorCategory's SECURITY paragraph applies for provenance, including
	// that the render boundary owing control-character and terminal-escape stripping
	// is the CLIENT's. It does NOT apply for shape, and a consumer that treats the
	// two alike gets this one wrong. ErrorCategory is a short category token; this is
	// arbitrary prose, and newlines, terminal escapes, markup, a URL and text
	// impersonating daemon chrome all fit inside 256 bytes. Render it as inert text
	// attributed to claude — UnrecognizedMessagePayload.Raw's rule, the closer
	// neighbour on shape — never as the daemon's own statement.
	//
	// NOTHING IN THE DAEMON ACTS ON IT: no retry, no backoff, no teardown and no
	// routing is keyed on this value. Whoever first makes the daemon behave
	// differently on it owes the review that turns claude-authored prose into an
	// actuator.
	ErrorText string
}

// CompactionBoundary reports that a compaction finished, what triggered it, and
// how far the context shrank. It maps claude's system/compact_boundary line
// (#2237) — the seventh `system` subtype the parser translates rather than drops,
// and the sibling of the line Compacting's edge pair is read off.
//
// IT IS A SEPARATE VARIANT RATHER THAN THREE MORE FIELDS ON Compacting, and the
// reason is an ORDERING that no amount of design preference can work around. The
// committed capture (internal/e2e/realclaude/testdata/compaction_v2.1.259.json,
// claude 2.1.259, one manual /compact) puts the compact turn's lines in this
// order: status:"compacting", then status:null + compact_result — WHICH IS WHERE
// THE FALLING EDGE FIRES — then system/init, then this line. By the time claude
// states the counts, the frame that would have carried them has shipped. So this
// is conversation-scoped exactly as Compacting is, and a client applies it to the
// divider it has already drawn.
//
// IT ALSO FIRES WITH NO EDGE BEFORE IT, deliberately. The producer
// (streamsup's emitCompactionBoundary) reads and writes NO parser state — not the
// compacting flag, not anything — so a boundary line that followed no
// status:"compacting" is mapped identically to one that did. An auto-compaction
// that announces itself differently is therefore still published, which is the
// second reason the falling edge was the wrong carrier.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BOUNDARY. That is a class
// change rather than a wider payload, and it is worth stating because the nearest
// sibling is weaker: Compacting.Active is a bool the daemon COMPUTES from a string
// comparison it makes itself, so a client could read that field as the daemon's
// own observation. Nothing here is. A fabricated line reading pre_tokens 999999
// and post_tokens 1 draws a plausible compaction mark where nothing was compacted.
// Render this as claude's ASSERTION, attributed to claude, never as the daemon's
// finding — and note that NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE: no retry,
// no backoff, no teardown and no routing is keyed on them, which is what keeps a
// fabricated value a misleading label rather than an actuator. Whoever first makes
// the daemon behave differently on one owes the review that changes that.
//
// The rest of compact_metadata is NOT carried, and the exclusion is structural
// rather than a filter: streamsup's decode target declares these three fields and
// encoding/json discards every other key, including ones claude has not shipped
// yet. cumulative_dropped_tokens and duration_ms are left out because nothing asks
// for them and an unused field is a claim nobody checks; preserved_segment,
// preserved_messages and logical_parent_uuid are left out because they name
// entries in the OPERATOR'S OWN TRANSCRIPT, and they sit unredacted in the
// committed capture. Like every sibling here it also carries neither claude's
// session_id nor claude's uuid, on BackgroundTaskStarted's rule.
//
// It opens and closes no turn, exactly as ModelAnnounced does not. Like every
// variant here it carries no conversation identity of the DAEMON's — the bridge
// injects that.
type CompactionBoundary struct {
	// Trigger is claude's own compact_metadata.trigger — "manual" on the observed
	// path (claude 2.1.259, a typed /compact), with "auto" claude's other documented
	// value. An OPEN SET carried verbatim, on TurnEnd.Outcome's rule: a consumer
	// treats an unrecognised token as unknown rather than as an error.
	//
	// BOUNDED AT 256 BYTES BY streamsup's maxCompactTrigger, and DROPPED rather than
	// cut — the opposite of Compacting.ErrorText beside it, and the same answer
	// maxTurnEndStopField gives for turn_end's three strings. That field is prose,
	// where a cut sentence still reads as what it is; this is a token a consumer
	// MATCHES, where a cut token would match no known value while looking like one.
	// Carrying the empty value says "no trigger I can offer you", which the consumer
	// must already handle because the set is open. There is consequently no
	// truncation report and none is owed: a dropped scalar is directly observable as
	// the empty value.
	//
	// SECURITY: claude-authored, bounded by the daemon and NOT sanitized. The
	// provenance reading is Compacting.ErrorText's; the SHAPE reading is not, and a
	// consumer that treats the two alike gets this one wrong in the safe direction
	// but for the wrong reason. This is a short token from an open set, so it takes
	// TurnEnd.Outcome's rule — switch on it against known values — never
	// UnrecognizedMessagePayload.Raw's prose latitude.
	Trigger string
	// PreTokens and PostTokens are claude's context size before and after the
	// compaction, exactly as it stated them.
	//
	// POINTERS, and this is where the variant departs from Compacting's field shape
	// on purpose. Compacting.Result is a plain string because absent, empty and
	// daemon-reset are one reading there. Here they are not: a count claude OMITTED
	// and a count of ZERO are different facts, post_tokens is optional in claude's
	// own shape, and a consumer that collapses them renders "24k → 0 tokens" for a
	// boundary claude reported without a post count. nil means claude stated no such
	// count; a non-nil pointer to 0 means claude stated zero.
	//
	// NEITHER IS CLAMPED, RANGE-CHECKED OR ORDERED, on RateLimited.ResetsAt's rule:
	// they are claude's numbers, not the daemon's. PostTokens greater than PreTokens
	// is not rejected and not corrected. A value encoding/json cannot fit in an int
	// fails the WHOLE line's decode and produces no event at all, which is
	// fail-closed and is the one place a single absurd field costs the frame rather
	// than the field.
	PreTokens  *int
	PostTokens *int
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

// UnrecognizedSite names WHERE in the stream-json line mapping a payload was
// found that the parser has no mapping for. String-backed so the producer's call
// site is enum-safe and the value crosses the wire unchanged.
type UnrecognizedSite string

const (
	// UnrecognizedLineType is a top-level stream-json line whose `type` is
	// neither mapped nor on the measured known-ignored list.
	UnrecognizedLineType UnrecognizedSite = "line_type"
	// UnrecognizedAssistantBlock is an assistant message content block whose
	// `type` is not text, thinking, or tool_use.
	UnrecognizedAssistantBlock UnrecognizedSite = "assistant_block"
	// UnrecognizedUserBlock is a user message content block whose `type` is not
	// tool_result.
	UnrecognizedUserBlock UnrecognizedSite = "user_block"
	// UnrecognizedUndecodable is a line or block that failed to JSON-decode at
	// all. Kind is empty for this site — there is no type to report.
	UnrecognizedUndecodable UnrecognizedSite = "undecodable"
)

// Unrecognized is a diagnostic marker: the stream-json parser met a payload it
// has no mapping for and dropped it. It exists so genuinely unknown claude
// output becomes VISIBLE the moment it arrives, instead of vanishing into a
// debug log the production daemon does not print.
//
// It is deliberately NOT the parser's tolerate-and-drop path. Every UNMAPPED
// system subtype stays silent; only output outside that measured set reaches
// here. A row per turn would make the feature worthless noise, so the
// known-ignored list is the whole design.
//
// CORRECTED 2026-08-09 (#1404): rate_limit_event is no longer named among the
// silent set, because it is no longer ON the known-ignored list — it has its own
// arm in the parser's main switch (→ RateLimited above). A rate_limit_event line
// the parser's gate does not map is still silent, but now because that arm
// CONSUMES it rather than because a list says to, which makes this lane
// unreachable for the type by matching rather than by list membership.
//
// CORRECTED 2026-08-07 (#1380): system is no longer ignored WHOLESALE, so this
// no longer reads "system/*, rate_limit_event … exactly as before". The system
// subtypes streamsup maps become their own variants instead — BackgroundTaskStarted
// is the first. That changed what is SENT, not what is DRAWN, so the 2026-07-27
// noise measurement behind the drop rule stands. streamsup's ignoredLineTypes
// carries the full statement and its emitSystemSubtype is the one enumeration of
// the mapped set; a prose pointer, not an import, because turnevent must not
// depend on streamsup.
//
// Raw is a plain string, not json.RawMessage, because the producer truncates it
// at construction: a truncated blob is no longer valid JSON, so typing it as raw
// JSON would be a lie. Truncated says whether that happened. Like every variant
// here it carries no conversation identity — the bridge injects that.
type Unrecognized struct {
	// Site is where the drop happened.
	Site UnrecognizedSite
	// Kind is the message or block `type` that had no mapping. Empty when Site
	// is UnrecognizedUndecodable (nothing decoded, so no type was ever read).
	Kind string
	// Raw is the offending JSON, already truncated by the producer.
	Raw string
	// Truncated reports whether Raw was cut to fit the producer's cap.
	Truncated bool
}

// Location is a file a tool call touches (ACP tool-call location). Line is
// 1-based; 0 means unspecified.
type Location struct {
	Path string
	Line int
}

// The events are pure value types, so each marker is implemented on a value
// receiver: TextChunk{}, not only &TextChunk{}, satisfies Event.
func (TextChunk) isTurnEvent()             {}
func (ThoughtChunk) isTurnEvent()          {}
func (ToolStart) isTurnEvent()             {}
func (ToolUpdate) isTurnEvent()            {}
func (TurnEnd) isTurnEvent()               {}
func (BackgroundTaskStarted) isTurnEvent() {}
func (BackgroundTaskUpdated) isTurnEvent() {}
func (BackgroundTaskRoster) isTurnEvent()  {}
func (ThinkingProgress) isTurnEvent()      {}
func (RateLimited) isTurnEvent()           {}
func (ModelAnnounced) isTurnEvent()        {}
func (ModelList) isTurnEvent()             {}
func (SlashCommandList) isTurnEvent()      {}
func (Stall) isTurnEvent()                 {}
func (ApiRetry) isTurnEvent()              {}
func (Compacting) isTurnEvent()            {}
func (CompactionBoundary) isTurnEvent()    {}
func (ConversationReset) isTurnEvent()     {}
func (Unrecognized) isTurnEvent()          {}

var (
	_ Event = TextChunk{}
	_ Event = ThoughtChunk{}
	_ Event = ToolStart{}
	_ Event = ToolUpdate{}
	_ Event = TurnEnd{}
	_ Event = BackgroundTaskStarted{}
	_ Event = BackgroundTaskUpdated{}
	_ Event = BackgroundTaskRoster{}
	_ Event = ThinkingProgress{}
	_ Event = RateLimited{}
	_ Event = ModelAnnounced{}
	_ Event = SlashCommandList{}
	_ Event = Stall{}
	_ Event = ApiRetry{}
	_ Event = Compacting{}
	_ Event = ConversationReset{}
	_ Event = Unrecognized{}
)

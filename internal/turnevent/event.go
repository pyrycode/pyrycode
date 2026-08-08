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
// BackgroundTaskUpdated, BackgroundTaskRoster, the internal-only status peers
// Stall, ApiRetry, and Compacting, and the diagnostic marker Unrecognized.
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
type ToolUpdate struct {
	ToolCallID string
	Status     ToolStatus
	Content    ToolContent
}

// TurnEnd marks the end of a claude turn, carrying the reason only.
//
// ACP models end-of-turn as the stopReason return value of session/prompt, not
// as an event; converting TurnEnd back into that RPC return is the ACP
// adapter's job, not this model's. Here we just carry the reason.
type TurnEnd struct {
	Reason TurnEndReason
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

// Compacting is a PTY-derived status peer of Stall: claude's auto-compaction
// banner. Banner-only (tui-driver streams no progress payload), so Active — the
// rising (true) / falling (false) edge — is the only field beyond the
// bridge-injected conversation id.
type Compacting struct{ Active bool }

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
// It is deliberately NOT the parser's tolerate-and-drop path. rate_limit_event
// and every UNMAPPED system subtype stay silent; only output outside that
// measured set reaches here. A row per turn would make the feature worthless
// noise, so the known-ignored list is the whole design.
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
func (Stall) isTurnEvent()                 {}
func (ApiRetry) isTurnEvent()              {}
func (Compacting) isTurnEvent()            {}
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
	_ Event = Stall{}
	_ Event = ApiRetry{}
	_ Event = Compacting{}
	_ Event = Unrecognized{}
)

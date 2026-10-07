package turnevent

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
// changed state. It is the peer of BackgroundTaskStarted: that variant opens the
// task, this one reports what happened to it afterwards.
//
// It exists so a task's state AFTER it starts is representable at all. Without
// it, everything claude says about a running task is discarded at the parser and
// a client can only ever know a task began.
//
// TWO OF CLAUDE'S SUBTYPES PRODUCE IT, and they fill DISJOINT fields:
//
//   - system/task_updated (#1382) fills Patch and leaves Status and Summary
//     empty. It reports a mid-life change.
//   - system/task_notification (#2245) fills Status and Summary and leaves Patch
//     empty. It reports the task ENDING, which is the transition #1240's symptom
//     needs and the one nothing in this family could express before.
//
// A non-empty Status is therefore what says a terminal state was reported on this
// event. That reading is stated rather than left to be inferred, and it is why no
// daemon-authored discriminator field exists here: a field naming the producing
// subtype would be content the daemon invented, which is exactly what Patch's
// contract forbids one field over.
//
// A SECOND event variant was the alternative and was rejected. The three shipped
// background-task frames were carried to the wire together, and a client already
// joins them on TaskID; a fourth would have added a frame whose only difference
// from this one is which fields it fills.
//
// It opens and closes no turn, exactly as BackgroundTaskStarted does not — a
// background task's lifecycle is orthogonal to the turn that spawned it, which
// is the whole #1240 point.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives: translating at this boundary keeps a claude rename of task_updated
// landing in the parser and nowhere else.
//
// Four keys the captured lines carry are deliberately NOT fields here:
//
//   - session_id — claude's session identity, which is NOT the daemon's
//     conversation identity. A field of that name would invite a consumer, or
//     a later wire mapper, to route on it (#1380).
//   - uuid — claude's per-line message id, which nothing in the daemon reads
//     (#1380).
//   - output_file — documented as A PATH ON THE OPERATOR'S HOST, and the only
//     path-shaped value either line carries. Absence from the DECODE TARGET, not
//     a redaction, is the guarantee: a field that is never declared cannot leak,
//     so no code path in the daemon ever holds it (#2245).
//   - tool_use_id — carried only by the task_notification line, and nothing here
//     needs it: the join key is TaskID, and BackgroundTaskStarted already
//     published the tool call as ToolCallID. Declaring it would ALSO leave it
//     empty on every event the task_updated arm produces, which a consumer could
//     not tell apart from claude omitting it (#2245).
//
// Every string field is claude-derived and is bounded by the producer AT
// CONSTRUCTION (streamsup's maxTaskFieldID / maxTaskPatch / maxTaskSummary),
// following Unrecognized's precedent, so an oversized payload never enters the
// event stream, a queue, or a log. Like every variant here it carries no
// conversation identity — the bridge injects that.
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
	// Status is the terminal state claude reported for the task, from its
	// system/task_notification line. Empty on every event the task_updated arm
	// produces, and empty when claude omits the key.
	//
	// CLAUDE'S REPORT, NOT THE DAEMON'S DETECTION. The daemon does not verify that
	// a task reporting a finish has stopped running; it carries a claim. That
	// distinction is the same one BackgroundTaskRoster's doc enforces when it
	// refuses to infer a finish from a task's disappearance, and a consumer must
	// not read this field as an observation.
	//
	// A plain string, NOT a closed token set, for BackgroundTaskStarted.TaskType's
	// reason. One token has been observed ("completed"); the capture's own
	// limitations record that the documented "failed" and "stopped" states were
	// never staged and that nothing in it says what they carry. A closed set would
	// be declaring two of its three members from a docs page, which is the move
	// this family refuses.
	//
	// Safe to RENDER as text, never to execute or re-shell — it is claude-authored
	// like every other string here, and a short token today is not a guarantee
	// about tomorrow's.
	Status string
	// Summary is claude's account of what the task did, from its
	// system/task_notification line. Empty on every event the task_updated arm
	// produces, and empty when claude omits the key.
	//
	// Model-authored FREE TEXT with no documented length bound, which is why it
	// gets its own cap (streamsup's maxTaskSummary) rather than riding an
	// identifier's. The observed values span both extremes of that: in the
	// #2245 capture it is the task's literal command line, and in an earlier
	// capture of the same subtype it is multi-line model prose.
	//
	// Safe to RENDER as inert text, NEVER to execute or re-shell. That warning is
	// stated here rather than delegated to a sibling because this is the family's
	// SECOND field that can carry a command line — BackgroundTaskStarted's
	// Description is the first — and unlike Patch, which a consumer is told to
	// treat as an opaque blob, this field is prose a client will actually render.
	Summary string
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "task_id", "patch",
	// "status", "summary". None is translated from claude's key here (contrast
	// BackgroundTaskStarted's tool_use_id -> "tool_call_id"). nil when nothing
	// was cut, never an empty non-nil slice, so a consumer can emit it as absent
	// rather than [].
	//
	// Because the two producing subtypes fill disjoint fields, one event's list
	// can only ever name fields from ONE of them plus "task_id", which both carry.
	TruncatedFields []string
}

// BackgroundTask is a daemon-enriched roster row, not an Event. Claude's
// systemBackgroundTaskEntry input has only task_id, task_type and description;
// ToolCallID is joined from a retained BackgroundTaskStarted by the session hold.
type BackgroundTask struct {
	// TaskID is claude's opaque handle for the task: the join key back to the
	// BackgroundTaskStarted that opened it and every BackgroundTaskUpdated since.
	// Same name, no translation.
	TaskID string
	// ToolCallID names the launching tool call in this session and child lifetime.
	// Empty means no retained start match. The hold copies the already-bounded
	// BackgroundTaskStarted.ToolCallID (at most streamsup's maxTaskFieldID bytes).
	ToolCallID string
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
	// caps, in Claude's field order, using the DAEMON's snake_case names: "task_id",
	// "task_type", "description", followed by "tool_call_id" when the joined
	// start id was cut. nil when nothing was cut, never an empty non-nil slice.
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
// CORRECTED 2026-09-10 (#2245): the paragraph above is spent, and it is left
// unedited because its reasoning is the thing that still holds. A terminal state
// exists in the family now — BackgroundTaskUpdated.Status, from claude's
// system/task_notification line, captured by #2247 on a turn that finally let a
// backgrounded command FINISH. What changed is the evidence, not the principle:
// the daemon still reports no finish it cannot detect, and it still refuses to
// infer one from a roster diff. It reports this one because claude states it
// outright, and Status is documented as claude's REPORT rather than the daemon's
// observation for exactly that reason. This variant is unaffected: a roster stays
// a snapshot, and it enumerates what is alive rather than what ended.
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

// BackgroundTaskProgress reports that a background task claude already started is
// still doing work, and what it is doing right now. It maps claude's
// system/task_progress line (#2246), the fifth background-task subtype the parser
// translates rather than drops.
//
// It exists so a long-running task shows something MOVING between the
// BackgroundTaskStarted that opened it and the BackgroundTaskUpdated that closes
// it. Those two are edges; without this a client draws a row that sits silent for
// however long the task runs, which is the half of #1240's symptom that survived
// #2245.
//
// A FOURTH VARIANT RATHER THAN A WIDENING OF BackgroundTaskUpdated, and the
// captured field set is what decides it — the opposite call from #2245's, made on
// the same test. Two of claude's ten keys settle it. Description here is the
// task's CURRENT ACTIVITY ("Reading alpha.txt"), where Description everywhere else
// in this family is the task's OPENING description, a field this family has
// already seen carry a literal operator command line; putting two meanings under
// one name on one task row is a wire-contract trap no later ticket can undo. And
// SubagentType and LastToolName describe the AGENT DOING THE WORK, not what
// happened to the task, which is what BackgroundTaskUpdated's doc says that event
// reports. #2245 could widen because its subtype reported a task's state; this one
// reports an agent's activity.
//
// Nothing here synthesizes a Patch, for the reason emitBackgroundTaskNotification's
// doc gives: BackgroundTaskUpdated.Patch is contractually claude's own bytes, and a
// manufactured one would make that promise false. Reporting progress as a patch
// object would have been the obvious-looking shortcut to reusing that event.
//
// The NAME is the daemon's, not claude's, for the reason BackgroundTaskStarted's
// doc gives — and the discriminating word is "progress", what the daemon reports,
// rather than claude's subtype spelling. It also disambiguates against
// ThinkingProgress, which reports the MODEL reasoning inside a turn; this reports a
// background task's subagent working outside one.
//
// RATE. The producer emits at most one of these per
// streamsup.minTaskToolCallsPerEvent tool calls a task's own counter advances, so
// the event stream carries fewer of them than claude emits lines — on the committed
// capture, 2 lines become 1 event. Two consequences a consumer must not get wrong,
// and they are ThinkingProgress's two: the events do NOT enumerate claude's lines,
// and the ABSENCE of one within any window does NOT mean the task stalled. It may
// only mean the counter has not advanced far enough yet. Do not build a "task
// stalled" inference on the gap between two of these; nothing in the daemon detects
// that, and BackgroundTaskRoster's doc refuses the neighbouring inference.
//
// THE BOUND IS PER TASK, which is where it parts company with ThinkingProgress's.
// That one is a single counter for the whole turn; this is keyed by ids claude
// chooses, so the producer retains a small bounded map — see
// streamsup.maxTaskProgressTasks. A consequence rides that bound and is stated here
// because a consumer cannot see it: past the producer's task-cardinality cap a task
// gets NO progress events at all. Its opening and terminal frames are unaffected,
// so a silent row is still a row that opens and closes correctly.
//
// It opens and closes no turn, exactly as the four variants above do not — a
// background task's lifecycle is orthogonal to the turn that spawned it, which is
// the whole #1240 point.
//
// Three keys the captured lines carry are deliberately NOT fields here, on the set
// BackgroundTaskUpdated's doc states and for its reasons: session_id, claude's
// session identity and NOT the daemon's conversation identity (#1380); uuid,
// claude's per-line message id, which nothing in the daemon reads (#1380); and
// tool_use_id, whose join key is TaskID and whose tool call BackgroundTaskStarted
// already published as ToolCallID (#2245).
//
// claude's documented `summary` is NOT a field either, and its absence is a
// MEASUREMENT rather than an omission. The Agent SDK describes it for a local agent
// only with the progress-summaries option, and always for an MCP task; the
// committed capture is a local agent without that option and carries no such key,
// which task_progress_capture_test.go pins deliberately. Declaring it would be a
// field taken from a docs page, which is the one thing this family's rule forbids.
// If a client needs it, that is a new capture under a staging nobody has run.
//
// Every string field is claude-derived and is bounded by the producer AT
// CONSTRUCTION (streamsup's maxTaskFieldID / maxTaskDescription), following
// Unrecognized's precedent, so an oversized payload never enters the event stream, a
// queue, or a log. The three integers need no cap: they cannot grow. Like every
// variant here it carries no conversation identity — the bridge injects that.
type BackgroundTaskProgress struct {
	// TaskID is claude's opaque handle for the task: the join key back to the
	// BackgroundTaskStarted that opened it. Same name, no translation.
	//
	// It is ALSO the producer's rate-bound key, which is a second load it does not
	// carry anywhere else in this family. An empty TaskID is claude's to choose and
	// still produces an event; the producer buckets every unkeyed line together so
	// nothing escapes the bound, which means two simultaneous unkeyed tasks would
	// share one counter and each report less often.
	TaskID string
	// Description is what the task is doing RIGHT NOW, from the emitting line —
	// "Reading alpha.txt" in the capture. NOT the task's opening description, which
	// travels on BackgroundTaskStarted under the same name and is the reason this
	// variant exists rather than a wider BackgroundTaskUpdated.
	//
	// Model-authored FREE TEXT with no documented length bound, which is why it
	// takes the same cap as its namesake (streamsup's maxTaskDescription) rather
	// than an identifier's. It does NOT take the roster's tighter one:
	// maxTaskRosterDescription is smaller because a roster multiplies the field by a
	// count claude chooses within ONE event, and this event carries it once.
	//
	// Safe to RENDER as inert text, NEVER to execute or re-shell. Both captured
	// values NAME A FILE the subagent is reading, so in practice this field carries
	// path fragments from the operator's host even though claude's documented
	// path-shaped key is not present on this subtype. A consumer must not read it as
	// a safe label, and its namesake already carries a literal command line.
	Description string
	// SubagentType is claude's kind for the agent doing the work
	// ("general-purpose" in the capture).
	//
	// A plain string rather than a closed enum, for BackgroundTaskStarted.TaskType's
	// reason: one value has been observed and a closed set would be declaring its
	// other members from nowhere. Claude-authored, so it is safe to RENDER and never
	// to execute — a short token today is not a guarantee about tomorrow's.
	SubagentType string
	// LastToolName is the tool the subagent most recently invoked ("Read" in the
	// capture), which is the other half of what SubagentType says: who is working
	// and with what.
	//
	// Tool-authored under the same rule as SubagentType, plain string for the same
	// reason, safe to RENDER and never to execute. It is NOT a claim that the tool
	// finished, or succeeded: claude states a name, and nothing here observes the
	// call.
	LastToolName string
	// TotalTokens, ToolUses and DurationMS are claude's own readings off the
	// emitting line's nested usage object, carried verbatim.
	//
	// CUMULATIVE PER TASK, not per line and not per turn, and that is the property a
	// consumer gets wrong by default: two of these events for one task carry a
	// growing total, so their values must never be summed. Diff them if a rate is
	// wanted. This is the mirror image of ThinkingProgress's hazard, where the
	// per-line deltas a consumer receives do NOT sum to the turn's total — there the
	// trap is adding, here it is adding numbers that already include each other.
	//
	// The producer accumulates NOTHING: every value here is the byte claude put on
	// the line that produced this event, so no number on this variant is arithmetic
	// the daemon did. The rate bound reads ToolUses to decide whether a line earns an
	// event, and reading is all it does.
	//
	// A counter is not guaranteed monotonic across a turn. The producer handles a
	// counter that goes backwards without silencing the task, but nothing repairs the
	// VALUES: a consumer that subtracts two readings must tolerate a negative result,
	// exactly as ThinkingProgress.EstimatedTokens' non-monotonicity requires.
	TotalTokens int
	ToolUses    int
	DurationMS  int
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "task_id",
	// "description", "subagent_type", "last_tool_name". No name is translated here —
	// claude's keys and these fields agree, unlike BackgroundTaskStarted's
	// tool_use_id -> "tool_call_id". nil when nothing was cut, never an empty
	// non-nil slice, so a consumer can emit it as absent rather than [].
	//
	// The three integers can never appear here: nothing cuts an int.
	TruncatedFields []string
}

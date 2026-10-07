package turnevent

// BackgroundTaskStarted announces that claude started a background task: work
// that outlives the turn that spawned it. It maps claude's system/task_started
// line.
//
// It opens and closes no turn, and that is the point: a backgrounded command is
// still alive when the turn ends, and without this event a client cannot tell
// that turn end from a genuine finish. Every background-task variant shares
// this.
//
// The name is the daemon's: "Task" alone collides with ACP tool-call
// vocabulary, and translating at this boundary keeps a claude rename of the
// subtype inside the parser. Every background-task variant follows this rule.
//
// Every string field is claude-derived and bounded at construction (streamsup's
// maxTaskFieldID and maxTaskDescription), so an oversized payload never enters
// the event stream, a queue or a log.
type BackgroundTaskStarted struct {
	// TaskID is claude's opaque handle for the task, the join key the later
	// lifecycle lines carry.
	TaskID string
	// ToolCallID is the tool call that spawned the task: claude's tool_use_id,
	// the identifier ToolStart and ToolUpdate carry under this name. The producer
	// cuts and reports an over-long value, where the later
	// ToolCallDenied.ToolCallID drops one.
	ToolCallID string
	// Description is the task's label. For claude's local_bash task type it is
	// the literal command line: safe to render as text, never to execute or
	// re-shell.
	Description string
	// TaskType is claude's kind for the task ("local_bash" in the captured line).
	// A plain string, not a closed enum: one observation does not earn a closed
	// set.
	TaskType string
	// TruncatedFields names the fields the producer cut to fit their caps, in
	// declaration order, by the daemon's snake_case field names rather than
	// claude's keys: "task_id", "tool_call_id", "description", "task_type". nil
	// when nothing was cut, never an empty non-nil slice, so a consumer can emit
	// it as absent rather than []. Every TruncatedFields in this package follows
	// this convention.
	TruncatedFields []string
}

// BackgroundTaskUpdated reports that a background task claude already started
// changed state. Two claude subtypes produce it and fill disjoint fields:
//
//   - system/task_updated fills Patch: a mid-life change.
//   - system/task_notification fills Status and Summary: the task ending.
//
// A non-empty Status therefore says a terminal state was reported. No field
// names the producing subtype, since that would be content the daemon invented.
// It is one variant rather than two because a client already joins the
// background-task frames on TaskID, and a second frame would differ only in
// which fields it fills.
//
// output_file (a path on the operator's host) and tool_use_id are not on the
// producer's decode target, so no code path holds them; the task's tool call is
// already BackgroundTaskStarted.ToolCallID. Every string field is bounded at
// construction (streamsup's maxTaskFieldID, maxTaskPatch and maxTaskSummary).
type BackgroundTaskUpdated struct {
	// TaskID is claude's handle for the task, the join key back to its
	// BackgroundTaskStarted.
	TaskID string
	// Patch is claude's patch object (what changed about the task), carried
	// whole and unparsed as text, so a patch key claude adds later is not
	// discarded. One key has been observed (is_backgrounded). Empty when claude
	// omits it; "" and "{}" stay distinguishable.
	//
	// A plain string, not json.RawMessage, for Unrecognized.Raw's reason: a
	// truncated object is not valid JSON, so a consumer must not assume it
	// parses. The producer also deletes invalid UTF-8 bytes (streamsup's
	// truncateField), even when nothing is cut, and TruncatedFields reports only
	// the cap cut, so Patch can differ from claude's bytes without being listed.
	//
	// Safe to render as text, never to execute or re-shell: a patch key may carry
	// command text.
	Patch string
	// Status is the terminal state claude reported on system/task_notification;
	// empty on task_updated events and when claude omits it.
	//
	// It is claude's report, not the daemon's detection: the daemon does not
	// verify that the task stopped. A plain string, not a closed set: only
	// "completed" has been observed, and the documented "failed" and "stopped"
	// were never captured. Safe to render, never to execute.
	Status string
	// Summary is claude's account of what the task did, from
	// system/task_notification; empty on task_updated events and when claude
	// omits it. Model-authored free text with no documented length bound, capped
	// by streamsup's maxTaskSummary. Captured values range from the task's
	// literal command line to multi-line prose, so it is inert text to render,
	// never to execute or re-shell.
	Summary string
	// TruncatedFields names cut fields ("task_id", "patch", "status", "summary")
	// per BackgroundTaskStarted.TruncatedFields. Because the subtypes fill
	// disjoint fields, one event names fields from only one of them, plus
	// "task_id".
	TruncatedFields []string
}

// BackgroundTask is one row of a BackgroundTaskRoster, not an Event. claude's
// roster entry has only task_id, task_type and description; ToolCallID is
// joined in by cmd/pyry's sessionBackgroundTaskHold from a retained
// BackgroundTaskStarted.
type BackgroundTask struct {
	// TaskID is claude's handle for the task, the join key back to its
	// BackgroundTaskStarted and every BackgroundTaskUpdated.
	TaskID string
	// ToolCallID names the launching tool call in this session and child
	// lifetime; empty when no retained start matched. The hold copies the
	// already-bounded BackgroundTaskStarted.ToolCallID.
	ToolCallID string
	// TaskType is claude's kind for the task, a plain string for
	// BackgroundTaskStarted.TaskType's reason.
	TaskType string
	// Description is the task's label. For local_bash it is the literal command
	// line: safe to render, never to execute or re-shell. A list of command lines
	// is a tempting thing to feed somewhere structured, hence the repeat here.
	//
	// It takes streamsup's tighter maxTaskRosterDescription cap: here it is one
	// label in a list whose length claude chooses, and the full copy already
	// crossed the wire on the BackgroundTaskStarted.
	Description string
	// TruncatedFields names this entry's cut fields in claude's field order,
	// "task_id", "task_type", "description", then "tool_call_id" when the joined
	// start id was cut; otherwise per BackgroundTaskStarted.TruncatedFields.
	TruncatedFields []string
}

// BackgroundTaskRoster carries the complete set of background tasks claude is
// tracking at one moment. It maps claude's system/background_tasks_changed
// line. The other background-task variants report what happened to one task;
// this reports what is alive.
//
// The name describes a snapshot, not claude's trigger: a variant called
// "...Changed" would invite a consumer to read it as a delta, one step from
// inferring a finish. A task's disappearance from a later roster is never
// reported as a finish; the only finish the daemon reports is the one claude
// states, in BackgroundTaskUpdated.Status. A consumer may diff snapshots on its
// own terms.
//
// Bounded at construction in both dimensions: the entry count by streamsup's
// maxTaskRosterEntries, since the array length is claude's to choose, and each
// entry's text by maxTaskFieldID and maxTaskRosterDescription.
type BackgroundTaskRoster struct {
	// Tasks is the roster in claude's order, truncated from the tail past the
	// entry cap; no ranking is invented. nil for an empty roster and when claude
	// omits the key. An empty roster is still emitted: it says nothing is alive.
	Tasks []BackgroundTask
	// DroppedTasks counts entries past the producer's cap that this event does
	// not carry; 0 when none. len(Tasks) + DroppedTasks is claude's roster size.
	// The count reports here rather than as a top-level TruncatedFields, since a
	// name-only report would lose how many were lost; text cuts ride each entry.
	DroppedTasks int
}

// BackgroundTaskProgress reports that a background task claude already started
// is still working, and on what. It maps claude's system/task_progress line, so
// a long-running task shows movement between its BackgroundTaskStarted and the
// BackgroundTaskUpdated that ends it.
//
// It is its own variant rather than more BackgroundTaskUpdated fields. Its
// Description is the task's current activity, not its opening description, and
// two meanings under one name on one task row would be a wire trap; and
// SubagentType and LastToolName describe the agent doing the work, not the
// task's state. It synthesizes no Patch, because BackgroundTaskUpdated.Patch is
// claude's own bytes by contract.
//
// Rate: the producer emits at most one event per streamsup's
// minTaskToolCallsPerEvent tool calls of a task's own counter, so events do not
// enumerate claude's lines, and a gap between two does not mean the task
// stalled. The rate state is per task and bounded by maxTaskProgressTasks; a
// task past that cap gets no progress events, though its start and end frames
// are unaffected.
//
// claude's tool_use_id is not carried; the task's call is
// BackgroundTaskStarted.ToolCallID. Its documented `summary` is not a field
// either: the committed capture, a local agent without progress summaries,
// carries no such key (TestRealClaudeTaskProgressCaptureKeysArePinned). Every
// string is bounded at construction (streamsup's maxTaskFieldID and
// maxTaskDescription); the integers need no cap.
type BackgroundTaskProgress struct {
	// TaskID is claude's handle for the task, the join key back to its
	// BackgroundTaskStarted. It is also the producer's rate key: an empty TaskID
	// still produces events, and all unkeyed lines share one counter.
	TaskID string
	// Description is what the task is doing now, from the emitting line
	// ("Reading alpha.txt" in the capture), not its opening description. Free
	// text capped by streamsup's maxTaskDescription, not the roster's tighter
	// cap, because this event carries it once.
	//
	// Inert text to render, never to execute or re-shell. Captured values name a
	// file the subagent is reading, so it carries path fragments from the
	// operator's host; do not treat it as a safe label.
	Description string
	// SubagentType is claude's kind for the agent doing the work
	// ("general-purpose" in the capture), a plain string for
	// BackgroundTaskStarted.TaskType's reason. Safe to render, never to execute.
	SubagentType string
	// LastToolName is the tool the subagent most recently invoked ("Read" in the
	// capture), under SubagentType's rules. It is not a claim that the tool
	// finished or succeeded.
	LastToolName string
	// TotalTokens, ToolUses and DurationMS are claude's readings off the line's
	// nested usage object, verbatim. They are cumulative per task, so two events
	// for one task carry growing totals: never sum them, diff them for a rate.
	// The producer accumulates nothing. A counter may go backwards and nothing
	// repairs it, so a consumer that subtracts readings must tolerate a negative
	// result.
	TotalTokens int
	ToolUses    int
	DurationMS  int
	// TruncatedFields names cut fields ("task_id", "description",
	// "subagent_type", "last_tool_name") per
	// BackgroundTaskStarted.TruncatedFields.
	TruncatedFields []string
}

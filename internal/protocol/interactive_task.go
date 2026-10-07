package protocol

import (
	"encoding/json"
)

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

// BackgroundTask is one daemon-enriched row of a BackgroundTaskRosterPayload.
// Claude's roster has three text fields; ToolCallID is joined from the same
// task's retained start in this session and child lifetime. Empty means no
// retained match, including unseen or forgotten starts.
//
// TruncatedFields names THIS row's cut fields ("task_id", "task_type",
// "description", "tool_call_id"), null when nothing was cut.
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
	ToolCallID      string   `json:"tool_call_id"`
	TaskType        string   `json:"task_type"`
	Description     string   `json:"description"`
	TruncatedFields []string `json:"truncated_fields"`
}

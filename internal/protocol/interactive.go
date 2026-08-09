package protocol

import "encoding/json"

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

// AssistantDeltaPayload is the body of an Envelope whose Type ==
// TypeAssistantDelta (docs/protocol-mobile.md § assistant_delta). Binary →
// phone direction; an incremental, coalesced chunk of assistant text. Seq
// is a per-turn, non-negative delta-ordering counter that resets each turn
// (distinct from the session-monotonic Envelope.ID).
type AssistantDeltaPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	Seq            int    `json:"seq"`
	Text           string `json:"text"`
}

// ToolUsePayload is the body of an Envelope whose Type == TypeToolUse
// (docs/protocol-mobile.md § tool_use). Binary → phone direction; announces
// a tool invocation. InputSummary is a human-readable précis of the tool
// input, not the raw input.
type ToolUsePayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	ToolUseID      string `json:"tool_use_id"`
	Name           string `json:"name"`
	InputSummary   string `json:"input_summary"`
}

// ToolResultPayload is the body of an Envelope whose Type == TypeToolResult
// (docs/protocol-mobile.md § tool_result). Binary → phone direction;
// reports the outcome of the tool_use with the matching ToolUseID.
// ResultSummary is a human-readable précis of the result, not the raw
// output.
type ToolResultPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	ToolUseID      string `json:"tool_use_id"`
	IsError        bool   `json:"is_error"`
	ResultSummary  string `json:"result_summary"`
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
type TurnEndPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	StopReason     string `json:"stop_reason"`
}

// StallPayload is the body of an Envelope whose Type == TypeStall
// (docs/protocol-mobile.md § stall). Binary → phone direction; the wire form
// of the internal-only turnevent.Stall onset marker. It carries conversation
// identity only — like turn_state, a stall is a coarse conversation-level
// signal, not turn-scoped, so there is no turn_id; and it is onset-only, so
// there is no clearing field (the phone self-clears on the next turn
// activity). The bridge (#608) supplies ConversationID because the internal
// Stall marker carries none.
type StallPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ApiRetryPayload is the body of an Envelope whose Type == TypeApiRetry
// (docs/protocol-mobile.md § api_retry). Binary → phone direction; the wire
// form of the internal-only turnevent.ApiRetry status peer. Like turn_state it
// is a coarse conversation-level signal, not turn-scoped, so there is no
// turn_id. Active is the show (true) / clear (false) edge; Current/Total are the
// parsed `attempt N/M` counter ({0,0} when claude's counter did not parse). No
// field carries raw banner or screen text. The bridge (#608) supplies
// ConversationID because the internal ApiRetry marker carries none.
type ApiRetryPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	Current        int    `json:"current"`
	Total          int    `json:"total"`
}

// CompactingPayload is the body of an Envelope whose Type == TypeCompacting
// (docs/protocol-mobile.md § compacting). Binary → phone direction; the wire
// form of the internal-only turnevent.Compacting status peer. Banner-only
// (tui-driver streams no compaction progress), so beyond the bridge-supplied
// ConversationID the only field is Active — the show (true) / clear (false)
// edge. Not turn-scoped, so there is no turn_id.
type CompactingPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
}

// UnrecognizedMessagePayload is the body of an Envelope whose Type ==
// TypeUnrecognizedMessage (docs/protocol-mobile.md § unrecognized_message).
// Binary → phone direction; the wire form of the internal-only
// turnevent.Unrecognized diagnostic marker. Like turn_state it is a coarse
// conversation-level signal, not turn-scoped, so there is no turn_id — an
// unrecognized message has no turn we can honestly attribute it to.
//
// Site is where the parser dropped the payload ("line_type", "assistant_block",
// "user_block", "undecodable"). MessageType is the offending message or block
// `type`, empty when Site is "undecodable" (nothing decoded, so no type was
// read). Raw is the offending JSON, truncated by the producer to a fixed byte
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
// TruncatedFields names the cut fields ("task_id", "patch"), null when nothing
// was cut, and carries the same load as its sibling's.
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
// This is the file's only custom marshaller, and the deviation is deliberate.
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

// ThinkingProgressPayload is the body of an Envelope whose Type ==
// TypeThinkingProgress (docs/protocol-mobile.md § thinking_progress, #1386).
// Binary → phone direction; the wire form of turnevent.ThinkingProgress, the
// daemon's translation of claude's system/thinking_tokens line. It reports that
// claude is actively reasoning and roughly how much — claude's only mid-turn
// proof of life on the stream-json surface.
//
// Like turn_state it is a coarse conversation-level signal, NOT turn-scoped, so
// there is no turn_id, and receiving one neither opens nor closes a turn. The
// bridge supplies ConversationID because the internal event carries none;
// claude's own session_id is deliberately absent for BackgroundTaskStartedPayload's
// reason, and the parser drops it before the event exists (#1380/#1385).
//
// Both fields are claude's own integer readings, carried verbatim. There is no
// TruncatedFields, and its absence is a decision rather than an omission: unlike
// every sibling above this payload carries no claude-authored TEXT at all, so
// nothing is ever cut, and a permanently-nil field would claim a bound that does
// not exist. For the same reason there is no producer byte cap to mirror here.
//
// Two consumer hazards ride these numbers and are NOT restated here, because
// turnevent.ThinkingProgress's field comments are their single source of truth
// (with the measured numbers): EstimatedTokens is not monotonic across a turn,
// and the EstimatedTokensDelta values a client receives do not sum to the turn's
// total. A consumer-facing statement of both, plus the two reasons absence proves
// nothing, is in docs/protocol-mobile.md § thinking_progress.
type ThinkingProgressPayload struct {
	ConversationID       string `json:"conversation_id"`
	EstimatedTokens      int    `json:"estimated_tokens"`
	EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}

// BackgroundTask is one row of a BackgroundTaskRosterPayload
// (docs/protocol-mobile.md § background_task_roster, #1394). Its fields are
// exactly the per-entry keys claude's roster line carries and nothing invented —
// in particular there is no tool_call_id and no patch, which the scalar frames
// carry because their LINES do. TaskID is the join key back to the
// background_task_started that opened the task.
//
// TruncatedFields names THIS row's cut fields ("task_id", "task_type",
// "description"), null when nothing was cut.
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
	TaskType        string   `json:"task_type"`
	Description     string   `json:"description"`
	TruncatedFields []string `json:"truncated_fields"`
}

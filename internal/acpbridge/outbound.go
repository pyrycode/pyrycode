// Package acpbridge maps the neutral, daemon-owned turnevent model OUT to the
// Agent Client Protocol (ACP) session/update payloads. It is the ACP mirror of
// internal/turnbridge (which maps the same neutral events out to the mobile v2
// wire): same neutral source, different framing. ADR 027
// (docs/knowledge/decisions/027-acp-mapping.md) is the authoritative outbound
// mapping table this file implements.
//
// It is a pure value-to-value adapter — no transport, no I/O, no goroutine, no
// clock read, no state. Everything stateful (which session the update belongs
// to, holding the session/prompt call open for TurnEnd, writing Stall to
// stderr, grouping chunks into a message) is the streaming consumer's job
// (#750). Keeping those out is what makes MapUpdate table-testable and isolates
// it from the ACP session lifecycle.
//
// Import discipline: this package imports only internal/turnevent and
// encoding/json. It does NOT import internal/acp (the JSON-RPC transport floor,
// which is deliberately sealed to the standard library and knows no concrete
// ACP method) — the mapper never touches the transport — and it does NOT import
// internal/protocol (the mobile wire types), exactly as turnevent's package doc
// requires the two adapters to stay independent over one neutral model.
package acpbridge

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// session/update variant discriminants — the value of the ACP `sessionUpdate`
// field. These ARE the ACP wire strings (ADR 027 "ACP taxonomy reference").
const (
	SessionUpdateAgentMessageChunk = "agent_message_chunk"
	SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
)

// Background-task session/update discriminants. Unlike the four above these are
// pyry EXTENSIONS, not ACP wire strings: ACP's sessionUpdate taxonomy has no
// background-task variant (ADR 027 "ACP taxonomy reference" lists all ten of
// them), so the facts have no in-spec home. Their own const block for exactly
// that reason — the comment above must stay true of the four it covers. ADR 027
// divergence 7 records the choice.
//
// Reusing one of the four over a background-task body was rejected, not merely
// passed over: an ACP tool_call body requires toolCallId/title/kind/status, so a
// strict host rejects the notification just as hard as it rejects an unknown
// discriminant, while a lenient host BELIEVES it and grows a phantom tool call
// in its tool view. That is strictly worse than the facts being dropped. A
// conforming body is not available either — it has no destination for TaskType,
// TruncatedFields or DroppedTasks, and no shape at all for an EMPTY roster,
// which is the feature's payoff.
//
// The "pyry/" prefix is load-bearing. A bare background_task_started is shaped
// exactly like the ten spec strings, so a future spec-added variant of that name
// would collide with a different body, and a reader of a wire log could not tell
// ours from spec truth where the ADR is not available. The namespace/name shape
// matches ACP's own method names (session/update, fs/read_text_file), so it
// reads as in-protocol rather than malformed.
//
// Residual risk, recorded in divergence 7: a strict sessionUpdate enum decoder
// may reject the whole notification. Nothing emits these yet — #1402 owns the
// MapUpdate arms and the gating decision that risk feeds.
const (
	SessionUpdateBackgroundTaskStarted = "pyry/background_task_started"
	SessionUpdateBackgroundTaskUpdated = "pyry/background_task_updated"
	SessionUpdateBackgroundTaskRoster  = "pyry/background_task_roster"
)

// MethodSessionUpdate is the JSON-RPC notification method the consumer (#750)
// sends carrying one of the payloads below. Defined here for the consumer's
// convenience; the {sessionId, update} params wrapper is the consumer's.
const MethodSessionUpdate = "session/update"

// AgentMessageChunk is the session/update payload for incremental assistant
// text. The neutral MessageID is NOT a field here — ACP has no messageId on the
// wire; MapUpdate returns it out-of-band for the consumer to group by.
type AgentMessageChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// AgentThoughtChunk is the session/update payload for streaming reasoning
// ("thinking") text. Kept as its own named type (not shared with
// AgentMessageChunk) so each call site matches the ADR taxonomy 1:1; the tiny
// duplication is deliberate.
type AgentThoughtChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// ContentBlock is an ACP content block. Text-only today: turnevent builds only
// text/diff/terminal content, and ACP image/resource blocks have no turnevent
// shape yet (ADR 027). New fields are added when the producer emits them.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCall is the session/update payload announcing a new tool invocation. It
// carries no Content: a new invocation has no result yet — content first
// appears via a ToolCallUpdate.
type ToolCall struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`             // always "pending" from a ToolStart
	RawInput      json.RawMessage    `json:"rawInput,omitempty"` // opaque pass-through, never parsed here
	Locations     []ToolCallLocation `json:"locations,omitempty"`
}

// ToolCallUpdate is the session/update payload carrying the changed fields of an
// existing tool call. Status is omitempty (a degenerate empty-status update
// omits it) and Content is nil for a status-only update.
type ToolCallUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Status        string            `json:"status,omitempty"`
	Content       []ToolCallContent `json:"content,omitempty"`
}

// ToolCallLocation is a file a tool call touches. Line is 1-based; 0 (unspecified)
// is omitted.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolCallContent is a flat tagged union over the ACP tool-call content shapes:
// "content" (a text block), "diff", or "terminal". Only the fields for the
// active Type are set; the rest are omitempty.
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`    // Type == "content"
	Path       string        `json:"path,omitempty"`       // Type == "diff"
	OldText    string        `json:"oldText,omitempty"`    // Type == "diff"
	NewText    string        `json:"newText,omitempty"`    // Type == "diff"
	TerminalID string        `json:"terminalId,omitempty"` // Type == "terminal"
}

// BackgroundTaskStarted is the session/update payload announcing that claude
// started work that outlives the turn which spawned it — the ACP form of
// turnevent.BackgroundTaskStarted, riding the extension discriminant
// SessionUpdateBackgroundTaskStarted (ADR 027 divergence 7). Without it a desktop
// client cannot separate a turn that ended with work still running from a genuine
// finish, which is #1240's symptom.
//
// The types in this file are named for the ACP VARIANT rather than the neutral
// event. Here the two names coincide, because the extension discriminant was
// derived FROM the neutral name — the rule is invisible at this one site, so it
// is stated rather than left to be inferred.
//
// None of the three background payloads carries a session or conversation
// identifier. The mobile payloads carry ConversationID because the mobile
// envelope has no session addressing; on ACP the session id is the consumer's
// wrapper field (sessionUpdateParams.SessionID, cmd/pyry/acp_turn_stream.go), and
// every payload above omits it for the same reason.
//
// ToolCallID is the tool call that spawned the task: the same identifier a
// ToolCall payload already carried under this name, so a host correlates the two
// for free. Its two siblings carry none because their neutral events carry none,
// and synthesising one would need a stateful TaskID -> ToolCallID join table this
// package is forbidden to hold.
//
// TruncatedFields names the fields the producer cut to fit their caps, using the
// DAEMON's snake_case names ("task_id", "tool_call_id", "description",
// "task_type"); it is load-bearing, not decoration — a payload that dropped it
// would present claude's truncated text to a host as complete. It is omitempty
// on all four types here, so both nil and [] vanish from the wire. That is a
// DELIBERATE divergence from the mobile lane's tag (which ships null for nil and
// [] for empty): nil and [] say the identical thing there ("nothing was cut") and
// no consumer branches on the difference, so omitting both is a stronger
// realisation of the same call, and it matches this package's own optional-list
// convention (locations,omitempty). Contrast Tasks below, whose empty value IS
// the signal and which is therefore normalised rather than omitted.
//
// SECURITY: Description is claude's label for the task, and for the local_bash
// task type it is the LITERAL command line. It is safe to RENDER as inert text
// and never to execute, re-shell, or feed to an HTML sink, an attribute, or a
// URL. Its bound is the producer's, decided at construction
// (internal/streamsup/parser.go's maxTaskFieldID / maxTaskDescription), so this
// struct re-decides no maximum: a second cap here would be a second place the
// limit is decided, and the two could disagree silently.
type BackgroundTaskStarted struct {
	SessionUpdate   string   `json:"sessionUpdate"`
	TaskID          string   `json:"taskId"`
	ToolCallID      string   `json:"toolCallId"`
	Description     string   `json:"description"`
	TaskType        string   `json:"taskType"`
	TruncatedFields []string `json:"truncatedFields,omitempty"`
}

// BackgroundTaskUpdated is the session/update payload reporting what happened to
// a task BackgroundTaskStarted already opened — the ACP form of
// turnevent.BackgroundTaskUpdated, riding SessionUpdateBackgroundTaskUpdated (ADR
// 027 divergence 7). TaskID is the join key back to that payload; there is no
// toolCallId, for BackgroundTaskStarted's stated reason.
//
// Patch has no omitempty: "" (claude omitted the key) and "{}" (an empty patch
// object) say different things and must stay distinguishable on the wire, which
// is the neutral type's stated property.
//
// SECURITY: Patch is claude's patch object — what CHANGED about the task —
// carried WHOLE and unparsed as its serialized text, so nothing here is declared
// about its contents and no key is enumerated. It is a plain string and NEVER
// json.RawMessage: the producer truncates it at construction
// (internal/streamsup/parser.go's maxTaskPatch) and a truncated object is no
// longer valid JSON, so raw-JSON typing would be a lie AND would break
// marshalling — encoding/json rejects an invalid RawMessage, so json.Marshal of
// the whole payload would fail and the consumer's failure path would drop the
// notification, turning a truncation claude's output length alone can trigger
// into a silent loss of the entire update. A consumer MUST NOT assume it parses,
// must render it as inert text, and must never execute it, re-shell it, or feed
// it to an HTML sink, an attribute, or a URL — a patch's structured shape makes
// it the more tempting thing to feed somewhere that runs it, and its sibling's
// Description already carries a literal command line.
type BackgroundTaskUpdated struct {
	SessionUpdate   string   `json:"sessionUpdate"`
	TaskID          string   `json:"taskId"`
	Patch           string   `json:"patch"`
	TruncatedFields []string `json:"truncatedFields,omitempty"`
}

// BackgroundTaskRoster is the session/update payload carrying the complete set of
// background tasks claude is tracking at one moment — the ACP form of
// turnevent.BackgroundTaskRoster, riding SessionUpdateBackgroundTaskRoster (ADR
// 027 divergence 7). A SNAPSHOT, not a delta: a task's disappearance from a later
// roster is the available finish signal, but diffing successive snapshots is the
// HOST's call to make on its own terms, not a finish the daemon reports.
//
// Tasks is in claude's own order, truncated from the tail by the producer. The
// key is always present and never null — see MarshalJSON. An EMPTY roster is
// meaningful and is still a positive statement, "nothing is alive", which is
// exactly the signal a consumer of #1240's symptom needs.
//
// DroppedTasks is how many entries claude sent beyond the producer's entry cap
// (maxTaskRosterEntries) that this payload does NOT carry, so the roster's true
// size is len(Tasks) + DroppedTasks. It has no omitempty: 0 is the positive
// statement "nothing was dropped", not an absence. It is also this payload's ONLY
// truncation report — the type has no TruncatedFields, deliberately, mirroring
// the neutral type: a name-only report loses HOW MANY were lost, and each
// dimension reports where it is decided, a text cut being a property of one entry
// and riding that entry.
type BackgroundTaskRoster struct {
	SessionUpdate string           `json:"sessionUpdate"`
	Tasks         []BackgroundTask `json:"tasks"`
	DroppedTasks  int              `json:"droppedTasks"`
}

// MarshalJSON normalises a nil Tasks to an empty array, so an empty roster always
// serialises as "tasks":[] and never as "tasks":null. It is this package's only
// custom marshaller, and three properties are load-bearing.
//
// omitempty is NOT the alternative, and dropping it is not a sufficient device
// either. Go marshals a nil slice as null whether or not the tag is set, so the
// tag alone only moves the failure from "key absent" to "key present, value
// null" — the same "no roster information" reading on the host side, when what
// the empty roster states is the opposite. The tag is absent AND this method
// exists; neither alone would do. Between null and [], [] is the better contract:
// it reads as an empty list where null reads as absent, and a host decoding into
// a non-optional array type never has to branch.
//
// The type is the only place this can live. turnevent's Tasks is nil both for an
// empty roster and when claude omits the key, so #1402's mapper passing it
// straight through hands this type a nil slice; pre-allocating in the mapper
// instead would produce identical bytes while hiding the normalisation the type
// owns, which is the call the mobile lane already made and stated
// (internal/protocol/interactive.go, internal/turnbridge/outbound.go).
//
// Value receiver, not pointer. MapUpdate returns payloads as values into an any,
// and json.Marshal on a value boxed in an interface finds only value-receiver
// methods — a pointer receiver would silently never fire. The copy also means the
// substitution never touches the caller's slice header. The type alias is the
// standard indirection that keeps json.Marshal from recursing back into here.
//
// TruncatedFields is deliberately NOT normalised the same way, on any of the four
// types: nil and [] say the identical thing there, whereas Tasks is this
// payload's subject and its empty value is the signal.
func (u BackgroundTaskRoster) MarshalJSON() ([]byte, error) {
	if u.Tasks == nil {
		u.Tasks = []BackgroundTask{}
	}
	type alias BackgroundTaskRoster
	return json.Marshal(alias(u))
}

// BackgroundTask is one entry of a BackgroundTaskRoster: an element shape, not a
// payload, so it carries no sessionUpdate discriminant. Its fields are exactly
// the per-entry keys claude's roster line shows and nothing invented — in
// particular there is no toolCallId and no patch, which the two scalar payloads
// carry because their LINES do.
//
// TaskType is a plain string rather than a closed enum (only local_bash has been
// observed, and one observation does not earn a closed set), matching the neutral
// type and BackgroundTaskStarted above.
//
// SECURITY: Description is the task's label, and for claude's local_bash task
// type it is the LITERAL command line — safe to RENDER as inert text, never to
// execute, re-shell, or feed to an HTML sink, an attribute, or a URL. The warning
// is repeated here rather than delegated to BackgroundTaskStarted's because a
// roster carries a LIST of command lines, which is a more tempting shape to feed
// somewhere structured than a single one. It is bounded by a tighter producer cap
// than its scalar counterpart (maxTaskRosterDescription, not maxTaskDescription):
// here the value is a label in a list whose length claude chooses, and the
// full-length copy already crossed the wire on the BackgroundTaskStarted this
// entry's TaskID joins back to.
type BackgroundTask struct {
	TaskID          string   `json:"taskId"`
	TaskType        string   `json:"taskType"`
	Description     string   `json:"description"`
	TruncatedFields []string `json:"truncatedFields,omitempty"`
}

// MapUpdate maps one neutral turnevent.Event to its ACP session/update payload,
// or reports "no notification" (ok == false). It is the ACP mirror of
// turnbridge.MapEvent — pure, exhaustive over the sealed turnevent.Event, with
// no I/O, no goroutine and no clock read; safe on a zero-value / nil Event.
//
// update is one of the payload structs above, or nil when ok is false. It is
// any because the payloads share no marker interface; the consumer json.Marshals
// it directly under the session/update params. The variant discriminant rides
// INSIDE update as its sessionUpdate field, so there is no separate type return
// (unlike the mobile envelope). The method is always MethodSessionUpdate.
//
// msgID is the neutral MessageID returned OUT-OF-BAND (ACP has no messageId wire
// field): non-empty only for the two chunk variants, so the consumer can group
// chunks by message. All stateful grouping is the consumer's (#750).
//
// ok is false for the two events with no session/update representation — TurnEnd
// (it maps to the session/prompt stopReason return, divergence 1) and Stall
// (internal-only; the consumer writes it to stderr) — and for a nil/unknown
// Event. What to do with a no-notification outcome is the consumer's job.
func MapUpdate(ev turnevent.Event) (update any, msgID string, ok bool) {
	switch e := ev.(type) {
	case turnevent.TextChunk:
		return AgentMessageChunk{
			SessionUpdate: SessionUpdateAgentMessageChunk,
			Content:       ContentBlock{Type: "text", Text: e.Text},
		}, e.MessageID, true
	case turnevent.ThoughtChunk:
		return AgentThoughtChunk{
			SessionUpdate: SessionUpdateAgentThoughtChunk,
			Content:       ContentBlock{Type: "text", Text: e.Text},
		}, e.MessageID, true
	case turnevent.ToolStart:
		// Kind is emitted as the ACP taxonomy string verbatim (string(e.Kind)):
		// no translation table is needed because the turnevent.ToolKind values
		// already ARE the ACP strings (taxonomy.go), and "other" is already the
		// neutral fallback kind, so no unknown-kind coalescing happens here.
		// ToolStart has no status field, so a new tool_call is emitted with the
		// ACP default "pending" (turnevent.ToolStatusPending).
		return ToolCall{
			SessionUpdate: SessionUpdateToolCall,
			ToolCallID:    e.ToolCallID,
			Title:         e.Title,
			Kind:          string(e.Kind),
			Status:        string(turnevent.ToolStatusPending),
			RawInput:      e.RawInput,
			Locations:     mapLocations(e.Locations),
		}, "", true
	case turnevent.ToolUpdate:
		// Status is likewise the ACP taxonomy string verbatim (string(e.Status)):
		// turnevent.ToolStatus values ARE the ACP strings, so no table.
		return ToolCallUpdate{
			SessionUpdate: SessionUpdateToolCallUpdate,
			ToolCallID:    e.ToolCallID,
			Status:        string(e.Status),
			Content:       mapToolContent(e.Content),
		}, "", true
	case turnevent.TurnEnd:
		// Divergence 1: end-of-turn is the stopReason RETURN of session/prompt,
		// not a session/update notification. The consumer resolves the held
		// session/prompt call with string(e.Reason). Nothing to emit here.
		return nil, "", false
	case turnevent.Stall:
		// Internal-only onset marker; ACP has no home for it (ADR 027 outbound
		// table). The consumer surfaces it on stderr. Not a session/update.
		return nil, "", false
	default:
		// nil, or any impossible future variant of the sealed Event: no
		// notification. Kept explicit so a new producer variant surfaces as a
		// visible drop rather than silently vanishing (same posture as the
		// template's default arm).
		return nil, "", false
	}
}

// mapToolContent maps the sealed turnevent.ToolContent to the ACP tool-call
// content list, exhaustive over the sum type. A nil content (the legal
// status-only ToolUpdate) yields nil, so the payload's content field is omitted.
// The default arm is kept even though the type is sealed so a future producer
// variant cannot silently vanish (mirrors turnbridge.resultSummary).
func mapToolContent(c turnevent.ToolContent) []ToolCallContent {
	switch v := c.(type) {
	case turnevent.TextContent:
		return []ToolCallContent{{Type: "content", Content: &ContentBlock{Type: "text", Text: v.Text}}}
	case turnevent.DiffContent:
		return []ToolCallContent{{Type: "diff", Path: v.Path, OldText: v.OldText, NewText: v.NewText}}
	case turnevent.TerminalContent:
		return []ToolCallContent{{Type: "terminal", TerminalID: v.TerminalID}}
	default:
		return nil
	}
}

// mapLocations maps each turnevent.Location to its ACP wire shape, returning nil
// for an empty input so the payload's locations field is omitted.
func mapLocations(locs []turnevent.Location) []ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]ToolCallLocation, len(locs))
	for i, l := range locs {
		out[i] = ToolCallLocation{Path: l.Path, Line: l.Line}
	}
	return out
}

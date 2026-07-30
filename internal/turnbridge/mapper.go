package turnbridge

import (
	"encoding/json"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// mapEvent maps one tui-driver Event to a neutral turnevent.Event. ok is false
// for events the internal model has no representation for — the caller drops +
// debug-logs those. Pure; safe on a zero-value Event.
//
// The robust JSONL-sourced kinds (assistant text, tool use/result, end-of-turn)
// map, as do the screen-derived status peers: the stall onset marker (#373) and
// the api-retry / compacting show/hide edges (#1074). The remaining PTY-state
// kinds (idle/thinking/modal/mcp/network) and Unknown drop, because the internal
// model (#606) has no type for them — not because the screen signals are
// worthless (ADR 025 § brittleness split).
func mapEvent(ev tuidriver.Event) (turnevent.Event, bool) {
	switch ev.Kind {
	case tuidriver.EventKindJsonlEntry:
		return mapEntry(ev.Entry)
	case tuidriver.EventKindJsonlEndOfTurn:
		// EventKindJsonlEndOfTurn fires only after IsEndTurn held (assistant +
		// stop_reason=="end_turn" + non-empty text), so the reason is always
		// end_turn. Other stop reasons (max_tokens, refusal, …) are not
		// distinguishable from this event kind in tui-driver v1.3.0.
		return turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, true
	case tuidriver.EventKindStallDetected:
		// One-shot rising-edge marker, no payload and no clearing edge: map to
		// the zero-field Stall signal (no field extraction). The bridge injects
		// conversation identity when shaping the wire payload.
		return turnevent.Stall{}, true
	case tuidriver.EventKindPtyApiRetryShown:
		// Rising edge of claude's API-error retry: read only the two parsed
		// counter ints (never screen bytes; the Event exposes no string field
		// for this kind). {0,0} passes through as a legitimate "count unknown".
		return turnevent.ApiRetry{Active: true, Current: ev.Retry.Current, Total: ev.Retry.Total}, true
	case tuidriver.EventKindPtyApiRetryHidden:
		// Falling edge: tui-driver carries the last-known counter so the final
		// render stays coherent; copy it verbatim (the phone ignores it when
		// active is false).
		return turnevent.ApiRetry{Active: false, Current: ev.Retry.Current, Total: ev.Retry.Total}, true
	case tuidriver.EventKindPtyCompactingShown:
		// Banner-only rising edge: no counter payload (contrast api-retry).
		return turnevent.Compacting{Active: true}, true
	case tuidriver.EventKindPtyCompactingHidden:
		return turnevent.Compacting{Active: false}, true
	default:
		return nil, false
	}
}

// mapEntry maps a JSONL-carried entry to an internal event. Branches split on
// e.Type first so ParseToolUse / ParseToolResult (which re-parse RawLine and
// gate on envelope type) run only where they can match. In claude's streaming
// JSONL each line carries one content block, so the assistant sub-conditions
// are mutually exclusive in practice; the priority order is defensive.
func mapEntry(e tuidriver.JSONLEntry) (turnevent.Event, bool) {
	switch e.Type {
	case "assistant":
		if tu := tuidriver.ParseToolUse(e.RawLine); tu != nil {
			return turnevent.ToolStart{
				ToolCallID: tu.ID,
				Title:      tu.Name,
				Kind:       toolKind(tu.Name),
				RawInput:   rawInput(tu.Input),
			}, true
		}
		if text := tuidriver.AssistantText(e); text != "" {
			return turnevent.TextChunk{MessageID: messageID(e), Text: text}, true
		}
		if think := thinkingText(e); think != "" {
			return turnevent.ThoughtChunk{MessageID: messageID(e), Text: think}, true
		}
		return nil, false
	case "user":
		if tr := tuidriver.ParseToolResult(e.RawLine); tr != nil {
			return turnevent.ToolUpdate{
				ToolCallID: tr.ToolUseID,
				Status:     toolStatus(tr.IsError),
				Content:    toolResultContent(tr.Content),
			}, true
		}
		// Deliberately after the tool_result branch: tool output is the most
		// attacker-controllable text on this path (a fetched page, a catted
		// file), and any entry carrying a tool_result block is matched and
		// returned above — including one that also carries a text block quoting
		// the marker, which is the only shape where this order is what stands
		// between tool output and the prose matcher. Only a user-envelope entry
		// with no tool_result block gets here.
		if isInterruptMarker(e) {
			return turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled}, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// interruptMarkerSentinel is the prefix of the user-role entry claude records
// when a turn is interrupted. A prefix, not an exact string: claude 2.1.128
// writes "[Request interrupted by user]" and 2.1.220 writes "[Request
// interrupted by user for tool use]" (#1243), and a further suffix costs no
// code change. This one line is the entire prose-treadmill surface.
const interruptMarkerSentinel = "[Request interrupted by user"

// isInterruptMarker reports whether e is claude's record that the turn was
// interrupted — the sole signal this path has for a cancelled turn, since
// EventKindJsonlEndOfTurn fires only on a clean end_turn.
//
// Two independent questions, answered by two deliberately different signals:
//
//   - "did an interruption happen?" — only the marker prose says so. claude
//     emits no structural field for it: toolUseResult.interrupted rides the
//     tool_result entry, so it cannot see the no-tool-in-flight shape at all,
//     and all 33 tracked occurrences are false.
//   - "who wrote this entry?" — structural, via userAuthored. Phone-sent
//     prompt text round-trips into this same transcript as a type:"user"
//     entry, so the prose alone would let a client end its own turn by
//     quoting the marker (docs/protocol-mobile.md threat #1).
//
// The authorship gate is evaluated first because it is a map lookup: a genuine
// prompt, whose body can run to tens of KB, is never concatenated.
//
// Failure direction: if claude renames the prose this stops matching and the
// turn simply never ends — degrading to the bug this fixed, never to a
// spurious turn end. (The opposite direction is userAuthored's; see there.)
func isInterruptMarker(e tuidriver.JSONLEntry) bool {
	if userAuthored(e) {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(userText(e)), interruptMarkerSentinel)
}

// userAuthored reports whether a user-envelope entry was written by the human
// (a prompt) rather than by claude (a tool_result, a skill injection, the
// interruption marker). Presence of the top-level "permissionMode" key is the
// signal and its value is never read — the JSONLEntry doc names checking Raw
// directly as the idiom for presence-vs-absence semantics, and Raw costs no
// second parse of a line that may be 34 KB.
//
// The invariant this rests on, measured at #1243 spec time: claude writes
// permissionMode on user-authored prompt entries and on nothing else. In-repo,
// 3/3 prompts carry it and 34/34 claude-authored user entries (33 tool_result,
// 1 marker) do not; a 60-session scan of live transcripts added 60 prompts
// carrying it and 8 isMeta skill injections without, with no counterexample in
// either direction.
//
// This is the one part of the design that fails OPEN: if a future claude stops
// writing the field, every entry reads as claude-authored and a prompt quoting
// the marker can forge its own turn end again. That is the first thing to check
// if the "a prompt cannot forge a turn end" row ever goes red after a claude
// upgrade.
//
// A caller-constructed entry may carry a nil Raw — population is the TailJSONL
// contract, not a struct invariant. A nil-map lookup yields ok == false, so such
// an entry reads as claude-authored and is then held out by the prose gate
// alone; that is already the behaviour of every synthetic entry in this
// package's tests.
func userAuthored(e tuidriver.JSONLEntry) bool {
	_, ok := e.Raw["permissionMode"]
	return ok
}

// userText concatenates the "text" field of every type=="text" content block on
// e. Mirrors thinkingText's shape above; tuidriver.AssistantText cannot be
// reused because it gates on e.Type == "assistant". Returns "" on a nil message
// or no text content. Kept separate from thinkingText rather than folded into a
// shared block-text helper until a third caller earns the refactor.
func userText(e tuidriver.JSONLEntry) string {
	if e.Message == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range e.Message.Content {
		if c.Type != "text" {
			continue
		}
		t, _ := c.Raw["text"].(string)
		b.WriteString(t)
	}
	return b.String()
}

// messageID reads e.Message.ID, guarding a nil message. Reached only after a
// non-empty text/thinking extraction (which implies a non-nil message), so the
// guard is belt-and-suspenders.
func messageID(e tuidriver.JSONLEntry) string {
	if e.Message == nil {
		return ""
	}
	return e.Message.ID
}

// thinkingText concatenates the "thinking" field of every type=="thinking"
// content block on e. Mirrors tuidriver.AssistantText's shape (which reads
// "text" from type=="text" blocks) — tui-driver has no thinking-text helper.
// Returns "" on a nil message or no thinking content. The
// {"type":"thinking","thinking":"…"} shape is the Anthropic extended-thinking
// block, confirmed against tui-driver's JSONL fixtures.
func thinkingText(e tuidriver.JSONLEntry) string {
	if e.Message == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range e.Message.Content {
		if c.Type != "thinking" {
			continue
		}
		t, _ := c.Raw["thinking"].(string)
		b.WriteString(t)
	}
	return b.String()
}

// toolStatus maps a tool_result's is_error flag to a terminal tool status: a
// tool_result marks the call finished, so completed/failed (never pending).
func toolStatus(isError bool) turnevent.ToolStatus {
	if isError {
		return turnevent.ToolStatusFailed
	}
	return turnevent.ToolStatusCompleted
}

// toolResultContent maps a tool_result's Content union to ToolContent. Empty or
// absent content yields nil — the legal status-only ToolUpdate.
func toolResultContent(content any) turnevent.ToolContent {
	text := toolResultText(content)
	if text == "" {
		return nil
	}
	return turnevent.TextContent{Text: text}
}

// toolResultText extracts plain text from a tool_result Content union (see
// tuidriver.ToolResult): a string returns itself; a []any joins the "text"
// field of each {"type":"text",…} block; anything else returns "".
func toolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := block["type"].(string); t != "text" {
				continue
			}
			s, _ := block["text"].(string)
			b.WriteString(s)
		}
		return b.String()
	default:
		return ""
	}
}

// toolKind maps a claude tool name to its ACP kind, best-effort. Intentionally
// minimal — refinement (and deriving touched-file Locations from tool input) is
// downstream (#616). Unknown names fall to ToolKindOther.
func toolKind(name string) turnevent.ToolKind {
	switch name {
	case "Read":
		return turnevent.ToolKindRead
	case "Edit", "Write":
		return turnevent.ToolKindEdit
	case "Bash":
		return turnevent.ToolKindExecute
	case "Grep", "Glob":
		return turnevent.ToolKindSearch
	case "WebFetch":
		return turnevent.ToolKindFetch
	case "Task":
		return turnevent.ToolKindThink
	default:
		return turnevent.ToolKindOther
	}
}

// rawInput re-marshals a tool_use input map to opaque JSON for ToolStart. An
// empty/nil input → nil; a marshal error → nil (RawInput is best-effort,
// opaque pass-through the consumer never key-orders against, so re-marshalling
// sorting keys is acceptable).
func rawInput(in map[string]any) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	return b
}

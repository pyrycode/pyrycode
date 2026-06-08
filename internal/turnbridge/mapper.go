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
// map; every PTY-state kind (idle/thinking/modal/mcp/network), the stall marker,
// and Unknown drop, because the internal model (#606) has no type for them — not
// because the screen signals are worthless (ADR 025 § brittleness split).
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
		return nil, false
	default:
		return nil, false
	}
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

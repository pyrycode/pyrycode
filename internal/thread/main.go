package thread

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type mainKey struct {
	source history.SessionProvenance
	tagged bool
	scope  uint64
	turn   string
}
type mainTurn struct {
	key     mainKey
	opening uint64
	text    int
	calls   map[string]*mainCall
	end     *mainOutcome
}
type mainCall struct {
	item     int
	launcher bool
	terminal *mainOutcome
}
type mainOutcome struct {
	id            uint64
	status, field string
	raw           json.RawMessage
}

// mainIdentity also validates optional identity-loss markers on recorded facts.
type mainIdentity struct {
	TurnID     string   `json:"turn_id"`
	ToolUseID  string   `json:"tool_use_id"`
	ToolCallID string   `json:"tool_call_id"`
	Parent     string   `json:"parent_tool_use_id"`
	ParentCall string   `json:"parent_tool_call_id"`
	Opening    *uint64  `json:"turn_opened_entry_id"`
	Truncated  []string `json:"truncated_fields"`
	Dropped    []string `json:"dropped_fields"`
}
type mainRuntime struct {
	TurnID     string    `json:"turn_id"`
	ToolCallID string    `json:"tool_call_id"`
	Tool       string    `json:"tool"`
	Cause      string    `json:"cause"`
	OccurredAt time.Time `json:"occurred_at"`
}

func mainDecode(e history.Entry) (mainIdentity, string, string, bool, bool) {
	var id mainIdentity
	var summary, status string
	shown := true
	valid := false
	switch e.Type {
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		valid = decode(e.Payload, &p, "turn_id", "text")
		summary, status = p.Text, "running"
	case protocol.TypeToolUse:
		var p protocol.ToolUsePayload
		valid = decode(e.Payload, &p, "turn_id", "tool_use_id", "name") && p.Name != ""
		summary, status = p.Name+": "+p.InputSummary, "running"
	case protocol.TypeToolResult:
		var p protocol.ToolResultPayload
		valid = decode(e.Payload, &p, "turn_id", "tool_use_id", "is_error")
		status = "done"
		if p.IsError {
			status = "failed"
		}
	case protocol.TypeToolDenied:
		var p protocol.ToolDeniedPayload
		valid = decode(e.Payload, &p, "turn_id", "tool_use_id")
		status = "denied"
	case protocol.TypeTurnEnd:
		var p protocol.TurnEndPayload
		valid = decode(e.Payload, &p, "turn_id", "stop_reason")
		summary, status = p.StopReason, "done"
		if p.IsError {
			status = "failed"
		}
		shown = !(p.StopReason == "end_turn" && !p.IsError && (p.Outcome == "" || p.Outcome == "success") && (p.TerminalReason == "" || p.TerminalReason == "completed") && p.ErrorCategory == "")
	case "main_turn_opened", "main_tool_interrupted", "main_turn_interrupted":
		var p mainRuntime
		valid = decode(e.Payload, &p, "turn_id", "occurred_at") && !p.OccurredAt.IsZero()
		if e.Type != "main_turn_opened" {
			valid = valid && p.Cause != ""
		}
		summary, status = p.Cause, "interrupted"
	default:
		return id, "", "", false, false
	}
	valid = valid && decode(e.Payload, &id) && id.TurnID != "" && id.Parent == "" && id.ParentCall == ""
	if valid && id.Opening == nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(e.Payload, &fields)
		for field := range fields {
			if strings.EqualFold(field, "turn_opened_entry_id") {
				valid = false // A supplied null reference cannot become an unreferenced closure.
			}
		}
	}
	callField := "tool_use_id"
	if e.Type == "main_tool_interrupted" {
		callField = "tool_call_id"
	}
	for _, field := range []string{"turn_id", callField, "parent_tool_use_id", "parent_tool_call_id", "turn_opened_entry_id"} {
		if slices.Contains(id.Truncated, field) || slices.Contains(id.Dropped, field) {
			valid = false
		}
	}
	if e.Type == protocol.TypeToolUse || e.Type == protocol.TypeToolResult || e.Type == protocol.TypeToolDenied {
		valid = valid && id.ToolUseID != ""
	}
	if e.Type == "main_tool_interrupted" {
		valid = valid && id.ToolCallID != ""
	}
	return id, summary, status, shown, valid
}

func (f *Fold) mainWork(e history.Entry) bool {
	id, summary, status, shown, valid := mainDecode(e)
	if !valid || f.agentLaunchIsChild(e, id.ToolUseID) {
		return false
	}
	key := mainKey{scope: f.legacyScope, turn: id.TurnID}
	if e.Session != nil {
		key.source, key.tagged, key.scope = *e.Session, true, 0
	}
	st := f.turns[key]
	referenced := id.Opening != nil && (e.Type == "main_turn_interrupted" || e.Type == "main_tool_interrupted")
	if referenced {
		st = f.openings[*id.Opening]
		// A reference bypasses append-time scope, but never recorded source/turn checks.
		if st != nil && (st.key.turn != key.turn || st.key.tagged != key.tagged || st.key.source != key.source) {
			st = nil
		}
	} else {
		if st == nil || (e.Type == "main_turn_opened" && st.opening != 0) {
			st = &mainTurn{key: key, text: -1, calls: make(map[string]*mainCall)}
			f.turns[key] = st
		}
		if st.opening == 0 && e.Type != protocol.TypeTurnEnd && e.Type != "main_turn_interrupted" && e.Type != "main_tool_interrupted" {
			st.opening = e.ID
			f.openings[e.ID] = st
		}
	}
	if e.Type == "main_turn_opened" {
		return true
	}
	ending := e.Type == protocol.TypeTurnEnd || e.Type == "main_turn_interrupted"
	if ending {
		item := Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "turn_end", Turn: id.TurnID, Status: status, Summary: summary, Shown: shown}
		f.addItem(e, item)
		if st != nil && st.end == nil {
			st.end = &mainOutcome{id: e.ID, status: "interrupted", field: "ending", raw: append(json.RawMessage(nil), e.Payload...)}
			f.closeText(st, e.ID)
			for _, call := range st.calls {
				if call.terminal == nil {
					call.terminal = st.end
					f.applyTerminal(call)
				}
			}
		}
		return true
	}
	if st == nil {
		return true
	}
	if e.Type == protocol.TypeAssistantDelta {
		if st.text >= 0 {
			item := &f.items[st.text]
			var old, delta protocol.AssistantDeltaPayload
			// Both payloads were validated before storage.
			_ = json.Unmarshal(item.Content, &old)
			_ = json.Unmarshal(e.Payload, &delta)
			if delta.Text != "" {
				f.setContent(st.text, "text", old.Text+delta.Text)
				item.Rev, item.Summary = e.ID, plainSummary(old.Text+delta.Text)
				if item.Summary == "" {
					item.Summary = "assistant message"
				}
			}
		} else {
			active := st.end == nil
			if !active {
				status = "done"
			}
			index := f.addItem(e, Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "assistant_message", Turn: id.TurnID, Status: status, Active: active, Shown: shown, Summary: summary})
			st.text = index
		}
		return true
	}
	callID := id.ToolUseID
	if e.Type == "main_tool_interrupted" {
		callID = id.ToolCallID
	}
	call := st.calls[callID]
	if call == nil {
		call = &mainCall{item: -1, terminal: st.end}
		st.calls[callID] = call
	}
	if e.Type == protocol.TypeToolUse {
		f.closeText(st, e.ID)
		if call.item >= 0 || call.launcher {
			return true
		}
		var p protocol.ToolUsePayload
		_ = json.Unmarshal(e.Payload, &p)
		if (p.Name == "Agent" || p.Name == "Task") && (!key.tagged || key.source.Kind == "claude") {
			call.launcher = true
			return true
		}
		call.item = f.addItem(e, Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "tool_call", Turn: id.TurnID, Status: status, Active: true, Shown: shown, Summary: summary})
		if call.terminal == nil {
			call.terminal = st.end
		}
		f.applyTerminal(call)
	} else if call.terminal == nil {
		field := "result"
		if e.Type == protocol.TypeToolDenied {
			field = "denial"
		}
		if e.Type == "main_tool_interrupted" {
			field = "interruption"
		}
		call.terminal = &mainOutcome{id: e.ID, status: status, field: field, raw: append(json.RawMessage(nil), e.Payload...)}
		f.applyTerminal(call)
	}
	return true
}

func (f *Fold) closeText(st *mainTurn, rev uint64) {
	if st.text < 0 {
		return
	}
	item := &f.items[st.text]
	if item.Active {
		item.Status, item.Active, item.Rev = "done", false, max(item.ID, rev)
	}
	st.text = -1
}
func (f *Fold) applyTerminal(call *mainCall) {
	if call.item < 0 || call.terminal == nil || call.launcher {
		return
	}
	item := &f.items[call.item]
	item.Status, item.Active, item.Rev = call.terminal.status, false, max(item.ID, call.terminal.id)
	f.setContent(call.item, call.terminal.field, call.terminal.raw)
}
func (f *Fold) setContent(index int, key string, value any) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(f.items[index].Content, &fields)
	fields[key], _ = json.Marshal(value) // Values are strings or already-valid recorded JSON.
	f.items[index].Content, _ = json.Marshal(fields)
}

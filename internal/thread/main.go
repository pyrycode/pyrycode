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
	key      mainKey
	opening  uint64
	text     int
	textSlot int // one-based slot in Fold.openTexts; zero for untracked/child text
	calls    map[string]*mainCall
	end      *mainOutcome
}
type mainCall struct {
	shell    *agentCall
	group    agentKey
	item     int
	launcher bool
	terminal *mainOutcome
}
type mainOutcome struct {
	id, content   uint64
	status, field string
}

func (f *Fold) outcome(e history.Entry, status, field string) *mainOutcome {
	if f.source == nil {
		if f.legacyOutcomes == nil {
			f.legacyOutcomes = make(map[uint64]json.RawMessage)
		}
		f.legacyOutcomes[e.ID] = append(json.RawMessage(nil), e.Payload...)
	}
	return &mainOutcome{id: e.ID, content: e.ID, status: status, field: field}
}

// closureWorkCounts measures closure discovery and tracking maintenance.
// Only package tests enable it on their own serialized fold.
type closureWorkCounts struct{ work int }

func (f *Fold) countClosureWork(n int) {
	if f.closureCounts != nil {
		f.closureCounts.work += n
	}
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

func mainDecode(e history.Entry, counts ...*ingestionWorkCounts) (mainIdentity, string, string, bool, bool) {
	d := foldDecoder{}
	if len(counts) != 0 {
		d.counts = counts[0]
	}
	decode := d.decode
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
	valid = valid && decode(e.Payload, &id) && id.TurnID != ""
	if valid && id.Opening == nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(e.Payload, &fields)
		d.count(len(e.Payload))
		for field := range fields {
			d.count(len(field) + len("turn_opened_entry_id"))
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
	id, summary, status, shown, valid := mainDecode(e, f.ingestCounts)
	if !valid || id.Parent != "" || id.ParentCall != "" || f.agentLaunchIsChild(e, id.ToolUseID) {
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
	return f.foldWork(e, id, summary, status, shown, st, false)
}

func (f *Fold) foldWork(e history.Entry, id mainIdentity, summary, status string, shown bool, st *mainTurn, child bool) bool {
	ending := e.Type == protocol.TypeTurnEnd || e.Type == "main_turn_interrupted"
	if child && st != nil && st.text >= 0 && (ending || e.Type == protocol.TypeAssistantDelta || e.Type == protocol.TypeToolUse) {
		f.restoreChild(st.text)
	}
	if ending {
		item := Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "turn_end", Turn: id.TurnID, Status: status, Summary: summary, Shown: shown}
		if !child {
			f.addItem(e, item)
		}
		if st != nil && st.end == nil {
			st.end = f.outcome(e, "interrupted", "ending")
			if child && st.text >= 0 {
				f.setOutcome(st.text, st.end)
			}
			f.closeText(st, e.ID)
			for _, call := range st.calls {
				f.countIngestion(1)
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
			f.countPublicWork(1) // inspected append candidate, including empty deltas
			item := &f.items[st.text]
			if r := f.recipes[item.ID]; r.creating != 0 {
				if summary != "" {
					f.appendedText = st.text
					r.text = append(r.text, e.ID)
					f.recipes[item.ID] = r
					item.Rev = e.ID
					f.markAffected(st.text)
				}
				return true
			}
			if summary != "" {
				f.appendedText = st.text
				f.appendText(st.text, summary)
				item.Rev = e.ID
			}
		} else {
			active := st.end == nil
			if !active {
				status = "done"
			}
			index := f.addItem(e, Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "assistant_message", Turn: id.TurnID, Status: status, Active: active, Shown: shown, Summary: summary})
			st.text = index
			if !child {
				f.trackText(st)
			}
		}
		return true
	}
	callID := id.ToolUseID
	if e.Type == "main_tool_interrupted" {
		callID = id.ToolCallID
	}
	call := st.calls[callID]
	if call != nil && call.shell != nil && call.group != f.childGroup(e) && id.Opening == nil {
		call = nil
	}
	if call == nil {
		call = &mainCall{group: f.childGroup(e), item: -1, terminal: st.end}
		st.calls[callID] = call
	}
	if e.Type == protocol.TypeToolUse {
		f.closeText(st, e.ID)
		if call.item >= 0 || call.launcher {
			return true
		}
		var p protocol.ToolUsePayload
		_ = json.Unmarshal(e.Payload, &p)
		f.countIngestion(len(e.Payload))
		if (p.Name == "Agent" || p.Name == "Task") && (!st.key.tagged || st.key.source.Kind == "claude") {
			call.launcher = true
			return true
		}
		call.item = f.addItem(e, Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "tool_call", Turn: id.TurnID, Status: status, Active: true, Shown: shown, Summary: summary})
		if (p.Name == "Bash" || p.Name == "local_bash") && (!st.key.tagged || st.key.source.Kind == "claude") {
			f.registerShell(e, callID, call)
		} else if f.source != nil {
			f.referenceItem(call.item, e)
		}
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
		call.terminal = f.outcome(e, status, field)
		f.applyTerminal(call)
	}
	return true
}

// trackText registers a newly created main coalescing run, even if inactive.
func (f *Fold) trackText(st *mainTurn) {
	f.countClosureWork(3) // capacity check, append and slot assignment
	oldLen, oldCap := len(f.openTexts), cap(f.openTexts)
	f.openTexts = append(f.openTexts, st)
	if cap(f.openTexts) != oldCap {
		f.countClosureWork(oldLen + cap(f.openTexts)) // copying and initializing new storage
	}
	st.textSlot = len(f.openTexts)
}

func (f *Fold) closeText(st *mainTurn, rev uint64) {
	f.countClosureWork(1) // text lookup, including already closed scoped candidates
	if st.text < 0 {
		return
	}
	f.countClosureWork(1) // tracking-slot lookup, including child text
	if st.textSlot != 0 {
		f.countClosureWork(6) // slot/last indexes, comparison, pointer cleanup, truncation and slot reset
		index, last := st.textSlot-1, len(f.openTexts)-1
		if index != last {
			f.countClosureWork(3) // moved candidate lookup and both membership updates
			moved := f.openTexts[last]
			f.openTexts[index], moved.textSlot = moved, index+1
		}
		f.openTexts[last] = nil
		f.openTexts = f.openTexts[:last]
		st.textSlot = 0
	}
	f.restoreChild(st.text)
	f.countPublicWork(1) // inspected closure candidate, including inactive rows
	item := &f.items[st.text]
	if item.Active {
		item.Status, item.Active, item.Rev = "done", false, max(item.ID, rev)
		f.markAffected(st.text)
	}
	f.countClosureWork(1) // release the coalescing row reference, including inactive text
	st.text = -1
}
func (f *Fold) applyTerminal(call *mainCall) {
	if call.item < 0 || call.terminal == nil || call.launcher || (call.shell != nil && call.shell.linked) {
		return
	}
	f.restoreChild(call.item)
	f.countPublicWork(1)
	item := &f.items[call.item]
	item.Status, item.Active, item.Rev = call.terminal.status, false, max(item.ID, call.terminal.id)
	f.setOutcome(call.item, call.terminal)
}
func (f *Fold) setContent(index int, key string, value any) {
	f.markAffected(index)
	if f.recipes[f.items[index].ID].creating != 0 {
		raw, _ := json.Marshal(value)
		f.referenceField(index, key, recipeField{value: raw})
		return
	}
	if f.textEnrichment(index, key, value) {
		return
	}
	f.countIngestion(len(f.items[index].Content) * 3)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(f.items[index].Content, &fields)
	fields[key], _ = json.Marshal(value) // Values are strings or already-valid recorded JSON.
	f.items[index].Content, _ = json.Marshal(fields)
	f.countIngestion(len(f.items[index].Content) * 2)
}

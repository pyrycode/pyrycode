package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type memoryReplyKey struct {
	source history.SessionProvenance
	tagged bool
	scope  uint64
	turn   string
}
type memoryReplyLifetime struct {
	key                     memoryReplyKey
	opening, user, terminal uint64
	completed               bool
	deltas                  []uint64
}
type memoryReplyChildKey struct {
	source   history.SessionProvenance
	tagged   bool
	lifetime string
	scope    uint64
	turn     string
}
type memoryReplyEvidence struct {
	conversation string
	scope        uint64
	turns        map[memoryReplyKey]*memoryReplyLifetime
	openings     map[uint64]*memoryReplyLifetime
	pending      map[memoryReplyKey]uint64
	text         map[uint64]*memoryReplyLifetime
	children     map[memoryReplyChildKey]map[string]bool
	lifetimes    map[memoryReplyKey]string
}

func newMemoryReplyEvidence(id string) *memoryReplyEvidence {
	return &memoryReplyEvidence{conversation: id, turns: make(map[memoryReplyKey]*memoryReplyLifetime), openings: make(map[uint64]*memoryReplyLifetime), pending: make(map[memoryReplyKey]uint64), text: make(map[uint64]*memoryReplyLifetime), children: make(map[memoryReplyChildKey]map[string]bool), lifetimes: make(map[memoryReplyKey]string)}
}

type memoryReplyIdentity struct {
	Conversation string   `json:"conversation_id"`
	Turn         string   `json:"turn_id"`
	Call         string   `json:"tool_use_id"`
	RuntimeCall  string   `json:"tool_call_id"`
	Parent       string   `json:"parent_tool_use_id"`
	ParentCall   string   `json:"parent_tool_call_id"`
	Opening      *uint64  `json:"turn_opened_entry_id"`
	Truncated    []string `json:"truncated_fields"`
	Dropped      []string `json:"dropped_fields"`
}

// Decode the same recorded DTO fields as the fold, including scalar null rejection.
// Unknown fields remain inert; loss of a join identity cannot authorize a lifetime.
func memoryReplyDecode(raw json.RawMessage, dst any, required ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, dst) != nil {
		return false
	}
	for _, field := range required {
		v, ok := fields[field]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return memoryReplyFields(raw, reflect.TypeOf(dst).Elem())
}
func memoryReplyFields(raw json.RawMessage, typ reflect.Type) bool {
	if string(raw) == "null" {
		return typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return memoryReplyFields(raw, typ.Elem())
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) {
			return true
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			for key, v := range fields {
				if strings.EqualFold(key, name) && !memoryReplyFields(v, field.Type) {
					return false
				}
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, v := range values {
			if !memoryReplyFields(v, typ.Elem()) {
				return false
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, v := range values {
			if !memoryReplyFields(v, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
func (r *memoryReplyEvidence) key(e history.Entry, turn string) memoryReplyKey {
	key := memoryReplyKey{scope: r.scope, turn: turn}
	if e.Session != nil {
		key.source, key.tagged, key.scope = *e.Session, true, 0
	}
	return key
}
func memoryReplyFact(e history.Entry) (memoryReplyIdentity, bool) {
	var id memoryReplyIdentity
	if !memoryReplyDecode(e.Payload, &id) || id.Turn == "" {
		return id, false
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	for key := range fields {
		if strings.EqualFold(key, "turn_opened_entry_id") && id.Opening == nil {
			return id, false
		}
	}
	for _, field := range []string{"turn_id", "tool_use_id", "parent_tool_use_id", "parent_tool_call_id", "turn_opened_entry_id"} {
		if slices.Contains(id.Truncated, field) || slices.Contains(id.Dropped, field) {
			return id, false
		}
	}
	valid := false
	switch e.Type {
	case "assistant_delta":
		var p protocol.AssistantDeltaPayload
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "text")
	case "tool_use":
		var p protocol.ToolUsePayload
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "tool_use_id", "name") && p.Name != "" && id.Call != ""
	case "tool_result":
		var p protocol.ToolResultPayload
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "tool_use_id", "is_error") && id.Call != ""
	case "tool_denied":
		var p protocol.ToolDeniedPayload
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "tool_use_id") && id.Call != ""
	case "turn_end":
		var p protocol.TurnEndPayload
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "stop_reason")
	case "main_turn_opened", "main_turn_interrupted":
		var p struct {
			Occurred time.Time `json:"occurred_at"`
			Cause    string    `json:"cause"`
			ToolCall string    `json:"tool_call_id"`
			Tool     string    `json:"tool"`
		}
		valid = memoryReplyDecode(e.Payload, &p, "turn_id", "occurred_at") && !p.Occurred.IsZero() && (e.Type == "main_turn_opened" || p.Cause != "")
	}
	return id, valid
}

func (r *memoryReplyEvidence) childKey(e history.Entry, turn string) memoryReplyChildKey {
	source := r.key(e, "")
	source.scope = 0
	key := memoryReplyChildKey{source: source.source, tagged: source.tagged, lifetime: r.lifetimes[source], scope: r.scope, turn: turn}
	if key.lifetime != "" {
		key.scope = 0
	}
	return key
}

func (r *memoryReplyEvidence) rememberChild(key memoryReplyChildKey, call string) {
	if call == "" {
		return
	}
	if r.children[key] == nil {
		r.children[key] = make(map[string]bool)
	}
	r.children[key][call] = true
}

// Durable call ownership precedes mapped reports and is independent of turn IDs.
// Recovery references cannot replace the active source's producer lifetime.
func (r *memoryReplyEvidence) agentFact(e history.Entry) {
	if !agentHistoryType(e.Type) || (e.Session != nil && e.Session.Kind != "claude") {
		return
	}
	var p struct {
		Conversation string    `json:"conversation_id"`
		Lifetime     string    `json:"lifetime_id"`
		Call         string    `json:"tool_call_id"`
		Use          string    `json:"tool_use_id"`
		Task         string    `json:"task_id"`
		Name         string    `json:"name"`
		Tool         string    `json:"tool"`
		Turn         string    `json:"turn_id"`
		Parent       string    `json:"parent_tool_use_id"`
		ParentCall   string    `json:"parent_tool_call_id"`
		Status       string    `json:"status"`
		Cause        string    `json:"cause"`
		IsError      bool      `json:"is_error"`
		CallRef      *uint64   `json:"call_observed_entry_id"`
		TaskRef      *uint64   `json:"task_observed_entry_id"`
		At           time.Time `json:"occurred_at"`
		Truncated    []string  `json:"truncated_fields"`
		Dropped      []string  `json:"dropped_fields"`
	}
	if !memoryReplyDecode(e.Payload, &p) || p.Conversation != r.conversation || p.At.IsZero() || !conversations.ValidID(p.Lifetime) {
		return
	}
	lost := func(field string) bool {
		return slices.Contains(p.Truncated, field) || slices.Contains(p.Dropped, field)
	}
	if lost("conversation_id") || lost("lifetime_id") {
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	for field := range fields {
		if strings.EqualFold(field, "call_observed_entry_id") || strings.EqualFold(field, "task_observed_entry_id") {
			return
		}
	}
	call, task := p.Call != "" && !lost("tool_call_id"), p.Task != "" && !lost("task_id")
	valid := false
	switch e.Type {
	case historyAgentObserved:
		valid = call && (p.Tool == "Agent" || p.Tool == "Task")
	case historyAgentResult:
		valid = call && (p.Status == "completed" || p.Status == "failed")
	case historyAgentDenied:
		valid = call && p.Status == "denied"
	case historyTaskObserved, historyTaskLinked:
		valid = task
	case historyTaskOutcome:
		valid = task && p.Status != ""
	case historyTaskGone:
		valid = task && call && p.Status == "gone"
	case historyAgentSessionEnded:
		valid = (call || task) && p.Cause != ""
	}
	if !valid {
		return
	}
	source := r.key(e, "")
	source.scope = 0
	r.lifetimes[source] = p.Lifetime
	if call && (p.Parent != "" || p.ParentCall != "") && !lost("parent_tool_use_id") && !lost("parent_tool_call_id") {
		r.rememberChild(r.childKey(e, ""), p.Call)
	}
}

func (r *memoryReplyEvidence) feed(entries []history.Entry, items []thread.Item) {
	// Public fold rows certify accepted main endings and paired boundary identities.
	mainEnds := make(map[uint64]bool)
	mainTools := make(map[uint64]bool)
	boundaries := make(map[uint64]bool)
	for _, item := range items {
		if (item.Kind == "tool_call" || item.Kind == "agent") && item.Parent == 0 {
			mainTools[item.ID] = true
		}
		if item.Kind == "turn_end" && item.Parent == 0 {
			mainEnds[item.ID] = true
		}
		if item.Kind == "session_divider" {
			boundaries[item.ID] = true
		}
	}
	for _, e := range entries {
		var owner struct {
			Conversation string `json:"conversation_id"`
		}
		if !memoryReplyDecode(e.Payload, &owner) || (owner.Conversation != "" && owner.Conversation != r.conversation) {
			continue
		}
		r.agentFact(e)
		if boundaries[e.ID] {
			var p struct {
				Cause, Reason string
				Previous      string `json:"previous_session_id"`
				Next          string `json:"new_session_id"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if e.Type != "session_divider" || p.Cause != "daemon_restart" {
				r.scope++
			}
			if p.Cause != "idle_sleep" && p.Cause != "capacity_eviction" && p.Reason != "idle_evict" && p.Previous != "" && p.Next != "" && p.Previous != p.Next {
				// Only lifetimes already joined to a user retain exchange permission
				// across a logical replacement, including reuse of a recorded source.
				clear(r.pending)
			}
			continue
		}
		if e.Type == "message" {
			var p protocol.MessagePayload
			if memoryReplyDecode(e.Payload, &p, "role", "text") && p.Role == "user" && (e.Session == nil || e.Session.Kind != "none") {
				r.pending[r.key(e, "")] = e.ID
			}
			continue
		}
		id, valid := memoryReplyFact(e)
		if !valid || (e.Session != nil && e.Session.Kind == "none") {
			continue
		}
		key := r.key(e, id.Turn)
		childKey := r.childKey(e, id.Turn)
		if id.Parent != "" || id.ParentCall != "" || (e.Type == "tool_use" && !mainTools[e.ID]) {
			r.rememberChild(childKey, id.Call)
			continue
		}
		observedKey := childKey
		observedKey.turn = ""
		if e.Type != "tool_use" && (r.children[childKey][id.Call] || r.children[observedKey][id.Call]) {
			continue
		}
		source := key
		source.turn = ""
		st := r.turns[key]
		terminal := e.Type == "turn_end" || e.Type == "main_turn_interrupted"
		if terminal && !mainEnds[e.ID] {
			continue
		}
		referenced := e.Type == "main_turn_interrupted" && id.Opening != nil
		if referenced {
			st = r.openings[*id.Opening]
			if st == nil || st.key.source != key.source || st.key.tagged != key.tagged || st.key.turn != key.turn {
				continue
			}
		} else {
			if st == nil || e.Type == "main_turn_opened" {
				st = &memoryReplyLifetime{key: key}
				r.turns[key] = st
			}
			if st.opening == 0 && !terminal {
				st.opening = e.ID
				r.openings[e.ID] = st
				st.user = r.pending[source]
				delete(r.pending, source)
			}
		}
		if terminal {
			if st.terminal == 0 {
				st.terminal = e.ID
				st.completed = e.Type == "turn_end"
				if st.opening != 0 && st.user == 0 && r.pending[source] > st.opening {
					st.user = r.pending[source]
					delete(r.pending, source)
				}
				if st.opening != 0 && r.turns[key] == st {
					delete(r.pending, source)
				}
			}
			continue
		}
		if st.user == 0 && st.terminal == 0 {
			st.user = r.pending[source]
			delete(r.pending, source)
		}
		if e.Type == "assistant_delta" {
			r.text[e.ID] = st
			var p protocol.AssistantDeltaPayload
			_ = json.Unmarshal(e.Payload, &p)
			if p.Text != "" {
				st.deltas = append(st.deltas, e.ID)
			}
		}
	}
}

// A row's revision can name a tool or terminal; only recorded nonempty deltas
// within its creating-ID interval advance the exported text boundary.
func (r *memoryReplyEvidence) rows(items []thread.Item) (map[uint64]uint64, map[uint64]uint64) {
	users, last := make(map[uint64]uint64), make(map[uint64]uint64)
	next := make(map[*memoryReplyLifetime]uint64)
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		st := r.text[item.ID]
		if item.Kind != "assistant_message" || item.Parent != 0 || item.NoChild || st == nil {
			continue
		}
		end := next[st]
		next[st] = item.ID
		if !st.completed || st.user == 0 {
			continue
		}
		users[item.ID] = st.user
		at := len(st.deltas)
		if end != 0 {
			at, _ = slices.BinarySearch(st.deltas, end)
		}
		if at > 0 && st.deltas[at-1] >= item.ID {
			last[item.ID] = st.deltas[at-1]
		}
	}
	return users, last
}

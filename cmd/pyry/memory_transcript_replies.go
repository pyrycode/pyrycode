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
type memoryReplyCallOwner struct {
	child, created, shell bool
	observed              uint64
}
type memoryReplyTaskOwner struct {
	call     string
	observed uint64
}
type memoryReplyObservation struct {
	key        memoryReplyChildKey
	call, task string
}
type memoryReplyEvidence struct {
	conversation string
	scope        uint64
	turns        map[memoryReplyKey]*memoryReplyLifetime
	openings     map[uint64]*memoryReplyLifetime
	pending      map[memoryReplyKey]uint64
	text         map[uint64]*memoryReplyLifetime
	children     map[memoryReplyChildKey]map[string]bool
	owners       map[memoryReplyChildKey]map[string]*memoryReplyCallOwner
	lifetimes    map[memoryReplyKey]string
	tasks        map[memoryReplyChildKey]map[string]*memoryReplyTaskOwner
	observations map[uint64][]*memoryReplyObservation
}

func newMemoryReplyEvidence(id string) *memoryReplyEvidence {
	return &memoryReplyEvidence{conversation: id, turns: make(map[memoryReplyKey]*memoryReplyLifetime), openings: make(map[uint64]*memoryReplyLifetime), pending: make(map[memoryReplyKey]uint64), text: make(map[uint64]*memoryReplyLifetime), children: make(map[memoryReplyChildKey]map[string]bool), owners: make(map[memoryReplyChildKey]map[string]*memoryReplyCallOwner), lifetimes: make(map[memoryReplyKey]string), tasks: make(map[memoryReplyChildKey]map[string]*memoryReplyTaskOwner), observations: make(map[uint64][]*memoryReplyObservation)}
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

type memoryReplyAgentFact struct {
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
func memoryReplySource(e history.Entry) memoryReplyKey {
	var key memoryReplyKey
	if e.Session != nil {
		key.source, key.tagged = *e.Session, true
	}
	return key
}
func (r *memoryReplyEvidence) key(e history.Entry, turn string) memoryReplyKey {
	key := memoryReplySource(e)
	key.turn = turn
	if !key.tagged {
		key.scope = r.scope
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
	source := memoryReplySource(e)
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

func (r *memoryReplyEvidence) callOwner(key memoryReplyChildKey, call string) *memoryReplyCallOwner {
	if r.owners[key] == nil {
		r.owners[key] = make(map[string]*memoryReplyCallOwner)
	}
	owner := r.owners[key][call]
	if owner == nil {
		owner = &memoryReplyCallOwner{}
		r.owners[key][call] = owner
	}
	return owner
}

// Match promoteShellEvidence: pending calls and ordinary shells may move from
// the uninterrupted pre-lifetime scope. Created Agent/Task calls and collisions
// retain their original owner; turn-local ordinary child evidence never moves.
func (r *memoryReplyEvidence) promoteOwners(key memoryReplyChildKey) {
	old := key
	old.lifetime, old.scope = "", r.scope
	moved := make(map[string]bool)
	for call, owner := range r.owners[old] {
		if (owner.created && !owner.shell) || r.owners[key][call] != nil {
			continue
		}
		if r.owners[key] == nil {
			r.owners[key] = make(map[string]*memoryReplyCallOwner)
		}
		r.owners[key][call] = owner
		delete(r.owners[old], call)
		moved[call] = true
	}
	tasks := make(map[string]bool)
	for id, task := range r.tasks[old] {
		if (task.call != "" && !moved[task.call]) || r.tasks[key][id] != nil {
			continue
		}
		r.taskOwner(key, id)
		r.tasks[key][id] = task
		delete(r.tasks[old], id)
		tasks[id] = true
	}
	for _, observations := range r.observations {
		for _, obs := range observations {
			if obs.key == old && (moved[obs.call] || tasks[obs.task]) {
				obs.key = key
			}
		}
	}
}

// Launcher and shell owners are call-scoped, while ordinary child calls also
// retain their turn-local evidence. Merely observing a report occupies a call
// identity so promotion cannot overwrite an existing lifetime's owner.
func (r *memoryReplyEvidence) mappedOwner(e history.Entry, id memoryReplyIdentity) {
	if e.Session != nil && e.Session.Kind != "claude" {
		return
	}
	var fact memoryReplyAgentFact
	if !memoryReplyDecode(e.Payload, &fact) {
		return
	}
	// Mapped reports cannot carry original-observation recovery references.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	for field := range fields {
		if strings.EqualFold(field, "call_observed_entry_id") || strings.EqualFold(field, "task_observed_entry_id") {
			return
		}
	}
	key := r.childKey(e, "")
	parent := id.Parent != "" || id.ParentCall != ""
	switch e.Type {
	case "tool_use":
		var p protocol.ToolUsePayload
		_ = json.Unmarshal(e.Payload, &p)
		switch p.Name {
		case "Agent", "Task":
			owner := r.callOwner(key, id.Call)
			owner.child = owner.child || parent
			owner.created = true
			r.observe(e.ID, key, id.Call, "")
		case "Bash", "local_bash":
			owner := r.callOwner(key, id.Call)
			if !owner.created {
				owner.child, owner.created, owner.shell = parent, true, true
				r.observe(e.ID, key, id.Call, "")
			}
		}
	case "tool_result", "tool_denied":
		owner := r.callOwner(key, id.Call)
		owner.child = owner.child || parent
	}
}

func (r *memoryReplyEvidence) taskOwner(key memoryReplyChildKey, id string) *memoryReplyTaskOwner {
	if r.owners[key] == nil {
		r.owners[key] = make(map[string]*memoryReplyCallOwner)
	}
	if r.tasks[key] == nil {
		r.tasks[key] = make(map[string]*memoryReplyTaskOwner)
	}
	if r.tasks[key][id] == nil {
		r.tasks[key][id] = &memoryReplyTaskOwner{}
	}
	return r.tasks[key][id]
}

func (r *memoryReplyEvidence) observe(id uint64, key memoryReplyChildKey, call, task string) {
	var observed *uint64
	if call != "" {
		observed = &r.callOwner(key, call).observed
	} else {
		observed = &r.taskOwner(key, task).observed
	}
	if *observed == 0 {
		*observed = id
		r.observations[id] = append(r.observations[id], &memoryReplyObservation{key: key, call: call, task: task})
	}
}

func (p memoryReplyAgentFact) lost(field string) bool {
	return slices.Contains(p.Truncated, field) || slices.Contains(p.Dropped, field)
}

// Every supplied recovery reference must resolve to the same original group.
// There is no fallback to the ending's append-time lifetime or boundary scope.
func (r *memoryReplyEvidence) recoveryKey(e history.Entry, p memoryReplyAgentFact, key memoryReplyChildKey) (memoryReplyChildKey, bool, bool) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	var original *memoryReplyChildKey
	for _, ref := range []struct {
		field, identity, identityField string
		value                          *uint64
		task                           bool
	}{{"call_observed_entry_id", p.Call, "tool_call_id", p.CallRef, false}, {"task_observed_entry_id", p.Task, "task_id", p.TaskRef, true}} {
		supplied := false
		for field := range fields {
			supplied = supplied || strings.EqualFold(field, ref.field)
		}
		if !supplied {
			continue
		}
		if e.Type != historyAgentSessionEnded || ref.identity == "" || p.lost(ref.identityField) || p.lost(ref.field) || ref.value == nil || *ref.value == 0 || *ref.value >= e.ID || *ref.value > history.MaxEntryID {
			return key, true, false
		}
		var match *memoryReplyChildKey
		for _, obs := range r.observations[*ref.value] {
			identity := obs.call
			if ref.task {
				identity = obs.task
			}
			if identity == ref.identity && obs.key.source == key.source && obs.key.tagged == key.tagged && (key.lifetime == "" || key.lifetime == obs.key.lifetime) {
				match = &obs.key
				break
			}
		}
		if match == nil || (original != nil && *original != *match) {
			return key, true, false
		}
		original = match
	}
	if original != nil {
		return *original, true, true
	}
	return key, false, true
}

// Task links and recovery references are validated before any ownership mutation.
// Referenced endings enrich their original group without replacing active lifetime.
func (r *memoryReplyEvidence) agentFact(e history.Entry) {
	durable := agentHistoryType(e.Type)
	mappedLaunch := e.Type == protocol.TypeToolUse
	mappedTask := slices.Contains([]string{protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskProgress, protocol.TypeBackgroundTaskRoster}, e.Type)
	if (!durable && !mappedTask && !mappedLaunch) || (e.Session != nil && e.Session.Kind != "claude") {
		return
	}
	var p memoryReplyAgentFact
	if !memoryReplyDecode(e.Payload, &p) {
		return
	}
	source := memoryReplySource(e)
	key := r.childKey(e, "")
	if durable {
		if p.Conversation != r.conversation || p.At.IsZero() || p.lost("conversation_id") || p.lost("lifetime_id") || (p.Lifetime != "" && !conversations.ValidID(p.Lifetime)) || (p.Lifetime == "" && e.Type != historyAgentSessionEnded) {
			return
		}
		key.lifetime = p.Lifetime
		key.scope = r.scope
		if key.lifetime != "" {
			key.scope = 0
		}
	}
	key, referenced, valid := r.recoveryKey(e, p, key)
	if !valid {
		return
	}
	if mappedLaunch {
		p.Call = p.Use
	}
	call, task := p.Call != "" && !p.lost("tool_call_id"), p.Task != "" && !p.lost("task_id")
	var roster struct {
		Tasks []struct {
			Task      string   `json:"task_id"`
			Call      string   `json:"tool_call_id"`
			Truncated []string `json:"truncated_fields"`
			Dropped   []string `json:"dropped_fields"`
		} `json:"tasks"`
	}
	valid = false
	switch e.Type {
	case protocol.TypeToolUse:
		var dto protocol.ToolUsePayload
		valid = memoryReplyDecode(e.Payload, &dto, "tool_use_id", "name") && p.Use != "" && (p.Name == "Agent" || p.Name == "Task") && !p.lost("tool_use_id") && !p.lost("turn_id") && !p.lost("parent_tool_use_id") && !p.lost("parent_tool_call_id")
		call = valid
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
		valid = (call || task) && p.Cause != "" && (referenced || key.lifetime != "")
	case protocol.TypeBackgroundTaskStarted:
		var dto protocol.BackgroundTaskStartedPayload
		valid = task && memoryReplyDecode(e.Payload, &dto, "task_id")
	case protocol.TypeBackgroundTaskUpdated:
		var dto protocol.BackgroundTaskUpdatedPayload
		valid = task && memoryReplyDecode(e.Payload, &dto, "task_id")
		call = false
	case protocol.TypeBackgroundTaskProgress:
		var dto protocol.BackgroundTaskProgressPayload
		valid = task && memoryReplyDecode(e.Payload, &dto, "task_id")
		call = false
	case protocol.TypeBackgroundTaskRoster:
		var dto protocol.BackgroundTaskRosterPayload
		valid = memoryReplyDecode(e.Payload, &dto, "tasks") && memoryReplyDecode(e.Payload, &roster)
		call, task = false, false
	}
	if !valid {
		return
	}
	promote := durable && !referenced && r.lifetimes[source] == ""
	validationKey := key
	if promote && r.owners[key] == nil {
		validationKey.lifetime, validationKey.scope = "", r.scope
	}
	if e.Type == historyAgentSessionEnded && call && task {
		if linked := r.tasks[validationKey][p.Task]; linked != nil && linked.call != "" && linked.call != p.Call {
			return
		}
	}
	if promote {
		r.promoteOwners(key)
	}
	if durable && !referenced {
		r.lifetimes[source] = key.lifetime
	}
	if call && (durable || mappedLaunch) {
		owner := r.callOwner(key, p.Call)
		if (p.Parent != "" || p.ParentCall != "") && !p.lost("parent_tool_use_id") && !p.lost("parent_tool_call_id") {
			owner.child = true
		}
	}
	if e.Type == historyAgentObserved || mappedLaunch {
		if mappedLaunch {
			r.callOwner(key, p.Call).created = true
		}
		r.observe(e.ID, key, p.Call, "")
	} else if task {
		t := r.taskOwner(key, p.Task)
		if e.Type == historyTaskObserved || mappedTask {
			r.observe(e.ID, key, "", p.Task)
		}
		if call && slices.Contains([]string{historyTaskLinked, historyTaskGone, protocol.TypeBackgroundTaskStarted}, e.Type) {
			t.call = p.Call
		}
	}
	for _, row := range roster.Tasks {
		id := memoryReplyAgentFact{Truncated: row.Truncated, Dropped: row.Dropped}
		if row.Task == "" || id.lost("task_id") {
			continue
		}
		r.observe(e.ID, key, "", row.Task)
		if row.Call != "" && !id.lost("tool_call_id") {
			r.taskOwner(key, row.Task).call = row.Call
		}
	}
}

func (r *memoryReplyEvidence) feed(entries []history.Entry, items []thread.Item) {
	// Only divider acceptance needs a fold snapshot. Main facts are classified
	// using recorded ownership at this history position, independent of row reuse.
	boundaries := make(map[uint64]bool)
	for _, item := range items {
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
				clear(r.lifetimes)
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
		child := id.Parent != "" || id.ParentCall != ""
		switch e.Type {
		case "assistant_delta", "tool_use", "tool_result", "tool_denied", "turn_end":
			childKey := r.childKey(e, id.Turn)
			observedKey := childKey
			observedKey.turn = ""
			callOwner := r.owners[observedKey][id.Call]
			child = child || r.children[childKey][id.Call] || (callOwner != nil && callOwner.child)
			// Consult prior ownership before recording mapped work, as childWork does.
			// Main runtime facts retain turn authority regardless of incidental call IDs.
			r.mappedOwner(e, id)
			if child {
				r.rememberChild(childKey, id.Call)
			}
		}
		if child {
			continue
		}
		source := key
		source.turn = ""
		st := r.turns[key]
		terminal := e.Type == "turn_end" || e.Type == "main_turn_interrupted"
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

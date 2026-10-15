package thread

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type agentKey struct {
	source   history.SessionProvenance
	tagged   bool
	lifetime string
	scope    uint64
}
type agentGroup struct {
	key     agentKey
	calls   map[string]*agentCall
	tasks   map[string]*agentTask
	counts  *agentWorkCounts
	taskIDs map[string][]string
}

// agentWorkCounts counts inspected candidates, lookups and index maintenance.
// Only package tests enable it on their own serialized fold.
type agentWorkCounts struct{ work, promotions int }

func (g *agentGroup) count(n int) {
	if g.counts != nil {
		g.counts.work += n
	}
}

type agentCall struct {
	ordinary     *mainCall
	linked       bool
	parent       string
	observed     uint64
	item         int
	launch       uint64
	launchResult bool
	reports      []*agentReport
}
type agentTask struct {
	observed   uint64
	linkMapped bool
	call       string
	link       uint64
	reports    []*agentReport
}
type agentReport struct {
	id              uint64
	content         uint64
	turn            string
	foreground      bool
	status, field   string
	digest          [sha256.Size]byte
	durable, mapped bool
}
type agentObservation struct {
	group      *agentGroup
	call, task string
}
type agentFact struct {
	ConversationID string    `json:"conversation_id"`
	Lifetime       string    `json:"lifetime_id"`
	Call           string    `json:"tool_call_id"`
	Use            string    `json:"tool_use_id"`
	Task           string    `json:"task_id"`
	Name           string    `json:"name"`
	Tool           string    `json:"tool"`
	Turn           string    `json:"turn_id"`
	Parent         string    `json:"parent_tool_use_id"`
	ParentCall     string    `json:"parent_tool_call_id"`
	Status         string    `json:"status"`
	Cause          string    `json:"cause"`
	IsError        bool      `json:"is_error"`
	CallRef        *uint64   `json:"call_observed_entry_id"`
	TaskRef        *uint64   `json:"task_observed_entry_id"`
	At             time.Time `json:"occurred_at"`
	Truncated      []string  `json:"truncated_fields"`
	Dropped        []string  `json:"dropped_fields"`
}

func (p agentFact) usable(id, field string) bool {
	return id != "" && !p.lost(field)
}

func (p agentFact) lost(fields ...string) bool {
	for _, field := range fields {
		if slices.Contains(p.Truncated, field) || slices.Contains(p.Dropped, field) {
			return true
		}
	}
	return false
}

// agentLaunchIsChild consults only the launch's recorded source and active
// lifetime or legacy scope. Saved parent evidence cannot cross those boundaries.
func (f *Fold) agentLaunchIsChild(e history.Entry, call string) bool {
	decode := f.decode
	var p protocol.ToolUsePayload
	if e.Type != protocol.TypeToolUse || !decode(e.Payload, &p) || (p.Name != "Agent" && p.Name != "Task") {
		return false
	}
	key := agentKey{scope: f.legacyScope}
	if e.Session != nil {
		key.source, key.tagged = *e.Session, true
	}
	source := key
	source.scope = 0
	key.lifetime = f.agentLifetimes[source]
	if key.lifetime != "" {
		key.scope = 0
	}
	g := f.agentGroups[key]
	return g != nil && g.calls[call] != nil && g.calls[call].parent != ""
}

// agentWork preserves results separately from task endings so late links can
// reclassify provisional foreground completion without changing item identity.
func (f *Fold) agentWork(e history.Entry) bool {
	decode := f.decode
	durable := strings.HasPrefix(e.Type, "agent_call_") || e.Type == "agent_ended_with_session" || slices.Contains([]string{"background_task_observed", "background_task_linked", "background_task_outcome", "background_task_gone"}, e.Type)
	mapped := slices.Contains([]string{protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskProgress, protocol.TypeBackgroundTaskRoster}, e.Type)
	if !durable && !mapped {
		return false
	}
	if f.agentCounts != nil {
		f.agentCounts.work += 2
	}
	if e.Session != nil && e.Session.Kind != "claude" {
		return durable
	}
	var p agentFact
	if !decode(e.Payload, &p) {
		return durable
	}
	toolReport := e.Type == protocol.TypeToolUse || e.Type == protocol.TypeToolResult || e.Type == protocol.TypeToolDenied
	if toolReport && p.lost("turn_id", "parent_tool_use_id", "parent_tool_call_id") {
		return false
	}
	var roster struct {
		Tasks []struct {
			Task      string   `json:"task_id"`
			Call      string   `json:"tool_call_id"`
			Truncated []string `json:"truncated_fields"`
			Dropped   []string `json:"dropped_fields"`
		} `json:"tasks"`
	}
	key := agentKey{scope: f.legacyScope}
	if e.Session != nil {
		key.source, key.tagged = *e.Session, true
	}
	source := key
	source.scope = 0
	if durable {
		if p.ConversationID != f.conversationID || p.At.IsZero() || p.lost("conversation_id", "lifetime_id") || (p.Lifetime != "" && !conversations.ValidID(p.Lifetime)) {
			return true
		}
		if e.Type != "agent_ended_with_session" && p.Lifetime == "" {
			return true
		}
	} else {
		p.Lifetime = f.agentLifetimes[source]
	}
	key.lifetime = p.Lifetime
	if key.lifetime != "" {
		key.scope = 0
	}
	// Supplied references must all resolve to the original observation, even
	// when the recovery fact has a usable lifetime or a different append scope.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	f.countIngestion(len(e.Payload))
	referenced := false
	var original *agentGroup
	for _, ref := range []struct {
		field, id string
		value     *uint64
		task      bool
	}{{"call_observed_entry_id", p.Call, p.CallRef, false}, {"task_observed_entry_id", p.Task, p.TaskRef, true}} {
		supplied := false
		for field := range fields {
			if strings.EqualFold(field, ref.field) {
				supplied = true
			}
		}
		if !supplied {
			continue
		}
		referenced = true
		identityField := "tool_call_id"
		if ref.task {
			identityField = "task_id"
		}
		if !p.usable(ref.id, identityField) || slices.Contains(p.Truncated, ref.field) || slices.Contains(p.Dropped, ref.field) || e.Type != "agent_ended_with_session" || ref.value == nil || *ref.value == 0 || *ref.value >= e.ID || *ref.value > history.MaxEntryID {
			return true
		}
		var match *agentGroup
		if f.agentCounts != nil {
			f.agentCounts.work++
		}
		for _, obs := range f.agentObservations[*ref.value] {
			if f.agentCounts != nil {
				f.agentCounts.work++
			}
			identity := obs.call
			if ref.task {
				identity = obs.task
			}
			k := obs.group.key
			if identity == ref.id && ref.id != "" && k.source == key.source && k.tagged == key.tagged && (key.lifetime == "" || key.lifetime == k.lifetime) {
				match = obs.group
				break
			}
		}
		if match == nil || (original != nil && original != match) {
			return true
		}
		original = match
	}
	if referenced {
		key = original.key
	}
	call := ""
	if toolReport {
		call = p.Use
	} else if durable || e.Type == protocol.TypeBackgroundTaskStarted {
		call = p.Call
	}
	callField := "tool_call_id"
	if toolReport {
		callField = "tool_use_id"
	}
	if !p.usable(call, callField) {
		call = ""
	}
	if !p.usable(p.Task, "task_id") {
		p.Task = ""
	}
	typ := e.Type
	switch typ {
	case protocol.TypeToolUse:
		var dto protocol.ToolUsePayload
		if !decode(e.Payload, &dto, "tool_use_id", "name") || call == "" || (p.Name != "Agent" && p.Name != "Task") {
			return false
		}
	case protocol.TypeToolResult:
		var dto protocol.ToolResultPayload
		if !decode(e.Payload, &dto, "tool_use_id", "is_error") || call == "" {
			return false
		}
		p.Status = "completed"
		if p.IsError {
			p.Status = "failed"
		}
	case protocol.TypeToolDenied:
		var dto protocol.ToolDeniedPayload
		if !decode(e.Payload, &dto, "tool_use_id") || call == "" {
			return false
		}
		p.Status = "denied"
	case "agent_call_observed":
		if call == "" || (p.Tool != "Agent" && p.Tool != "Task") {
			return true
		}
	case "agent_call_result":
		if call == "" || (p.Status != "completed" && p.Status != "failed") {
			return true
		}
	case "agent_call_denied":
		if call == "" || p.Status != "denied" {
			return true
		}
	case protocol.TypeBackgroundTaskStarted:
		var dto protocol.BackgroundTaskStartedPayload
		if !decode(e.Payload, &dto, "task_id") || p.Task == "" {
			return false
		}
	case protocol.TypeBackgroundTaskUpdated:
		var dto protocol.BackgroundTaskUpdatedPayload
		if !decode(e.Payload, &dto, "task_id") || p.Task == "" {
			return false
		}
	case protocol.TypeBackgroundTaskProgress:
		var dto protocol.BackgroundTaskProgressPayload
		if !decode(e.Payload, &dto, "task_id") || p.Task == "" {
			return false
		}
	case "background_task_observed", "background_task_outcome":
		if p.Task == "" || (typ == "background_task_outcome" && p.Status == "") {
			return durable
		}
	case "background_task_linked", "background_task_gone":
		if p.Task == "" {
			return true
		}
		if typ == "background_task_gone" && (call == "" || p.Status != "gone") {
			return true
		}
	case "agent_ended_with_session":
		if p.Cause == "" || (call == "" && p.Task == "") || (!referenced && key.lifetime == "") {
			return true
		}
		p.Status = "ended_with_session"
	case protocol.TypeBackgroundTaskRoster:
		var dto protocol.BackgroundTaskRosterPayload
		if !decode(e.Payload, &dto, "tasks") || !decode(e.Payload, &roster) {
			return false
		}
		usable := roster.Tasks[:0]
		for _, row := range roster.Tasks {
			if f.agentCounts != nil {
				f.agentCounts.work++
			}
			id := agentFact{Truncated: row.Truncated, Dropped: row.Dropped}
			if id.usable(row.Task, "task_id") {
				usable = append(usable, row)
			}
		}
		roster.Tasks = usable
		if len(roster.Tasks) == 0 {
			return false
		}
	default:
		return durable
	}
	promote := durable && !referenced && key.lifetime != "" && f.agentLifetimes[source] == ""
	g := f.agentGroups[key]
	if promote && g == nil {
		original := source
		original.scope = f.legacyScope
		g = f.agentGroups[original]
	}
	if typ == "agent_ended_with_session" && g != nil && p.Task != "" && call != "" {
		if f.agentCounts != nil {
			f.agentCounts.work++
		}
		if task := g.tasks[p.Task]; task != nil && task.call != "" && task.call != call {
			return true
		}
	}
	if f.source == nil {
		f.outcome(e, "", "") // Legacy-only evidence for the shared renderer.
	}
	var promoted []string
	if promote {
		promoted = f.promoteShellEvidence(key)
		g = f.agentGroups[key]
	}
	if durable && !referenced {
		f.agentLifetimes[source] = key.lifetime
	}
	if g == nil {
		g = &agentGroup{key: key, calls: map[string]*agentCall{}, tasks: map[string]*agentTask{}}
		f.agentGroups[key] = g
	}
	g.counts = f.agentCounts
	affected := make(map[string]struct{})
	mark := func(id string) {
		g.count(1)
		if id != "" {
			affected[id] = struct{}{}
		}
	}
	mark(call)
	for _, id := range promoted {
		mark(id)
	}
	if call != "" && (durable || toolReport) {
		parent, field := p.ParentCall, "parent_tool_call_id"
		if parent == "" {
			parent, field = p.Parent, "parent_tool_use_id"
		}
		c := g.call(call)
		if p.usable(parent, field) && !p.lost("parent_tool_use_id", "parent_tool_call_id") {
			c.parent = parent
		}
	}
	observe := func(call, task string) {
		if call != "" {
			c := g.call(call)
			if c.observed != 0 {
				return
			}
			c.observed = e.ID
		}
		if task != "" {
			t := g.task(task)
			if t.observed != 0 {
				return
			}
			t.observed = e.ID
		}
		f.agentObservations[e.ID] = append(f.agentObservations[e.ID], agentObservation{g, call, task})
	}
	switch typ {
	case "agent_call_observed", protocol.TypeToolUse:
		c := g.call(call)
		observe(call, "")
		if typ == protocol.TypeToolUse && c.item < 0 {
			var dto protocol.ToolUsePayload
			_ = json.Unmarshal(e.Payload, &dto)
			f.countIngestion(len(e.Payload))
			c.launch = e.ID
			_, c.launchResult = fields["result"]
			c.item = f.addItem(e, Item{ID: e.ID, Order: e.ID, Rev: e.ID, Kind: "agent", Turn: p.Turn, Status: "running", Active: true, Shown: true, Summary: p.Name + ": " + dto.InputSummary})
			f.childAgents[c.item] = childParentKey{key, call}
			f.scheduleAgentChildren(c.item)
			if c.parent != "" {
				f.saveChild(c.item, key, c.parent)
			}
		}
	case "agent_call_result", protocol.TypeToolResult, "agent_call_denied", protocol.TypeToolDenied:
		field := "result"
		if p.Status == "denied" {
			field = "denial"
		}
		g.addReport(&g.call(call).reports, e, p.Status, field, durable)
	case protocol.TypeBackgroundTaskRoster:
		for _, row := range roster.Tasks {
			task := g.task(row.Task)
			mark(task.call)
			observe("", row.Task)
			id := agentFact{Truncated: row.Truncated, Dropped: row.Dropped}
			if id.usable(row.Call, "tool_call_id") {
				g.associate(row.Task, task, row.Call)
				mark(row.Call)
				if task.link == 0 || (!durable && !task.linkMapped) {
					task.linkMapped = !durable
					task.link = e.ID
				}
			}
		}
	default:
		if p.Task != "" {
			task := g.task(p.Task)
			mark(task.call)
			if typ == "background_task_observed" || !durable {
				observe("", p.Task)
			}
			if call != "" && (typ == "background_task_linked" || typ == protocol.TypeBackgroundTaskStarted || typ == "background_task_gone") {
				g.associate(p.Task, task, call)
				mark(call)
				if task.link == 0 || (!durable && !task.linkMapped) {
					task.linkMapped = !durable
					task.link = e.ID
				}
			}
			if typ == "background_task_outcome" || typ == "background_task_gone" || typ == "agent_ended_with_session" || typ == protocol.TypeBackgroundTaskUpdated {
				field := "task_report"
				if typ == "agent_ended_with_session" || typ == "background_task_gone" {
					field = "ending"
				}
				g.addReport(&task.reports, e, p.Status, field, durable)
			}
		}
		if typ == "agent_ended_with_session" && call != "" {
			g.addReport(&g.call(call).reports, e, p.Status, "ending", true)
		}
	}
	for id := range affected {
		g.count(2)
		if c := g.calls[id]; c != nil {
			f.applyAgent(g, id, c, e.ID)
		}
	}
	return durable || (typ == protocol.TypeToolUse && (p.Name == "Agent" || p.Name == "Task"))
}
func (g *agentGroup) call(id string) *agentCall {
	g.count(1)
	c := g.calls[id]
	if c == nil {
		c = &agentCall{item: -1}
		g.calls[id] = c
	}
	return c
}
func (g *agentGroup) task(id string) *agentTask {
	g.count(1)
	t := g.tasks[id]
	if t == nil {
		t = &agentTask{}
		g.tasks[id] = t
	}
	return t
}

// associate maintains lexical task order within one scoped call ownership.
func (g *agentGroup) associate(id string, task *agentTask, call string) {
	if task.call == call {
		return
	}
	g.removeTaskID(task.call, id)
	task.call = call
	g.insertTaskID(call, id)
}

func (g *agentGroup) taskPosition(ids []string, id string) (int, bool) {
	return slices.BinarySearchFunc(ids, id, func(a, b string) int {
		g.count(1)
		return strings.Compare(a, b)
	})
}

func (g *agentGroup) removeTaskID(call, id string) {
	if call == "" {
		return
	}
	g.count(1)
	ids := g.taskIDs[call]
	if i, found := g.taskPosition(ids, id); found {
		g.count(len(ids) - i)
		ids = slices.Delete(ids, i, i+1)
		if len(ids) == 0 {
			delete(g.taskIDs, call)
		} else {
			g.taskIDs[call] = ids
		}
	}
}

func (g *agentGroup) insertTaskID(call, id string) {
	if call == "" {
		return
	}
	if g.taskIDs == nil {
		g.taskIDs = make(map[string][]string)
	}
	g.count(1)
	ids := g.taskIDs[call]
	if i, found := g.taskPosition(ids, id); !found {
		g.count(len(ids) - i + 1)
		g.taskIDs[call] = slices.Insert(ids, i, id)
	}
}

// Companion evidence counts once; the earliest entry owns terminal order while
// the mapped report supplies content. Equal later reports do not replace it.
func (g *agentGroup) addReport(reports *[]*agentReport, e history.Entry, status, field string, durable bool) {
	// Fingerprint the embedded RawMessage encoding once on arrival. Repeated
	// equal enrichment reuses its source ID without retaining any payload bytes.
	var compact, escaped bytes.Buffer
	_ = json.Compact(&compact, e.Payload) // agentWork validated the object.
	json.HTMLEscape(&escaped, compact.Bytes())
	digest := sha256.Sum256(escaped.Bytes())
	content := e.ID
	for _, r := range *reports {
		g.count(1)
		if r.field == field && r.status == status && r.digest == digest && ((!durable && r.mapped) || (durable && r.durable && !r.mapped)) {
			content = r.content
			break
		}
	}
	var turn string
	var foreground bool
	if !durable && (field == "result" || field == "denial") {
		id, _, _, _, valid := mainDecode(e)
		turn, foreground = id.TurnID, valid
	}
	for _, r := range *reports {
		g.count(1)
		if r.status == status && r.field == field && ((durable && r.mapped && !r.durable) || (!durable && r.durable && !r.mapped)) {
			r.id = min(r.id, e.ID)
			r.durable, r.mapped = true, true
			if !durable {
				r.content, r.turn, r.foreground, r.digest = content, turn, foreground, digest
			}
			return
		}
	}
	*reports = append(*reports, &agentReport{id: e.ID, content: content, turn: turn, foreground: foreground, status: status, field: field, digest: digest, durable: durable, mapped: !durable})
}
func agentTerminal(status string) bool {
	return status != "" && status != "stopping"
}
func (f *Fold) applyAgent(g *agentGroup, id string, c *agentCall, rev uint64) {
	g.count(1)
	if c.item < 0 {
		return
	}
	// Recompute intrinsic lifecycle state without disturbing unrelated derived closure.
	item := &f.items[c.item]
	previous := f.recipes[item.ID]
	if state := f.children[c.item]; state != nil && !state.restored {
		item = &state.base
		previous = state.baseRecipe
	}
	recipe := contentRecipe{creating: c.launch, agent: true, fields: make(map[string]recipeField)}
	put := func(key string, value any) {
		raw, _ := json.Marshal(value)
		recipe.fields[key] = recipeField{value: raw}
	}
	entry := func(key string, id uint64) {
		recipe.fields[key] = recipeField{entry: id}
	}
	if c.parent != "" {
		put("parent_tool_call_id", c.parent)
	}
	linked := false
	var reports []*agentReport
	g.count(1)
	for _, taskID := range g.taskIDs[id] {
		g.count(2)
		task := g.tasks[taskID]
		linked = true
		put("task_id", taskID)
		if task.link != 0 {
			entry("task_link", task.link)
		} else {
			put("task_link", nil)
		}
		reports = append(reports, task.reports...)
		g.count(len(task.reports))
	}
	c.linked = linked
	if c.ordinary != nil && !linked {
		f.applyShellForeground(c)
		return
	}
	// Entry ordering, rather than map iteration, decides both enrichment and finals.
	reports = append(reports, c.reports...)
	g.count(len(c.reports))
	slices.SortStableFunc(reports, func(a, b *agentReport) int {
		g.count(2)
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	status, active, ended := "running", true, uint64(0)
	var final *agentReport
	for _, r := range reports {
		g.count(1)
		if c.ordinary != nil && r.mapped && !shellReportMatches(r, item.Turn) {
			continue
		}
		if r.field == "result" {
			if _, ok := recipe.fields["result"]; !ok && !c.launchResult {
				entry("result", r.content)
			}
			if linked {
				continue
			}
		}
		if agentTerminal(r.status) {
			if final == nil {
				final = r
			}
			continue
		}
		if final == nil && r.status == "stopping" {
			status = "stopping"
		}
		if r.field == "task_report" && r.mapped {
			entry("task_report", r.content)
		}
	}
	if final != nil {
		status, active, ended = final.status, false, max(item.ID, final.id)
		if status == "completed" {
			status = "finished"
		}
		entry(final.field, final.content)
		// Status-empty reports can enrich content after an ending without ending anew.
		for _, r := range reports {
			g.count(1)
			if r.field == "task_report" && r.status == "" && r.mapped {
				entry("task_update", r.content)
			}
		}
	}
	if ended != 0 {
		put("ended_order", ended)
	}
	changed := !reflect.DeepEqual(previous, recipe)
	var raw json.RawMessage
	if f.source == nil {
		rendered, err := renderShared([]Item{*item}, map[uint64]contentRecipe{item.ID: recipe}, legacySource(f.legacyOutcomes), nil)
		if err != nil {
			return // Legacy evidence was validated and copied on arrival.
		}
		raw = rendered[0].Content
		changed = !bytes.Equal(item.Content, raw)
	}
	if changed || item.Status != status || item.Active != active || item.EndedOrder != ended {
		f.restoreChild(c.item)
		if c.ordinary == nil && c.parent != "" {
			f.saveChild(c.item, g.key, c.parent)
		}
		target := &f.items[c.item]
		if f.source != nil {
			f.recipes[target.ID] = recipe
		}
		target.Content, target.Status, target.Active, target.EndedOrder, target.Rev = raw, status, active, ended, max(item.ID, rev)
		f.markAffected(c.item)
		if c.ordinary == nil {
			f.scheduleAgentChildren(c.item)
		}
	}
}

package thread

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/history"
)

// registerShell shares lifecycle evidence with the ordinary call while keeping
// its creating item, visibility, attribution and foreground terminal semantics.
func (f *Fold) registerShell(e history.Entry, id string, call *mainCall) {
	key := call.group
	g := f.agentGroups[key]
	if g == nil {
		g = &agentGroup{key: key, calls: map[string]*agentCall{}, tasks: map[string]*agentTask{}}
		f.agentGroups[key] = g
	}
	c := g.call(id)
	if c.item >= 0 {
		return
	}
	c.ordinary, call.shell = call, c
	c.item, c.launch = call.item, append(json.RawMessage(nil), e.Payload...)
	var p agentFact
	_ = json.Unmarshal(e.Payload, &p)
	c.parent = p.ParentCall
	if c.parent == "" {
		c.parent = p.Parent
	}
	if c.observed == 0 {
		c.observed = e.ID
		f.agentObservations[e.ID] = append(f.agentObservations[e.ID], agentObservation{g, id, ""})
	}
	f.applyAgent(g, id, c, e.ID)
}

// The first saved lifetime may arrive after ordinary shell evidence. Transfer
// only evidence retained in this source's uninterrupted pre-lifetime scope;
// established lifetimes and boundary-separated predecessors are never adopted.
func (f *Fold) promoteShellEvidence(key agentKey) {
	oldKey := key
	oldKey.lifetime = ""
	oldKey.scope = f.legacyScope
	old := f.agentGroups[oldKey]
	if old == nil {
		return
	}
	g := f.agentGroups[key]
	if g == nil {
		g = &agentGroup{key: key, calls: map[string]*agentCall{}, tasks: map[string]*agentTask{}}
		f.agentGroups[key] = g
	}
	moved := make(map[string]bool)
	for id, c := range old.calls {
		if c.ordinary == nil && c.item >= 0 {
			continue
		}
		if g.calls[id] != nil {
			continue
		}
		g.calls[id] = c
		delete(old.calls, id)
		moved[id] = true
		if c.ordinary != nil {
			c.ordinary.group = key
		}
	}
	tasks := make(map[string]bool)
	for id, task := range old.tasks {
		if task.call != "" && !moved[task.call] {
			continue
		}
		if g.tasks[id] != nil {
			continue
		}
		g.tasks[id] = task
		delete(old.tasks, id)
		tasks[id] = true
	}
	for id, observations := range f.agentObservations {
		for i := range observations {
			obs := &observations[i]
			if obs.group == old && (moved[obs.call] || tasks[obs.task]) {
				obs.group = g
			}
		}
		f.agentObservations[id] = observations
	}
	for _, turn := range f.turns {
		for _, call := range turn.calls {
			if call.item < 0 && call.group == oldKey {
				call.group = key
			}
		}
	}
}

// Ordinary mapped results keep the source turn validation used by mainWork;
// lifecycle reports have no main-turn identity and remain independently scoped.
func shellReportMatches(r *agentReport, turn string) bool {
	typ := "tool_result"
	if r.field == "denial" {
		typ = "tool_denied"
	} else if r.field != "result" {
		return true
	}
	id, _, _, _, valid := mainDecode(history.Entry{Type: typ, Payload: r.raw})
	return valid && id.TurnID == turn
}

// A lifetime may be learned after a child creation. Its retained ordinary owner
// still accepts foreground results, without replacing its original parent lane.
func (f *Fold) applyShellForeground(c *agentCall) {
	if c.ordinary.terminal != nil {
		return
	}
	var first *agentReport
	for _, r := range c.reports {
		if r.mapped && (r.field == "result" || r.field == "denial") && shellReportMatches(r, f.items[c.item].Turn) && (first == nil || r.id < first.id) {
			first = r
		}
	}
	if first == nil {
		return
	}
	status := first.status
	if status == "completed" {
		status = "done"
	}
	c.ordinary.terminal = &mainOutcome{id: first.id, status: status, field: first.field, raw: first.raw}
	f.applyTerminal(c.ordinary)
}

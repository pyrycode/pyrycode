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
	g.counts = f.agentCounts
	c := g.call(id)
	if c.item >= 0 {
		return
	}
	c.ordinary, call.shell = call, c
	c.item, c.launch = call.item, e.ID
	if f.source == nil {
		f.outcome(e, "", "")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(e.Payload, &fields)
	_, c.launchResult = fields["result"]
	var p agentFact
	_ = json.Unmarshal(e.Payload, &p)
	f.countIngestion(len(e.Payload))
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
func (f *Fold) promoteShellEvidence(key agentKey) []string {
	if f.agentCounts != nil {
		f.agentCounts.promotions++
		f.agentCounts.work++
	}
	oldKey := key
	oldKey.lifetime = ""
	oldKey.scope = f.legacyScope
	old := f.agentGroups[oldKey]
	if old == nil {
		return nil
	}
	old.counts = f.agentCounts
	g := f.agentGroups[key]
	if g == nil {
		g = &agentGroup{key: key, calls: map[string]*agentCall{}, tasks: map[string]*agentTask{}}
		f.agentGroups[key] = g
	}
	g.counts = f.agentCounts
	moved := make(map[string]bool)
	var affected []string
	for id, c := range old.calls {
		g.count(1)
		if c.ordinary == nil && c.item >= 0 {
			continue
		}
		g.count(1)
		if g.calls[id] != nil {
			continue
		}
		g.calls[id] = c
		delete(old.calls, id)
		moved[id] = true
		affected = append(affected, id)
		if c.ordinary != nil {
			c.ordinary.group = key
		}
	}
	tasks := make(map[string]bool)
	for id, task := range old.tasks {
		g.count(2)
		if task.call != "" && !moved[task.call] {
			continue
		}
		g.count(1)
		if g.tasks[id] != nil {
			continue
		}
		g.tasks[id] = task
		old.removeTaskID(task.call, id)
		g.insertTaskID(task.call, id)
		delete(old.tasks, id)
		tasks[id] = true
	}
	for id, observations := range f.agentObservations {
		g.count(1)
		for i := range observations {
			g.count(3)
			obs := &observations[i]
			if obs.group == old && (moved[obs.call] || tasks[obs.task]) {
				obs.group = g
			}
		}
		f.agentObservations[id] = observations
	}
	for _, turn := range f.turns {
		g.count(1)
		for _, call := range turn.calls {
			g.count(1)
			if call.item < 0 && call.group == oldKey {
				call.group = key
			}
		}
	}
	return affected
}

// Ordinary mapped results keep the source turn validation used by mainWork;
// lifecycle reports have no main-turn identity and remain independently scoped.
func shellReportMatches(r *agentReport, turn string) bool {
	if r.field != "denial" && r.field != "result" {
		return true
	}
	return r.foreground && r.turn == turn
}

// A lifetime may be learned after a child creation. Its retained ordinary owner
// still accepts foreground results, without replacing its original parent lane.
func (f *Fold) applyShellForeground(c *agentCall) {
	if c.ordinary.terminal != nil {
		return
	}
	var first *agentReport
	for _, r := range c.reports.entries() {
		if f.agentCounts != nil {
			f.agentCounts.work++
		}
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
	outcome := &mainOutcome{id: first.id, content: first.content, status: status, field: first.field}
	c.ordinary.terminal = outcome
	f.applyTerminal(c.ordinary)
}

package thread

import (
	"cmp"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/pyrycode/pyrycode/internal/history"
)

// ExportCheckpoint returns two detached JSON-compatible parts without reading
// history or rendering content. The owner must serialize access and call it at a
// completed Feed boundary. Inline folds made with New do not support export.
func (f *Fold) ExportCheckpoint() (CheckpointState, []CheckpointItem, error) {
	if f.source == nil {
		return CheckpointState{}, nil, errors.New("thread: checkpoint requires reference fold")
	}
	x := checkpointExporter{}
	d := CheckpointState{
		Schema: checkpointSchema, Rules: foldingRules, ConversationID: f.conversationID,
		Version: f.version, LastShown: f.lastShown, Suppressed: f.suppressed, UnprovenThrough: f.unprovenThrough,
		Published: maps.Clone(f.published), Touches: maps.Clone(f.touches), PublicVisible: maps.Clone(f.publicVisible),
		Successor: checkpointProvenance(f.successor), LastBoundary: f.lastBoundary, LegacyScope: f.legacyScope,
		Accepted: maps.Clone(f.accepted), Messages: maps.Clone(f.messages), Openings: make(map[uint64]int),
		Children: make(map[int]checkpointChild), ChildEndings: make(map[int]checkpointEnding), Observations: make(map[uint64][]checkpointObservation),
	}
	var records []CheckpointItem
	if len(f.items) > 0 {
		d.ItemIDs = make([]uint64, 0, len(f.items))
		records = make([]CheckpointItem, 0, len(f.items))
	}
	for _, item := range f.items {
		d.ItemIDs = append(d.ItemIDs, item.ID)
		records = append(records, checkpointRecord(item, f.recipes[item.ID]))
	}
	for _, key := range checkpointKeys(f.pending) {
		d.PendingBoundaries = append(d.PendingBoundaries, checkpointBoundary{key.at, key.previous, key.next, key.reason, slices.Clone(f.pending[key])})
	}
	for _, key := range checkpointKeys(f.turns) {
		d.CurrentTurns = append(d.CurrentTurns, checkpointTurnRef{checkpointMainKey(key), x.turn(f.turns[key])})
	}
	for _, id := range checkpointKeys(f.openings) {
		d.Openings[id] = x.turn(f.openings[id])
	}
	for _, turn := range f.openTexts {
		d.OpenTexts = append(d.OpenTexts, x.turn(turn))
	}
	for _, key := range checkpointKeys(f.childTurns) {
		d.ChildTurns = append(d.ChildTurns, checkpointChildTurn{checkpointAgentKey(key.group), key.parent, key.turn, x.turn(f.childTurns[key])})
	}
	for _, key := range checkpointKeys(f.childCalls) {
		call := f.childCalls[key]
		d.ChildCalls = append(d.ChildCalls, checkpointChildCall{checkpointAgentKey(key.group), key.turn, key.call, call.parent, x.mainCall(call.call)})
	}
	for _, key := range checkpointKeys(f.childReports) {
		d.ChildReports = append(d.ChildReports, checkpointChildCall{checkpointAgentKey(key.group), key.turn, key.call, "", x.outcome(f.childReports[key])})
	}
	for index, child := range f.children {
		c := checkpointChild{Group: checkpointAgentKey(child.group), Parent: child.parent, Final: child.final}
		if s := child.childSnapshot; s != nil {
			c.Snapshot = &checkpointSnapshot{checkpointRecord(s.base, s.baseRecipe), checkpointRecord(s.previous, s.previousRecipe), s.restored}
		}
		d.Children[index] = c
	}
	for index, ending := range f.childEndings {
		d.ChildEndings[index] = checkpointEnding{ending.item, ending.revision}
	}
	for _, key := range checkpointKeys(f.agentGroups) {
		x.group(f.agentGroups[key])
	}
	for _, key := range checkpointKeys(f.agentLifetimes) {
		d.Lifetimes = append(d.Lifetimes, checkpointLifetime{checkpointAgentKey(key), f.agentLifetimes[key]})
	}
	for _, id := range checkpointKeys(f.agentObservations) {
		for _, o := range f.agentObservations[id] {
			d.Observations[id] = append(d.Observations[id], checkpointObservation{x.group(o.group), o.call, o.task})
		}
	}
	d.Turns, d.MainCalls, d.Outcomes = x.turns.records, x.mainCalls.records, x.outcomes.records
	d.Groups, d.AgentCalls, d.Reports = x.groups.records, x.agentCalls.records, x.reports.records
	return d, records, nil
}

func checkpointProvenance(s history.SessionProvenance) checkpointSource {
	return checkpointSource{s.Kind, s.SessionID}
}
func checkpointMainKey(k mainKey) checkpointKey {
	return checkpointKey{Source: checkpointProvenance(k.source), Tagged: k.tagged, Scope: k.scope, Turn: k.turn}
}
func checkpointAgentKey(k agentKey) checkpointKey {
	return checkpointKey{Source: checkpointProvenance(k.source), Tagged: k.tagged, Scope: k.scope, Lifetime: k.lifetime}
}

func checkpointRecord(item Item, r contentRecipe) CheckpointItem {
	m := checkpointMetadata{item.ID, item.Order, item.Rev, item.EndedOrder, item.Kind, item.Session, item.Agent, item.NoChild, item.Turn, item.Parent, item.Status, item.Active, item.Shown, item.Summary, item.Subtype}
	recipe := checkpointRecipe{Creating: r.creating, Agent: r.agent, Delivery: r.delivery, Text: slices.Clone(r.text)}
	if r.fields != nil {
		recipe.Fields = make(map[string]checkpointField, len(r.fields))
		for key, field := range r.fields {
			recipe.Fields[key] = checkpointField{field.entry, field.item, field.revision, slices.Clone(field.value), hex.EncodeToString(field.digest[:])}
		}
	}
	return CheckpointItem{m, recipe}
}

// Stable root traversal gives deterministic table identities, independent of Go
// map iteration. Go-syntax keys quote strings, avoiding ambiguous concatenation.
func checkpointKeys[K comparable, V any](m map[K]V) []K {
	keys := slices.Collect(maps.Keys(m))
	order := make(map[K]string, len(keys))
	for _, key := range keys {
		order[key] = fmt.Sprintf("%#v", key)
	}
	slices.SortFunc(keys, func(a, b K) int { return cmp.Compare(order[a], order[b]) })
	return keys
}

type checkpointRegistry[T, D any] struct {
	ids     map[*T]int
	records []D
}

func (r *checkpointRegistry[T, D]) ref(p *T, convert func(*T) D) int {
	if p == nil {
		return 0
	}
	if id := r.ids[p]; id != 0 {
		return id
	}
	if r.ids == nil {
		r.ids = make(map[*T]int)
	}
	id := len(r.records) + 1
	r.ids[p] = id
	var zero D
	r.records = append(r.records, zero)
	record := convert(p)
	r.records[id-1] = record
	return id
}

type checkpointExporter struct {
	turns      checkpointRegistry[mainTurn, checkpointTurn]
	mainCalls  checkpointRegistry[mainCall, checkpointMainCall]
	outcomes   checkpointRegistry[mainOutcome, checkpointOutcome]
	groups     checkpointRegistry[agentGroup, checkpointGroup]
	agentCalls checkpointRegistry[agentCall, checkpointAgentCall]
	reports    checkpointRegistry[agentReport, checkpointReport]
}

func (x *checkpointExporter) turn(p *mainTurn) int {
	return x.turns.ref(p, func(p *mainTurn) checkpointTurn {
		d := checkpointTurn{Key: checkpointMainKey(p.key), Opening: p.opening, Text: p.text, TextNonblank: p.textNonblank, End: x.outcome(p.end)}
		if p.calls != nil {
			d.Calls = make(map[string]int)
			for _, id := range checkpointKeys(p.calls) {
				d.Calls[id] = x.mainCall(p.calls[id])
			}
		}
		if p.finished != nil {
			d.Finished = make(map[string]checkpointMarker)
			for _, id := range checkpointKeys(p.finished) {
				m := p.finished[id]
				d.Finished[id] = checkpointMarker{m.item, m.scope, m.lifetime, x.outcome(m.terminal)}
			}
		}
		return d
	})
}

func (x *checkpointExporter) mainCall(p *mainCall) int {
	return x.mainCalls.ref(p, func(p *mainCall) checkpointMainCall {
		return checkpointMainCall{x.agentCall(p.shell), checkpointAgentKey(p.group), p.item, p.launcher, x.outcome(p.terminal)}
	})
}
func (x *checkpointExporter) outcome(p *mainOutcome) int {
	return x.outcomes.ref(p, func(p *mainOutcome) checkpointOutcome { return checkpointOutcome{p.id, p.content, p.status, p.field} })
}
func (x *checkpointExporter) group(p *agentGroup) int {
	return x.groups.ref(p, func(p *agentGroup) checkpointGroup {
		d := checkpointGroup{Key: checkpointAgentKey(p.key), Calls: make(map[string]int), Tasks: make(map[string]checkpointTask)}
		for _, id := range checkpointKeys(p.calls) {
			d.Calls[id] = x.agentCall(p.calls[id])
		}
		for _, id := range checkpointKeys(p.tasks) {
			t := p.tasks[id]
			d.Tasks[id] = checkpointTask{t.observed, t.linkMapped, t.call, t.link, hex.EncodeToString(t.linkDigest[:]), x.reportSet(t.reports)}
		}
		return d
	})
}
func (x *checkpointExporter) agentCall(p *agentCall) int {
	return x.agentCalls.ref(p, func(p *agentCall) checkpointAgentCall {
		return checkpointAgentCall{x.mainCall(p.ordinary), p.linked, p.parent, p.observed, p.item, p.launch, p.launchResult, x.reportSet(p.reports)}
	})
}
func (x *checkpointExporter) reportSet(h agentReports) checkpointReports {
	d := checkpointReports{}
	for _, r := range h.pending {
		d.Pending = append(d.Pending, x.report(r))
	}
	for i, r := range h.markers {
		d.Markers[i] = x.report(r)
	}
	return d
}
func (x *checkpointExporter) report(p *agentReport) int {
	return x.reports.ref(p, func(p *agentReport) checkpointReport {
		return checkpointReport{p.id, p.content, p.turn, p.foreground, p.status, p.field, hex.EncodeToString(p.digest[:]), p.durable, p.mapped}
	})
}

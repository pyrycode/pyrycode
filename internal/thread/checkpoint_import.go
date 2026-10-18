package thread

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/pyrycode/pyrycode/internal/history"
)

// HistorySource supplies immutable history entries by ID. A fold's owner must
// serialize access and keep the source available for subsequent Feed and Render.
type HistorySource interface {
	Lookup([]uint64) ([]history.Entry, error)
}

// NewReference creates a fold that retains history references rather than inline
// payloads. The caller supplies a non-nil source for the conversation's history.
func NewReference(conversationID string, source HistorySource) *Fold {
	return newReferenceFold(conversationID, source)
}

// CheckpointCompatibilityError identifies an unsupported schema or folding rules.
type CheckpointCompatibilityError struct{ Schema, Rules int }

func (e *CheckpointCompatibilityError) Error() string {
	return fmt.Sprintf("thread: incompatible checkpoint schema %d rules %d", e.Schema, e.Rules)
}

// ImportCheckpoint restores detached continuation state without reading history
// or rendering. It returns no fold on invalid input. The expected owner and source
// must belong to the same conversation; source contents are verified when used.
func ImportCheckpoint(conversationID string, source HistorySource, d CheckpointState, rows []CheckpointItem) (*Fold, error) {
	if d.Schema != checkpointSchema || d.Rules != foldingRules {
		return nil, &CheckpointCompatibilityError{d.Schema, d.Rules}
	}
	x := checkpointImporter{version: d.Version, ids: make(map[uint64]int)}
	x.check(d.Version <= history.MaxEntryID, "version")
	x.check(conversationID == d.ConversationID && source != nil, "owner or source")
	x.check(len(rows) == len(d.ItemIDs), "item inventory length")
	for i, id := range d.ItemIDs {
		x.check(id > 0 && id <= d.Version && (i == 0 || id > d.ItemIDs[i-1]), "item inventory order")
		x.ids[id] = i
	}
	f := NewReference(conversationID, source)
	for i, row := range rows {
		x.check(i < len(d.ItemIDs) && row.Metadata.ID == atID(d.ItemIDs, i), "item inventory record")
		item, recipe := x.record(row)
		f.items = append(f.items, item)
		f.recipes[item.ID] = recipe
	}
	f.version, f.lastShown, f.suppressed, f.unprovenThrough = d.Version, d.LastShown, d.Suppressed, d.UnprovenThrough
	f.lastBoundary, f.legacyScope, f.successor = d.LastBoundary, d.LegacyScope, restoreSource(d.Successor)
	for _, id := range []uint64{d.LastShown, d.Suppressed, d.UnprovenThrough, d.LastBoundary} {
		x.historyID(id, false)
	}
	for id, value := range d.Published {
		x.itemID(id, true)
		f.published[id] = value
	}
	for id, value := range d.PublicVisible {
		x.itemID(id, true)
		f.publicVisible[id] = value
	}
	for id, rev := range d.Touches {
		x.itemID(id, true)
		x.historyID(rev, true)
		f.touches[id] = rev
	}
	for _, b := range d.PendingBoundaries {
		key := boundaryKey{b.At, b.Previous, b.Next, b.Reason}
		_, duplicate := f.pending[key]
		x.check(!duplicate, "duplicate boundary")
		for _, id := range b.IDs {
			x.itemID(id, true)
		}
		f.pending[key] = slices.Clone(b.IDs)
	}
	// Allocate all identities before following edges, including reciprocal shells.
	turns, calls, outcomes := checkpointObjects[mainTurn](len(d.Turns)), checkpointObjects[mainCall](len(d.MainCalls)), checkpointObjects[mainOutcome](len(d.Outcomes))
	groups, agents, reports := checkpointObjects[agentGroup](len(d.Groups)), checkpointObjects[agentCall](len(d.AgentCalls)), checkpointObjects[agentReport](len(d.Reports))
	for i, o := range d.Outcomes {
		x.historyID(o.ID, true)
		x.historyID(o.Content, true)
		*outcomes[i] = mainOutcome{o.ID, o.Content, o.Status, o.Field}
	}
	for i, r := range d.Reports {
		x.historyID(r.ID, true)
		x.historyID(r.Content, true)
		*reports[i] = agentReport{r.ID, r.Content, r.Turn, r.Foreground, r.Status, r.Field, x.digest(r.Digest), r.Durable, r.Mapped}
	}
	for i, c := range d.MainCalls {
		x.index(c.Item, -1)
		*calls[i] = mainCall{checkpointGet(&x, agents, c.Shell), restoreAgentKey(c.Group), c.Item, c.Launcher, checkpointGet(&x, outcomes, c.Terminal)}
		if c.Item >= 0 {
			x.check(rowsKind(rows, c.Item) == "tool_call" && !c.Launcher, "ordinary call item")
		}
	}
	for i, c := range d.AgentCalls {
		x.index(c.Item, -1)
		x.historyID(c.Observed, false)
		x.historyID(c.Launch, false)
		*agents[i] = agentCall{checkpointGet(&x, calls, c.Ordinary), c.Linked, c.Parent, c.Observed, c.Item, c.Launch, c.LaunchResult, x.reportSet(c.Reports, reports)}
		if c.Item >= 0 {
			x.check(atID(d.ItemIDs, c.Item) == c.Launch, "agent launch item")
			kind := "agent"
			if c.Ordinary != 0 {
				kind = "tool_call"
			}
			x.check(rowsKind(rows, c.Item) == kind, "agent call item")
		}
	}
	for _, c := range calls {
		if c.shell != nil {
			x.check(c.shell.ordinary == c && c.item == c.shell.item, "reciprocal shell owner")
		}
	}
	for _, c := range agents {
		if c.ordinary != nil {
			x.check(c.ordinary.shell == c && c.item == c.ordinary.item, "reciprocal ordinary owner")
		}
	}
	for i, t := range d.Turns {
		x.index(t.Text, -1)
		x.historyID(t.Opening, false)
		st := turns[i]
		st.key, st.opening, st.text, st.end = restoreMainKey(t.Key), t.Opening, t.Text, checkpointGet(&x, outcomes, t.End)
		st.textNonblank = t.TextNonblank
		if t.Text >= 0 {
			x.check(rowsKind(rows, t.Text) == "assistant_message", "turn text item")
		}
		if t.Calls != nil {
			st.calls = make(map[string]*mainCall)
		}
		for id, ref := range t.Calls {
			st.calls[id] = checkpointRequired(&x, calls, ref)
		}
		if t.Finished != nil {
			st.finished = make(map[string]*mainCallMarker)
		}
		for id, m := range t.Finished {
			x.index(m.Item, -2)
			x.check(m.Item == -2 || (m.Item >= 0 && rowsKind(rows, m.Item) == "tool_call"), "finished call item")
			st.finished[id] = &mainCallMarker{item: m.Item, scope: m.Scope, lifetime: m.Lifetime, terminal: checkpointRequired(&x, outcomes, m.Terminal)}
		}
	}
	for _, r := range d.CurrentTurns {
		key := restoreMainKey(r.Key)
		_, duplicate := f.turns[key]
		x.check(!duplicate, "duplicate current turn")
		st := checkpointRequired(&x, turns, r.Ref)
		if st != nil {
			x.check(key == st.key, "current turn key")
		}
		f.turns[key] = st
	}
	for id, ref := range d.Openings {
		x.historyID(id, true)
		st := checkpointRequired(&x, turns, ref)
		if st != nil {
			x.check(st.opening == id, "opening identity")
		}
		f.openings[id] = st
	}
	for _, ref := range d.OpenTexts {
		st := checkpointRequired(&x, turns, ref)
		if st != nil {
			x.check(st.text >= 0 && st.textSlot == 0, "open text identity")
			f.openTexts = append(f.openTexts, st)
			st.textSlot = len(f.openTexts)
		}
	}
	for id, index := range d.Accepted {
		x.indexID(id, index, 0)
		f.accepted[id] = index
	}
	for id, index := range d.Messages {
		x.indexID(id, index, -1)
		f.messages[id] = index
	}
	for _, r := range d.ChildTurns {
		key := childKey{restoreAgentKey(r.Group), r.Parent, r.Turn}
		_, duplicate := f.childTurns[key]
		x.check(!duplicate, "duplicate child turn")
		f.childTurns[key] = checkpointRequired(&x, turns, r.Ref)
	}
	for _, r := range d.ChildCalls {
		key := childCallKey{restoreAgentKey(r.Group), r.Turn, r.Call}
		_, duplicate := f.childCalls[key]
		x.check(!duplicate, "duplicate child call")
		call := checkpointRequired(&x, calls, r.Ref)
		f.childCalls[key] = &childCall{r.Parent, call}
	}
	for _, r := range d.ChildReports {
		key := childCallKey{restoreAgentKey(r.Group), r.Turn, r.Call}
		_, duplicate := f.childReports[key]
		x.check(!duplicate, "duplicate child report")
		f.childReports[key] = checkpointRequired(&x, outcomes, r.Ref)
	}
	for index, c := range d.Children {
		x.index(index, 0)
		child := &childState{group: restoreAgentKey(c.Group), parent: c.Parent, final: c.Final}
		if s := c.Snapshot; s != nil {
			base, baseRecipe := x.record(s.Base)
			previous, previousRecipe := x.record(s.Previous)
			x.check(base.ID == atID(d.ItemIDs, index) && previous.ID == base.ID, "child snapshot identity")
			child.childSnapshot = &childSnapshot{base: base, previous: previous, baseRecipe: baseRecipe, previousRecipe: previousRecipe, restored: s.Restored}
		}
		x.check(child.final || child.childSnapshot != nil, "reversible child snapshot")
		f.children[index] = child
		key := childParentKey{child.group, child.parent}
		if f.childEdges[key] == nil {
			f.childEdges[key] = make(map[int]struct{})
		}
		f.childEdges[key][index] = struct{}{}
	}
	for index, e := range d.ChildEndings {
		x.index(index, 0)
		x.itemID(e.Item, true)
		x.historyID(e.Revision, true)
		f.childEndings[index] = endingReference{e.Item, e.Revision}
	}
	for i, g := range d.Groups {
		group := groups[i]
		group.key, group.calls, group.tasks = restoreAgentKey(g.Key), make(map[string]*agentCall), make(map[string]*agentTask)
		_, duplicate := f.agentGroups[group.key]
		x.check(!duplicate, "duplicate group")
		f.agentGroups[group.key] = group
		for id, ref := range g.Calls {
			c := checkpointRequired(&x, agents, ref)
			group.calls[id] = c
			if c != nil && c.item >= 0 && c.ordinary == nil {
				f.childAgents[c.item] = childParentKey{group.key, id}
			}
		}
		for id, t := range g.Tasks {
			x.historyID(t.Observed, false)
			x.historyID(t.Link, false)
			group.tasks[id] = &agentTask{t.Observed, t.LinkMapped, t.Call, t.Link, x.digest(t.LinkDigest), x.reportSet(t.Reports, reports)}
			group.insertTaskID(t.Call, id)
		}
	}
	for _, l := range d.Lifetimes {
		key := restoreAgentKey(l.Key)
		_, duplicate := f.agentLifetimes[key]
		x.check(!duplicate, "duplicate lifetime")
		f.agentLifetimes[key] = l.Lifetime
	}
	for id, observations := range d.Observations {
		x.historyID(id, true)
		for _, o := range observations {
			g := checkpointRequired(&x, groups, o.Group)
			if g != nil {
				x.check((o.Call != "" && g.calls[o.Call] != nil) || (o.Task != "" && g.tasks[o.Task] != nil), "observation owner")
			}
			f.agentObservations[id] = append(f.agentObservations[id], agentObservation{g, o.Call, o.Task})
		}
	}
	if x.err != nil {
		return nil, x.err
	}
	x.turnText(f, turns)
	for key, child := range f.childCalls {
		call := child.call
		if call.group == key.group {
			continue
		}
		// Promotion changes the reciprocal shell owner, retaining its child route.
		old := call.group
		old.lifetime, old.scope = "", key.group.scope
		group := f.agentGroups[call.group]
		x.check(key.group.lifetime == "" && call.group.lifetime != "" && call.group.scope == 0 && old == key.group && call.shell != nil && group != nil && group.calls[key.call] == call.shell, "child call group")
	}
	if x.err != nil {
		return nil, x.err
	}
	return f, nil
}

type checkpointImporter struct {
	version uint64
	ids     map[uint64]int
	err     error
}

func (x *checkpointImporter) check(ok bool, category string) {
	if !ok && x.err == nil {
		x.err = fmt.Errorf("thread: invalid checkpoint: %s", category)
	}
}
func (x *checkpointImporter) historyID(id uint64, required bool) {
	x.check((!required || id != 0) && id <= x.version, "history ID")
}
func (x *checkpointImporter) itemID(id uint64, required bool) {
	_, ok := x.ids[id]
	x.check((!required && id == 0) || ok, "item ID")
}
func (x *checkpointImporter) index(index, minimum int) {
	x.check(index >= minimum && index < len(x.ids), "item index")
}
func (x *checkpointImporter) indexID(id uint64, index, minimum int) {
	x.itemID(id, true)
	x.index(index, minimum)
	if index >= 0 {
		x.check(x.ids[id] == index, "item index identity")
	}
}

// turnText validates coalescing ownership using saved identities only. Main text
// belongs to its opening generation; child text belongs to its scoped parent lane.
func (x *checkpointImporter) turnText(f *Fold, turns []*mainTurn) {
	generations := make(map[mainKey][]uint64)
	for id, st := range f.openings {
		generations[st.key] = append(generations[st.key], id)
	}
	for _, ids := range generations {
		slices.Sort(ids)
	}
	children := make(map[*mainTurn]bool)
	for key, st := range f.childTurns {
		children[st] = true
		x.check(st.opening == 0 && st.key == (mainKey{key.group.source, key.group.tagged, key.group.scope, key.turn}), "child turn key")
		if st.text >= 0 {
			child := f.children[st.text]
			x.check(child != nil && child.group == key.group && child.parent == key.parent, "child text owner")
		}
	}
	owners := make(map[int]*mainTurn)
	for _, st := range turns {
		if st.text < 0 {
			continue
		}
		item := f.items[st.text]
		x.check(item.Turn == st.key.turn && owners[st.text] == nil, "turn text owner")
		owners[st.text] = st
		if st.key.tagged {
			source := st.key.source
			x.check(item.Session == source.SessionID && item.Agent == recordedAgent(source.Kind) && item.NoChild == (source.Kind == "none"), "turn text source")
		}
		if st.opening == 0 {
			x.check(children[st], "child text turn")
			continue
		}
		x.check(!children[st] && f.children[st.text] == nil && f.openings[st.opening] == st, "main text owner")
		ids := generations[st.key]
		position, found := slices.BinarySearch(ids, item.ID)
		if !found {
			position--
		}
		x.check(position >= 0 && ids[position] == st.opening, "turn text generation")
	}
}

func (x *checkpointImporter) digest(value string) (digest [32]byte) {
	decoded, err := hex.DecodeString(value)
	x.check(err == nil && len(decoded) == len(digest), "digest")
	copy(digest[:], decoded)
	return digest
}
func (x *checkpointImporter) record(row CheckpointItem) (Item, contentRecipe) {
	m, r := row.Metadata, row.Recipe
	x.itemID(m.ID, true)
	for _, id := range []uint64{m.Order, m.Rev, m.EndedOrder, r.Creating, r.Delivery} {
		x.historyID(id, false)
	}
	x.check(m.Rev >= m.ID && r.Creating == m.ID, "item revision or creating identity")
	x.itemID(m.Parent, false)
	x.check(m.Parent == 0 || m.Parent < m.ID, "parent order")
	item := Item{ID: m.ID, Order: m.Order, Rev: m.Rev, EndedOrder: m.EndedOrder, Kind: m.Kind, Session: m.Session, Agent: m.Agent, NoChild: m.NoChild, Turn: m.Turn, Parent: m.Parent, Status: m.Status, Active: m.Active, Shown: m.Shown, Summary: m.Summary, Subtype: m.Subtype}
	recipe := contentRecipe{creating: r.Creating, agent: r.Agent, delivery: r.Delivery, text: slices.Clone(r.Text)}
	for _, id := range r.Text {
		x.historyID(id, true)
	}
	if r.Fields != nil {
		recipe.fields = make(map[string]recipeField)
	}
	for key, field := range r.Fields {
		x.historyID(field.Entry, false)
		x.itemID(field.Item, false)
		x.historyID(field.Revision, false)
		x.check(field.Item == 0 || field.Item < m.ID, "recipe ancestor order")
		if len(field.Value) != 0 {
			switch key {
			case "task_id", "parent_tool_call_id":
				var value string
				x.check(json.Unmarshal(field.Value, &value) == nil && string(field.Value) != "null", "recipe scalar string")
			case "ended_order":
				var value uint64
				x.check(json.Unmarshal(field.Value, &value) == nil && string(field.Value) != "null" && value <= x.version, "recipe scalar order")
			default:
				x.check(false, "inline recipe payload")
			}
		}
		recipe.fields[key] = recipeField{field.Entry, field.Item, field.Revision, slices.Clone(field.Value), x.digest(field.Digest)}
	}
	return item, recipe
}
func (x *checkpointImporter) reportSet(r checkpointReports, reports []*agentReport) agentReports {
	h := agentReports{}
	for _, ref := range r.Pending {
		h.pending = append(h.pending, checkpointRequired(x, reports, ref))
	}
	for i, ref := range r.Markers {
		h.markers[i] = checkpointGet(x, reports, ref)
	}
	return h
}
func checkpointObjects[T any](n int) []*T {
	objects := make([]*T, n)
	for i := range objects {
		objects[i] = new(T)
	}
	return objects
}
func checkpointGet[T any](x *checkpointImporter, table []*T, ref int) *T {
	if ref == 0 {
		return nil
	}
	if ref < 0 || ref > len(table) {
		x.check(false, "table reference")
		return nil
	}
	return table[ref-1]
}
func checkpointRequired[T any](x *checkpointImporter, table []*T, ref int) *T {
	x.check(ref != 0, "missing table reference")
	return checkpointGet(x, table, ref)
}
func restoreSource(s checkpointSource) history.SessionProvenance {
	return history.SessionProvenance{Kind: s.Kind, SessionID: s.SessionID}
}
func restoreMainKey(k checkpointKey) mainKey {
	return mainKey{restoreSource(k.Source), k.Tagged, k.Scope, k.Turn}
}
func restoreAgentKey(k checkpointKey) agentKey {
	return agentKey{restoreSource(k.Source), k.Tagged, k.Lifetime, k.Scope}
}
func atID(ids []uint64, i int) uint64 {
	if i < 0 || i >= len(ids) {
		return 0
	}
	return ids[i]
}
func rowsKind(rows []CheckpointItem, i int) string {
	if i < 0 || i >= len(rows) {
		return ""
	}
	return rows[i].Metadata.Kind
}

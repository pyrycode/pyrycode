package thread

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// CompactQueryResult certifies a compact root range in one publication, without
// certifying complete-thread or expansion coverage. Only StateUsable carries
// identity, rows and bounds. Items and Headers have no presentation-order guarantee.
type CompactQueryResult struct {
	State                                SnapshotState
	ConversationID                       conversations.ConversationID
	Projection, Epoch                    string
	Version                              uint64
	Items                                []Item
	Headers                              []ChildSummary
	LowerOrder, UpperOrder, Continuation uint64
	OlderExists                          bool
	FromVersion                          uint64 // exclusive interval bound for nonzero catch-up and live progress
	ResetRequired                        bool   // no rows or bounds; reinitialize with a version-zero window
	Err                                  error
}

const compactProjection = "compact-v1"

// CompactNewestWindow reads ordered public roots and all roots with public
// subtree activity. Callers authorize id; this method never loads or waits.
func (s *Store) CompactNewestWindow(ctx context.Context, id conversations.ConversationID, limit int) (CompactQueryResult, error) {
	return s.compactQuery(ctx, id, 0, limit, true)
}

// CompactHistoryPage reads the public root range below exclusive upperOrder,
// including all lower-bound ties. Continuation equals the inclusive LowerOrder;
// OlderExists certifies an ordered root strictly below it. Pages add no extras.
func (s *Store) CompactHistoryPage(ctx context.Context, id conversations.ConversationID, upperOrder uint64, limit int) (CompactQueryResult, error) {
	return s.compactQuery(ctx, id, upperOrder, limit, false)
}

func (s *Store) compactQuery(ctx context.Context, id conversations.ConversationID, upper uint64, limit int, newest bool) (CompactQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return CompactQueryResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return CompactQueryResult{}, history.ErrInvalidID
	}
	if limit <= 0 {
		return CompactQueryResult{}, ErrInvalidLimit
	}
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return CompactQueryResult{State: snap.State, Err: snap.Err}, nil
	}
	if q == nil {
		return CompactQueryResult{State: StateUnavailable, Err: ErrUnavailable}, nil
	}
	if newest {
		upper = snap.Version + 1
	}
	p := CompactQueryResult{State: StateUsable, ConversationID: id, Projection: compactProjection, Epoch: snap.Epoch, Version: snap.Version, UpperOrder: upper}
	end := q.rank(q.roots, upper)
	start := max(0, end-min(limit, MaxQueryItems))
	if start < end {
		p.LowerOrder = q.at(q.roots, start).item.Order
		start = q.rank(q.roots, p.LowerOrder)
		p.Continuation, p.OlderExists = p.LowerOrder, start > 0
	}
	add := compactCollector(&p)
	if err := q.visit(ctx, q.roots, start, end, add); err != nil {
		return CompactQueryResult{}, err
	}
	if newest {
		if err := q.visit(ctx, q.activeRoots, 0, treeSize(q.activeRoots), add); err != nil {
			return CompactQueryResult{}, err
		}
	}
	return s.compactRender(ctx, id, q, p)
}

// compactCollector adds each root once: tool/agent rows as prepared headers,
// other rows as messages for compactRender. No body or descendant is read.
func compactCollector(p *CompactQueryResult) func(queryRow) {
	seen := make(map[uint64]bool)
	return func(row queryRow) {
		i := row.item
		if seen[i.ID] {
			return
		}
		seen[i.ID] = true
		if i.Kind == "tool_call" || i.Kind == "agent" {
			h := ChildSummary{ID: i.ID, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Kind: i.Kind, Subtype: i.Subtype, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, DetailOmitted: true}
			row.header.apply(&h)
			p.Headers = append(p.Headers, h)
		} else {
			p.Items = append(p.Items, *i)
		}
	}
}

// compactRender hydrates selected messages; failure discards prepared headers.
func (s *Store) compactRender(ctx context.Context, id conversations.ConversationID, q *queryIndex, p CompactQueryResult) (CompactQueryResult, error) {
	var err error
	p.Items, err = s.renderItems(ctx, id, q, p.Items)
	if ctx.Err() != nil {
		return CompactQueryResult{}, ctx.Err()
	}
	if err != nil {
		return CompactQueryResult{State: StateUnavailable, Err: ErrUnavailable}, nil
	}
	return p, nil
}

func (q *queryIndex) compactMembership(row, value queryRow) {
	i := row.item
	if i.Parent == 0 && i.Order != 0 {
		q.roots = q.treePut(q.roots, i.Order, i.ID, value)
	}
	if i.Parent == 0 {
		q.rootTouches = q.treePut(q.rootTouches, row.header.subtreeVersion, i.ID, value)
	}
	if row.activeBranch {
		q.activeBranches = q.treePutGroup(q.activeBranches, i.Parent, i.Order, i.ID, value)
		if i.Parent == 0 {
			q.activeRoots = q.treePut(q.activeRoots, i.ID, i.ID, value)
		}
	}
}

// Refresh only dirty rows, children before parents. Rank differences establish
// activity without enumerating even the active descendants of a selected root.
// Dirty lookup buckets must already belong to this publication.
func (q *queryIndex) refreshRootActivity(dirty map[uint64]bool) {
	pending := make(map[uint64]int, len(dirty))
	parents := make(map[uint64]uint64, len(dirty))
	for id := range dirty {
		if row, ok := q.lookup(id, true); ok {
			pending[id] = 0
			parents[id] = row.item.Parent
		}
	}
	for _, parent := range parents {
		if _, ok := pending[parent]; ok {
			pending[parent]++
		}
	}
	ready := make([]uint64, 0, len(pending))
	for id, count := range pending {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		row, _ := q.lookup(id, true)
		children := q.groupRank(q.activeBranches, id, ^uint64(0), ^uint64(0), true, true) - q.groupRank(q.activeBranches, id, 0, 0, false, true)
		active := row.item.Active || children > 0
		if active != row.activeBranch {
			q.membership(row, true)
			row.activeBranch = active
			q.membership(row, false)
		}
		parent := parents[id]
		if count, ok := pending[parent]; ok {
			pending[parent] = count - 1
			if count == 1 {
				ready = append(ready, parent)
			}
		}
	}
}

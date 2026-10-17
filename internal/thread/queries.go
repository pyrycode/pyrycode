package thread

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// MaxQueryItems caps ordered rows requested in a page or newest window.
// Active items, parent closure and ties at the lower bound are additional rows.
const MaxQueryItems = 256

var ErrInvalidLimit = errors.New("thread: query limit must be positive")

// QueryResult is detached from one publication. Only StateUsable carries items,
// epoch, consumed version and bounds. Continuation equals the inclusive lower
// order; OlderExists certifies an ordered public row strictly below it. Extras
// do not affect bounds, and Items has no presentation-order guarantee.
type QueryResult struct {
	State                                SnapshotState
	Items                                []Item
	Epoch                                string
	Version                              uint64
	LowerOrder, UpperOrder, Continuation uint64
	OlderExists                          bool
	FromVersion                          uint64 // exclusive covered bound for nonzero catch-up
	ResetRequired                        bool   // no items or covered/page bounds; acquire a newest window
	Err                                  error
}

// queryIndex borrows immutable publication items; private fold rows never enter it.
type queryIndex struct {
	worker                   *conversationWorker
	byID                     [256]map[uint64]queryRow
	ordered, active, touched *queryTree
	children                 *queryTree
	work                     func(bool)
	copied                   func(int) // copied pointers/index positions, separate from semantic visits
	invalidTouches           int
	coveredThrough           uint64
	unprovenThrough          uint64
	suppressed               uint64
	complete                 bool
}

func newQueryIndex(items []Item, work func(bool)) *queryIndex {
	shared := make([]*Item, len(items))
	for i := range items {
		shared[i] = &items[i]
	}
	return newPublicationIndex(shared, work)
}

func newPublicationIndex(items []*Item, work func(bool)) *queryIndex {
	q := &queryIndex{work: work}
	for _, item := range items {
		if item != nil {
			q.membership(queryRow{item: item, header: prepareChildHeader(*item)}, false)
		}
	}
	for _, item := range items {
		if item != nil {
			row, _ := q.lookup(item.ID, true)
			q.refreshHeader(row, item.Rev)
		}
	}
	return q
}

func (q *queryIndex) count(preparing bool) {
	if q.work != nil {
		q.work(preparing)
	}
}

// HistoryPage selects the newest ordered public range below upperOrder, plus
// complete public parent closure. The caller must already be authorized for id.
// It does not load or wait for recovery. Only selected references are read.
func (s *Store) HistoryPage(ctx context.Context, id conversations.ConversationID, upperOrder uint64, limit int) (QueryResult, error) {
	return s.query(ctx, id, upperOrder, limit, false)
}

// NewestWindow selects the newest ordered range below consumed Version+1, plus
// every active public item and complete public parents of all returned rows.
// The caller must already be authorized for id.
func (s *Store) NewestWindow(ctx context.Context, id conversations.ConversationID, limit int) (QueryResult, error) {
	return s.query(ctx, id, 0, limit, true)
}

func (s *Store) query(ctx context.Context, id conversations.ConversationID, upper uint64, limit int, newest bool) (QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return QueryResult{}, history.ErrInvalidID
	}
	if limit <= 0 {
		return QueryResult{}, ErrInvalidLimit
	}
	limit = min(limit, MaxQueryItems)
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return QueryResult{State: snap.State, Err: snap.Err}, nil
	}
	result, err := q.window(ctx, snap, upper, limit, newest)
	return s.renderQuery(ctx, id, q, result, err)
}

func (s *Store) captureQuery(id conversations.ConversationID) (Snapshot, *queryIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.workers[id]
	if s.closed || w == nil || w.retiring {
		return Snapshot{}, nil
	}
	return w.snapshot, w.query
}

func (q *queryIndex) window(ctx context.Context, snap Snapshot, upper uint64, limit int, newest bool) (QueryResult, error) {
	if newest {
		upper = snap.Version + 1
	}
	result := QueryResult{State: StateUsable, Epoch: snap.Epoch, Version: snap.Version, UpperOrder: upper}
	end := q.rank(q.ordered, upper)
	start := max(0, end-limit)
	if start < end {
		result.LowerOrder = q.at(q.ordered, start).item.Order
		// Tied orders must never leave a hole in the continuous range.
		start = q.rank(q.ordered, result.LowerOrder)
		result.Continuation = result.LowerOrder
		result.OlderExists = start > 0
	}
	seen := make(map[uint64]bool)
	add := func(row queryRow) {
		item := *row.item
		if !seen[item.ID] {
			seen[item.ID] = true
			item.Content = append(json.RawMessage(nil), item.Content...)
			result.Items = append(result.Items, item)
		}
	}
	if err := q.visit(ctx, q.ordered, start, end, add); err != nil {
		return QueryResult{}, err
	}
	if newest {
		if err := q.visit(ctx, q.active, 0, treeSize(q.active), add); err != nil {
			return QueryResult{}, err
		}
	}
	// Appended parents are visited in turn, making multi-level closure iterative.
	for i := 0; i < len(result.Items); i++ {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		parent := result.Items[i].Parent
		if parent != 0 && !seen[parent] {
			if row, ok := q.lookup(parent, false); ok {
				add(row)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	return result, nil
}

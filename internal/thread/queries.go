package thread

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

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
	items           []Item
	byID            map[uint64]int
	ordered, active []int
	work            func(bool)
	touched         []int
	touches         []uint64
	coveredThrough  uint64
	unprovenThrough uint64
	suppressed      uint64
	complete        bool
}

func newQueryIndex(items []Item, work func(bool)) *queryIndex {
	q := &queryIndex{items: items, byID: make(map[uint64]int, len(items)), work: work}
	for i, item := range items {
		q.count(true)
		q.byID[item.ID] = i
		if item.Order != 0 {
			q.ordered = append(q.ordered, i)
		}
		if item.Active {
			q.active = append(q.active, i)
		}
	}
	sort.Slice(q.ordered, func(i, j int) bool {
		q.count(true)
		a, b := items[q.ordered[i]], items[q.ordered[j]]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.ID < b.ID
	})
	return q
}

func (q *queryIndex) count(preparing bool) {
	if q.work != nil {
		q.work(preparing)
	}
}
func (q *queryIndex) item(i int) Item    { q.count(false); return q.items[i] }
func (q *queryIndex) order(i int) uint64 { return q.item(q.ordered[i]).Order }

// HistoryPage selects the newest ordered public range below upperOrder, plus
// complete public parent closure. The caller must already be authorized for id.
// It does not load, wait for recovery or read history.
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
	return q.window(ctx, snap, upper, limit, newest)
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
	end := sort.Search(len(q.ordered), func(i int) bool { return q.order(i) >= upper })
	start := max(0, end-limit)
	if start < end {
		result.LowerOrder = q.order(start)
		// Tied orders must never leave a hole in the continuous range.
		start = sort.Search(start, func(i int) bool { return q.order(i) >= result.LowerOrder })
		result.Continuation = result.LowerOrder
		result.OlderExists = start > 0
	}
	seen := make(map[uint64]bool)
	add := func(item Item) {
		if !seen[item.ID] {
			seen[item.ID] = true
			item.Content = append(json.RawMessage(nil), item.Content...)
			result.Items = append(result.Items, item)
		}
	}
	for i := start; i < end; i++ {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		add(q.item(q.ordered[i]))
	}
	if newest {
		for _, i := range q.active {
			if err := ctx.Err(); err != nil {
				return QueryResult{}, err
			}
			add(q.item(i))
		}
	}
	// Appended parents are visited in turn, making multi-level closure iterative.
	for i := 0; i < len(result.Items); i++ {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		parent := result.Items[i].Parent
		if parent != 0 && !seen[parent] {
			if index, ok := q.byID[parent]; ok {
				add(q.item(index))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	return result, nil
}

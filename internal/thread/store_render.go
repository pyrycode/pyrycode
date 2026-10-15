package thread

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type entryLookup func([]uint64) ([]history.Entry, error)

func (lookup entryLookup) Lookup(ids []uint64) ([]history.Entry, error) { return lookup(ids) }

func (s *Store) renderQuery(ctx context.Context, id conversations.ConversationID, q *queryIndex, result QueryResult, err error) (QueryResult, error) {
	if err != nil {
		return QueryResult{}, err
	}
	result.Items, err = s.renderItems(ctx, id, q, result.Items)
	if err != nil {
		if ctx.Err() != nil {
			return QueryResult{}, ctx.Err()
		}
		return QueryResult{State: StateUnavailable, Err: ErrUnavailable}, nil
	}
	return result, nil
}

// renderItems borrows recipe values only from the captured root. It never reads
// the worker's Fold or recent cache, whose contents continue changing on Feed.
func (s *Store) renderItems(ctx context.Context, id conversations.ConversationID, q *queryIndex, items []Item) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	recipes := make(map[uint64]contentRecipe, len(items))
	if q != nil {
		for _, item := range items {
			row := q.byID[item.ID%uint64(len(q.byID))][item.ID]
			if row.recipe.creating != 0 {
				recipes[item.ID] = row.recipe
			}
		}
	}
	source := entryLookup(func(ids []uint64) ([]history.Entry, error) { return s.lookup(ctx, id, ids) })
	items, err := renderShared(items, recipes, source)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		s.withdraw(ctx, id, q)
		return nil, ErrUnavailable
	}
	return copyItems(items), nil
}

func (s *Store) withdraw(ctx context.Context, id conversations.ConversationID, q *queryIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Cancellation while waiting for the mutex must preserve the worker's state.
	if ctx.Err() != nil {
		return
	}
	if q == nil || q.worker == nil || s.workers[id] != q.worker || q.worker.retiring || s.closed {
		return
	}
	w := q.worker
	w.renderFailed = true
	w.snapshot = Snapshot{State: StateUnavailable, Err: ErrUnavailable}
	w.items, w.query = nil, nil
	w.ranges, w.rangeBytes = nil, 0
	s.records[id] = progressRecord{Err: ErrUnavailable}
	w.cancel()
	w.notify()
}

package thread

import (
	"context"
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// MaxCatchUpGap is the inclusive maximum consumed history-ID distance for catch-up.
const MaxCatchUpGap = 1024

// prepareCatchUp copies derived evidence only after complete replay/checkpoint.
// Latest public touches suffice for current-state selection; suppressed rows
// require reset because the client has no projection removal operation.
func (q *queryIndex) prepareCatchUp(f *Fold) {
	q.touched, q.invalidTouches = nil, 0
	for _, bucket := range q.byID {
		for id, row := range bucket {
			q.count(true)
			row.touch = f.touches[id]
			bucket[id] = row
			q.touched = q.treePut(q.touched, row.touch, id, row)
			if row.touch == 0 || row.touch > f.version {
				q.invalidTouches++
			}
		}
	}
	q.evidence(f)
}

// CatchUp returns current full states touched in (afterVersion, Version], every
// active public item and complete public parents. The caller must be authorized
// for id. It neither loads nor waits for recovery nor reads history. Nonzero
// queries ignore limit; zero selects NewestWindow with its validation and clamp.
// ResetRequired carries the current epoch/version but no items or covered bounds.
// Cancellation discards partial results and leaves the worker running.
func (s *Store) CatchUp(ctx context.Context, id conversations.ConversationID, epoch string, afterVersion uint64, limit int) (QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return QueryResult{}, history.ErrInvalidID
	}
	if afterVersion == 0 && limit <= 0 {
		return QueryResult{}, ErrInvalidLimit
	}
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return QueryResult{State: snap.State, Err: snap.Err}, nil
	}
	reset := QueryResult{State: StateUsable, Epoch: snap.Epoch, Version: snap.Version, ResetRequired: true}
	if epoch != snap.Epoch && (epoch != "" || afterVersion != 0) {
		return reset, nil
	}
	if afterVersion == 0 {
		return q.window(ctx, snap, 0, min(limit, MaxQueryItems), true)
	}
	if afterVersion > snap.Version || snap.Version-afterVersion > MaxCatchUpGap || q == nil || !q.complete || q.coveredThrough != snap.Version || q.unprovenThrough > afterVersion || q.suppressed > afterVersion {
		return reset, nil
	}
	result := QueryResult{State: StateUsable, Epoch: snap.Epoch, Version: snap.Version, FromVersion: afterVersion}
	start := q.rank(q.touched, afterVersion+1)
	seen := make(map[uint64]bool)
	add := func(row queryRow) {
		item := *row.item
		if !seen[item.ID] {
			seen[item.ID] = true
			item.Content = append(json.RawMessage(nil), item.Content...)
			result.Items = append(result.Items, item)
		}
	}
	if err := q.visit(ctx, q.touched, start, treeSize(q.touched), add); err != nil {
		return QueryResult{}, err
	}
	if err := q.visit(ctx, q.active, 0, treeSize(q.active), add); err != nil {
		return QueryResult{}, err
	}
	for i := 0; i < len(result.Items); i++ {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		parent := result.Items[i].Parent
		if parent != 0 && !seen[parent] {
			if row, ok := q.lookup(parent, false); ok {
				add(row)
			} else {
				return reset, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	return result, nil
}

package thread

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// MaxCatchUpGap is the inclusive maximum consumed history-ID distance for catch-up.
const MaxCatchUpGap = 1024

// prepareCatchUp copies derived evidence only after complete replay/checkpoint.
// Latest public touches suffice for current-state selection; suppressed rows
// require reset because the client has no projection removal operation.
func (q *queryIndex) prepareCatchUp(f *Fold) {
	q.complete, q.coveredThrough, q.suppressed = true, f.version, f.suppressed
	q.unprovenThrough = f.unprovenThrough
	q.touches = make([]uint64, len(q.items))
	for i, item := range q.items {
		q.count(true)
		touch := f.touches[item.ID]
		if touch == 0 || touch > f.version {
			q.complete = false
		}
		q.touches[i] = touch
		q.touched = append(q.touched, i)
	}
	sort.Slice(q.touched, func(i, j int) bool {
		q.count(true)
		return q.touches[q.touched[i]] < q.touches[q.touched[j]]
	})
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
	start := sort.Search(len(q.touched), func(i int) bool {
		q.count(false)
		return q.touches[q.touched[i]] > afterVersion
	})
	seen := make(map[uint64]bool)
	add := func(i int) {
		item := q.item(i)
		if !seen[item.ID] {
			seen[item.ID] = true
			item.Content = append(json.RawMessage(nil), item.Content...)
			result.Items = append(result.Items, item)
		}
	}
	for _, i := range q.touched[start:] {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		add(i)
	}
	for _, i := range q.active {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		add(i)
	}
	for i := 0; i < len(result.Items); i++ {
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		parent := result.Items[i].Parent
		if parent != 0 && !seen[parent] {
			if index, ok := q.byID[parent]; ok {
				add(index)
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

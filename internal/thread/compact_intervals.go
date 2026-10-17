package thread

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// CompactCatchUp returns, from one publication, the final state of every root
// whose compact state changed in (afterVersion, Version] plus all roots with
// public subtree activity. Version zero returns CompactNewestWindow for any
// epoch, with its validation and clamp; nonzero ignores limit. A reset carries
// identity but no rows or bounds: recover with a version-zero window. Results
// certify compact root coverage only. The caller must be authorized for id.
func (s *Store) CompactCatchUp(ctx context.Context, id conversations.ConversationID, epoch string, afterVersion uint64, limit int) (CompactQueryResult, error) {
	if afterVersion == 0 {
		return s.compactQuery(ctx, id, 0, limit, true)
	}
	if err := ctx.Err(); err != nil {
		return CompactQueryResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return CompactQueryResult{}, history.ErrInvalidID
	}
	snap, q := s.captureQuery(id)
	if snap.State != StateUsable {
		return CompactQueryResult{State: snap.State, Err: snap.Err}, nil
	}
	return s.compactInterval(ctx, id, snap, q, epoch, afterVersion, true)
}

// CompactChanges waits while after is the current version of epoch, then
// returns the coalesced final states of roots whose compact state changed in
// (after, Version], without activity extras. A usable result without rows is
// explicit consumed progress. Resets follow CompactCatchUp. Cancellation returns
// the context error; retirement returns the zero result. No waiter owns a worker.
func (s *Store) CompactChanges(ctx context.Context, id conversations.ConversationID, epoch string, after uint64) (CompactQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return CompactQueryResult{}, err
	}
	if !conversations.ValidID(string(id)) {
		return CompactQueryResult{}, history.ErrInvalidID
	}
	for {
		s.mu.Lock()
		w := s.workers[id]
		if s.closed || w == nil || w.retiring {
			s.mu.Unlock()
			return CompactQueryResult{}, nil
		}
		snap, q, changed := w.snapshot, w.query, w.changed
		s.mu.Unlock()
		if snap.State != StateUsable {
			return CompactQueryResult{State: snap.State, Err: snap.Err}, nil
		}
		if epoch != snap.Epoch || after != snap.Version {
			return s.compactInterval(ctx, id, snap, q, epoch, after, false)
		}
		select {
		case <-ctx.Done():
			return CompactQueryResult{}, ctx.Err()
		case <-changed:
		}
	}
}

// compactInterval bounds the caller-controlled after before any arithmetic, so
// no value can widen the selection beyond MaxCatchUpGap or compact evidence.
func (s *Store) compactInterval(ctx context.Context, id conversations.ConversationID, snap Snapshot, q *queryIndex, epoch string, after uint64, extras bool) (CompactQueryResult, error) {
	p := CompactQueryResult{State: StateUsable, ConversationID: id, Projection: compactProjection, Epoch: snap.Epoch, Version: snap.Version}
	if epoch != snap.Epoch || after > snap.Version || snap.Version-after > MaxCatchUpGap || q == nil || q.coveredThrough != snap.Version || after < q.compactFrom || q.compactLost > after {
		p.ResetRequired = true
		return p, nil
	}
	p.FromVersion = after
	add := compactCollector(&p)
	if err := q.visit(ctx, q.rootTouches, q.rank(q.rootTouches, after+1), treeSize(q.rootTouches), add); err != nil {
		return CompactQueryResult{}, err
	}
	if extras {
		if err := q.visit(ctx, q.activeRoots, 0, treeSize(q.activeRoots), add); err != nil {
			return CompactQueryResult{}, err
		}
	}
	return s.compactRender(ctx, id, q, p)
}

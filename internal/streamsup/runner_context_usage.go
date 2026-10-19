package streamsup

import (
	"context"
	"time"
)

// WaitContextUsageReady waits through rotation for an entitled child binding.
// It observes the existing admission gate without changing its release rules.
// A missing child outside rotation refuses immediately. Callers supply a deadline
// shared with QueryContextUsageGeneration, so repeated rotations cannot renew it.
func (r *Runner) WaitContextUsageReady(ctx context.Context) (uint64, bool) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		r.mu.Lock()
		rotating, bound, generation := r.rotating, r.stdin != nil, r.childGeneration
		r.mu.Unlock()
		if !rotating {
			return generation, bound
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-ticker.C:
		}
	}
	return 0, false
}

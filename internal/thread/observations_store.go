package thread

import (
	"context"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const maxChangeBatches = 64
const maxChangeBytes = 1 << 20

type changeBatch struct {
	observation Observation
	bytes       int
}

// notify is called under Store.mu; waiters capture changed under that same lock.
func (w *conversationWorker) notify() { close(w.changed); w.changed = make(chan struct{}) }

func (w *conversationWorker) retain(batch changeBatch) {
	if batch.bytes > maxChangeBytes {
		w.ranges = nil
		w.rangeBytes = 0
		return
	}
	w.ranges = append(w.ranges, batch)
	w.rangeBytes += batch.bytes
	for len(w.ranges) > maxChangeBatches || w.rangeBytes > maxChangeBytes {
		w.rangeBytes -= w.ranges[0].bytes
		w.ranges[0] = changeBatch{}
		w.ranges = w.ranges[1:]
	}
}

// Observe acquires a consistent detached baseline without history I/O.
func (s *Store) Observe(id conversations.ConversationID) Observation {
	return Observation{Snapshot: s.Snapshot(id)}
}

// Changes waits for progress beyond after in epoch. Only retained publication
// boundaries establish continuity. Missing ranges, epoch mismatches and suppressed
// rows require a fresh Observe baseline. Cancellation returns the context error;
// retirement/failure returns a nonusable observation. No waiter owns a worker.
func (s *Store) Changes(ctx context.Context, id conversations.ConversationID, epoch string, after uint64) (Observation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		s.mu.Lock()
		w := s.workers[id]
		if w == nil || w.retiring {
			s.mu.Unlock()
			return Observation{}, nil
		}
		o := Observation{Snapshot: w.snapshot, FromVersion: after}
		o.Items = nil
		if o.State != StateUsable {
			s.mu.Unlock()
			return Observation{Snapshot: o.Snapshot}, nil
		}
		if epoch != o.Epoch || after > o.Version {
			o.BaselineRequired = true
			s.mu.Unlock()
			return o, nil
		}
		if after != o.Version {
			cursor := after
			for _, batch := range w.ranges {
				part := batch.observation
				if part.FromVersion != cursor {
					continue
				}
				if part.BaselineRequired {
					o.BaselineRequired = true
					break
				}
				o.Changes = append(o.Changes, part.Changes...)
				cursor = part.Version
			}
			if cursor != o.Version {
				o.BaselineRequired = true
			}
			if o.BaselineRequired {
				o.Changes = nil
			}
			s.mu.Unlock()
			return copyObservation(o), nil
		}
		changed := w.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		case <-changed:
		}
	}
}

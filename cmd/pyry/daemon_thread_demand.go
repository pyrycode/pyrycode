package main

import (
	"context"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Demand owns a loaded worker only while an authenticated query is recovering
// or selecting its detached result. Retirement does I/O outside the coordinator
// lock; a new owner waits for that retirement before starting another worker.
type threadQueryDemand struct {
	refs     int
	retiring chan struct{}
}

func (s *threadShadow) withThreadQuery(ctx context.Context, store *thread.Store, id conversations.ConversationID,
	query func(context.Context) (thread.QueryResult, error)) (thread.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return thread.QueryResult{}, err
		}
		s.mu.Lock()
		if _, ok := s.registry.Get(id); !ok || s.removed[id] || s.ctx.Err() != nil {
			s.mu.Unlock()
			return thread.QueryResult{}, thread.ErrUnavailable
		}
		select {
		case <-s.finish:
			s.mu.Unlock()
			return thread.QueryResult{}, thread.ErrClosed
		default:
		}
		if s.demands == nil {
			s.demands = make(map[conversations.ConversationID]*threadQueryDemand)
		}
		d := s.demands[id]
		if d == nil {
			d = &threadQueryDemand{}
			s.demands[id] = d
		}
		if d.retiring != nil {
			done := d.retiring
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return thread.QueryResult{}, ctx.Err()
			}
		}
		d.refs++
		// Load starts the daemon-owned worker without history I/O or waiting. The
		// request context governs only waiting/rendering, never a sibling's worker.
		previous := store.Status(id)
		err := store.Load(s.ctx, id)
		if err != nil {
			d.refs--
			s.mu.Unlock()
			return thread.QueryResult{}, err
		}
		if s.workers[id] == nil && previous.State == thread.StateNotLoaded {
			workerCtx, workerCancel := context.WithCancel(s.ctx)
			w := &shadowConversation{cancel: workerCancel, done: make(chan struct{})}
			s.workers[id] = w
			go s.follow(workerCtx, id, w)
		}
		s.mu.Unlock()
		defer func() { s.mu.Lock(); d.refs--; s.mu.Unlock() }()
		ready, err := store.WaitReady(ctx, id)
		if err != nil {
			return thread.QueryResult{}, err
		}
		if ready.State != thread.StateUsable {
			return thread.QueryResult{}, thread.ErrUnavailable
		}
		result, err := query(ctx)
		if _, ok := s.registry.Get(id); !ok {
			return thread.QueryResult{}, thread.ErrUnavailable
		}
		return result, err
	}
}

func (s *threadShadow) retireIdle(id conversations.ConversationID, w *shadowConversation, version uint64) {
	s.mu.Lock()
	if s.demands == nil {
		s.demands = make(map[conversations.ConversationID]*threadQueryDemand)
	}
	d := s.demands[id]
	if d == nil {
		d = &threadQueryDemand{}
		s.demands[id] = d
	}
	if d.refs != 0 || d.retiring != nil {
		s.mu.Unlock()
		return
	}
	done := make(chan struct{})
	d.retiring = done
	// Save the consumed boundary before retirement. A racing append still exceeds
	// it and reloads on the next coordinator pass.
	w.version = version
	s.mu.Unlock()
	err := s.store.Unload(id)
	s.mu.Lock()
	d.retiring = nil
	close(done)
	s.mu.Unlock()
	if err != nil {
		s.failure(id, "unload")
	}
}
